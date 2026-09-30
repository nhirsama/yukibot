package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nhirsama/yukibot/internal/contracts"
)

const (
	promptReserveTokens = 2000
	maxBatchInputTokens = 12000
	telegramChunkLimit  = 3900
	mergeGap            = 180 * time.Second
	maxConsecutiveRefs  = 3
	longMessageMarker   = "[长消息分段]\n"
)

// SummarizerService runs message-aware map/reduce summarization.
type SummarizerService struct {
	rules         RuleRepository
	runs          RunRepository
	modelConfigs  ModelConfigRepository
	telegram      Telegram
	generator     Generator
	defaultWindow int
	maxTopics     int
}

// NewSummarizerService uses a 1 day window and at most 12 topics.
func NewSummarizerService(
	rules RuleRepository,
	runs RunRepository,
	modelConfigs ModelConfigRepository,
	telegram Telegram,
	generator Generator,
) (*SummarizerService, error) {
	return NewSummarizerServiceConfigured(rules, runs, modelConfigs, telegram, generator, 86400, 12)
}

// NewSummarizerServiceConfigured validates the default window and topic cap.
func NewSummarizerServiceConfigured(
	rules RuleRepository,
	runs RunRepository,
	modelConfigs ModelConfigRepository,
	telegram Telegram,
	generator Generator,
	defaultWindowSeconds int,
	maxTopics int,
) (*SummarizerService, error) {
	if defaultWindowSeconds < 60 || defaultWindowSeconds > 30*86400 {
		return nil, &ValueError{Msg: "default summary window must be between 60 seconds and 30 days"}
	}
	if maxTopics <= 0 {
		return nil, &ValueError{Msg: "summary limits must be positive"}
	}
	return &SummarizerService{
		rules:         rules,
		runs:          runs,
		modelConfigs:  modelConfigs,
		telegram:      telegram,
		generator:     generator,
		defaultWindow: defaultWindowSeconds,
		maxTopics:     maxTopics,
	}, nil
}

// DefaultWindowSeconds is the window used when a command omits one.
func (s *SummarizerService) DefaultWindowSeconds() int { return s.defaultWindow }

// ResolveEndpoint delegates chat resolution to the telegram gateway.
func (s *SummarizerService) ResolveEndpoint(ctx context.Context, reference string) (SummaryEndpoint, error) {
	return s.telegram.ResolveEndpoint(ctx, reference)
}

// GetModelConfig returns the stored model configuration, or nil when unset.
func (s *SummarizerService) GetModelConfig(ctx context.Context) (*SummaryModelConfig, error) {
	return s.modelConfigs.GetModelConfig(ctx)
}

// ConfigureModel replaces the provider, model, API key, and base URL.
// A nil key or URL clears that field. Other settings are kept when a config exists.
func (s *SummarizerService) ConfigureModel(ctx context.Context, provider, model string, apiKey, baseURL *string) (SummaryModelConfig, error) {
	current, err := s.modelConfigs.GetModelConfig(ctx)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	var cfg SummaryModelConfig
	if current != nil {
		cfg = *current
	} else {
		cfg = SummaryModelConfig{
			InputTokenLimit:  32768,
			OutputTokenLimit: 4096,
			Temperature:      0.1,
			Timeout:          120 * time.Second,
			MaxRetries:       2,
			PromptPreset:     PromptFocused,
			MaxConcurrency:   3,
		}
	}
	cfg.Provider = provider
	cfg.Model = model
	cfg.APIKey = ""
	if apiKey != nil {
		cfg.APIKey = *apiKey
	}
	cfg.BaseURL = ""
	if baseURL != nil {
		cfg.BaseURL = *baseURL
	}
	cfg, err = cfg.Normalized()
	if err != nil {
		return SummaryModelConfig{}, err
	}
	if err := s.generator.Reset(ctx); err != nil {
		return SummaryModelConfig{}, err
	}
	if err := s.modelConfigs.SaveModelConfig(ctx, cfg); err != nil {
		return SummaryModelConfig{}, err
	}
	return cfg, nil
}

// TuneModel updates limits, sampling, timeout, retries, and optional concurrency.
// A nil maxConcurrency keeps the current value. timeout is the request deadline.
func (s *SummarizerService) TuneModel(
	ctx context.Context,
	inputTokenLimit int,
	outputTokenLimit int,
	temperature float64,
	timeout time.Duration,
	maxRetries int,
	maxConcurrency *int,
) (SummaryModelConfig, error) {
	current, err := s.requireModel(ctx)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	current.InputTokenLimit = inputTokenLimit
	current.OutputTokenLimit = outputTokenLimit
	current.Temperature = temperature
	current.Timeout = timeout
	current.MaxRetries = maxRetries
	if maxConcurrency != nil {
		current.MaxConcurrency = *maxConcurrency
	}
	current, err = current.Normalized()
	if err != nil {
		return SummaryModelConfig{}, err
	}
	if err := s.generator.Reset(ctx); err != nil {
		return SummaryModelConfig{}, err
	}
	if err := s.modelConfigs.SaveModelConfig(ctx, current); err != nil {
		return SummaryModelConfig{}, err
	}
	return current, nil
}

// SetPromptPreset selects a built-in preference and clears a custom prompt.
func (s *SummarizerService) SetPromptPreset(ctx context.Context, preset string) (SummaryModelConfig, error) {
	current, err := s.requireModel(ctx)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	selected := SummaryPromptPreset(casefold(strings.TrimSpace(preset)))
	switch selected {
	case PromptFocused, PromptDecisions, PromptTechnical, PromptDigest:
	default:
		return SummaryModelConfig{}, &ValueError{Msg: "未知总结预设: " + preset}
	}
	current.PromptPreset = selected
	current.CustomPrompt = ""
	if err := s.modelConfigs.SaveModelConfig(ctx, current); err != nil {
		return SummaryModelConfig{}, err
	}
	return current, nil
}

// SetCustomPrompt stores a custom preference without changing the preset.
func (s *SummarizerService) SetCustomPrompt(ctx context.Context, prompt string) (SummaryModelConfig, error) {
	current, err := s.requireModel(ctx)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	current.CustomPrompt = prompt
	current, err = current.Normalized()
	if err != nil {
		return SummaryModelConfig{}, err
	}
	if err := s.modelConfigs.SaveModelConfig(ctx, current); err != nil {
		return SummaryModelConfig{}, err
	}
	return current, nil
}

// ClearModelConfig drops the model configuration and resets the generator.
func (s *SummarizerService) ClearModelConfig(ctx context.Context) (bool, error) {
	if err := s.generator.Reset(ctx); err != nil {
		return false, err
	}
	return s.modelConfigs.ClearModelConfig(ctx)
}

// ListRules returns every stored rule.
func (s *SummarizerService) ListRules(ctx context.Context) ([]SummaryRule, error) {
	return s.rules.ListAll(ctx)
}

// GetRule returns one rule or SummaryRuleNotFoundError.
func (s *SummarizerService) GetRule(ctx context.Context, ruleID int) (SummaryRule, error) {
	rules, err := s.rules.ListAll(ctx)
	if err != nil {
		return SummaryRule{}, err
	}
	for _, rule := range rules {
		if rule.ID == ruleID {
			return rule, nil
		}
	}
	return SummaryRule{}, &SummaryRuleNotFoundError{Msg: ruleMissingText(ruleID)}
}

// AddRule resolves both ends and inserts a route, refreshing an identical one.
func (s *SummarizerService) AddRule(ctx context.Context, sourceReference, destinationReference string, windowSeconds *int) (SummaryRule, error) {
	draft, err := s.draft(ctx, sourceReference, destinationReference, windowSeconds)
	if err != nil {
		return SummaryRule{}, err
	}
	rules, err := s.rules.ListAll(ctx)
	if err != nil {
		return SummaryRule{}, err
	}
	var existing *SummaryRule
	for i := range rules {
		if draft.Matches(rules[i]) {
			existing = &rules[i]
			break
		}
	}
	if existing == nil {
		return s.rules.AddAuto(ctx, draft)
	}
	refreshed, err := draft.Bind(existing.ID)
	if err != nil {
		return SummaryRule{}, err
	}
	refreshed.Enabled = existing.Enabled
	if refreshed != *existing {
		if err := s.rules.Replace(ctx, refreshed); err != nil {
			return SummaryRule{}, err
		}
	}
	return refreshed, nil
}

// ReplaceRule updates one rule and preserves its enabled flag.
func (s *SummarizerService) ReplaceRule(ctx context.Context, ruleID int, sourceReference, destinationReference string, windowSeconds *int) (SummaryRule, error) {
	existing, err := s.GetRule(ctx, ruleID)
	if err != nil {
		return SummaryRule{}, err
	}
	draft, err := s.draft(ctx, sourceReference, destinationReference, windowSeconds)
	if err != nil {
		return SummaryRule{}, err
	}
	rule, err := draft.Bind(ruleID)
	if err != nil {
		return SummaryRule{}, err
	}
	rule.Enabled = existing.Enabled
	if err := s.rules.Replace(ctx, rule); err != nil {
		if errors.Is(err, ErrRuleMissing) {
			return SummaryRule{}, &SummaryRuleNotFoundError{Msg: ruleMissingText(ruleID)}
		}
		return SummaryRule{}, err
	}
	return rule, nil
}

// SetEnabled toggles a rule.
func (s *SummarizerService) SetEnabled(ctx context.Context, ruleID int, enabled bool) (SummaryRule, error) {
	rule, err := s.GetRule(ctx, ruleID)
	if err != nil {
		return SummaryRule{}, err
	}
	rule.Enabled = enabled
	if err := s.rules.Replace(ctx, rule); err != nil {
		if errors.Is(err, ErrRuleMissing) {
			return SummaryRule{}, &SummaryRuleNotFoundError{Msg: ruleMissingText(ruleID)}
		}
		return SummaryRule{}, err
	}
	return rule, nil
}

// RemoveRule deletes a rule or reports that it does not exist.
func (s *SummarizerService) RemoveRule(ctx context.Context, ruleID int) error {
	removed, err := s.rules.Remove(ctx, ruleID)
	if err != nil {
		return err
	}
	if !removed {
		return &SummaryRuleNotFoundError{Msg: ruleMissingText(ruleID)}
	}
	return nil
}

// RunRule fetches, summarizes, sends, and records one enabled rule.
// A nil window uses the rule's own window.
func (s *SummarizerService) RunRule(ctx context.Context, ruleID int, windowSeconds *int) (SummaryExecution, error) {
	rule, err := s.GetRule(ctx, ruleID)
	if err != nil {
		return SummaryExecution{}, err
	}
	if !rule.Enabled {
		return SummaryExecution{}, &SummarizerError{Msg: "summary rule " + itoa(ruleID) + " is disabled"}
	}
	modelConfig, err := s.requireModel(ctx)
	if err != nil {
		return SummaryExecution{}, err
	}
	effectiveWindow := rule.WindowSeconds
	if windowSeconds != nil {
		effectiveWindow = *windowSeconds
	}
	if err := validateWindow(effectiveWindow); err != nil {
		return SummaryExecution{}, err
	}
	startedAt := time.Now().UTC()
	fetched, err := s.telegram.FetchRecent(ctx, rule.Source, startedAt.Add(-time.Duration(effectiveWindow)*time.Second), nil)
	if err != nil {
		return SummaryExecution{}, err
	}
	sameChat := rule.Source.ChatID == rule.Destination.ChatID
	useful := make([]SummaryMessage, 0, len(fetched.Messages))
	for _, message := range fetched.Messages {
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		if sameChat && message.Outgoing {
			continue
		}
		useful = append(useful, message)
	}
	if len(useful) == 0 {
		return SummaryExecution{}, &NoMessagesToSummarizeError{Msg: "所选时间范围内没有可总结的文字消息"}
	}
	source := fetched
	source.Messages = mergeMessages(useful)
	document, err := s.summarize(ctx, source, modelConfig)
	if err != nil {
		return SummaryExecution{}, err
	}
	chunks := splitForTelegram(renderDocument(document, source), telegramChunkLimit)
	sent := make([]contracts.MessageRef, 0, len(chunks))
	for _, chunk := range chunks {
		ref, err := s.telegram.SendText(ctx, rule.Destination, chunk)
		if err != nil {
			return SummaryExecution{}, err
		}
		sent = append(sent, ref)
	}
	completedAt := time.Now().UTC()
	rawIDs := make([]int, 0, len(useful))
	for _, message := range useful {
		rawIDs = append(rawIDs, message.MessageIDs()...)
	}
	first, last := rawIDs[0], rawIDs[0]
	for _, id := range rawIDs[1:] {
		if id < first {
			first = id
		}
		if id > last {
			last = id
		}
	}
	run, err := NewSummaryRun(SummaryRun{
		RuleID:         rule.ID,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
		FirstMessageID: first,
		LastMessageID:  last,
		MessageCount:   len(useful),
		Provider:       modelConfig.Provider,
		Model:          modelConfig.Model,
		PromptVersion:  PromptVersion,
		Document:       document,
	})
	if err != nil {
		return SummaryExecution{}, err
	}
	if err := s.runs.Save(ctx, run); err != nil {
		return SummaryExecution{}, err
	}
	return SummaryExecution{
		Rule:         rule,
		MessageCount: len(useful),
		TopicCount:   len(document.Topics),
		SentMessages: sent,
	}, nil
}

func (s *SummarizerService) draft(ctx context.Context, sourceReference, destinationReference string, windowSeconds *int) (SummaryRuleDraft, error) {
	source, err := s.telegram.ResolveEndpoint(ctx, sourceReference)
	if err != nil {
		return SummaryRuleDraft{}, err
	}
	destination, err := s.telegram.ResolveEndpoint(ctx, destinationReference)
	if err != nil {
		return SummaryRuleDraft{}, err
	}
	window := s.defaultWindow
	if windowSeconds != nil {
		window = *windowSeconds
	}
	return NewSummaryRuleDraft(source, destination, window, true)
}

func (s *SummarizerService) requireModel(ctx context.Context) (SummaryModelConfig, error) {
	cfg, err := s.modelConfigs.GetModelConfig(ctx)
	if err != nil {
		return SummaryModelConfig{}, err
	}
	if cfg == nil {
		return SummaryModelConfig{}, &SummaryModelUnavailableError{Msg: "消息总结模型未配置, 请使用 /summary model set 配置。"}
	}
	return *cfg, nil
}

func ruleMissingText(ruleID int) string {
	return "summary rule " + itoa(ruleID) + " does not exist"
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *SummarizerService) summarize(ctx context.Context, source FetchedSummaryMessages, cfg SummaryModelConfig) (SummaryDocument, error) {
	preference := PromptPreference(cfg.PromptPreset, cfg.CustomPrompt)
	batches, err := messageBatches(source.Messages, cfg.InputTokenLimit, cfg.OutputTokenLimit, preference)
	if err != nil {
		return SummaryDocument{}, err
	}
	sem := make(chan struct{}, cfg.MaxConcurrency)
	mapped := make([]SummaryDocument, len(batches))
	err = gather(ctx, len(batches), func(ctx context.Context, i int) error {
		doc, err := s.summarizeBatch(ctx, source, batches[i], cfg, preference, sem)
		if err != nil {
			return err
		}
		mapped[i] = doc
		return nil
	})
	if err != nil {
		return SummaryDocument{}, err
	}
	documents := make([]SummaryDocument, 0, len(mapped))
	for _, document := range mapped {
		if len(document.Topics) > 0 {
			documents = append(documents, document)
		}
	}
	if len(documents) == 0 {
		return SummaryDocument{}, nil
	}
	return s.reduceDocuments(ctx, source, documents, cfg, preference, sem)
}

func (s *SummarizerService) summarizeBatch(
	ctx context.Context,
	source FetchedSummaryMessages,
	batch []SummaryMessage,
	cfg SummaryModelConfig,
	preference string,
	sem chan struct{},
) (SummaryDocument, error) {
	allowed := map[int]struct{}{}
	payload := make([]json.RawMessage, 0, len(batch))
	for _, message := range batch {
		for _, id := range message.MessageIDs() {
			allowed[id] = struct{}{}
		}
		raw, err := marshalCompact(messagePayloadFrom(message))
		if err != nil {
			return SummaryDocument{}, err
		}
		payload = append(payload, json.RawMessage(raw))
	}
	system, user := MapPrompts(source, payload, preference)
	if err := acquire(ctx, sem); err != nil {
		return SummaryDocument{}, err
	}
	defer release(sem)
	generated, err := s.generator.Generate(ctx, cfg, system, user)
	if err != nil {
		return SummaryDocument{}, err
	}
	return groundDocument(generated, allowed, s.maxTopics), nil
}

func (s *SummarizerService) reduceDocuments(
	ctx context.Context,
	source FetchedSummaryMessages,
	documents []SummaryDocument,
	cfg SummaryModelConfig,
	preference string,
	sem chan struct{},
) (SummaryDocument, error) {
	current := documents
	allowed := map[int]struct{}{}
	for _, message := range source.Messages {
		for _, id := range message.MessageIDs() {
			allowed[id] = struct{}{}
		}
	}
	for len(current) > 1 {
		groups, err := documentBatches(source, current, cfg.InputTokenLimit, cfg.OutputTokenLimit, preference)
		if err != nil {
			return SummaryDocument{}, err
		}
		allSingle := true
		for _, group := range groups {
			if len(group) != 1 {
				allSingle = false
				break
			}
		}
		if allSingle {
			return mergeDocuments(current, s.maxTopics), nil
		}
		next := make([]SummaryDocument, len(groups))
		err = gather(ctx, len(groups), func(ctx context.Context, i int) error {
			doc, err := s.reduceGroup(ctx, source, groups[i], cfg, preference, sem, allowed)
			if err != nil {
				return err
			}
			next[i] = doc
			return nil
		})
		if err != nil {
			return SummaryDocument{}, err
		}
		current = next
	}
	return current[0], nil
}

func (s *SummarizerService) reduceGroup(
	ctx context.Context,
	source FetchedSummaryMessages,
	group []SummaryDocument,
	cfg SummaryModelConfig,
	preference string,
	sem chan struct{},
	allowed map[int]struct{},
) (SummaryDocument, error) {
	if len(group) == 1 {
		return group[0], nil
	}
	system, user := ReducePrompts(source, group, preference)
	if err := acquire(ctx, sem); err != nil {
		return SummaryDocument{}, err
	}
	defer release(sem)
	reduced, err := s.generator.Generate(ctx, cfg, system, user)
	if err != nil {
		return SummaryDocument{}, err
	}
	grounded := groundDocument(reduced, allowed, s.maxTopics)
	if len(grounded.Topics) == 0 {
		return mergeDocuments(group, s.maxTopics), nil
	}
	return grounded, nil
}

func acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release(sem chan struct{}) { <-sem }

func gather(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if n == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	record := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if first == nil || (errors.Is(first, context.Canceled) && !errors.Is(err, context.Canceled)) {
			first = err
		}
		cancel()
	}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			record(fn(ctx, i))
		}(i)
	}
	wg.Wait()
	return first
}

func mergeMessages(messages []SummaryMessage) []SummaryMessage {
	ordered := append([]SummaryMessage(nil), messages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].OccurredAt.Equal(ordered[j].OccurredAt) {
			return ordered[i].Refs[0].MessageID < ordered[j].Refs[0].MessageID
		}
		return ordered[i].OccurredAt.Before(ordered[j].OccurredAt)
	})
	merged := make([]SummaryMessage, 0, len(ordered))
	for _, message := range ordered {
		if len(merged) == 0 {
			merged = append(merged, message)
			continue
		}
		previous := merged[len(merged)-1]
		sameAlbum := message.GroupedID.IsSet() && message.GroupedID.Equal(previous.GroupedID)
		consecutive := !message.GroupedID.IsSet() &&
			!previous.GroupedID.IsSet() &&
			sameOptionalInt64(message.SenderID, previous.SenderID) &&
			message.SenderName == previous.SenderName &&
			sameOptionalInt(message.ReplyToMessageID, previous.ReplyToMessageID) &&
			len(previous.Refs) < maxConsecutiveRefs &&
			message.OccurredAt.Sub(previous.OccurredAt) <= mergeGap
		if sameAlbum || consecutive {
			refs := make([]contracts.MessageRef, 0, len(previous.Refs)+len(message.Refs))
			refs = append(refs, previous.Refs...)
			refs = append(refs, message.Refs...)
			links := uniqueStrings(append(append([]string{}, previous.Links...), message.Links...))
			merged[len(merged)-1] = SummaryMessage{
				Refs:             refs,
				OccurredAt:       previous.OccurredAt,
				SenderName:       previous.SenderName,
				Text:             previous.Text + "\n" + message.Text,
				SenderID:         previous.SenderID,
				ReplyToMessageID: previous.ReplyToMessageID,
				GroupedID:        pickGrouped(previous.GroupedID, message.GroupedID),
				ForwardedFrom:    pickString(previous.ForwardedFrom, message.ForwardedFrom),
				Links:            links,
				Outgoing:         previous.Outgoing,
			}
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func sameOptionalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameOptionalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func pickGrouped(previous, message GroupedID) GroupedID {
	if previous.Truthy() {
		return previous
	}
	return message
}

func pickString(previous, message string) string {
	if previous != "" {
		return previous
	}
	return message
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func messageBatches(messages []SummaryMessage, inputTokenLimit, outputTokenLimit int, preference string) ([][]SummaryMessage, error) {
	budget, err := batchTokenBudget(inputTokenLimit, outputTokenLimit, estimateTokens(preference))
	if err != nil {
		return nil, err
	}
	batches := [][]SummaryMessage{{}}
	used := 0
	for _, message := range messages {
		for _, prepared := range splitMessage(message, budget) {
			size := messageTokens(prepared)
			if len(batches[len(batches)-1]) > 0 && used+size > budget {
				batches = append(batches, []SummaryMessage{})
				used = 0
			}
			batches[len(batches)-1] = append(batches[len(batches)-1], prepared)
			used += size
		}
	}
	return nonemptyMessageBatches(batches), nil
}

func nonemptyMessageBatches(batches [][]SummaryMessage) [][]SummaryMessage {
	out := make([][]SummaryMessage, 0, len(batches))
	for _, batch := range batches {
		if len(batch) > 0 {
			out = append(out, batch)
		}
	}
	return out
}

func documentBatches(
	source FetchedSummaryMessages,
	documents []SummaryDocument,
	inputTokenLimit int,
	outputTokenLimit int,
	preference string,
) ([][]SummaryDocument, error) {
	budget, err := batchTokenBudget(inputTokenLimit, outputTokenLimit, estimateTokens(preference))
	if err != nil {
		return nil, err
	}
	batches := [][]SummaryDocument{{}}
	for _, document := range documents {
		candidate := append(append([]SummaryDocument{}, batches[len(batches)-1]...), document)
		system, user := ReducePrompts(source, candidate, preference)
		if len(batches[len(batches)-1]) > 0 && estimateTokens(system+user) > budget {
			batches = append(batches, []SummaryDocument{document})
		} else {
			batches[len(batches)-1] = append(batches[len(batches)-1], document)
		}
	}
	out := make([][]SummaryDocument, 0, len(batches))
	for _, batch := range batches {
		if len(batch) > 0 {
			out = append(out, batch)
		}
	}
	return out, nil
}

func batchTokenBudget(inputTokenLimit, outputTokenLimit, extraReserved int) (int, error) {
	available := inputTokenLimit - outputTokenLimit - promptReserveTokens - extraReserved
	if available < 128 {
		return 0, &ValueError{Msg: "summary token limits leave no usable input budget"}
	}
	if available > maxBatchInputTokens {
		return maxBatchInputTokens, nil
	}
	return available, nil
}

func messageTokens(message SummaryMessage) int {
	payload, err := marshalCompact(messagePayloadFrom(message))
	if err != nil {
		return estimateTokens(message.Text)
	}
	return estimateTokens(payload)
}

func estimateTokens(text string) int {
	ascii := 0
	total := 0
	for _, character := range text {
		total++
		if character <= unicode.MaxASCII {
			ascii++
		}
	}
	return (ascii+2)/3 + (total-ascii)*2
}

func splitMessage(message SummaryMessage, tokenBudget int) []SummaryMessage {
	if messageTokens(message) <= tokenBudget {
		return []SummaryMessage{message}
	}
	runes := []rune(message.Text)
	fragments := make([]SummaryMessage, 0, 2)
	start := 0
	for start < len(runes) {
		lower, upper := start+1, len(runes)
		end := lower
		for lower <= upper {
			midpoint := (lower + upper) / 2
			candidate := message
			candidate.Text = longMessageMarker + string(runes[start:midpoint])
			if messageTokens(candidate) <= tokenBudget {
				end = midpoint
				lower = midpoint + 1
			} else {
				upper = midpoint - 1
			}
		}
		fragment := message
		fragment.Text = longMessageMarker + string(runes[start:end])
		fragments = append(fragments, fragment)
		if end <= start {
			end = start + 1
		}
		start = end
	}
	return fragments
}

func groundDocument(document SummaryDocument, allowed map[int]struct{}, maxTopics int) SummaryDocument {
	topics := make([]SummaryTopic, 0, len(document.Topics))
	seen := map[string]struct{}{}
	for _, topic := range document.Topics {
		title := strings.Join(strings.Fields(topic.Title), " ")
		summary := strings.Join(strings.Fields(topic.Summary), " ")
		key := casefold(title)
		evidence := filterEvidence(topic.EvidenceMessageIDs, allowed)
		if title == "" || summary == "" || len(evidence) == 0 {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		actions := make([]SummaryActionItem, 0, len(topic.ActionItems))
		for _, item := range topic.ActionItems {
			if strings.TrimSpace(item.Task) == "" {
				continue
			}
			actions = append(actions, SummaryActionItem{
				Task:     strings.Join(strings.Fields(item.Task), " "),
				Owner:    cleanOptional(item.Owner),
				Deadline: cleanOptional(item.Deadline),
			})
		}
		topics = append(topics, SummaryTopic{
			Title:              title,
			Summary:            summary,
			EvidenceMessageIDs: evidence,
			Participants:       cleanStrings(topic.Participants),
			Decisions:          cleanStrings(topic.Decisions),
			ActionItems:        actions,
			OpenQuestions:      cleanStrings(topic.OpenQuestions),
		})
		if len(topics) >= maxTopics {
			break
		}
	}
	return SummaryDocument{Topics: topics}
}

func filterEvidence(ids []int, allowed map[int]struct{}) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func cleanOptional(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	cleaned := strings.Join(strings.Fields(*value), " ")
	return &cleaned
}

func cleanStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		cleaned := strings.Join(strings.Fields(value), " ")
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	if out == nil {
		return []string{}
	}
	return out
}

func mergeDocuments(documents []SummaryDocument, maxTopics int) SummaryDocument {
	seen := map[string]struct{}{}
	topics := make([]SummaryTopic, 0)
	for _, document := range documents {
		for _, topic := range document.Topics {
			key := casefold(topic.Title)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			topics = append(topics, topic)
			if len(topics) >= maxTopics {
				return SummaryDocument{Topics: topics}
			}
		}
	}
	return SummaryDocument{Topics: topics}
}

func renderDocument(document SummaryDocument, source FetchedSummaryMessages) string {
	lines := []string{source.ChatTitle + " 消息总结"}
	if len(document.Topics) == 0 {
		lines = append(lines, "", "所选时间范围内没有值得总结的有效信息。")
		return strings.Join(lines, "\n")
	}
	for index, topic := range document.Topics {
		lines = append(lines, "", itoa(index+1)+". "+topic.Title, topic.Summary)
		if source.ChatKind != ChatChannel && len(topic.Participants) > 0 {
			lines = append(lines, "参与者: "+strings.Join(topic.Participants, ", "))
		}
		if len(topic.Decisions) > 0 {
			lines = append(lines, "结论: "+strings.Join(topic.Decisions, "; "))
		}
		for _, item := range topic.ActionItems {
			detail := item.Task
			if item.Owner != nil && *item.Owner != "" {
				detail += " | 负责人: " + *item.Owner
			}
			if item.Deadline != nil && *item.Deadline != "" {
				detail += " | 截止: " + *item.Deadline
			}
			lines = append(lines, "行动项: "+detail)
		}
		if len(topic.OpenQuestions) > 0 {
			lines = append(lines, "待确认: "+strings.Join(topic.OpenQuestions, "; "))
		}
		evidence := uniqueInts(topic.EvidenceMessageIDs)
		sort.Ints(evidence)
		firstID, lastID := evidence[0], evidence[len(evidence)-1]
		messageRange := itoa(firstID)
		if firstID != lastID {
			messageRange = itoa(firstID) + "-" + itoa(lastID)
		}
		lines = append(lines, "原消息: "+messageReference(source, firstID)+" | 消息范围: "+messageRange)
	}
	return strings.Join(lines, "\n")
}

func uniqueInts(values []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func messageReference(source FetchedSummaryMessages, messageID int) string {
	endpoint := source.Source
	if source.ChatKind != ChatPrivate && endpoint.Username != "" {
		return "https://t.me/" + endpoint.Username + "/" + itoa(messageID)
	}
	raw := formatInt64(endpoint.ChatID)
	if strings.HasPrefix(raw, "-100") {
		return "https://t.me/c/" + raw[4:] + "/" + itoa(messageID)
	}
	return "#" + itoa(messageID)
}

func formatInt64(n int64) string { return strconv.FormatInt(n, 10) }

func splitForTelegram(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	paragraphs := strings.Split(text, "\n\n")
	chunks := make([]string, 0, 2)
	current := ""
	for _, paragraph := range paragraphs {
		candidate := paragraph
		if current != "" {
			candidate = current + "\n\n" + paragraph
		}
		if len([]rune(candidate)) <= limit {
			current = candidate
			continue
		}
		if current != "" {
			chunks = append(chunks, current)
		}
		paragraphRunes := []rune(paragraph)
		for len(paragraphRunes) > limit {
			chunks = append(chunks, string(paragraphRunes[:limit]))
			paragraphRunes = paragraphRunes[limit:]
		}
		current = string(paragraphRunes)
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}
