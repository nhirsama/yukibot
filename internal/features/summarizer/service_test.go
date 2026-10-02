package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestSummaryTitleDeduplicationUsesPythonCasefold(t *testing.T) {
	topics := []SummaryTopic{
		{Title: "İstanbul", Summary: "first", EvidenceMessageIDs: []int{10}},
		{Title: "i\u0307stanbul", Summary: "duplicate", EvidenceMessageIDs: []int{10}},
		{Title: "Straße", Summary: "second", EvidenceMessageIDs: []int{10}},
		{Title: "STRASSE", Summary: "duplicate", EvidenceMessageIDs: []int{10}},
	}
	document := SummaryDocument{Topics: topics}
	grounded := groundDocument(document, map[int]struct{}{10: {}}, 12)
	merged := mergeDocuments([]SummaryDocument{document}, 12)
	for _, got := range []SummaryDocument{grounded, merged} {
		if len(got.Topics) != 2 || got.Topics[0].Title != "İstanbul" || got.Topics[1].Title != "Straße" {
			t.Fatalf("unexpected deduplication: %+v", got)
		}
	}
}

func TestOversizedSingleMessageIsSplitWithoutLosingText(t *testing.T) {
	original := strings.Repeat("中", 10000)
	batches, err := messageBatches([]SummaryMessage{testMessage(10, original, 0, 7, "Alice", false)}, 32768, 4096, "")
	if err != nil {
		t.Fatal(err)
	}
	var fragments []SummaryMessage
	for _, batch := range batches {
		fragments = append(fragments, batch...)
	}
	if len(fragments) < 2 {
		t.Fatalf("fragments %d", len(fragments))
	}
	var joined strings.Builder
	for _, fragment := range fragments {
		ids := fragment.MessageIDs()
		if len(ids) != 1 || ids[0] != 10 {
			t.Fatalf("ids %v", ids)
		}
		joined.WriteString(strings.TrimPrefix(fragment.Text, longMessageMarker))
	}
	if joined.String() != original {
		t.Fatal("split dropped text")
	}
}

func TestSplitForTelegramUses3900(t *testing.T) {
	if got := splitForTelegram("short", telegramChunkLimit); len(got) != 1 || got[0] != "short" {
		t.Fatalf("%q", got)
	}
	text := strings.Repeat("中", telegramChunkLimit+10)
	parts := splitForTelegram(text, telegramChunkLimit)
	if len(parts) != 2 || len([]rune(parts[0])) != telegramChunkLimit || strings.Join(parts, "") != text {
		t.Fatalf("parts %d", len(parts))
	}
}

func TestRuleManagementUsesDefaultsAndRejectsZeroWindow(t *testing.T) {
	repo := NewMemoryRepository()
	telegram := newFakeTelegram()
	service := newTestService(t, repo, telegram, &fakeGenerator{})
	configured, err := service.AddRule(context.Background(), "source", "destination", nil)
	if err != nil {
		t.Fatal(err)
	}
	renamed := telegram.endpoints["source"]
	renamed.Username = "renamed_source"
	telegram.endpoints["source"] = renamed
	duplicate, err := service.AddRule(context.Background(), "source", "destination", nil)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != configured.ID || duplicate.Source.Username != "renamed_source" || configured.WindowSeconds != 86400 {
		t.Fatalf("configured %+v duplicate %+v", configured, duplicate)
	}
	if len(repo.Rules) != 1 {
		t.Fatalf("rules %d", len(repo.Rules))
	}
	zero := 0
	_, err = service.AddRule(context.Background(), "source", "destination", &zero)
	if err == nil || !strings.Contains(err.Error(), "between 60 seconds") {
		t.Fatal(err)
	}
}

func TestModelConfigurationIsManagedAsBusinessData(t *testing.T) {
	repo := NewMemoryRepository()
	generator := &fakeGenerator{}
	service := newTestService(t, repo, newFakeTelegram(), generator)
	key := " secret "
	base := "https://models.example/v1/"
	configured, err := service.ConfigureModel(context.Background(), " OpenAI ", " gpt-test ", &key, &base)
	if err != nil {
		t.Fatal(err)
	}
	concurrency := 4
	tuned, err := service.TuneModel(context.Background(), 16000, 2000, 0.2, 60*time.Second, 3, &concurrency)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := service.SetPromptPreset(context.Background(), "technical")
	if err != nil {
		t.Fatal(err)
	}
	customized, err := service.SetCustomPrompt(context.Background(), "只保留故障根因和验证结果")
	if err != nil {
		t.Fatal(err)
	}
	if configured.Provider != "openai" || configured.APIKey != "secret" || configured.BaseURL != "https://models.example/v1" {
		t.Fatalf("%+v", configured)
	}
	rendered := configured.String() + configured.GoString() + fmt.Sprintf("%v %#v %s", configured, configured, configured)
	if strings.Contains(rendered, "secret") {
		t.Fatal(rendered)
	}
	if tuned.InputTokenLimit != 16000 || tuned.MaxConcurrency != 4 {
		t.Fatalf("%+v", tuned)
	}
	if selected.PromptPreset != PromptTechnical || selected.CustomPrompt != "" {
		t.Fatalf("%+v", selected)
	}
	if customized.CustomPrompt != "只保留故障根因和验证结果" {
		t.Fatal(customized.CustomPrompt)
	}
	cleared, err := service.ClearModelConfig(context.Background())
	if err != nil || !cleared {
		t.Fatal(err, cleared)
	}
	current, err := service.GetModelConfig(context.Background())
	if err != nil || current != nil {
		t.Fatal(err, current)
	}
	if generator.resetCalls != 3 {
		t.Fatalf("resets %d", generator.resetCalls)
	}
	invalid := SummaryModelConfig{Provider: "openai", Model: "gpt-test", OutputTokenLimit: -1, InputTokenLimit: 32768, Temperature: 0.1, Timeout: 120 * time.Second, MaxRetries: 2, PromptPreset: PromptFocused, MaxConcurrency: 3}
	if _, err := invalid.Normalized(); err == nil || !strings.Contains(err.Error(), "token limits must be positive") {
		t.Fatal(err)
	}
}

func TestRunRequiresBusinessModelConfigurationBeforeReadingHistory(t *testing.T) {
	repo := NewMemoryRepository()
	telegram := newFakeTelegram()
	repo.Rules[1] = mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 86400)
	service := newTestService(t, repo, telegram, &fakeGenerator{})
	_, err := service.RunRule(context.Background(), 1, nil)
	var unavailable *SummaryModelUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "/summary model set") {
		t.Fatal(err)
	}
	if len(telegram.fetches) != 0 {
		t.Fatal("history was read")
	}
}

func TestRunMergesMessagesGroundsModelOutputAndSendsToTopic(t *testing.T) {
	repo := configuredRepo(t, "test", "structured-test")
	telegram := newFakeTelegram()
	telegram.fetched = FetchedSummaryMessages{
		Source:    telegram.endpoints["source"],
		ChatKind:  ChatChannel,
		ChatTitle: "Source channel",
		Messages:  []SummaryMessage{testMessage(10, "first", 0, 7, "Alice", false), testMessage(11, "second", 1, 7, "Alice", false)},
	}
	owner := " Alice "
	generator := &fakeGenerator{fn: func(string, string) (SummaryDocument, error) {
		return SummaryDocument{Topics: []SummaryTopic{{
			Title:              "  Release   update ",
			Summary:            " Version   one shipped. ",
			EvidenceMessageIDs: []int{10, 11, 999, 10},
			Participants:       []string{" Alice ", "Alice"},
			Decisions:          []string{" Published "},
			ActionItems:        []SummaryActionItem{{Task: " Verify ", Owner: &owner}},
		}}}, nil
	}}
	rule := mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 3600)
	repo.Rules[1] = rule
	service := newTestService(t, repo, telegram, generator)
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.MessageCount != 2 || execution.TopicCount != 1 {
		t.Fatalf("%+v", execution)
	}
	if len(telegram.sent) != 1 || telegram.sent[0].destination != mustEndpoint(-1002, intPtr(42), nil) {
		t.Fatalf("%+v", telegram.sent)
	}
	sent := telegram.sent[0].text
	for _, part := range []string{"Release update", "https://t.me/source_channel/10", "消息范围: 10-11"} {
		if !strings.Contains(sent, part) {
			t.Fatal(sent)
		}
	}
	if strings.Contains(sent, "https://t.me/source_channel/11") || strings.Contains(sent, "/999") {
		t.Fatal(sent)
	}
	prompts := generator.snapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0][1], `"message_ids":[10,11]`) {
		t.Fatal(prompts)
	}
	if !reflect.DeepEqual(generator.configs, []SummaryModelConfig{*repo.ModelConfig}) {
		t.Fatalf("%+v vs %+v", generator.configs, repo.ModelConfig)
	}
	if len(repo.Runs) != 1 || repo.Runs[0].MessageCount != 2 {
		t.Fatalf("%+v", repo.Runs)
	}
	if !reflect.DeepEqual(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs, []int{10, 11}) {
		t.Fatal(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs)
	}
	if len(telegram.fetches) != 1 || telegram.fetches[0].source != rule.Source || telegram.fetches[0].limit != nil {
		t.Fatalf("%+v", telegram.fetches)
	}
	age := time.Since(telegram.fetches[0].since)
	if age <= 59*time.Minute || age >= 61*time.Minute {
		t.Fatal(age)
	}
}

func TestSameChatSummaryExcludesOutgoingMessages(t *testing.T) {
	repo := configuredRepo(t, "test", "structured-test")
	telegram := newFakeTelegram()
	endpoint := telegram.endpoints["source"]
	telegram.endpoints["destination"] = endpoint
	telegram.fetched = FetchedSummaryMessages{
		Source:    endpoint,
		ChatKind:  ChatGroup,
		ChatTitle: "Shared group",
		Messages: []SummaryMessage{
			testMessage(10, "有效信息", 0, 7, "Alice", false),
			testMessage(11, "上一条机器人总结", 0, 999, "Bot", true),
		},
	}
	generator := &fakeGenerator{fn: func(string, string) (SummaryDocument, error) {
		return SummaryDocument{Topics: []SummaryTopic{{Title: "主题", Summary: "结论", EvidenceMessageIDs: []int{10}}}}, nil
	}}
	repo.Rules[1] = mustRule(1, endpoint, endpoint, 86400)
	service := newTestService(t, repo, telegram, generator)
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.MessageCount != 1 {
		t.Fatal(execution.MessageCount)
	}
	prompt := generator.snapshot()[0][1]
	if !strings.Contains(prompt, `"message_ids":[10]`) || strings.Contains(prompt, "11") {
		t.Fatal(prompt)
	}
}

func TestRunTreatsAnEmptyDocumentAsNoUsefulInformation(t *testing.T) {
	repo := configuredRepo(t, "test", "structured-test")
	telegram := newFakeTelegram()
	telegram.fetched = FetchedSummaryMessages{
		Source:    telegram.endpoints["source"],
		ChatKind:  ChatGroup,
		ChatTitle: "Chatty group",
		Messages:  []SummaryMessage{testMessage(10, "早上好", 0, 7, "Alice", false)},
	}
	repo.Rules[1] = mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 86400)
	service := newTestService(t, repo, telegram, &fakeGenerator{fn: func(string, string) (SummaryDocument, error) {
		return SummaryDocument{}, nil
	}})
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.TopicCount != 0 || !strings.Contains(telegram.sent[0].text, "没有值得总结的有效信息") {
		t.Fatalf("%+v %s", execution, telegram.sent[0].text)
	}
	if len(repo.Runs) != 1 || len(repo.Runs[0].Document.Topics) != 0 {
		t.Fatalf("%+v", repo.Runs)
	}
}

func TestLargeHistoryUsesMapReduceAndDiscardsInventedEvidence(t *testing.T) {
	repo := NewMemoryRepository()
	config, err := NewSummaryModelConfig("test", "small-context", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	config.InputTokenLimit = 4000
	config.OutputTokenLimit = 500
	config, err = config.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	repo.ModelConfig = &config
	telegram := newFakeTelegram()
	telegram.fetched = FetchedSummaryMessages{
		Source:    telegram.endpoints["source"],
		ChatKind:  ChatGroup,
		ChatTitle: "Busy group",
		Messages: []SummaryMessage{
			testMessage(10, strings.Repeat("a", 2200), 0, 7, "Alice", false),
			testMessage(20, strings.Repeat("b", 2200), 10, 8, "Bob", false),
		},
	}
	generator := &fakeGenerator{fn: func(_, user string) (SummaryDocument, error) {
		if strings.Contains(user, "分批摘要候选") {
			return SummaryDocument{Topics: []SummaryTopic{{Title: "Combined", Summary: "A and B", EvidenceMessageIDs: []int{10, 20, 999}}}}, nil
		}
		if strings.Contains(user, `"message_ids":[20]`) {
			return SummaryDocument{Topics: []SummaryTopic{{Title: "Second", Summary: "B", EvidenceMessageIDs: []int{20}}}}, nil
		}
		return SummaryDocument{Topics: []SummaryTopic{{Title: "First", Summary: "A", EvidenceMessageIDs: []int{10}}}}, nil
	}}
	repo.Rules[1] = mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 86400)
	service := newTestService(t, repo, telegram, generator)
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompts := generator.snapshot()
	if execution.TopicCount != 1 || len(prompts) != 3 || !strings.Contains(prompts[len(prompts)-1][1], "分批摘要候选") {
		t.Fatalf("topics %d prompts %d", execution.TopicCount, len(prompts))
	}
	if !reflect.DeepEqual(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs, []int{10, 20}) {
		t.Fatal(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs)
	}
	if strings.Contains(telegram.sent[0].text, "/999") {
		t.Fatal(telegram.sent[0].text)
	}
}

func TestChineseHistoryUsesConservativeBatchesAndHierarchicalReduce(t *testing.T) {
	repo := configuredRepo(t, "test", "structured-test")
	telegram := newFakeTelegram()
	messages := make([]SummaryMessage, 5)
	for index := 0; index < 5; index++ {
		messages[index] = testMessage(10+index, strings.Repeat("中", 4000), index, int64(100+index), fmt.Sprintf("User %d", index), false)
	}
	telegram.fetched = FetchedSummaryMessages{
		Source:    telegram.endpoints["source"],
		ChatKind:  ChatGroup,
		ChatTitle: "Busy group",
		Messages:  messages,
	}
	generator := &fakeGenerator{fn: chineseHistoryDocument}
	repo.Rules[1] = mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 86400)
	service := newTestService(t, repo, telegram, generator)
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	var maps, reduces int
	for _, prompt := range generator.snapshot() {
		if strings.Contains(prompt[1], "请筛选并总结下面 JSON") {
			maps++
		}
		if strings.Contains(prompt[1], "分批摘要候选") {
			reduces++
		}
	}
	if maps != 5 || reduces != 3 || execution.TopicCount != 1 {
		t.Fatalf("maps %d reduces %d topics %d", maps, reduces, execution.TopicCount)
	}
	if !reflect.DeepEqual(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs, []int{10, 11, 12, 13, 14}) {
		t.Fatal(repo.Runs[0].Document.Topics[0].EvidenceMessageIDs)
	}
}

func TestMapAndIndependentReduceGroupsRunWithBoundedConcurrency(t *testing.T) {
	repo := configuredRepo(t, "test", "concurrent-test")
	config := *repo.ModelConfig
	config.MaxConcurrency = 2
	config, err := config.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	repo.ModelConfig = &config
	telegram := newFakeTelegram()
	messages := make([]SummaryMessage, 6)
	for index := 0; index < 6; index++ {
		messages[index] = testMessage(100+index, strings.Repeat("中", 4000), index, int64(100+index), fmt.Sprintf("User %d", index), false)
	}
	telegram.fetched = FetchedSummaryMessages{
		Source:    telegram.endpoints["source"],
		ChatKind:  ChatGroup,
		ChatTitle: "Busy group",
		Messages:  messages,
	}
	repo.Rules[1] = mustRule(1, telegram.endpoints["source"], telegram.endpoints["destination"], 86400)
	generator := newConcurrentGenerator()
	service := newTestService(t, repo, telegram, generator)
	execution, err := service.RunRule(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.TopicCount != 1 || generator.gates["map"].peakValue() != 2 || generator.gates["reduce"].peakValue() != 2 {
		t.Fatalf("topics %d map %d reduce %d", execution.TopicCount, generator.gates["map"].peakValue(), generator.gates["reduce"].peakValue())
	}
}

func chineseHistoryDocument(_, user string) (SummaryDocument, error) {
	if strings.Contains(user, "请筛选并总结") {
		id := firstMessageID(user)
		return SummaryDocument{Topics: []SummaryTopic{{
			Title:              fmt.Sprintf("Map %d", id),
			Summary:            strings.Repeat("中", 1500),
			EvidenceMessageIDs: []int{id},
		}}}, nil
	}
	ids := reduceEvidence(user)
	sort.Ints(ids)
	switch {
	case len(ids) >= 5:
		return SummaryDocument{Topics: []SummaryTopic{{Title: "Final", Summary: "Combined", EvidenceMessageIDs: []int{10, 11, 12, 13, 14}}}}, nil
	case intsContain(ids, 13):
		return SummaryDocument{Topics: []SummaryTopic{{Title: "Group B", Summary: "B", EvidenceMessageIDs: []int{13, 14}}}}, nil
	default:
		return SummaryDocument{Topics: []SummaryTopic{{Title: "Group A", Summary: "A", EvidenceMessageIDs: []int{10, 11, 12}}}}, nil
	}
}

func firstMessageID(user string) int {
	var payload struct {
		Messages []struct {
			MessageIDs []int `json:"message_ids"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(lastLine(user)), &payload); err != nil || len(payload.Messages) == 0 || len(payload.Messages[0].MessageIDs) == 0 {
		return 0
	}
	return payload.Messages[0].MessageIDs[0]
}

func reduceEvidence(user string) []int {
	var payload struct {
		Candidates []struct {
			Topics []struct {
				Evidence []int `json:"evidence_message_ids"`
			} `json:"topics"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(lastLine(user)), &payload); err != nil {
		return nil
	}
	var ids []int
	for _, candidate := range payload.Candidates {
		for _, topic := range candidate.Topics {
			ids = append(ids, topic.Evidence...)
		}
	}
	return ids
}

func lastLine(value string) string {
	if index := strings.LastIndex(value, "\n"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func intsContain(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func strPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }

func mustEndpoint(chatID int64, topicID *int, username *string) SummaryEndpoint {
	endpoint, err := NewSummaryEndpoint(chatID, topicID, username)
	if err != nil {
		panic(err)
	}
	return endpoint
}

func mustRule(id int, source, destination SummaryEndpoint, window int) SummaryRule {
	rule, err := NewSummaryRule(id, source, destination, window, true)
	if err != nil {
		panic(err)
	}
	return rule
}

func testMessage(id int, text string, minute int, senderID int64, senderName string, outgoing bool) SummaryMessage {
	sender := senderID
	return SummaryMessage{
		Refs:       []contracts.MessageRef{{ChatID: -1001, MessageID: id}},
		OccurredAt: time.Date(2026, 8, 6, 10, minute, 0, 0, time.UTC),
		SenderName: senderName,
		Text:       text,
		SenderID:   &sender,
		Links:      []string{},
		Outgoing:   outgoing,
	}
}

func configuredRepo(t *testing.T, provider, model string) *MemoryRepository {
	t.Helper()
	repo := NewMemoryRepository()
	config, err := NewSummaryModelConfig(provider, model, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo.ModelConfig = &config
	return repo
}

func newTestService(t *testing.T, repo *MemoryRepository, telegram Telegram, generator Generator) *SummarizerService {
	t.Helper()
	service, err := NewSummarizerService(repo, repo, repo, telegram, generator)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fetchRecord struct {
	source SummaryEndpoint
	since  time.Time
	limit  *int
}

type sentRecord struct {
	destination SummaryEndpoint
	text        string
}

type fakeTelegram struct {
	endpoints map[string]SummaryEndpoint
	fetched   FetchedSummaryMessages
	fetches   []fetchRecord
	sent      []sentRecord
}

func newFakeTelegram() *fakeTelegram {
	source := mustEndpoint(-1001, nil, strPtr("source_channel"))
	destination := mustEndpoint(-1002, intPtr(42), nil)
	return &fakeTelegram{
		endpoints: map[string]SummaryEndpoint{"source": source, "destination": destination},
		fetched:   FetchedSummaryMessages{Source: source, ChatKind: ChatChannel, ChatTitle: "Source"},
	}
}

func (f *fakeTelegram) ResolveEndpoint(_ context.Context, reference string) (SummaryEndpoint, error) {
	endpoint, ok := f.endpoints[reference]
	if !ok {
		return SummaryEndpoint{}, &ValueError{Msg: "unknown reference"}
	}
	return endpoint, nil
}

func (f *fakeTelegram) FetchRecent(_ context.Context, source SummaryEndpoint, since time.Time, limit *int) (FetchedSummaryMessages, error) {
	f.fetches = append(f.fetches, fetchRecord{source: source, since: since, limit: limit})
	return f.fetched, nil
}

func (f *fakeTelegram) SendText(_ context.Context, destination SummaryEndpoint, text string) (contracts.MessageRef, error) {
	f.sent = append(f.sent, sentRecord{destination: destination, text: text})
	return contracts.MessageRef{ChatID: destination.ChatID, MessageID: 100 + len(f.sent)}, nil
}

type fakeGenerator struct {
	mu         sync.Mutex
	fn         func(system, user string) (SummaryDocument, error)
	prompts    [][2]string
	configs    []SummaryModelConfig
	resetCalls int
}

func (g *fakeGenerator) Reset(context.Context) error {
	g.mu.Lock()
	g.resetCalls++
	g.mu.Unlock()
	return nil
}

func (g *fakeGenerator) Generate(_ context.Context, config SummaryModelConfig, systemPrompt, userPrompt string) (SummaryDocument, error) {
	g.mu.Lock()
	g.prompts = append(g.prompts, [2]string{systemPrompt, userPrompt})
	g.configs = append(g.configs, config)
	fn := g.fn
	g.mu.Unlock()
	if fn == nil {
		return SummaryDocument{}, errors.New("unexpected generate")
	}
	return fn(systemPrompt, userPrompt)
}

func (g *fakeGenerator) snapshot() [][2]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][2]string(nil), g.prompts...)
}

type concurrentGenerator struct {
	gates map[string]*concurrencyGate
}

func newConcurrentGenerator() *concurrentGenerator {
	return &concurrentGenerator{
		gates: map[string]*concurrencyGate{"map": newConcurrencyGate(), "reduce": newConcurrencyGate()},
	}
}

func (g *concurrentGenerator) Reset(context.Context) error { return nil }

func (g *concurrentGenerator) Generate(ctx context.Context, _ SummaryModelConfig, _, userPrompt string) (SummaryDocument, error) {
	phase, evidence, err := promptPhase(userPrompt)
	if err != nil {
		return SummaryDocument{}, err
	}
	gate := g.gates[phase]
	if err := gate.enter(ctx); err != nil {
		return SummaryDocument{}, err
	}
	defer gate.leave()
	minID := evidence[0]
	for _, id := range evidence[1:] {
		if id < minID {
			minID = id
		}
	}
	return SummaryDocument{Topics: []SummaryTopic{{
		Title:              fmt.Sprintf("%s-%d", phase, minID),
		Summary:            strings.Repeat("中", 1500),
		EvidenceMessageIDs: evidence,
	}}}, nil
}

func promptPhase(user string) (string, []int, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lastLine(user)), &probe); err != nil {
		return "", nil, err
	}
	if raw, ok := probe["messages"]; ok {
		var messages []struct {
			MessageIDs []int `json:"message_ids"`
		}
		if err := json.Unmarshal(raw, &messages); err != nil {
			return "", nil, err
		}
		var ids []int
		for _, message := range messages {
			ids = append(ids, message.MessageIDs...)
		}
		return "map", ids, nil
	}
	return "reduce", reduceEvidence(user), nil
}

type concurrencyGate struct {
	mu     sync.Mutex
	active int
	peak   int
	ready  chan struct{}
	once   sync.Once
}

func newConcurrencyGate() *concurrencyGate {
	return &concurrencyGate{ready: make(chan struct{})}
}

func (g *concurrencyGate) enter(ctx context.Context) error {
	g.mu.Lock()
	g.active++
	if g.active > g.peak {
		g.peak = g.active
	}
	if g.active >= 2 {
		g.once.Do(func() { close(g.ready) })
	}
	g.mu.Unlock()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-g.ready:
		return nil
	case <-timer.C:
		return errors.New("concurrency rendezvous timed out")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *concurrencyGate) leave() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

func (g *concurrencyGate) peakValue() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}
