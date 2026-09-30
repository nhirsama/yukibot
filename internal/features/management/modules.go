package management

import "context"

// ModuleStatus is one managed module's desired and actual state.
type ModuleStatus struct {
	Name    string
	Enabled bool
	Running bool
}

// Modules lists and toggles managed feature modules.
// Enable and Disable return *kernel.ModuleNotFoundError when the name is unknown
// so /admin can reply with that error text.
type Modules interface {
	ListModules(ctx context.Context) ([]ModuleStatus, error)
	Enable(ctx context.Context, name string) (ModuleStatus, error)
	Disable(ctx context.Context, name string) (ModuleStatus, error)
}
