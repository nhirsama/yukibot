package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResponsesURL(t *testing.T) {
	cases := map[string]string{
		"":                          "https://api.openai.com/v1/responses",
		"https://api.openai.com":    "https://api.openai.com/v1/responses",
		"https://models.example/v1": "https://models.example/v1/responses",
		"https://models.example":    "https://models.example/v1/responses",
	}
	for base, want := range cases {
		if got := responsesURL(base); got != want {
			t.Fatalf("%q -> %s, want %s", base, got, want)
		}
	}
}

func TestGeneratorRetriesEmptyOutputAfterOneSecond(t *testing.T) {
	const outputText = `{"topics":[{"title":"Release","summary":"Version one shipped.","evidence_message_ids":[10],"action_items":[{"task":"Verify","owner":"Alice"}]}]}`
	var mu sync.Mutex
	var bodies [][]byte
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		calls++
		attempt := calls
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "yukibot/0.1.0" {
			t.Errorf("user agent %s", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = io.WriteString(w, sseEvent(t, map[string]any{"type": "response.in_progress"}))
			_, _ = io.WriteString(w, sseEvent(t, map[string]any{"type": "response.completed", "response": map[string]any{}}))
			return
		}
		_, _ = io.WriteString(w, sseEvent(t, map[string]any{"type": "response.output_text.delta", "delta": outputText}))
		_, _ = io.WriteString(w, sseEvent(t, map[string]any{"type": "response.completed", "response": map[string]any{}}))
	}))
	defer server.Close()

	var slept []time.Duration
	generator := NewOpenAISummaryGenerator()
	generator.http = server.Client()
	generator.SetSleep(func(_ context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	})
	base := server.URL + "/v1"
	config, err := NewSummaryModelConfig("openai", "deepseek-v4-flash", strPtr("secret"), &base)
	if err != nil {
		t.Fatal(err)
	}
	document, err := generator.Generate(context.Background(), config, "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := generator.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(document.Topics) != 1 || !reflectEvidence(document, 10) || document.Topics[0].ActionItems[0].Owner == nil || *document.Topics[0].ActionItems[0].Owner != "Alice" {
		t.Fatalf("%+v", document)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("sleeps %v", slept)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests %d", len(bodies))
	}
	assertResponsesRequest(t, bodies[1])
}

func TestGeneratorRetriesUpstreamError(t *testing.T) {
	const outputText = `{"topics":[{"title":"Release","summary":"Version one shipped.","evidence_message_ids":[10]}]}`
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = io.WriteString(w, sseEvent(t, map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"error": map[string]any{"code": "upstream_error", "message": "upstream failed"},
				},
			}))
			return
		}
		_, _ = io.WriteString(w, sseEvent(t, map[string]any{"type": "response.output_text.delta", "delta": outputText}))
	}))
	defer server.Close()
	var slept []time.Duration
	generator := NewOpenAISummaryGenerator()
	generator.http = server.Client()
	generator.SetSleep(func(_ context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	})
	base := server.URL + "/v1"
	config, err := NewSummaryModelConfig("openai", "deepseek-v4-flash", strPtr("secret"), &base)
	if err != nil {
		t.Fatal(err)
	}
	document, err := generator.Generate(context.Background(), config, "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(slept) != 1 || slept[0] != time.Second || !reflectEvidence(document, 10) {
		t.Fatalf("calls %d sleeps %v document %+v", calls, slept, document)
	}
}

func TestGeneratorRejectsNonOpenAIProvider(t *testing.T) {
	generator := NewOpenAISummaryGenerator()
	_, err := generator.Generate(context.Background(), SummaryModelConfig{Provider: "apiarc", Model: "deepseek-v4-flash"}, "system", "user")
	var unavailable *SummaryModelUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "provider must be openai") {
		t.Fatal(err)
	}
}

func TestGeneratorReraisesContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	generator := NewOpenAISummaryGenerator()
	generator.http = server.Client()
	base := server.URL
	config, err := NewSummaryModelConfig("openai", "deepseek-v4-flash", strPtr("secret"), &base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = generator.Generate(ctx, config, "system", "user")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var unavailable *SummaryModelUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatal("canceled error was wrapped")
	}
}

func TestResponsesTimeoutIsIdleNotTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; i < 16; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ": heartbeat\n\n")
				flusher.Flush()
			}
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"topics\\\":[]}\"}\n\n")
	}))
	defer server.Close()
	generator := NewOpenAISummaryGenerator()
	generator.http = server.Client()
	started := time.Now()
	text, err := generator.streamText(context.Background(), SummaryModelConfig{
		Model: "test", BaseURL: server.URL, Timeout: 150 * time.Millisecond,
	}, nil)
	if err != nil || text != `{"topics":[]}` {
		t.Fatalf("active stream aborted: text=%q err=%v", text, err)
	}
	if time.Since(started) <= 150*time.Millisecond {
		t.Fatal("test must outlast the configured idle timeout")
	}
}

func TestResponsesIdleTimeoutAndParentCancellation(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprintf("headers=%t", headers), func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			generator := NewOpenAISummaryGenerator()
			generator.http = server.Client()
			config := SummaryModelConfig{Model: "test", BaseURL: server.URL, Timeout: 50 * time.Millisecond}
			_, err := generator.streamText(context.Background(), config, nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stalled request should time out: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = generator.streamText(ctx, config, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("parent cancellation must survive: %v", err)
			}
		})
	}
}

func sseEvent(t *testing.T, event any) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(raw) + "\n\n"
}

func assertResponsesRequest(t *testing.T, body []byte) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"temperature", "max_output_tokens", "tools", "tool_choice"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("forbidden field %s in %s", key, body)
		}
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Input  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "deepseek-v4-flash" || !request.Stream || len(request.Input) != 2 {
		t.Fatalf("%+v", request)
	}
	if request.Input[0].Role != "system" || !strings.Contains(request.Input[0].Content, "JSON Schema:") || !strings.Contains(request.Input[0].Content, summaryJSONSchema) {
		t.Fatal(request.Input[0].Content)
	}
	if request.Input[1].Role != "user" || request.Input[1].Content != "user" {
		t.Fatal(request.Input[1])
	}
}

func reflectEvidence(document SummaryDocument, id int) bool {
	return len(document.Topics) == 1 && len(document.Topics[0].EvidenceMessageIDs) == 1 && document.Topics[0].EvidenceMessageIDs[0] == id
}

func TestConsumeSSERejectsOversizedMultilineEvent(t *testing.T) {
	// Every line is small enough for Scanner, but the event as a whole is not.
	stream := strings.Repeat("data: "+strings.Repeat("x", 1024)+"\n", 4097) + "\n"
	called := false
	err := consumeSSE(strings.NewReader(stream), func(string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("oversized event accepted: error=%v callback=%v", err, called)
	}
}

func TestConsumeSSERejectsOversizedStream(t *testing.T) {
	// Comments do not contribute to output, but still consume network/CPU resources.
	stream := strings.Repeat(":"+strings.Repeat("x", 1023)+"\n", 16385)
	if err := consumeSSE(strings.NewReader(stream), func(string) error { return nil }); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestApplyStreamEventBoundsAccumulatedOutput(t *testing.T) {
	var text strings.Builder
	text.WriteString(strings.Repeat("x", 4*1024*1024))
	err := applyStreamEvent(`{"type":"response.output_text.delta","delta":"x"}`, &text)
	if err == nil || text.Len() != 4*1024*1024 {
		t.Fatalf("output exceeded limit: error=%v length=%d", err, text.Len())
	}
}

func TestConsumeSSEMultilineAndDone(t *testing.T) {
	var payloads []string
	err := consumeSSE(strings.NewReader(": comment\r\ndata: first\r\ndata: second\r\n\r\ndata: [DONE]\n\ndata: ignored\n\n"), func(data string) error {
		payloads = append(payloads, data)
		return nil
	})
	if err != nil || len(payloads) != 1 || payloads[0] != "first\nsecond" {
		t.Fatalf("error=%v payloads=%v", err, payloads)
	}
}
