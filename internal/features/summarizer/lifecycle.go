package summarizer

import (
	"context"
	"sync"
)

// CommandHandler executes one registered command root.
type CommandHandler func(context.Context, ControlCommand) (CommandResult, error)

// CommandSubscription removes one registered command root.
type CommandSubscription interface {
	Unregister()
}

// CommandRegistrar stores slash-command roots.
// The composition root adapts the kernel command registry to this interface.
type CommandRegistrar interface {
	Register(name, summary, helpText string, handler CommandHandler) (CommandSubscription, error)
}

// Lifecycle registers /summary and resets the generator when that registration is removed.
type Lifecycle struct {
	commands CommandRegistrar
	handler  CommandHandler
	reset    func(context.Context) error

	mu  sync.Mutex
	sub CommandSubscription
}

// NewLifecycle returns the summarizer process component.
// reset runs from Stop only after a subscription was registered.
func NewLifecycle(commands CommandRegistrar, handler CommandHandler, reset func(context.Context) error) *Lifecycle {
	return &Lifecycle{commands: commands, handler: handler, reset: reset}
}

// Name is the feature identifier.
func (l *Lifecycle) Name() string { return FeatureName }

// Start registers /summary. A second call while registered does nothing.
func (l *Lifecycle) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sub != nil {
		return nil
	}
	if l.commands == nil || l.handler == nil {
		return &ValueError{Msg: "summarizer command registry is not configured"}
	}
	spec := Spec()
	subscription, err := l.commands.Register(spec.Name, spec.Summary, spec.HelpText, l.handler)
	if err != nil {
		return err
	}
	l.sub = subscription
	return nil
}

// Stop unregisters /summary, then resets the generator when a subscription existed.
func (l *Lifecycle) Stop(ctx context.Context) error {
	l.mu.Lock()
	subscription := l.sub
	l.sub = nil
	reset := l.reset
	l.mu.Unlock()
	if subscription == nil {
		return nil
	}
	subscription.Unregister()
	if reset == nil {
		return nil
	}
	return reset(ctx)
}
