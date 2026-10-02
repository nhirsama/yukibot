package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/nhirsama/yukibot/internal/contracts"
)

type capturePublisher struct{ events []any }

func (p *capturePublisher) Publish(_ context.Context, event any) error {
	p.events = append(p.events, event)
	return nil
}

func TestEventSourcePublishesAllNormalizedEventKinds(t *testing.T) {
	publisher := &capturePublisher{}
	source := NewEventSource(nil, publisher, nil, time.Second)
	source.accepting = true
	ctx := context.Background()
	message := &tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 123}, Message: "/route add -1001 -2001"}
	if err := source.onMessage(ctx, tg.Entities{}, message, false); err != nil {
		t.Fatal(err)
	}
	if err := source.onMessage(ctx, tg.Entities{}, message, true); err != nil {
		t.Fatal(err)
	}
	if err := source.onDelete(ctx, nil, []int{10}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 3 {
		t.Fatalf("events=%v", publisher.events)
	}
	received, ok := publisher.events[0].(contracts.TelegramMessageReceived)
	if !ok || received.Message.SenderID == nil || *received.Message.SenderID != 123 || received.Message.Text != message.Message {
		t.Fatalf("normalized command=%+v", publisher.events[0])
	}
	if _, ok := publisher.events[1].(contracts.TelegramMessageEdited); !ok {
		t.Fatal("missing edit")
	}
	if _, ok := publisher.events[2].(contracts.TelegramMessagesDeleted); !ok {
		t.Fatal("missing delete")
	}
	if err := source.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := source.onMessage(ctx, tg.Entities{}, message, false); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 3 {
		t.Fatal("accepted after shutdown")
	}
}
