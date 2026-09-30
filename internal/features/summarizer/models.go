package summarizer

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// SummaryChatKind is the source conversation kind used to specialize prompts.
type SummaryChatKind string

const (
	ChatPrivate SummaryChatKind = "private"
	ChatGroup   SummaryChatKind = "group"
	ChatChannel SummaryChatKind = "channel"
)

// SummaryPromptPreset selects a built-in summary preference.
type SummaryPromptPreset string

const (
	PromptFocused   SummaryPromptPreset = "focused"
	PromptDecisions SummaryPromptPreset = "decisions"
	PromptTechnical SummaryPromptPreset = "technical"
	PromptDigest    SummaryPromptPreset = "digest"
)

// SummaryModelConfig is the command-managed model configuration.
// Timeout is a duration; temperature stays a float. An empty API key, base URL,
// or custom prompt means unset. String formatting redacts the API key.
type SummaryModelConfig struct {
	Provider         string
	Model            string
	APIKey           string
	BaseURL          string
	InputTokenLimit  int
	OutputTokenLimit int
	Temperature      float64
	Timeout          time.Duration
	MaxRetries       int
	PromptPreset     SummaryPromptPreset
	CustomPrompt     string
	MaxConcurrency   int
}

// NewSummaryModelConfig validates a config and applies Python dataclass defaults.
// Nil apiKey or baseURL means unset.
func NewSummaryModelConfig(provider, model string, apiKey, baseURL *string) (SummaryModelConfig, error) {
	cfg := SummaryModelConfig{
		Provider:         provider,
		Model:            model,
		InputTokenLimit:  32768,
		OutputTokenLimit: 4096,
		Temperature:      0.1,
		Timeout:          120 * time.Second,
		MaxRetries:       2,
		PromptPreset:     PromptFocused,
		MaxConcurrency:   3,
	}
	if apiKey != nil {
		cfg.APIKey = *apiKey
	}
	if baseURL != nil {
		cfg.BaseURL = *baseURL
	}
	return cfg.Normalized()
}

// Normalized trims fields and checks the same invariants as the Python model.
func (c SummaryModelConfig) Normalized() (SummaryModelConfig, error) {
	c.Provider = casefold(strings.TrimSpace(c.Provider))
	if c.Provider == "" || strings.Contains(c.Provider, "/") || strings.ContainsFunc(c.Provider, unicode.IsSpace) {
		return SummaryModelConfig{}, &ValueError{Msg: "summary provider must be one provider name"}
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" {
		return SummaryModelConfig{}, &ValueError{Msg: "summary model must not be blank"}
	}
	if c.InputTokenLimit <= 0 || c.OutputTokenLimit <= 0 {
		return SummaryModelConfig{}, &ValueError{Msg: "summary model token limits must be positive"}
	}
	if c.InputTokenLimit <= c.OutputTokenLimit+2000 {
		return SummaryModelConfig{}, &ValueError{Msg: "summary token limits leave no usable input budget"}
	}
	if math.IsNaN(c.Temperature) || c.Temperature < 0 || c.Temperature > 2 {
		return SummaryModelConfig{}, &ValueError{Msg: "summary model temperature must be between 0 and 2"}
	}
	seconds := float64(c.Timeout) / float64(time.Second)
	if !(seconds > 0 && seconds <= 1800) || c.MaxRetries < 0 || c.MaxRetries > 10 {
		return SummaryModelConfig{}, &ValueError{Msg: "summary model timeout and retry count are invalid"}
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 8 {
		return SummaryModelConfig{}, &ValueError{Msg: "summary model concurrency must be between 1 and 8"}
	}
	switch c.PromptPreset {
	case PromptFocused, PromptDecisions, PromptTechnical, PromptDigest:
	default:
		return SummaryModelConfig{}, &ValueError{Msg: "unknown summary prompt preset"}
	}
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.CustomPrompt = strings.TrimSpace(c.CustomPrompt)
	if utf8.RuneCountInString(c.CustomPrompt) > 4000 {
		return SummaryModelConfig{}, &ValueError{Msg: "custom summary prompt must not exceed 4000 characters"}
	}
	return c, nil
}

func (c SummaryModelConfig) redacted() string {
	key := ""
	if c.APIKey != "" {
		key = "<redacted>"
	}
	return fmt.Sprintf(
		"SummaryModelConfig{Provider:%s Model:%s APIKey:%s BaseURL:%s InputTokenLimit:%d OutputTokenLimit:%d Temperature:%v Timeout:%s MaxRetries:%d PromptPreset:%s CustomPrompt:%s MaxConcurrency:%d}",
		c.Provider, c.Model, key, c.BaseURL, c.InputTokenLimit, c.OutputTokenLimit, c.Temperature, c.Timeout, c.MaxRetries, c.PromptPreset, c.CustomPrompt, c.MaxConcurrency,
	)
}

func (c SummaryModelConfig) String() string { return c.redacted() }

func (c SummaryModelConfig) GoString() string { return c.redacted() }

func (c SummaryModelConfig) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, c.redacted())
}

// SummaryEndpoint is one Telegram chat, optionally inside a forum topic.
// TopicID 0 and an empty Username mean unset.
type SummaryEndpoint struct {
	ChatID   int64
	TopicID  int
	Username string
}

// NewSummaryEndpoint validates a chat reference.
// A nil topic or username means unset. An empty username string is rejected.
func NewSummaryEndpoint(chatID int64, topicID *int, username *string) (SummaryEndpoint, error) {
	if chatID == 0 {
		return SummaryEndpoint{}, &ValueError{Msg: "chat_id must not be zero"}
	}
	endpoint := SummaryEndpoint{ChatID: chatID}
	if topicID != nil {
		if *topicID <= 0 {
			return SummaryEndpoint{}, &ValueError{Msg: "topic_id must be positive"}
		}
		endpoint.TopicID = *topicID
	}
	if username != nil {
		normalized, err := normalizeUsername(*username)
		if err != nil {
			return SummaryEndpoint{}, err
		}
		endpoint.Username = normalized
	}
	return endpoint, nil
}

func normalizeUsername(username string) (string, error) {
	normalized := strings.TrimSpace(username)
	normalized = strings.TrimPrefix(normalized, "@")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" || strings.ContainsFunc(normalized, unicode.IsSpace) {
		return "", &ValueError{Msg: "Telegram username must not be empty or contain whitespace"}
	}
	return normalized, nil
}

// SummaryRule is a persisted source-to-destination summary route.
type SummaryRule struct {
	ID            int
	Source        SummaryEndpoint
	Destination   SummaryEndpoint
	WindowSeconds int
	Enabled       bool
}

// NewSummaryRule validates a stored rule.
func NewSummaryRule(id int, source, destination SummaryEndpoint, windowSeconds int, enabled bool) (SummaryRule, error) {
	if id <= 0 {
		return SummaryRule{}, &ValueError{Msg: "summary rule id must be positive"}
	}
	if err := validateWindow(windowSeconds); err != nil {
		return SummaryRule{}, err
	}
	return SummaryRule{
		ID:            id,
		Source:        source,
		Destination:   destination,
		WindowSeconds: windowSeconds,
		Enabled:       enabled,
	}, nil
}

// SummaryRuleDraft is a rule that does not yet have an identifier.
type SummaryRuleDraft struct {
	Source        SummaryEndpoint
	Destination   SummaryEndpoint
	WindowSeconds int
	Enabled       bool
}

// NewSummaryRuleDraft validates a rule before it is stored.
func NewSummaryRuleDraft(source, destination SummaryEndpoint, windowSeconds int, enabled bool) (SummaryRuleDraft, error) {
	if err := validateWindow(windowSeconds); err != nil {
		return SummaryRuleDraft{}, err
	}
	return SummaryRuleDraft{
		Source:        source,
		Destination:   destination,
		WindowSeconds: windowSeconds,
		Enabled:       enabled,
	}, nil
}

// Bind assigns a positive identifier.
func (d SummaryRuleDraft) Bind(ruleID int) (SummaryRule, error) {
	return NewSummaryRule(ruleID, d.Source, d.Destination, d.WindowSeconds, d.Enabled)
}

// Matches reports whether draft and rule describe the same route and window.
// Usernames and the enabled flag are ignored.
func (d SummaryRuleDraft) Matches(rule SummaryRule) bool {
	return d.Source.ChatID == rule.Source.ChatID &&
		d.Source.TopicID == rule.Source.TopicID &&
		d.Destination.ChatID == rule.Destination.ChatID &&
		d.Destination.TopicID == rule.Destination.TopicID &&
		d.WindowSeconds == rule.WindowSeconds
}

func validateWindow(seconds int) error {
	if seconds < 60 || seconds > 30*86400 {
		return &ValueError{Msg: "summary window must be between 60 seconds and 30 days"}
	}
	return nil
}

// GroupedID is an optional Telegram album identifier.
// The zero value is unset. Numeric zero is set but falsy, matching Python.
type GroupedID struct {
	kind   int
	number int64
	text   string
}

// NumericGroupedID returns a numeric album id. Zero is set but falsy.
func NumericGroupedID(n int64) GroupedID { return GroupedID{kind: 1, number: n} }

// TextGroupedID returns a string album id.
func TextGroupedID(s string) GroupedID { return GroupedID{kind: 2, text: s} }

// IsSet reports whether the id was provided, including numeric zero.
func (g GroupedID) IsSet() bool { return g.kind != 0 }

// Truthy reports whether Python would treat the value as true.
func (g GroupedID) Truthy() bool {
	switch g.kind {
	case 1:
		return g.number != 0
	case 2:
		return g.text != ""
	default:
		return false
	}
}

// Equal compares kind and value, including two unset ids.
func (g GroupedID) Equal(other GroupedID) bool {
	return g.kind == other.kind && g.number == other.number && g.text == other.text
}

// SummaryMessage is one useful chat message, or several merged ones.
type SummaryMessage struct {
	Refs             []contracts.MessageRef
	OccurredAt       time.Time
	SenderName       string
	Text             string
	SenderID         *int64
	ReplyToMessageID *int
	GroupedID        GroupedID
	ForwardedFrom    string
	Links            []string
	Outgoing         bool
}

// NewSummaryMessage validates message invariants from the Python model.
func NewSummaryMessage(message SummaryMessage) (SummaryMessage, error) {
	if len(message.Refs) == 0 {
		return SummaryMessage{}, &ValueError{Msg: "summary message refs must not be empty"}
	}
	for i, ref := range message.Refs {
		validated, err := contracts.NewMessageRef(ref.ChatID, ref.MessageID)
		if err != nil {
			return SummaryMessage{}, &ValueError{Msg: err.Error()}
		}
		if validated.ChatID != message.Refs[0].ChatID {
			return SummaryMessage{}, &ValueError{Msg: "merged summary messages must belong to one chat"}
		}
		message.Refs[i] = validated
	}
	if strings.TrimSpace(message.SenderName) == "" {
		return SummaryMessage{}, &ValueError{Msg: "sender_name must not be blank"}
	}
	if strings.TrimSpace(message.Text) == "" {
		return SummaryMessage{}, &ValueError{Msg: "summary message text must not be blank"}
	}
	if message.Links == nil {
		message.Links = []string{}
	}
	return message, nil
}

// MessageIDs returns the Telegram message ids carried by the refs.
func (m SummaryMessage) MessageIDs() []int {
	ids := make([]int, len(m.Refs))
	for i, ref := range m.Refs {
		ids[i] = ref.MessageID
	}
	return ids
}

// FetchedSummaryMessages is one history page from the source chat.
type FetchedSummaryMessages struct {
	Source    SummaryEndpoint
	ChatKind  SummaryChatKind
	ChatTitle string
	Messages  []SummaryMessage
}

// SummaryActionItem is one grounded task. Nil owner or deadline means unset.
type SummaryActionItem struct {
	Task     string
	Owner    *string
	Deadline *string
}

// SummaryTopic is one grounded subject in a summary document.
type SummaryTopic struct {
	Title              string
	Summary            string
	EvidenceMessageIDs []int
	Participants       []string
	Decisions          []string
	ActionItems        []SummaryActionItem
	OpenQuestions      []string
}

// SummaryDocument is the structured model output after optional grounding.
type SummaryDocument struct {
	Topics []SummaryTopic
}

// SummaryExecution is the result of sending one summary.
type SummaryExecution struct {
	Rule         SummaryRule
	MessageCount int
	TopicCount   int
	SentMessages []contracts.MessageRef
}

// SummaryRun is one successful generation persisted for history.
type SummaryRun struct {
	RuleID         int
	StartedAt      time.Time
	CompletedAt    time.Time
	FirstMessageID int
	LastMessageID  int
	MessageCount   int
	Provider       string
	Model          string
	PromptVersion  int
	Document       SummaryDocument
}

// NewSummaryRun validates a completed run.
func NewSummaryRun(run SummaryRun) (SummaryRun, error) {
	if run.RuleID <= 0 {
		return SummaryRun{}, &ValueError{Msg: "summary run rule id must be positive"}
	}
	if run.MessageCount <= 0 {
		return SummaryRun{}, &ValueError{Msg: "summary run message count must be positive"}
	}
	if run.FirstMessageID <= 0 || run.LastMessageID < run.FirstMessageID {
		return SummaryRun{}, &ValueError{Msg: "summary run message range is invalid"}
	}
	return run, nil
}
