package observability

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// JSONHandler writes one JSON object per log record and redacts secrets.
type JSONHandler struct {
	level slog.Leveler
	out   io.Writer
	attrs []slog.Attr
	group string
}

// NewJSONHandler writes to out, or stderr when out is nil.
func NewJSONHandler(level slog.Level, out io.Writer) *JSONHandler {
	if out == nil {
		out = os.Stderr
	}
	return &JSONHandler{level: level, out: out}
}

func (h *JSONHandler) Enabled(_ context.Context, level slog.Level) bool {
	min := slog.LevelInfo
	if h.level != nil {
		min = h.level.Level()
	}
	return level >= min
}

func (h *JSONHandler) Handle(_ context.Context, record slog.Record) error {
	payload := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"level":     record.Level.String(),
		"logger":    "yukibot",
		"message":   record.Message,
	}
	record.Attrs(func(attr slog.Attr) bool {
		key := attr.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		payload[key] = redact(key, attr.Value.Any())
		return true
	})
	for _, attr := range h.attrs {
		payload[attr.Key] = redact(attr.Key, attr.Value.Any())
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = h.out.Write(encoded)
	return err
}

func (h *JSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *JSONHandler) WithGroup(name string) slog.Handler {
	next := *h
	if next.group == "" {
		next.group = name
	} else {
		next.group = next.group + "." + name
	}
	return &next
}

func redact(key string, value any) any {
	folded := strings.ToLower(key)
	for _, secret := range []string{"api_hash", "password", "secret", "session", "token"} {
		if strings.Contains(folded, secret) {
			return "[redacted]"
		}
	}
	return value
}

// NewLogger installs a JSON logger at the given level name.
func NewLogger(level string, out io.Writer) *slog.Logger {
	parsed := slog.LevelInfo
	switch strings.ToUpper(level) {
	case "DEBUG":
		parsed = slog.LevelDebug
	case "WARN", "WARNING":
		parsed = slog.LevelWarn
	case "ERROR":
		parsed = slog.LevelError
	case "CRITICAL":
		parsed = slog.LevelError + 4
	}
	return slog.New(NewJSONHandler(parsed, out))
}
