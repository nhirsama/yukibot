package kernel

import (
	"context"
	"fmt"
	"sync"
)

// ModuleNotFoundError means the managed module name is unknown.
type ModuleNotFoundError struct{ Name string }

func (e *ModuleNotFoundError) Error() string {
	return fmt.Sprintf("module %q does not exist", e.Name)
}

// ModuleStateStore persists the desired enabled flag. A nil result means no row.
type ModuleStateStore interface {
	GetEnabled(ctx context.Context, name string) (*bool, error)
	SetEnabled(ctx context.Context, name string, enabled bool) error
}

// ModuleStatus is one managed module's desired and actual state.
type ModuleStatus struct {
	Name    string
	Enabled bool
	Running bool
}

// ModuleController starts and stops an explicit set of features.
type ModuleController struct {
	modules map[string]Feature
	order   []string
	states  ModuleStateStore
	running map[string]struct{}
	started bool
	mu      sync.Mutex
}

// NewModuleController rejects duplicate managed names.
func NewModuleController(modules []Feature, states ModuleStateStore) (*ModuleController, error) {
	order := make([]string, len(modules))
	seen := map[string]int{}
	index := map[string]Feature{}
	for i, module := range modules {
		order[i] = module.Name()
		seen[order[i]]++
		index[order[i]] = module
	}
	for _, name := range order {
		if seen[name] > 1 {
			return nil, &DuplicateFeatureError{Message: "duplicate managed module name: " + name}
		}
	}
	return &ModuleController{
		modules: index,
		order:   order,
		states:  states,
		running: map[string]struct{}{},
	}, nil
}

func (c *ModuleController) Name() string { return "modules" }

// Start persists a default enabled row when missing, then starts enabled modules.
func (c *ModuleController) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	var started []Feature
	for _, name := range c.order {
		enabled, err := c.states.GetEnabled(ctx, name)
		if err != nil {
			c.rollback(ctx, started)
			return err
		}
		if enabled == nil {
			if err := c.states.SetEnabled(ctx, name, true); err != nil {
				c.rollback(ctx, started)
				return err
			}
			value := true
			enabled = &value
		}
		if !*enabled {
			continue
		}
		module := c.modules[name]
		if err := module.Start(ctx); err != nil {
			c.rollback(ctx, started)
			return err
		}
		started = append(started, module)
		c.running[name] = struct{}{}
	}
	c.started = true
	return nil
}

func (c *ModuleController) rollback(ctx context.Context, started []Feature) {
	for i := len(started) - 1; i >= 0; i-- {
		_ = started[i].Stop(ctx)
		delete(c.running, started[i].Name())
	}
}

// Stop stops running modules in reverse registration order.
func (c *ModuleController) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil
	}
	var failures []error
	for i := len(c.order) - 1; i >= 0; i-- {
		name := c.order[i]
		if _, ok := c.running[name]; !ok {
			continue
		}
		if err := c.modules[name].Stop(ctx); err != nil {
			failures = append(failures, err)
			continue
		}
		delete(c.running, name)
	}
	c.started = false
	if len(failures) > 0 {
		return fmt.Errorf("%d managed module(s) failed to stop", len(failures))
	}
	return nil
}

// ListModules reports desired state. A missing row displays as enabled and is not persisted.
func (c *ModuleController) ListModules(ctx context.Context) ([]ModuleStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ModuleStatus, 0, len(c.order))
	for _, name := range c.order {
		enabled, err := c.states.GetEnabled(ctx, name)
		if err != nil {
			return nil, err
		}
		flag := enabled == nil || *enabled
		_, running := c.running[name]
		out = append(out, ModuleStatus{Name: name, Enabled: flag, Running: running})
	}
	return out, nil
}

// Enable persists the flag and starts the module unless it is already running.
func (c *ModuleController) Enable(ctx context.Context, name string) (ModuleStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	module, err := c.require(name)
	if err != nil {
		return ModuleStatus{}, err
	}
	if _, ok := c.running[name]; ok {
		if err := c.states.SetEnabled(ctx, name, true); err != nil {
			return ModuleStatus{}, err
		}
		return ModuleStatus{Name: name, Enabled: true, Running: true}, nil
	}
	if err := c.states.SetEnabled(ctx, name, true); err != nil {
		return ModuleStatus{}, err
	}
	if err := module.Start(ctx); err != nil {
		_ = c.states.SetEnabled(ctx, name, false)
		return ModuleStatus{}, err
	}
	c.running[name] = struct{}{}
	return ModuleStatus{Name: name, Enabled: true, Running: true}, nil
}

// Disable persists the flag first, then stops the module when it is running.
func (c *ModuleController) Disable(ctx context.Context, name string) (ModuleStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	module, err := c.require(name)
	if err != nil {
		return ModuleStatus{}, err
	}
	if err := c.states.SetEnabled(ctx, name, false); err != nil {
		return ModuleStatus{}, err
	}
	if _, ok := c.running[name]; !ok {
		return ModuleStatus{Name: name, Enabled: false, Running: false}, nil
	}
	if err := module.Stop(ctx); err != nil {
		return ModuleStatus{}, err
	}
	delete(c.running, name)
	return ModuleStatus{Name: name, Enabled: false, Running: false}, nil
}

func (c *ModuleController) require(name string) (Feature, error) {
	module, ok := c.modules[name]
	if !ok {
		return nil, &ModuleNotFoundError{Name: name}
	}
	return module, nil
}
