package forwarder

import (
	"context"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// EventSubscription removes one event handler. Unsubscribe is idempotent.
type EventSubscription interface {
	Unsubscribe()
}

// EventPublisher is the in-process bus the feature and poller use.
// The composition root adapts the kernel event bus to this interface.
type EventPublisher interface {
	SubscribeReceived(handler func(context.Context, contracts.TelegramMessageReceived) error) (EventSubscription, error)
	SubscribeEdited(handler func(context.Context, contracts.TelegramMessageEdited) error) (EventSubscription, error)
	SubscribeDeleted(handler func(context.Context, contracts.TelegramMessagesDeleted) error) (EventSubscription, error)
	Publish(ctx context.Context, event any) error
}

// RunningTask is one background goroutine owned by TaskStarter.
type RunningTask interface {
	Cancel()
	Wait(ctx context.Context) error
}

// TaskStarter launches feature background work.
// The composition root adapts the kernel task supervisor to this interface.
type TaskStarter interface {
	Go(name string, critical bool, fn func(context.Context) error) (RunningTask, error)
}

// CommandSubscription removes one registered command root.
type CommandSubscription interface {
	Unregister()
}

// CommandRegistrar stores slash-command roots.
// The composition root adapts the kernel command registry to this interface.
type CommandRegistrar interface {
	Register(name, summary, helpText string, handler CommandHandler) (CommandSubscription, error)
}

// ForwardRunner is the durable job loop controlled by ForwarderFeature.
type ForwardRunner interface {
	Prepare(ctx context.Context) (int, error)
	Enqueue(ctx context.Context, jobs []PendingForwardJob) (int, error)
	Wake()
	RequestStop()
	Run(ctx context.Context) error
}

// ControlCommand is one control-plane slash command.
// Fields mirror the kernel command so the composition root can copy them.
type ControlCommand struct {
	Name         string
	RawArguments string
	ChatID       int64
	MessageID    int
	ActorID      *int64
	Outgoing     bool
}

// CommandResult is the reply text for one command.
type CommandResult struct {
	Text string
}

// CommandHandler executes one registered command root.
type CommandHandler func(context.Context, ControlCommand) (CommandResult, error)
