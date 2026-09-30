package forwarder

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func pollMessage(id int) IncomingMessage {
	return IncomingMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: id},
		ContentType: contracts.ContentText,
		OccurredAt:  time.Unix(100, 0).UTC(),
		Text:        fmt.Sprintf("message %d", id),
	}
}

func pollingRoute() Route {
	return mustRoute(1, mustSource(-1001, SourceConfig{
		Username:  "source",
		PollEvery: 300 * time.Second,
		Polled:    true,
	}), mustDestination(-2001, DestinationConfig{}))
}

func TestFirstPollInitializesCursorWithoutForwardingHistory(t *testing.T) {
	routes := mustRoutes(pollingRoute())
	cursors := NewInMemoryPollCursorRepository(nil)
	sources := newFakeSources()
	sources.latest = 50
	sources.messages = []IncomingMessage{pollMessage(40), pollMessage(50)}
	bus := newMemBus()
	var received []contracts.TelegramMessageReceived
	if _, err := bus.SubscribeReceived(func(_ context.Context, event contracts.TelegramMessageReceived) error {
		received = append(received, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	poller, err := NewSourcePoller(routes, cursors, sources, bus, SourcePollerConfig{})
	if err != nil {
		t.Fatal(err)
	}

	count, err := poller.PollOnceAt(context.Background(), time.Unix(100, 0).UTC())
	if err != nil || count != 0 {
		t.Fatalf("count %d err %v", count, err)
	}
	cursor, ok, err := cursors.Get(context.Background(), -1001)
	if err != nil || !ok || cursor != (PollCursor{SourceChatID: -1001, LastMessageID: 50}) {
		t.Fatalf("cursor %#v ok %v err %v", cursor, ok, err)
	}
	if len(received) != 0 || len(sources.fetches) != 0 {
		t.Fatalf("received %d fetches %#v", len(received), sources.fetches)
	}
	if len(sources.prepared) != 1 || sources.prepared[0].join || sources.prepared[0].source != pollingRoute().Source {
		t.Fatalf("prepared %#v", sources.prepared)
	}
}

func TestPollingPagesInOrderAndObeysInterval(t *testing.T) {
	routes := mustRoutes(pollingRoute())
	cursor, err := NewPollCursor(-1001, 10)
	if err != nil {
		t.Fatal(err)
	}
	cursors := NewInMemoryPollCursorRepository([]PollCursor{cursor})
	sources := newFakeSources()
	sources.latest = 13
	sources.messages = []IncomingMessage{pollMessage(11), pollMessage(12), pollMessage(13)}
	bus := newMemBus()
	var received []contracts.TelegramMessageReceived
	if _, err := bus.SubscribeReceived(func(_ context.Context, event contracts.TelegramMessageReceived) error {
		received = append(received, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	poller, err := NewSourcePoller(routes, cursors, sources, bus, SourcePollerConfig{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	count, err := poller.PollOnceAt(ctx, time.Unix(100, 0).UTC())
	if err != nil || count != 3 {
		t.Fatalf("count %d err %v", count, err)
	}
	if len(received) != 3 || received[0].Message.Ref.MessageID != 11 || received[1].Message.Ref.MessageID != 12 || received[2].Message.Ref.MessageID != 13 {
		t.Fatalf("received %#v", received)
	}
	stored, ok, err := cursors.Get(ctx, -1001)
	if err != nil || !ok || stored.LastMessageID != 13 {
		t.Fatalf("cursor %#v ok %v err %v", stored, ok, err)
	}
	if len(sources.fetches) != 2 || sources.fetches[0] != [2]int{10, 2} || sources.fetches[1] != [2]int{12, 2} {
		t.Fatalf("fetches %#v", sources.fetches)
	}

	count, err = poller.PollOnceAt(ctx, time.Unix(399, 0).UTC())
	if err != nil || count != 0 || len(sources.fetches) != 2 {
		t.Fatalf("early count %d fetches %#v err %v", count, sources.fetches, err)
	}
	count, err = poller.PollOnceAt(ctx, time.Unix(400, 0).UTC())
	if err != nil || count != 0 || sources.fetches[len(sources.fetches)-1] != [2]int{13, 2} {
		t.Fatalf("due count %d fetches %#v err %v", count, sources.fetches, err)
	}
}
