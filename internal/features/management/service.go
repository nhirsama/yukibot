package management

import (
	"context"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// Service changes administrators and modules behind the authorized dispatcher.
// Like the other feature services, it is a trusted in-process business API, not
// an authorization entry point. Every external command must use the dispatcher.
type Service struct {
	admins   AdminStore
	modules  Modules
	identity Identity
}

// NewService returns the management use cases.
// admins, modules, and identity may be used immediately and must be non-nil
// for the operations that need them.
func NewService(admins AdminStore, modules Modules, identity Identity) *Service {
	return &Service{admins: admins, modules: modules, identity: identity}
}

// ListAdmins returns the account owner and delegated administrators.
// The owner is omitted from the delegated list even if a row was stored.
func (s *Service) ListAdmins(ctx context.Context) (int64, []int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	owner, err := s.ownerID()
	if err != nil {
		return 0, nil, err
	}
	all, err := s.admins.ListAdmins(ctx)
	if err != nil {
		return 0, nil, err
	}
	delegated := make([]int64, 0, len(all))
	for _, userID := range all {
		if userID != owner {
			delegated = append(delegated, userID)
		}
	}
	return owner, delegated, nil
}

// AddAdmin stores a delegated administrator.
// The owner is not stored. A non-positive user ID is rejected.
// Outgoing commands attribute the grant to the account owner; incoming
// commands attribute it to the actor.
func (s *Service) AddAdmin(ctx context.Context, command kernel.ControlCommand, userID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID <= 0 {
		return &ValueError{Message: "administrator user ID must be positive"}
	}
	owner, err := s.ownerID()
	if err != nil {
		return err
	}
	if userID == owner {
		return nil
	}
	grantedBy := owner
	if !command.Outgoing {
		if command.ActorID == nil {
			return &PermissionError{}
		}
		grantedBy = *command.ActorID
	}
	return s.admins.AddAdmin(ctx, userID, grantedBy)
}

// RemoveAdmin deletes a delegated administrator.
// The account owner cannot be removed. A non-positive user ID is rejected.
func (s *Service) RemoveAdmin(ctx context.Context, command kernel.ControlCommand, userID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if userID <= 0 {
		return &ValueError{Message: "administrator user ID must be positive"}
	}
	owner, err := s.ownerID()
	if err != nil {
		return err
	}
	if userID == owner {
		return &ValueError{Message: "the current account owner cannot be removed"}
	}
	return s.admins.RemoveAdmin(ctx, userID)
}

// ListModules returns the controller's module statuses.
func (s *Service) ListModules(ctx context.Context) ([]ModuleStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.modules.ListModules(ctx)
}

// EnableModule enables and starts one module.
func (s *Service) EnableModule(ctx context.Context, name string) (ModuleStatus, error) {
	if err := ctx.Err(); err != nil {
		return ModuleStatus{}, err
	}
	return s.modules.Enable(ctx, name)
}

// DisableModule disables and stops one module.
func (s *Service) DisableModule(ctx context.Context, name string) (ModuleStatus, error) {
	if err := ctx.Err(); err != nil {
		return ModuleStatus{}, err
	}
	return s.modules.Disable(ctx, name)
}

func (s *Service) ownerID() (int64, error) {
	if s.identity == nil {
		return 0, errIdentityUnavailable
	}
	return s.identity.UserID()
}
