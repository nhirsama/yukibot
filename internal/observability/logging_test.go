package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
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
