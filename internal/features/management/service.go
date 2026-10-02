package management

import (
	"context"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// Service authorizes control commands and changes administrators and modules.
type Service struct {
	admins   AdminStore
	modules  Modules
	identity Identity
}

var _ kernel.CommandAuthorizer = (*Service)(nil)

// NewService returns the management use cases.
// admins, modules, and identity may be used immediately and must be non-nil
// for the operations that need them.
func NewService(admins AdminStore, modules Modules, identity Identity) *Service {
	return &Service{admins: admins, modules: modules, identity: identity}
}

// IsAuthorized allows every outgoing command.
// Incoming commands must identify the authenticated owner or a stored admin.
// The owner need not be stored and does not depend on the Telegram out flag.
func (s *Service) IsAuthorized(ctx context.Context, command kernel.ControlCommand) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if command.Outgoing {
		return true, nil
	}
	if command.ActorID == nil || *command.ActorID <= 0 {
		return false, nil
	}
	owner, err := s.ownerID()
	if err != nil {
		return false, err
	}
	if *command.ActorID == owner {
		return true, nil
	}
	return s.admins.IsAdmin(ctx, *command.ActorID)
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
	if err := s.requireAdmin(ctx, command); err != nil {
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
	if err := s.requireAdmin(ctx, command); err != nil {
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

func (s *Service) requireAdmin(ctx context.Context, command kernel.ControlCommand) error {
	ok, err := s.IsAuthorized(ctx, command)
	if err != nil {
		return err
	}
	if !ok {
		return &PermissionError{}
	}
	return nil
}

func (s *Service) ownerID() (int64, error) {
	if s.identity == nil {
		return 0, errIdentityUnavailable
	}
	return s.identity.UserID()
}
