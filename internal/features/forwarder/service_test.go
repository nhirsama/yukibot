package forwarder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func TestServicePreservesReplyMapping(t *testing.T) {
	topic := 7
	destinationTopic := 11
	routes := mustRoutes(
		mustRoute(1, mustSource(-1001, SourceConfig{TopicID: &topic}), mustDestination(-2001, DestinationConfig{TopicID: &destinationTopic})),
		mustRoute(2, mustSource(-9999, SourceConfig{}), mustDestination(-2002, DestinationConfig{})),
	)
	routes.mu.Lock()
	first := routes.routes[1]
	first.Filter = NewMessageFilter([]string{"hello"}, nil, nil, false)
	routes.routes[1] = first
	routes.mu.Unlock()
	links := NewInMemoryMessageLinkRepository(nil)
	gateway := newFakeGateway()
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	ctx := context.Background()
	opening := textMessage(10, "hello")
	opening.TopicID = &topic
	reply := textMessage(11, "hello")
	reply.TopicID = &topic
	replyTo := 10
	reply.ReplyToMessageID = &replyTo

	firstReport, err := service.ForwardMessage(ctx, opening)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := service.ForwardMessage(ctx, reply)
	if err != nil {
		t.Fatal(err)
	}
	if firstReport.DeliveredMessages() != 1 || secondReport.DeliveredMessages() != 1 || secondReport.MatchedRoutes != 1 {
		t.Fatalf("reports %#v %#v", firstReport, secondReport)
	}
	if len(gateway.calls) != 2 || gateway.calls[1].reply == nil || *gateway.calls[1].reply != firstReport.Outcomes[0].Destinations[0].MessageID {
		t.Fatalf("reply call %#v", gateway.calls)
	}
	count, err := links.Count(ctx)
	if err != nil || count != 2 {
		t.Fatalf("links %d %v", count, err)
	}
}

func TestNativeForwardCanFallBackToCopy(t *testing.T) {
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	links := NewInMemoryMessageLinkRepository(nil)
	gateway := newFakeGateway()
	gateway.rejectNative = true
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	report, err := service.ForwardMessage(context.Background(), textMessage(10, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 2 || gateway.calls[0].mode != ForwardModeForward || gateway.calls[1].mode != ForwardModeCopy {
		t.Fatalf("modes %#v", gateway.calls)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].ModeUsed != ForwardModeCopy || len(report.Failures) != 0 {
		t.Fatalf("report %#v", report)
	}
	link, ok, err := links.Get(context.Background(), 1, MessageRef{ChatID: -1001, MessageID: 10})
	if err != nil || !ok || link.DeliveryMode != ForwardModeCopy {
		t.Fatalf("link %#v ok %v err %v", link, ok, err)
	}
}

func TestNativeForwardEditIsSkipped(t *testing.T) {
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	links := NewInMemoryMessageLinkRepository(nil)
	gateway := newFakeGateway()
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	ctx := context.Background()
	if _, err := service.ForwardMessage(ctx, textMessage(10, "hello")); err != nil {
		t.Fatal(err)
	}
	edited := textMessage(10, "edited")
	report, err := service.SynchronizeEdit(ctx, edited)
	if err != nil {
		t.Fatal(err)
	}
	if report.Synchronized != 1 || len(report.Failures) != 0 || len(gateway.edits) != 0 {
		t.Fatalf("report %#v edits %#v", report, gateway.edits)
	}
}

func TestAlbumIsSortedAndMapped(t *testing.T) {
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	links := NewInMemoryMessageLinkRepository(nil)
	gateway := newFakeGateway()
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	ctx := context.Background()
	album := []IncomingMessage{
		photo(12),
		photo(10),
		photo(11),
	}
	album[2].ContentType = contracts.ContentVideo
	report, err := service.ForwardAlbum(ctx, album)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeliveredMessages() != 3 || len(gateway.calls) != 1 {
		t.Fatalf("report %#v calls %d", report, len(gateway.calls))
	}
	ids := messageIDs(gateway.calls[0].messages)
	if len(ids) != 3 || ids[0] != 10 || ids[1] != 11 || ids[2] != 12 {
		t.Fatalf("ids %v", ids)
	}
	replay, err := service.ForwardAlbum(ctx, album)
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 1 || replay.DeliveredMessages() != 0 || replay.DeduplicatedMessages() != 3 {
		t.Fatalf("replay %#v calls %d", replay, len(gateway.calls))
	}
}

func TestPartialAlbumMappingIsNotResent(t *testing.T) {
	link, err := NewMessageLink(1, MessageRef{ChatID: -1001, MessageID: 10}, MessageRef{ChatID: -2001, MessageID: 100}, ForwardModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	links := NewInMemoryMessageLinkRepository([]MessageLink{link})
	gateway := newFakeGateway()
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	report, err := service.ForwardAlbum(context.Background(), []IncomingMessage{photo(10), photo(11)})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report %#v", report)
	}
	var partial PartialDeliveryState
	if !errors.As(report.Failures[0].Error, &partial) {
		t.Fatalf("error %v", report.Failures[0].Error)
	}
}

func TestServiceMessageIsRenderedAsPlainText(t *testing.T) {
	route := mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{}))
	route.Filter = NewMessageFilter(nil, nil, nil, true)
	routes := mustRoutes(route)
	gateway := newFakeGateway()
	service := NewForwarderService(routes, NewInMemoryMessageLinkRepository(nil), gateway, ForwarderOptions{}, nil)
	topic := 7
	event := textMessage(10, "")
	event.ContentType = contracts.ContentService
	event.Text = ""
	event.TopicID = &topic
	event.Service = &contracts.ServiceMessage{Kind: contracts.ServiceMembersJoined, MemberNames: []string{"A", "B"}}
	report, err := service.ForwardMessage(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeliveredMessages() != 1 || len(gateway.sent) != 1 || gateway.sent[0].text != "A, B joined the group in topic 7." {
		t.Fatalf("report %#v sent %#v", report, gateway.sent)
	}
}

func TestEditAndDeleteAreSynchronized(t *testing.T) {
	source := MessageRef{ChatID: -1001, MessageID: 10}
	first, err := NewMessageLink(1, source, MessageRef{ChatID: -2001, MessageID: 100}, ForwardModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewMessageLink(2, source, MessageRef{ChatID: -2002, MessageID: 200}, ForwardModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	links := NewInMemoryMessageLinkRepository([]MessageLink{first, second})
	gateway := newFakeGateway()
	routes, err := NewInMemoryRouteRepository(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	ctx := context.Background()
	editReport, err := service.SynchronizeEdit(ctx, textMessage(10, "edited"))
	if err != nil {
		t.Fatal(err)
	}
	chatID := int64(-1001)
	deleteReport, err := service.SynchronizeDelete(ctx, MessagesDeleted{
		MessageIDs: []int{10},
		OccurredAt: time.Unix(100, 0).UTC(),
		ChatID:     &chatID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if editReport.Synchronized != 2 || deleteReport.Synchronized != 2 || len(gateway.deletes) != 2 {
		t.Fatalf("edit %#v delete %#v deletes %#v", editReport, deleteReport, gateway.deletes)
	}
	count, err := links.Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("links %d %v", count, err)
	}
}

func TestDeleteSyncCanBeDisabled(t *testing.T) {
	link, err := NewMessageLink(1, MessageRef{ChatID: -1001, MessageID: 10}, MessageRef{ChatID: -2001, MessageID: 100}, ForwardModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	links := NewInMemoryMessageLinkRepository([]MessageLink{link})
	routes, err := NewInMemoryRouteRepository(nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := newFakeGateway()
	disabled := false
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{SyncDeletes: &disabled}, nil)
	chatID := int64(-1001)
	report, err := service.SynchronizeDelete(context.Background(), MessagesDeleted{
		MessageIDs: []int{10},
		OccurredAt: time.Unix(100, 0).UTC(),
		ChatID:     &chatID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.IgnoredReason != "delete_sync_disabled" || len(gateway.deletes) != 0 {
		t.Fatalf("report %#v deletes %#v", report, gateway.deletes)
	}
	count, err := links.Count(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("links %d %v", count, err)
	}
}

func TestDeleteWithoutChatIDIsIgnored(t *testing.T) {
	link, err := NewMessageLink(1, MessageRef{ChatID: -1001, MessageID: 10}, MessageRef{ChatID: -2001, MessageID: 100}, ForwardModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	links := NewInMemoryMessageLinkRepository([]MessageLink{link})
	routes, err := NewInMemoryRouteRepository(nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := newFakeGateway()
	service := NewForwarderService(routes, links, gateway, ForwarderOptions{}, nil)
	report, err := service.SynchronizeDelete(context.Background(), MessagesDeleted{
		MessageIDs: []int{10},
		OccurredAt: time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.IgnoredReason != "source_chat_unknown" || len(gateway.deletes) != 0 {
		t.Fatalf("report %#v", report)
	}
	count, err := links.Count(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("links %d %v", count, err)
	}
}

func photo(id int) IncomingMessage {
	message := textMessage(id, "")
	message.ContentType = contracts.ContentPhoto
	message.Text = ""
	message.Caption = "album"
	grouped := 50
	message.GroupedID = grouped
	return message
}

func messageIDs(messages []IncomingMessage) []int {
	ids := make([]int, len(messages))
	for i, message := range messages {
		ids[i] = message.Ref.MessageID
	}
	return ids
}
