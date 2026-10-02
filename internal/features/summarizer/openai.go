package summarizer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const summaryUserAgent = "yukibot/0.1.0"

// Bound untrusted provider data independently of the request timeout.
const (
	maxSSEEventBytes      = 4 * 1024 * 1024
	maxSSEStreamBytes     = 16 * 1024 * 1024
	maxSummaryOutputBytes = 4 * 1024 * 1024
)

// summaryJSONSchema is pydantic 2.13.4 _SummaryOutput.model_json_schema(),
// encoded with ensure_ascii=False and compact separators.
const summaryJSONSchema = `{"$defs":{"_ActionItemOutput":{"properties":{"task":{"maxLength":500,"minLength":1,"title":"Task","type":"string"},"owner":{"anyOf":[{"maxLength":200,"type":"string"},{"type":"null"}],"default":null,"title":"Owner"},"deadline":{"anyOf":[{"maxLength":200,"type":"string"},{"type":"null"}],"default":null,"title":"Deadline"}},"required":["task"],"title":"_ActionItemOutput","type":"object"},"_TopicOutput":{"properties":{"title":{"maxLength":200,"minLength":1,"title":"Title","type":"string"},"summary":{"maxLength":2000,"minLength":1,"title":"Summary","type":"string"},"evidence_message_ids":{"items":{"type":"integer"},"maxItems":10,"minItems":1,"title":"Evidence Message Ids","type":"array"},"participants":{"items":{"type":"string"},"maxItems":30,"title":"Participants","type":"array"},"decisions":{"items":{"type":"string"},"maxItems":10,"title":"Decisions","type":"array"},"action_items":{"items":{"$ref":"#/$defs/_ActionItemOutput"},"maxItems":10,"title":"Action Items","type":"array"},"open_questions":{"items":{"type":"string"},"maxItems":10,"title":"Open Questions","type":"array"}},"required":["title","summary","evidence_message_ids"],"title":"_TopicOutput","type":"object"}},"properties":{"topics":{"items":{"$ref":"#/$defs/_TopicOutput"},"maxItems":12,"title":"Topics","type":"array"}},"required":["topics"],"title":"_SummaryOutput","type":"object"}`

// OpenAISummaryGenerator calls the OpenAI Responses stream API with net/http.
type OpenAISummaryGenerator struct {
	mu     sync.Mutex
	http   *http.Client
	sleep  func(context.Context, time.Duration) error
	config *SummaryModelConfig
}

// NewOpenAISummaryGenerator returns a generator that really sleeps between retries.
func NewOpenAISummaryGenerator() *OpenAISummaryGenerator {
	return &OpenAISummaryGenerator{
		http:  &http.Client{},
		sleep: realSleep,
	}
}

// SetSleep replaces the retry delay. A nil function restores the real sleep.
func (g *OpenAISummaryGenerator) SetSleep(fn func(context.Context, time.Duration) error) {
	if fn == nil {
		fn = realSleep
	}
	g.mu.Lock()
	g.sleep = fn
	g.mu.Unlock()
}

// Reset drops the cached client configuration.
func (g *OpenAISummaryGenerator) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.config = nil
	if g.http != nil {
		g.http.CloseIdleConnections()
	}
	return nil
}

// Generate streams one structured summary. Empty output and upstream_error are retried.
// context.Canceled is returned unchanged.
func (g *OpenAISummaryGenerator) Generate(ctx context.Context, config SummaryModelConfig, systemPrompt, userPrompt string) (SummaryDocument, error) {
	if config.Provider != "openai" {
		return SummaryDocument{}, modelFailure(&ValueError{Msg: "summary model provider must be openai"})
	}
	g.remember(config)
	sleep := g.sleeper()
	input := responseInput(systemPrompt, userPrompt)
	var output string
	var err error
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		output, err = g.streamText(ctx, config, input)
		if err == nil {
			break
		}
		var retryable *retryableResponseError
		if !errors.As(err, &retryable) {
			return SummaryDocument{}, modelFailure(err)
		}
		if attempt == config.MaxRetries {
			return SummaryDocument{}, modelFailure(err)
		}
		if sleepErr := sleep(ctx, time.Second<<attempt); sleepErr != nil {
			return SummaryDocument{}, modelFailure(sleepErr)
		}
	}
	document, err := parseSummaryOutput(output)
	if err != nil {
		return SummaryDocument{}, modelFailure(err)
	}
	return document, nil
}

func (g *OpenAISummaryGenerator) remember(config SummaryModelConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.config != nil && *g.config == config {
		return
	}
	if g.http != nil {
		g.http.CloseIdleConnections()
	}
	copied := config
	g.config = &copied
}

func (g *OpenAISummaryGenerator) sleeper() func(context.Context, time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sleep == nil {
		return realSleep
	}
	return g.sleep
}

func realSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type responseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseRequest struct {
	Model  string            `json:"model"`
	Input  []responseMessage `json:"input"`
	Stream bool              `json:"stream"`
}

func responseInput(systemPrompt, userPrompt string) []responseMessage {
	system := systemPrompt + "\n" +
		"只返回符合下面 JSON Schema 的 JSON 对象, 不要使用 Markdown 或附加说明。\n" +
		"JSON Schema: " + summaryJSONSchema
	return []responseMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: userPrompt},
	}
}

func responsesURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/responses"
}

func (g *OpenAISummaryGenerator) streamText(ctx context.Context, config SummaryModelConfig, input []responseMessage) (string, error) {
	payload, err := marshalCompact(responseRequest{Model: config.Model, Input: input, Stream: true})
	if err != nil {
		return "", &runtimeError{msg: err.Error()}
	}
	reqCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, responsesURL(config.BaseURL), strings.NewReader(payload))
	if err != nil {
		return "", &runtimeError{msg: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", summaryUserAgent)
	if config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	client := g.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", &runtimeError{msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &runtimeError{msg: fmt.Sprintf("Responses API status %d: %s", resp.StatusCode, bytes.TrimSpace(body))}
	}
	var text strings.Builder
	if err := consumeSSE(resp.Body, func(data string) error {
		return applyStreamEvent(data, &text)
	}); err != nil {
		return "", err
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", &retryableResponseError{msg: "Responses API did not return output text"}
	}
	return text.String(), nil
}

func consumeSSE(r io.Reader, fn func(string) error) error {
	limited := &io.LimitedReader{R: r, N: maxSSEStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maxSSEEventBytes)
	var data []string
	eventBytes := 0
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		eventBytes = 0
		if payload == "[DONE]" {
			return errSSEDone
		}
		return fn(payload)
	}
	for scanner.Scan() {
		if limited.N == 0 {
			return &runtimeError{msg: "Responses stream exceeds byte limit"}
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				if errors.Is(err, errSSEDone) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			size := len(value)
			if len(data) > 0 {
				size++ // The newline inserted when joining data lines.
			}
			if size > maxSSEEventBytes-eventBytes {
				return &runtimeError{msg: "Responses stream event exceeds byte limit"}
			}
			eventBytes += size
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &runtimeError{msg: err.Error()}
	}
	if limited.N == 0 {
		return &runtimeError{msg: "Responses stream exceeds byte limit"}
	}
	if err := flush(); err != nil && !errors.Is(err, errSSEDone) {
		return err
	}
	return nil
}

var errSSEDone = errors.New("sse done")

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    json.RawMessage `json:"delta"`
	Message  *string         `json:"message"`
	Response *struct {
		Error             json.RawMessage `json:"error"`
		IncompleteDetails json.RawMessage `json:"incomplete_details"`
	} `json:"response"`
}

type failedErrorBody struct {
	Code json.RawMessage `json:"code"`
}

func applyStreamEvent(data string, text *strings.Builder) error {
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return &runtimeError{msg: "Responses stream error: " + data}
	}
	switch event.Type {
	case "response.output_text.delta":
		if delta, ok := jsonString(event.Delta); ok {
			if len(delta) > maxSummaryOutputBytes-text.Len() {
				return &runtimeError{msg: "Responses output text exceeds byte limit"}
			}
			text.WriteString(delta)
		}
	case "response.failed":
		detail := "None"
		code := ""
		if event.Response != nil && len(event.Response.Error) > 0 && string(event.Response.Error) != "null" {
			detail = string(event.Response.Error)
			var body failedErrorBody
			if json.Unmarshal(event.Response.Error, &body) == nil {
				code = rawScalar(body.Code)
			}
		}
		if code == "upstream_error" {
			return &retryableResponseError{msg: detail}
		}
		return &runtimeError{msg: "Responses stream ended with response.failed: " + detail}
	case "response.incomplete":
		details := "None"
		if event.Response != nil && len(event.Response.IncompleteDetails) > 0 && string(event.Response.IncompleteDetails) != "null" {
			details = string(event.Response.IncompleteDetails)
		}
		return &runtimeError{msg: "Responses stream ended with response.incomplete: " + details}
	case "error":
		message := "None"
		if event.Message != nil {
			message = *event.Message
		}
		return &runtimeError{msg: "Responses stream error: " + message}
	}
	return nil
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

type outputAction struct {
	Task     string  `json:"task"`
	Owner    *string `json:"owner"`
	Deadline *string `json:"deadline"`
}

type outputTopic struct {
	Title              string         `json:"title"`
	Summary            string         `json:"summary"`
	EvidenceMessageIDs []int          `json:"evidence_message_ids"`
	Participants       []string       `json:"participants"`
	Decisions          []string       `json:"decisions"`
	ActionItems        []outputAction `json:"action_items"`
	OpenQuestions      []string       `json:"open_questions"`
}

func parseSummaryOutput(raw string) (SummaryDocument, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return SummaryDocument{}, &validationError{msg: err.Error()}
	}
	topicsRaw, ok := envelope["topics"]
	if !ok || string(topicsRaw) == "null" {
		return SummaryDocument{}, &validationError{msg: "topics is required"}
	}
	var topics []outputTopic
	if err := json.Unmarshal(topicsRaw, &topics); err != nil {
		return SummaryDocument{}, &validationError{msg: err.Error()}
	}
	if len(topics) > 12 {
		return SummaryDocument{}, &validationError{msg: "topics exceeds 12 items"}
	}
	document := SummaryDocument{Topics: make([]SummaryTopic, 0, len(topics))}
	for _, topic := range topics {
		if err := validateOutputTopic(topic); err != nil {
			return SummaryDocument{}, err
		}
		actions := make([]SummaryActionItem, 0, len(topic.ActionItems))
		for _, item := range topic.ActionItems {
			actions = append(actions, SummaryActionItem{Task: item.Task, Owner: item.Owner, Deadline: item.Deadline})
		}
		document.Topics = append(document.Topics, SummaryTopic{
			Title:              topic.Title,
			Summary:            topic.Summary,
			EvidenceMessageIDs: topic.EvidenceMessageIDs,
			Participants:       topic.Participants,
			Decisions:          topic.Decisions,
			ActionItems:        actions,
			OpenQuestions:      topic.OpenQuestions,
		})
	}
	return document, nil
}

func validateOutputTopic(topic outputTopic) error {
	if utf8.RuneCountInString(topic.Title) < 1 || utf8.RuneCountInString(topic.Title) > 200 {
		return &validationError{msg: "title length is invalid"}
	}
	if utf8.RuneCountInString(topic.Summary) < 1 || utf8.RuneCountInString(topic.Summary) > 2000 {
		return &validationError{msg: "summary length is invalid"}
	}
	if len(topic.EvidenceMessageIDs) < 1 || len(topic.EvidenceMessageIDs) > 10 {
		return &validationError{msg: "evidence_message_ids length is invalid"}
	}
	if len(topic.Participants) > 30 || len(topic.Decisions) > 10 || len(topic.ActionItems) > 10 || len(topic.OpenQuestions) > 10 {
		return &validationError{msg: "topic list length is invalid"}
	}
	for _, item := range topic.ActionItems {
		if utf8.RuneCountInString(item.Task) < 1 || utf8.RuneCountInString(item.Task) > 500 {
			return &validationError{msg: "action item task length is invalid"}
		}
		if item.Owner != nil && utf8.RuneCountInString(*item.Owner) > 200 {
			return &validationError{msg: "action item owner is too long"}
		}
		if item.Deadline != nil && utf8.RuneCountInString(*item.Deadline) > 200 {
			return &validationError{msg: "action item deadline is too long"}
		}
	}
	return nil
}

type retryableResponseError struct{ msg string }

func (e *retryableResponseError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

func (e *retryableResponseError) TypeName() string { return "_RetryableResponseError" }

type runtimeError struct{ msg string }

func (e *runtimeError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

func (e *runtimeError) TypeName() string { return "RuntimeError" }

type validationError struct{ msg string }

func (e *validationError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

func (e *validationError) TypeName() string { return "ValidationError" }

func modelFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	name := "Exception"
	var named interface{ TypeName() string }
	if errors.As(err, &named) {
		name = named.TypeName()
	}
	return &SummaryModelUnavailableError{Msg: fmt.Sprintf("消息总结模型调用失败 (%s): %s", name, err.Error())}
}
