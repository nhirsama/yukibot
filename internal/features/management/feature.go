package management

import (
	"context"
	"errors"
	"sync"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// Registrar stores one command root. *kernel.CommandRegistry implements it.
type Registrar interface {
	Register(name, summary, helpText string, handler kernel.CommandHandler) (*kernel.CommandSubscription, error)
}

// Feature registers /admin for the lifetime of the process component.
type Feature struct {
	registry Registrar
	commands *Commands

	mu           sync.Mutex
	subscription *kernel.CommandSubscription
}

var _ kernel.Feature = (*Feature)(nil)

// NewFeature returns the management lifecycle component. Registration happens in Start.
func NewFeature(registry Registrar, commands *Commands) *Feature {
	return &Feature{registry: registry, commands: commands}
}

// Name is the lifecycle name.
func (f *Feature) Name() string { return "management" }

// Start registers /admin. A second call while registered does nothing.
func (f *Feature) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscription != nil {
		return nil
	}
	if f.registry == nil || f.commands == nil {
		return errors.New("management command registry is not configured")
	}
	subscription, err := f.registry.Register(CommandName, CommandSummary, AdminHelp, f.commands.Handle)
	if err != nil {
		return err
	}
	f.subscription = subscription
	return nil
}

// Stop unregisters /admin. A second call does nothing.
func (f *Feature) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscription != nil {
		f.subscription.Unregister()
		f.subscription = nil
	}
	return nil
}
