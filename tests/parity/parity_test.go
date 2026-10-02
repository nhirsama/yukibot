// Package parity compares Go behavior with frozen outputs captured from the
// original Python implementation before retirement. It does not require Python.
package parity

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
	"github.com/nhirsama/yukibot/internal/kernel"
	"github.com/nhirsama/yukibot/internal/textutil"
)

//go:embed testdata/python312.json
var pythonBaseline []byte

type messageInput struct {
	Text, Caption, Kind string
	Group               any
	Edited              bool
}

func (m messageInput) message() contracts.TelegramMessage {
	kind := contracts.ContentType(m.Kind)
	if kind == "" {
		kind = contracts.ContentText
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	result := contracts.TelegramMessage{
		Ref:         contracts.MessageRef{ChatID: -1001, MessageID: 10},
		ContentType: kind, OccurredAt: now, Text: m.Text, Caption: m.Caption, GroupedID: m.Group,
	}
	if m.Edited {
		edited := now.Truncate(time.Second)
		result.EditedAt = &edited
	}
	if kind == contracts.ContentService {
		result.Service = &contracts.ServiceMessage{Kind: contracts.ServiceOther}
	}
	return result
}

func evaluate(op string, input json.RawMessage) (any, error) {
	switch op {
	case "command":
		var inputText string
		if err := json.Unmarshal(input, &inputText); err != nil {
			return nil, err
		}
		name, args, ok := kernel.SplitCommand(inputText)
		if !ok {
			return nil, nil
		}
		return []string{name, args}, nil
	case "prompt":
		var v struct{ Kind, Preset, Custom, Stage, Payload string }
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, err
		}
		source := summarizer.FetchedSummaryMessages{ChatKind: summarizer.SummaryChatKind(v.Kind), ChatTitle: "测试 <chat>"}
		preference := summarizer.PromptPreference(summarizer.SummaryPromptPreset(v.Preset), v.Custom)
		var system, user string
		if v.Stage == "map" {
			system, user = summarizer.MapPrompts(source, []json.RawMessage{json.RawMessage(v.Payload)}, preference)
		} else {
			owner := "Alice"
			topic := summarizer.SummaryTopic{
				Title: "部署", Summary: "保留证据", EvidenceMessageIDs: []int{10, 11},
				Participants: []string{"Alice"}, Decisions: []string{"发布"},
				ActionItems:   []summarizer.SummaryActionItem{{Task: "验证", Owner: &owner}},
				OpenQuestions: []string{"何时完成?"},
			}
			system, user = summarizer.ReducePrompts(source, []summarizer.SummaryDocument{{Topics: []summarizer.SummaryTopic{topic}}, {}}, preference)
		}
		return []string{system, user}, nil
	case "casefold":
		table := map[string]string{}
		for r := rune(0); r <= utf8.MaxRune; r++ {
			if !utf8.ValidRune(r) {
				continue
			}
			if folded := textutil.Casefold(string(r)); folded != string(r) {
				table[strconv.Itoa(int(r))] = folded
			}
		}
		return table, nil
	case "filter":
		var v struct {
			Keywords         []string
			Allowed, Blocked []contracts.ContentType
			IncludeService   bool `json:"include_service"`
			Messages         []messageInput
		}
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, err
		}
		filter := forwarder.NewMessageFilter(v.Keywords, v.Allowed, v.Blocked, v.IncludeService)
		messages := make([]contracts.TelegramMessage, len(v.Messages))
		singles := make([]bool, len(v.Messages))
		for i, m := range v.Messages {
			messages[i] = m.message()
			singles[i] = filter.Allows(messages[i])
		}
		return map[string]any{"single": singles, "album": filter.AllowsAlbum(messages)}, nil
	case "source":
		var v struct {
			Chat       int64
			Topic      *int
			OtherChat  int64 `json:"other_chat"`
			OtherTopic *int  `json:"other_topic"`
		}
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, err
		}
		source, err := forwarder.NewSourceEndpoint(v.Chat, forwarder.SourceConfig{TopicID: v.Topic})
		if err != nil {
			return nil, err
		}
		return source.Matches(v.OtherChat, v.OtherTopic), nil
	case "reference":
		var text string
		if err := json.Unmarshal(input, &text); err != nil {
			return nil, err
		}
		ref, err := summarizer.ParseEndpointReference(text)
		if err != nil {
			return nil, err
		}
		var chat any = ref.Username
		if ref.Numeric {
			chat = ref.ChatID
		}
		var topic any
		if ref.HasTopic {
			topic = ref.TopicID
		}
		return map[string]any{"chat": chat, "topic": topic}, nil
	case "jobs":
		var v struct {
			messageInput
			Event string
			Delay float64
			Chat  *int64
			IDs   []int
		}
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, err
		}
		var event any
		switch v.Event {
		case "receive":
			event = contracts.TelegramMessageReceived{Message: v.message()}
		case "edit":
			event = contracts.TelegramMessageEdited{Message: v.message()}
		case "delete":
			deleted := contracts.TelegramMessagesDeleted{ChatID: v.Chat, MessageIDs: v.IDs, OccurredAt: v.message().OccurredAt}
			// Python validates in __post_init__; Go uses explicit Validate.
			// Compare the same contract boundary, not an unchecked Go literal.
			if err := deleted.Validate(); err != nil {
				return nil, err
			}
			event = deleted
		default:
			return nil, fmt.Errorf("unknown event %q", v.Event)
		}
		jobs, err := forwarder.PendingJobsForEvent(event, time.Unix(100, 250000000), time.Duration(v.Delay*float64(time.Second)))
		if err != nil {
			return nil, err
		}
		results := make([]map[string]any, 0, len(jobs))
		for _, job := range jobs {
			ids := []int{10}
			if deleted, ok := job.Event.(contracts.TelegramMessagesDeleted); ok {
				ids = deleted.MessageIDs
			}
			results = append(results, map[string]any{
				"kind": job.Kind, "key": job.DeduplicationKey, "group": job.GroupKey,
				"available": float64(job.AvailableAt.UnixNano()) / 1e9, "ids": ids,
			})
		}
		return results, nil
	default:
		return nil, fmt.Errorf("unknown operation %q", op)
	}
}

func TestPythonParity(t *testing.T) {
	var cases []struct {
		Name, Op string
		Input    json.RawMessage
		Expected map[string]any
	}
	if err := json.Unmarshal(pythonBaseline, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1113 {
		t.Fatalf("incomplete baseline: got %d cases, want 1113", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			result, err := evaluate(tc.Op, tc.Input)
			actual := map[string]any{"result": result}
			if err != nil {
				actual = map[string]any{"error": err.Error()}
			}
			// Canonical JSON normalizes numeric widths and nil pointers only;
			// result values, error messages and list order must match exactly.
			encoded, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			var normalized map[string]any
			if err := json.Unmarshal(encoded, &normalized); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalized, tc.Expected) {
				t.Errorf("input=%s\nGo=%s\nPython=%v", tc.Input, encoded, tc.Expected)
			}
		})
	}
	t.Logf("compared %d cases against the frozen Python 3.12 baseline", len(cases))
}
