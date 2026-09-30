package forwarder

import (
	"context"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestStandaloneFacadeAssemblesGroupedMessages(t *testing.T) {
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	service := NewForwarderService(routes, NewInMemoryMessageLinkRepository(nil), newFakeGateway(), ForwarderOptions{}, nil)
	reports := make(chan ForwardingReport, 1)
	forwarder, err := NewForwarder(service, ForwarderConfig{
		AlbumDelay: 10 * time.Millisecond,
		OnReport: func(_ context.Context, report ForwardingReport) error {
			reports <- report
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, messageID := range []int{11, 10} {
		grouped := 50
		result, err := forwarder.HandleMessage(ctx, IncomingMessage{
			Ref:         MessageRef{ChatID: -1001, MessageID: messageID},
			ContentType: contracts.ContentPhoto,
			OccurredAt:  time.Now().UTC(),
			GroupedID:   grouped,
			Caption:     "album",
		})
		if err != nil || !result.Buffered {
			t.Fatalf("buffered %v err %v", result.Buffered, err)
		}
	}

	select {
	case report := <-reports:
		if report.DeliveredMessages() != 2 {
			t.Fatalf("delivered %d", report.DeliveredMessages())
		}
	case <-time.After(time.Second):
		t.Fatal("album was not flushed")
	}
	if err := forwarder.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
}
