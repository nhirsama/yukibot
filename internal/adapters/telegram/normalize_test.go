package telegram

import (
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestNormalizePollText(t *testing.T) {
	message := &tg.Message{
		ID:     3,
		PeerID: &tg.PeerUser{UserID: 7},
	}
	message.SetMedia(&tg.MessageMediaPoll{Poll: tg.Poll{
		Question: tg.TextWithEntities{Text: "问题"},
		Answers: []tg.PollAnswerClass{
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "甲"}},
			nil,
			(*tg.PollAnswer)(nil),
			&tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "not a received answer"}},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: " "}},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "乙"}},
		},
	}})
	got, ok := Normalize(message, time.Unix(10, 0).UTC())
	if !ok {
		t.Fatal("expected a message")
	}
	if got.ContentType != contracts.ContentPoll || got.Text != "[投票] 问题 选项: 甲; 乙" || got.Caption != "" {
		t.Fatalf("poll = %+v", got)
	}
}

func TestNormalizeTopicCreateUsesMessageID(t *testing.T) {
	message := &tg.MessageService{
		ID:     9,
		PeerID: &tg.PeerChannel{ChannelID: 1},
		Date:   100,
		Action: &tg.MessageActionTopicCreate{Title: "计划"},
	}
	got, ok := Normalize(message, time.Unix(10, 0).UTC())
	if !ok {
		t.Fatal("expected a service message")
	}
	if got.TopicID == nil || *got.TopicID != 9 || got.ContentType != contracts.ContentService {
		t.Fatalf("topic = %+v", got)
	}
	if got.Service == nil || got.Service.Kind != contracts.ServiceTopicCreated || got.Service.NewTitle != "计划" {
		t.Fatalf("service = %+v", got.Service)
	}
}

func TestNormalizePrivateSenderFallback(t *testing.T) {
	for _, service := range []bool{false, true} {
		t.Run(map[bool]string{false: "message", true: "service"}[service], func(t *testing.T) {
			for _, outgoing := range []bool{false, true} {
				var raw tg.MessageClass = &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 123}, Out: outgoing}
				if service {
					raw = &tg.MessageService{ID: 1, PeerID: &tg.PeerUser{UserID: 123}, Out: outgoing, Action: &tg.MessageActionHistoryClear{}}
				}
				got, ok := Normalize(raw, time.Now())
				if !ok {
					t.Fatal("normalization failed")
				}
				if outgoing && got.SenderID != nil {
					t.Fatal("outgoing recipient was mistaken for sender")
				}
				if !outgoing && (got.SenderID == nil || *got.SenderID != 123) {
					t.Fatal("incoming private sender was lost")
				}
			}
		})
	}
}
