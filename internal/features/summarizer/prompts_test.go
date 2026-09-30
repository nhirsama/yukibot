package summarizer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestPromptVersion(t *testing.T) {
	if PromptVersion != 2 {
		t.Fatalf("prompt version %d", PromptVersion)
	}
}

func TestFocusedPromptFiltersChatterAndAllowsNoTopics(t *testing.T) {
	preference := PromptPreference(PromptFocused, "")
	if estimateTokens(preference) != 228 {
		t.Fatalf("focused preference tokens = %d, want 228", estimateTokens(preference))
	}
	source := FetchedSummaryMessages{
		Source:    mustEndpoint(-1001, nil, nil),
		ChatKind:  ChatGroup,
		ChatTitle: "Test group",
	}
	message := SummaryMessage{
		Refs:       []contracts.MessageRef{{ChatID: -1001, MessageID: 10}},
		OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
		SenderName: "Alice",
		Text:       "hello",
	}
	raw, err := marshalCompact(map[string]any{"message_ids": message.MessageIDs(), "text": message.Text})
	if err != nil {
		t.Fatal(err)
	}
	system, user := MapPrompts(source, []json.RawMessage{json.RawMessage(raw)}, preference)
	for _, part := range []string{"问候", "与主题无关的闲聊", "没有值得总结的有效信息, 返回空 topics"} {
		if !strings.Contains(system, part) {
			t.Fatalf("system missing %q", part)
		}
	}
	if !strings.Contains(user, "筛选并总结") {
		t.Fatalf("user %s", user)
	}
}

func TestAllPresetsExistAndCustomPreferenceIsApplied(t *testing.T) {
	if len(PresetDefinitions) != 4 || len(PresetOrder) != 4 {
		t.Fatalf("presets %d order %d", len(PresetDefinitions), len(PresetOrder))
	}
	for _, preset := range []SummaryPromptPreset{PromptFocused, PromptDecisions, PromptTechnical, PromptDigest} {
		if _, ok := PresetDefinitions[preset]; !ok {
			t.Fatalf("missing %s", preset)
		}
	}
	got := PromptPreference(PromptDigest, "只保留发布版本、故障根因和验证结果")
	want := "用户自定义总结偏好:\n只保留发布版本、故障根因和验证结果"
	if got != want {
		t.Fatalf("%q", got)
	}
}
