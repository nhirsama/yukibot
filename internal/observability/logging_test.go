package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestJSONLogRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("INFO", &buf)
	logger.Info("operation complete", slog.String("feature", "forwarder"), slog.String("api_hash", "do-not-log"))
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["level"] != "INFO" || payload["message"] != "operation complete" || payload["feature"] != "forwarder" {
		t.Fatalf("%v", payload)
	}
	if payload["api_hash"] != "[redacted]" || strings.Contains(buf.String(), "do-not-log") {
		t.Fatalf("secret leaked: %s", buf.String())
	}
}

type credentialLogValue struct{}

func (credentialLogValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("api_key", "do-not-log"))
}

func TestJSONLogRedactsStructuredSecrets(t *testing.T) {
	tests := map[string]slog.Attr{
		"api key": slog.String("api_key", "do-not-log"),
		"header":  slog.String("Authorization", "do-not-log"),
		"group":   slog.Group("config", slog.String("password", "do-not-log")),
		"map": slog.Any("config", map[string]any{
			"nested": map[string]any{"api_key": "do-not-log", "model": "test-model"},
		}),
		"typed map": slog.Any("config", map[string]string{"X-API-Key": "do-not-log"}),
		"slice":     slog.Any("config", []any{map[string]any{"token": "do-not-log"}}),
		"struct":    slog.Any("config", struct{ APIKey string }{APIKey: "do-not-log"}),
		"logvaluer": slog.Any("config", credentialLogValue{}),
	}
	for name, attr := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLogger("INFO", &buf)
			logger.Info("test", attr)
			logger.With(attr).Info("bound")
			for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				if !json.Valid([]byte(line)) || !strings.Contains(line, "[redacted]") || strings.Contains(line, "do-not-log") {
					t.Fatalf("unredacted or invalid record: %s", line)
				}
			}
		})
	}
}

func TestJSONLogPreservesLargeIDsAndSafeFields(t *testing.T) {
	var buf bytes.Buffer
	NewLogger("INFO", &buf).Info("test", "config", map[string]any{
		"id": uint64(18446744073709551615), "model": "safe-model", "api_key": "do-not-log",
	})
	if !strings.Contains(buf.String(), `"id":18446744073709551615`) ||
		!strings.Contains(buf.String(), `"model":"safe-model"`) ||
		strings.Contains(buf.String(), "do-not-log") {
		t.Fatal(buf.String())
	}
}

func TestJSONLogAttributeScopesAndPrecedence(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("INFO", &buf).With("feature", "original")
	logger.WithGroup("request").With("id", "42").WithGroup("").Info("test", "feature", "current")
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["feature"] != "original" || payload["request.id"] != "42" || payload["request.feature"] != "current" {
		t.Fatalf("attribute scope lost: %v", payload)
	}
	buf.Reset()
	logger.Info("test", "feature", "override")
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["feature"] != "override" {
		t.Fatalf("record attribute did not override bound attribute: %v", payload)
	}
}

func TestJSONLogConcurrentDerivedHandlers(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("INFO", &buf)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			child := logger.WithGroup("worker").With("id", i)
			for j := 0; j < 100; j++ {
				child.Info("test", "iteration", j)
			}
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 800 {
		t.Fatalf("got %d records, want 800", len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("corrupted record: %s", line)
		}
	}
}
