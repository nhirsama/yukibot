package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/features/management"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
	"github.com/nhirsama/yukibot/internal/kernel"
)

type countingAuthorizer struct {
	policy kernel.CommandAuthorizer
	calls  map[string]int
}

func (a *countingAuthorizer) IsAuthorized(ctx context.Context, command kernel.ControlCommand) (bool, error) {
	a.calls[command.Name]++
	return a.policy.IsAuthorized(ctx, command)
}

type capturedReply struct {
	message contracts.TelegramMessage
	text    string
	sentID  int
}
type streamReplies struct {
	values []capturedReply
	fail   bool
}

func (r *streamReplies) Reply(_ context.Context, message contracts.TelegramMessage, text string) (int, error) {
	if r.fail {
		return 0, errors.New("reply failed")
	}
	id := 2000 + len(r.values)
	r.values = append(r.values, capturedReply{message: message, text: text, sentID: id})
	return id, nil
}

func TestUnifiedStreamUsesOneAuthorizationForAllCommandRoots(t *testing.T) {
	ctx := context.Background()
	owner := management.Owner{ID: 999}
	admins := management.NewMemoryRepository(owner)
	checkStreamErr(t, admins.AddAdmin(ctx, 123, owner.ID))
	registry := kernel.NewCommandRegistry()
	routes, err := forwarder.NewInMemoryRouteRepository(nil)
	checkStreamErr(t, err)
	routeService, err := forwarder.NewForwarderManagementService(routes, forwarder.ForwarderManagementConfig{})
	checkStreamErr(t, err)
	routeCommands, err := forwarder.NewForwarderCommands(routeService, nil)
	checkStreamErr(t, err)
	_, err = (forwarderCommands{registry: registry}).Register("/route", "", forwarder.RouteHelp, routeCommands.Handle)
	checkStreamErr(t, err)
	_, err = registry.Register("/admin", "", management.AdminHelp, management.NewCommands(management.NewService(admins, nil, owner)).Handle)
	checkStreamErr(t, err)
	_, err = (summaryCommands{registry: registry}).Register("/summary", "", "summary help", func(context.Context, summarizer.ControlCommand) (summarizer.CommandResult, error) {
		return summarizer.CommandResult{Text: "summary ok"}, nil
	})
	checkStreamErr(t, err)
	policy := &countingAuthorizer{policy: management.NewAuthorizer(admins, owner), calls: map[string]int{}}
	dispatcher := kernel.NewCommandDispatcher(registry, policy, admins, nil)
	replies := &streamReplies{}
	router := telegram.NewCommandRouter(commandPlane{dispatcher: dispatcher}, replies, nil)
	bus := kernel.NewEventBus(nil)
	var data []any
	_, err = kernel.Subscribe(bus, func(_ context.Context, event contracts.TelegramMessageReceived) error {
		data = append(data, event)
		return nil
	})
	checkStreamErr(t, err)
	_, err = kernel.Subscribe(bus, func(_ context.Context, event contracts.TelegramMessageEdited) error {
		data = append(data, event)
		return nil
	})
	checkStreamErr(t, err)
	_, err = kernel.Subscribe(bus, func(_ context.Context, event contracts.TelegramMessagesDeleted) error {
		data = append(data, event)
		return nil
	})
	checkStreamErr(t, err)
	supervisor := kernel.NewTaskSupervisor(nil)
	stream, err := kernel.NewMessageStream(16, time.Second, supervisor, nil)
	checkStreamErr(t, err)
	t.Cleanup(func() { _ = stream.Stop(ctx); _ = supervisor.Stop(time.Second) })
	checkStreamErr(t, stream.Subscribe("control", router.HandleEvent))
	checkStreamErr(t, stream.Subscribe("features", distributeMessages(bus)))
	checkStreamErr(t, stream.Start(ctx))

	message := func(id int, actor int64, text string) contracts.TelegramMessage {
		return contracts.TelegramMessage{Ref: contracts.MessageRef{ChatID: actor, MessageID: id}, SenderID: &actor, Text: text}
	}
	send := func(event any) {
		t.Helper()
		checkStreamErr(t, stream.PublishAndWait(ctx, contracts.TelegramEventEnvelope{Origin: contracts.OriginLive, Event: event}))
	}
	ownerRoute := message(1, 999, "/route add -1001 -2001")
	send(contracts.TelegramMessageReceived{Message: ownerRoute})
	if replies.values[0].text != "Route 1 is configured." {
		t.Fatal(replies.values)
	}
	send(contracts.TelegramMessageReceived{Message: message(2, 123, "/route add -1002 -2002")})
	send(contracts.TelegramMessageReceived{Message: message(3, 8, "/route add -1003 -2003")})
	if replies.values[2].text != "Permission denied." {
		t.Fatal(replies.values)
	}
	send(contracts.TelegramMessageReceived{Message: message(4, 123, "/admin admin add 456")})
	send(contracts.TelegramMessageReceived{Message: message(5, 456, "/summary run 1")})
	if replies.values[4].text != "summary ok" {
		t.Fatal(replies.values)
	}
	send(contracts.TelegramMessageReceived{Message: message(6, 123, "/admin admin remove 456")})
	send(contracts.TelegramMessageReceived{Message: message(7, 456, "/route add -1003 -2003")})
	if replies.values[6].text != "Permission denied." {
		t.Fatal(replies.values)
	}
	send(contracts.TelegramMessageReceived{Message: message(8, 999, "/help")})
	if !strings.Contains(replies.values[7].text, "/route") {
		t.Fatal(replies.values)
	}
	send(contracts.TelegramMessageReceived{Message: ownerRoute}) // Receipt: no execution or auth replay.
	send(contracts.TelegramMessageEdited{Message: ownerRoute})   // Edit: no command execution.
	response := message(replies.values[0].sentID, 999, replies.values[0].text)
	response.Outgoing = false                                  // Saved Messages may omit the flag on our own reply.
	send(contracts.TelegramMessageReceived{Message: response}) // Own reply: no forwarding.
	send(contracts.TelegramMessageReceived{Message: message(9, 8, "/unknown ordinary data")})
	send(contracts.TelegramMessageEdited{Message: message(10, 8, "ordinary edit")})
	send(contracts.TelegramMessagesDeleted{MessageIDs: []int{10}, OccurredAt: time.Now()})
	history := queuedPublisher{busAdapter: busAdapter{bus: bus}, stream: stream, origin: contracts.OriginHistory}
	checkStreamErr(t, history.Publish(ctx, contracts.TelegramMessageReceived{Message: message(11, 999, "/admin admin add 777")}))
	if len(data) != 4 {
		t.Fatalf("command leaked or data missing: %v", data)
	}
	if stored, err := admins.IsAdmin(ctx, 777); err != nil || stored {
		t.Fatalf("history executed: %v %v", stored, err)
	}
	if policy.calls["/route"] != 4 || policy.calls["/admin"] != 2 || policy.calls["/summary"] != 1 || policy.calls["/help"] != 1 {
		t.Fatalf("split or repeated authorization: %v", policy.calls)
	}
	stored, err := routes.ListAll(ctx)
	checkStreamErr(t, err)
	if len(stored) != 2 {
		t.Fatalf("routes=%v", stored)
	}

	// Reply delivery failure must also fail closed, without killing the stream.
	replies.fail = true
	err = stream.PublishAndWait(ctx, contracts.TelegramEventEnvelope{Origin: contracts.OriginLive, Event: contracts.TelegramMessageReceived{Message: message(12, 999, "/help")}})
	if err == nil {
		t.Fatal("reply error was hidden")
	}
	send(contracts.TelegramMessageReceived{Message: message(13, 8, "after error")})
	if len(data) != 5 {
		t.Fatal("reply failure leaked a command or stopped the stream")
	}
}

func TestHistoryPublisherRequiresSuccessfulDownstreamDelivery(t *testing.T) {
	ctx := context.Background()
	bus := kernel.NewEventBus(nil)
	supervisor := kernel.NewTaskSupervisor(nil)
	stream, err := kernel.NewMessageStream(4, time.Second, supervisor, nil)
	checkStreamErr(t, err)
	t.Cleanup(func() { _ = stream.Stop(ctx); _ = supervisor.Stop(time.Second) })
	checkStreamErr(t, stream.Subscribe("features", distributeMessages(bus)))
	checkStreamErr(t, stream.Start(ctx))
	publisher := queuedPublisher{busAdapter: busAdapter{bus: bus}, stream: stream, origin: contracts.OriginHistory}
	event := contracts.TelegramMessageReceived{}
	if err := publisher.Publish(ctx, event); err == nil {
		t.Fatal("no subscribers acknowledged history")
	}
	sub, err := kernel.Subscribe(bus, func(context.Context, contracts.TelegramMessageReceived) error {
		return errors.New("database unavailable")
	})
	checkStreamErr(t, err)
	if err := publisher.Publish(ctx, event); err == nil {
		t.Fatal("failed persistence acknowledged history")
	}
	sub.Unsubscribe()
	_, err = kernel.Subscribe(bus, func(context.Context, contracts.TelegramMessageReceived) error { return nil })
	checkStreamErr(t, err)
	checkStreamErr(t, publisher.Publish(ctx, event))
}

func checkStreamErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
