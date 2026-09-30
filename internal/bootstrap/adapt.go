package bootstrap

import (
	"context"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/features/management"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
	"github.com/nhirsama/yukibot/internal/kernel"
)

// busAdapter serves the forwarder publisher and the telegram event source.
type busAdapter struct {
	bus *kernel.EventBus
}

func (b busAdapter) Publish(ctx context.Context, event any) error {
	_, err := b.bus.Publish(ctx, event)
	return err
}

func (b busAdapter) SubscribeReceived(handler func(context.Context, kernelEventReceived) error) (forwarder.EventSubscription, error) {
	return kernel.Subscribe(b.bus, handler)
}

func (b busAdapter) SubscribeEdited(handler func(context.Context, kernelEventEdited) error) (forwarder.EventSubscription, error) {
	return kernel.Subscribe(b.bus, handler)
}

func (b busAdapter) SubscribeDeleted(handler func(context.Context, kernelEventDeleted) error) (forwarder.EventSubscription, error) {
	return kernel.Subscribe(b.bus, handler)
}

type forwarderTasks struct {
	supervisor *kernel.TaskSupervisor
}

func (t forwarderTasks) Go(name string, critical bool, fn func(context.Context) error) (forwarder.RunningTask, error) {
	return t.supervisor.Start(name, critical, fn)
}

type telegramTasks struct {
	supervisor *kernel.TaskSupervisor
}

func (t telegramTasks) Go(name string, critical bool, fn func(context.Context) error) (telegram.RunningTask, error) {
	return t.supervisor.Start(name, critical, fn)
}

type forwarderCommands struct {
	registry *kernel.CommandRegistry
}

func (c forwarderCommands) Register(name, summary, helpText string, handler forwarder.CommandHandler) (forwarder.CommandSubscription, error) {
	return c.registry.Register(name, summary, helpText, func(ctx context.Context, command kernel.ControlCommand) (kernel.CommandResult, error) {
		result, err := handler(ctx, forwarder.ControlCommand{
			Name:         command.Name,
			RawArguments: command.RawArguments,
			ChatID:       command.ChatID,
			MessageID:    command.MessageID,
			ActorID:      command.ActorID,
			Outgoing:     command.Outgoing,
		})
		if err != nil {
			return kernel.CommandResult{}, err
		}
		if result.Text == "" {
			return kernel.CommandResult{}, nil
		}
		return kernel.TextResult(result.Text), nil
	})
}

type summaryCommands struct {
	registry *kernel.CommandRegistry
}

func (c summaryCommands) Register(name, summary, helpText string, handler summarizer.CommandHandler) (summarizer.CommandSubscription, error) {
	return c.registry.Register(name, summary, helpText, func(ctx context.Context, command kernel.ControlCommand) (kernel.CommandResult, error) {
		result, err := handler(ctx, summarizer.ControlCommand{
			Name:         command.Name,
			RawArguments: command.RawArguments,
			ChatID:       command.ChatID,
			MessageID:    command.MessageID,
			ActorID:      command.ActorID,
			Outgoing:     command.Outgoing,
		})
		if err != nil {
			return kernel.CommandResult{}, err
		}
		if result.Text == "" {
			return kernel.CommandResult{}, nil
		}
		return kernel.TextResult(result.Text), nil
	})
}

// moduleAdapter exposes the kernel controller through the management port.
// *kernel.ModuleNotFoundError is returned unchanged.
type moduleAdapter struct {
	modules *kernel.ModuleController
}

func (m moduleAdapter) ListModules(ctx context.Context) ([]management.ModuleStatus, error) {
	listed, err := m.modules.ListModules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]management.ModuleStatus, len(listed))
	for i, status := range listed {
		out[i] = management.ModuleStatus{Name: status.Name, Enabled: status.Enabled, Running: status.Running}
	}
	return out, nil
}

func (m moduleAdapter) Enable(ctx context.Context, name string) (management.ModuleStatus, error) {
	status, err := m.modules.Enable(ctx, name)
	if err != nil {
		return management.ModuleStatus{}, err
	}
	return management.ModuleStatus{Name: status.Name, Enabled: status.Enabled, Running: status.Running}, nil
}

func (m moduleAdapter) Disable(ctx context.Context, name string) (management.ModuleStatus, error) {
	status, err := m.modules.Disable(ctx, name)
	if err != nil {
		return management.ModuleStatus{}, err
	}
	return management.ModuleStatus{Name: status.Name, Enabled: status.Enabled, Running: status.Running}, nil
}

type commandPlane struct {
	dispatcher *kernel.CommandDispatcher
}

func (p commandPlane) Recognizes(text string) bool {
	return p.dispatcher.Recognizes(text)
}

func (p commandPlane) Dispatch(ctx context.Context, text string, chatID int64, messageID int, actorID *int64, outgoing bool) (telegram.CommandOutcome, error) {
	outcome, err := p.dispatcher.Dispatch(ctx, text, chatID, messageID, actorID, outgoing)
	if err != nil {
		return telegram.CommandOutcome{}, err
	}
	return telegram.CommandOutcome{Consumed: outcome.Consumed, Response: outcome.Response, Duplicate: outcome.Duplicate}, nil
}
