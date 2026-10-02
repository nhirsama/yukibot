package management

import (
	"context"
	"errors"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// AdminLookup exposes no administrator mutation operations to the policy.
type AdminLookup interface {
	IsAdmin(context.Context, int64) (bool, error)
}

// Authorizer is the single owner/admin policy, invoked only by the kernel
// command dispatcher. Business services neither infer actors nor reauthorize.
type Authorizer struct {
	admins   AdminLookup
	identity Identity
}

var _ kernel.CommandAuthorizer = (*Authorizer)(nil)

func NewAuthorizer(admins AdminLookup, identity Identity) *Authorizer {
	return &Authorizer{admins: admins, identity: identity}
}

func (a *Authorizer) IsAuthorized(ctx context.Context, command kernel.ControlCommand) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if command.Outgoing {
		return true, nil
	}
	if command.ActorID == nil || *command.ActorID <= 0 {
		return false, nil
	}
	if a.identity == nil {
		return false, errIdentityUnavailable
	}
	owner, err := a.identity.UserID()
	if err != nil {
		return false, err
	}
	if *command.ActorID == owner {
		return true, nil
	}
	if a.admins == nil {
		return false, errors.New("administrator lookup is not available")
	}
	return a.admins.IsAdmin(ctx, *command.ActorID)
}
