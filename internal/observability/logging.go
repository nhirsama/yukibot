package observability

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// JSONHandler writes one JSON object per log record and redacts secrets.
type JSONHandler struct {
	level slog.Leveler
	out   io.Writer
	attrs []slog.Attr
	group string
	mu    *sync.Mutex // Shared by WithAttrs/WithGroup derivatives using the same writer.
}

// NewJSONHandler writes to out, or stderr when out is nil.
func NewJSONHandler(level slog.Level, out io.Writer) *JSONHandler {
	if out == nil {
		out = os.Stderr
	}
	return &JSONHandler{level: level, out: out, mu: &sync.Mutex{}}
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
	for _, attr := range h.attrs {
		appendAttr(payload, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		key := attr.Key
		if h.group != "" && key != "" {
			key = h.group + "." + key
		}
		attr.Key = key
		if key == "" && h.group != "" && attr.Value.Kind() == slog.KindGroup {
			for _, child := range attr.Value.Group() {
				child.Key = h.group + "." + child.Key
				appendAttr(payload, child)
			}
		} else {
			appendAttr(payload, attr)
		}
		return true
	})
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.out.Write(encoded)
	return err
}

func (h *JSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append([]slog.Attr{}, h.attrs...)
	for _, attr := range attrs {
		if h.group != "" {
			// Bind the current scope now, not the scope of a later child handler.
			if attr.Key == "" && attr.Value.Kind() == slog.KindGroup {
				for _, child := range attr.Value.Group() {
					child.Key = h.group + "." + child.Key
					next.attrs = append(next.attrs, child)
				}
				continue
			}
			if attr.Key != "" {
				attr.Key = h.group + "." + attr.Key
			}
		}
		next.attrs = append(next.attrs, attr)
	}
	return &next
}

func (h *JSONHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	if next.group == "" {
		next.group = name
	} else {
		next.group = next.group + "." + name
	}
	return &next
}

func secretKey(key string) bool {
	folded := strings.ToLower(key)
	folded = strings.NewReplacer("_", "", "-", "", ".", "").Replace(folded)
	for _, secret := range []string{"apihash", "apikey", "authorization", "password", "secret", "session", "token"} {
		if strings.Contains(folded, secret) {
			return true
		}
	}
	return false
}

func appendAttr(payload map[string]any, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if secretKey(attr.Key) {
		payload[attr.Key] = "[redacted]"
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := map[string]any{}
		for _, child := range attr.Value.Group() {
			appendAttr(group, child)
		}
		if attr.Key == "" {
			for key, value := range group {
				payload[key] = value
			}
		} else if len(group) > 0 {
			payload[attr.Key] = group
		}
		return
	}
	payload[attr.Key] = redact(attr.Key, attr.Value.Any())
}

func redact(key string, value any) any {
	if secretKey(key) {
		return "[redacted]"
	}
	// Normalize maps, slices and structs (including custom JSON marshalers)
	// before inspecting nested field names. Marshal also rejects cyclic values.
	raw, err := json.Marshal(value)
	if err != nil {
		return "[unserializable]"
	}
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return "[unserializable]"
	}
	return redactJSON(normalized, 0)
}

func redactJSON(value any, depth int) any {
	if depth >= 32 {
		return "[redacted]"
	}
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if secretKey(key) {
				item[key] = "[redacted]"
			} else {
				item[key] = redactJSON(child, depth+1)
			}
		}
	case []any:
		for i, child := range item {
			item[i] = redactJSON(child, depth+1)
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
