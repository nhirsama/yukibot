package summarizer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEndpointReferenceSupportsChatsAndTopics(t *testing.T) {
	cases := []struct {
		value    string
		numeric  bool
		chatID   int64
		username string
		topicID  int
		hasTopic bool
	}{
		{value: "-1001234567890/42", numeric: true, chatID: -1001234567890, topicID: 42, hasTopic: true},
		{value: "https://t.me/c/1234567890/42", numeric: true, chatID: -1001234567890, topicID: 42, hasTopic: true},
		{value: "https://t.me/public_group/42", username: "@public_group", topicID: 42, hasTopic: true},
		{value: "https://telegram.me/s/public_channel", username: "@public_channel"},
		{value: "@public_channel", username: "@public_channel"},
		{value: "123456", numeric: true, chatID: 123456},
	}
	for _, tc := range cases {
		got, err := ParseEndpointReference(tc.value)
		if err != nil {
			t.Fatalf("%s: %v", tc.value, err)
		}
		if got.Numeric != tc.numeric || got.ChatID != tc.chatID || got.Username != tc.username || got.TopicID != tc.topicID || got.HasTopic != tc.hasTopic {
			t.Fatalf("%s: %+v", tc.value, got)
		}
	}
}

func TestParseEndpointReferenceRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{
		"https://t.me/+invite_hash",
		"https://t.me/joinchat/invite_hash",
		"https://example.com/public_group/42",
		"https://t.me/c/not-a-number/42",
		"",
	} {
		if _, err := ParseEndpointReference(value); err == nil {
			t.Fatalf("%q was accepted", value)
		}
	}
}

func TestMapPromptIsSpecializedByChatKind(t *testing.T) {
	cases := []struct {
		kind   SummaryChatKind
		marker string
	}{
		{ChatPrivate, "这是私聊"},
		{ChatGroup, "这是群聊"},
		{ChatChannel, "这是广播频道"},
	}
	for _, tc := range cases {
		source := FetchedSummaryMessages{Source: mustEndpoint(-1001, nil, nil), ChatKind: tc.kind, ChatTitle: "Source"}
		system, user := MapPrompts(source, []json.RawMessage{json.RawMessage(`{"message_ids":[10],"text":"hello"}`)}, "")
		if !strings.Contains(system, tc.marker) || !strings.Contains(system, "不可信数据") {
			t.Fatalf("%s: %s", tc.kind, system)
		}
		if !strings.Contains(user, `"message_ids":[10]`) {
			t.Fatalf("%s: %s", tc.kind, user)
		}
	}
}
