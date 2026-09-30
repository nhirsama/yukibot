package forwarder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

func topicRoute(id int, sourceTopic, destinationTopic *int) Route {
	return mustRoute(id, mustSource(-1001, SourceConfig{TopicID: sourceTopic}), mustDestination(-2001, DestinationConfig{TopicID: destinationTopic}))
}

func TestAutomaticTopicIsCreatedOnceReusedAndRenamed(t *testing.T) {
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	gateway.titles[-1001] = "Source channel"
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	ctx := context.Background()

	first, err := topics.Resolve(ctx, topicRoute(1, nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := topics.Resolve(ctx, topicRoute(2, nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway.titles[-1001] = "-1001"
	reused, err := topics.Resolve(ctx, topicRoute(2, nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	renamedTitle := "Renamed channel"
	renamed, err := topics.Resolve(ctx, topicRoute(2, nil, nil), &renamedTitle)
	if err != nil {
		t.Fatal(err)
	}

	want := DestinationEndpoint{ChatID: -2001, TopicID: 500, HasTopic: true}
	if first != want || second != want || reused != want || renamed != want {
		t.Fatalf("got %v %v %v %v", first, second, reused, renamed)
	}
	if len(gateway.created) != 1 || gateway.created[0].chatID != -2001 || gateway.created[0].title != "Source channel" {
		t.Fatalf("created %#v", gateway.created)
	}
	if len(gateway.edited) != 1 || gateway.edited[0] != (editedTopic{chatID: -2001, topicID: 500, title: "Renamed channel"}) {
		t.Fatalf("edited %#v", gateway.edited)
	}
}

func TestAutomaticTopicRequiresResolvedSourceTitle(t *testing.T) {
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	gateway.titles = map[int64]string{}
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)

	_, err := topics.Resolve(context.Background(), topicRoute(1, nil, nil), nil)
	var permanent PermanentDeliveryError
	if !errors.As(err, &permanent) || !strings.Contains(err.Error(), "has no resolved title") {
		t.Fatalf("err %v", err)
	}
	if len(gateway.created) != 0 {
		t.Fatalf("created %#v", gateway.created)
	}
}

func TestDifferentSourceTopicsGetDistinctAutomaticTopics(t *testing.T) {
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	ctx := context.Background()
	announcements := "Source group/Announcements"
	support := "Source group/Support"

	first, err := topics.Resolve(ctx, topicRoute(1, intPtr(7), nil), &announcements)
	if err != nil {
		t.Fatal(err)
	}
	second, err := topics.Resolve(ctx, topicRoute(2, intPtr(8), nil), &support)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := topics.Resolve(ctx, topicRoute(3, intPtr(7), nil), &announcements)
	if err != nil {
		t.Fatal(err)
	}

	if first != (DestinationEndpoint{ChatID: -2001, TopicID: 500, HasTopic: true}) || first != repeated {
		t.Fatalf("first %v repeated %v", first, repeated)
	}
	if second != (DestinationEndpoint{ChatID: -2001, TopicID: 501, HasTopic: true}) {
		t.Fatalf("second %v", second)
	}
	if len(gateway.created) != 2 || gateway.created[0].title != announcements || gateway.created[1].title != support {
		t.Fatalf("created %#v", gateway.created)
	}
}

func TestExplicitTopicAndNonForumAreNotManaged(t *testing.T) {
	gateway := newFakeGateway()
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	ctx := context.Background()

	explicit, err := topics.Resolve(ctx, topicRoute(1, nil, intPtr(12)), nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := topics.Resolve(ctx, topicRoute(1, nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if explicit != (DestinationEndpoint{ChatID: -2001, TopicID: 12, HasTopic: true}) || plain != (DestinationEndpoint{ChatID: -2001}) {
		t.Fatalf("explicit %v plain %v", explicit, plain)
	}
	if len(gateway.created) != 0 {
		t.Fatalf("created %#v", gateway.created)
	}
}

type failingTopics struct{}

func (failingTopics) Get(context.Context, int64, *int, int64) (ManagedTopic, bool, error) {
	return ManagedTopic{}, false, nil
}

func (failingTopics) Save(context.Context, ManagedTopic) error {
	return errors.New("database unavailable")
}

func TestTopicCreationReusesStableRandomIDAfterPersistenceFailure(t *testing.T) {
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	gateway.titles[-1001] = "Source channel"
	topics := NewManagedTopicService(failingTopics{}, gateway)
	ctx := context.Background()

	_, err := topics.Resolve(ctx, topicRoute(1, nil, nil), nil)
	if err == nil || err.Error() != "database unavailable" {
		t.Fatalf("err %v", err)
	}
	_, err = topics.Resolve(ctx, topicRoute(1, nil, nil), nil)
	if err == nil || err.Error() != "database unavailable" {
		t.Fatalf("err %v", err)
	}
	if len(gateway.created) != 2 || gateway.created[0].randomID != gateway.created[1].randomID {
		t.Fatalf("created %#v", gateway.created)
	}
}

func TestForwarderUsesManagedTopicAndSyncsTitleWithoutForwardingIt(t *testing.T) {
	configured := topicRoute(1, nil, nil)
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	gateway.titles[-1001] = "Source channel"
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	service := NewForwarderService(mustRoutes(configured), NewInMemoryMessageLinkRepository(nil), gateway, ForwarderOptions{}, topics)
	ctx := context.Background()

	delivered, err := service.ForwardMessage(ctx, IncomingMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: 10},
		ContentType: contracts.ContentText,
		OccurredAt:  time.Now().UTC(),
		Text:        "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := service.ForwardMessage(ctx, IncomingMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: 11},
		ContentType: contracts.ContentService,
		OccurredAt:  time.Now().UTC(),
		Service:     &contracts.ServiceMessage{Kind: contracts.ServiceTitleChanged, NewTitle: "Renamed channel"},
		Outgoing:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if configured.Mode != ForwardModeForward {
		t.Fatalf("mode %s", configured.Mode)
	}
	if len(delivered.Outcomes) != 1 || delivered.Outcomes[0].ModeUsed != ForwardModeForward {
		t.Fatalf("delivered %#v", delivered)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].destination != (DestinationEndpoint{ChatID: -2001, TopicID: 500, HasTopic: true}) {
		t.Fatalf("calls %#v", gateway.calls)
	}
	if renamed.MatchedRoutes != 0 || renamed.DeliveredMessages() != 0 {
		t.Fatalf("renamed %#v", renamed)
	}
	if len(gateway.edited) != 1 || gateway.edited[0] != (editedTopic{chatID: -2001, topicID: 500, title: "Renamed channel"}) {
		t.Fatalf("edited %#v", gateway.edited)
	}
}

func TestGroupRenameDoesNotDropSpecificSourceTopicName(t *testing.T) {
	configured := topicRoute(1, intPtr(7), nil)
	gateway := newFakeGateway()
	gateway.forums[-2001] = true
	topics := NewManagedTopicService(NewInMemoryManagedTopicRepository(nil), gateway)
	title := "Source group/Announcements"
	if _, err := topics.Resolve(context.Background(), configured, &title); err != nil {
		t.Fatal(err)
	}
	service := NewForwarderService(mustRoutes(configured), NewInMemoryMessageLinkRepository(nil), gateway, ForwarderOptions{}, topics)
	topic := 7
	renamed, err := service.ForwardMessage(context.Background(), IncomingMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: 11},
		ContentType: contracts.ContentService,
		OccurredAt:  time.Now().UTC(),
		TopicID:     &topic,
		Service:     &contracts.ServiceMessage{Kind: contracts.ServiceTitleChanged, NewTitle: "Renamed group"},
		Outgoing:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DeliveredMessages() != 0 || len(gateway.edited) != 0 {
		t.Fatalf("renamed %#v edited %#v", renamed, gateway.edited)
	}
}
