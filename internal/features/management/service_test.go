package management

import (
	"context"
	"errors"
	"testing"

	"github.com/nhirsama/yukibot/internal/kernel"
)

type fakeModules struct {
	items []ModuleStatus
	err   error
}

func (f *fakeModules) ListModules(context.Context) ([]ModuleStatus, error) {
	return f.items, f.err
}

func (f *fakeModules) Enable(_ context.Context, name string) (ModuleStatus, error) {
	if f.err != nil {
		return ModuleStatus{}, f.err
	}
	for _, item := range f.items {
		if item.Name == name {
			return ModuleStatus{Name: name, Enabled: true, Running: true}, nil
		}
	}
	return ModuleStatus{}, &kernel.ModuleNotFoundError{Name: name}
}

func (f *fakeModules) Disable(_ context.Context, name string) (ModuleStatus, error) {
	if f.err != nil {
		return ModuleStatus{}, f.err
	}
	for _, item := range f.items {
		if item.Name == name {
			return ModuleStatus{Name: name, Enabled: false, Running: false}, nil
		}
	}
	return ModuleStatus{}, &kernel.ModuleNotFoundError{Name: name}
}

func TestOutgoingIsAuthorizedAndOwnerIsNotStored(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository(Owner{ID: 999})
	service := NewService(repository, &fakeModules{}, Owner{ID: 999})
	authorizer := NewAuthorizer(repository, Owner{ID: 999})
	ownerCommand := kernel.ControlCommand{Outgoing: true, ActorID: ptr(int64(111))}
	incomingOwner := kernel.ControlCommand{ActorID: ptr(int64(999))}
	delegated := kernel.ControlCommand{ActorID: ptr(int64(123))}
	anonymous := kernel.ControlCommand{}

	authorized, err := authorizer.IsAuthorized(ctx, ownerCommand)
	if err != nil || !authorized {
		t.Fatalf("outgoing %v %v", authorized, err)
	}
	authorized, err = authorizer.IsAuthorized(ctx, incomingOwner)
	if err != nil || !authorized {
		t.Fatalf("owner must be authorized independently of outgoing: %v %v", authorized, err)
	}
	authorized, err = authorizer.IsAuthorized(ctx, delegated)
	if err != nil || authorized {
		t.Fatalf("delegated before add %v %v", authorized, err)
	}
	authorized, err = authorizer.IsAuthorized(ctx, anonymous)
	if err != nil || authorized {
		t.Fatalf("anonymous %v %v", authorized, err)
	}

	if err := service.AddAdmin(ctx, ownerCommand, 123); err != nil {
		t.Fatal(err)
	}
	if err := service.AddAdmin(ctx, ownerCommand, 123); err != nil {
		t.Fatal(err)
	}
	grantedBy, ok, err := repository.GrantedBy(ctx, 123)
	if err != nil || !ok || grantedBy != 999 {
		t.Fatalf("outgoing grant attributed to owner %d %v %v", grantedBy, ok, err)
	}
	authorized, err = authorizer.IsAuthorized(ctx, delegated)
	if err != nil || !authorized {
		t.Fatalf("delegated after add %v %v", authorized, err)
	}
	if err := service.AddAdmin(ctx, delegated, 456); err != nil {
		t.Fatal(err)
	}
	grantedBy, ok, err = repository.GrantedBy(ctx, 456)
	if err != nil || !ok || grantedBy != 123 {
		t.Fatalf("incoming grant %d %v %v", grantedBy, ok, err)
	}
	owner, admins, err := service.ListAdmins(ctx)
	if err != nil || owner != 999 || len(admins) != 2 || admins[0] != 123 || admins[1] != 456 {
		t.Fatalf("list %d %v %v", owner, admins, err)
	}
	if err := service.AddAdmin(ctx, ownerCommand, 999); err != nil {
		t.Fatal(err)
	}
	if stored, err := repository.IsAdmin(ctx, 999); err != nil || stored {
		t.Fatalf("owner stored %v %v", stored, err)
	}
	if err := service.RemoveAdmin(ctx, delegated, 456); err != nil {
		t.Fatal(err)
	}
	_, admins, err = service.ListAdmins(ctx)
	if err != nil || len(admins) != 1 || admins[0] != 123 {
		t.Fatalf("after remove %v %v", admins, err)
	}

	err = service.RemoveAdmin(ctx, delegated, 999)
	var value *ValueError
	if !errors.As(err, &value) || value.Error() != "the current account owner cannot be removed" {
		t.Fatalf("remove owner %v", err)
	}
	err = service.RemoveAdmin(ctx, ownerCommand, -1)
	if !errors.As(err, &value) || value.Error() != "administrator user ID must be positive" {
		t.Fatalf("negative %v", err)
	}
	err = service.AddAdmin(ctx, ownerCommand, 0)
	if !errors.As(err, &value) || value.Error() != "administrator user ID must be positive" {
		t.Fatalf("zero %v", err)
	}
	err = service.AddAdmin(ctx, delegated, 7)
	if err != nil {
		t.Fatal(err)
	}
	registry := kernel.NewCommandRegistry()
	_, err = registry.Register("/admin", "", "", NewCommands(service).Handle)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := kernel.NewCommandDispatcher(registry, authorizer, repository, nil)
	result, err := dispatcher.Dispatch(ctx, "/admin admin add 9", 8, 1, ptr(8), false)
	if err != nil || result.Response == nil || *result.Response != "Permission denied." {
		t.Fatalf("permission %v %v", result, err)
	}
	if stored, err := repository.IsAdmin(ctx, 9); err != nil || stored {
		t.Fatalf("unauthorized mutation: %v %v", stored, err)
	}
}

func TestListAdminsOmitsStoredOwner(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository(Owner{ID: 999})
	if err := repository.AddAdmin(ctx, 999, 999); err != nil {
		t.Fatal(err)
	}
	if err := repository.AddAdmin(ctx, 4, 999); err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, &fakeModules{}, Owner{ID: 999})
	owner, admins, err := service.ListAdmins(ctx)
	if err != nil || owner != 999 || len(admins) != 1 || admins[0] != 4 {
		t.Fatalf("filtered %d %v %v", owner, admins, err)
	}
}

func TestServiceUnavailableIdentity(t *testing.T) {
	service := NewService(NewMemoryRepository(Owner{}), &fakeModules{}, Owner{})
	authorizer := NewAuthorizer(NewMemoryRepository(Owner{}), Owner{})
	_, _, err := service.ListAdmins(context.Background())
	if err == nil || err.Error() != "Telegram account identity is not available" {
		t.Fatalf("list %v", err)
	}
	allowed, err := authorizer.IsAuthorized(context.Background(), kernel.ControlCommand{ActorID: ptr(123)})
	if err == nil || allowed {
		t.Fatalf("incoming command with unavailable identity: %v %v", allowed, err)
	}
}

func TestAuthorizationRejectsInvalidActorsAndRevokedAdmins(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository(Owner{ID: 999})
	authorizer := NewAuthorizer(repository, Owner{ID: 999})
	for _, id := range []int64{0, -123} {
		// Even a malformed persisted row must not authorize an invalid actor.
		if err := repository.AddAdmin(ctx, id, 999); err != nil {
			t.Fatal(err)
		}
		allowed, err := authorizer.IsAuthorized(ctx, kernel.ControlCommand{ActorID: ptr(id)})
		if err != nil || allowed {
			t.Fatalf("invalid actor %d: %v %v", id, allowed, err)
		}
	}
	if err := repository.AddAdmin(ctx, 123, 999); err != nil {
		t.Fatal(err)
	}
	command := kernel.ControlCommand{ActorID: ptr(123)}
	allowed, err := authorizer.IsAuthorized(ctx, command)
	if err != nil || !allowed {
		t.Fatalf("registered admin: %v %v", allowed, err)
	}
	if err := repository.RemoveAdmin(ctx, 123); err != nil {
		t.Fatal(err)
	}
	allowed, err = authorizer.IsAuthorized(ctx, command)
	if err != nil || allowed {
		t.Fatalf("revoked admin: %v %v", allowed, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	allowed, err = authorizer.IsAuthorized(canceled, kernel.ControlCommand{Outgoing: true})
	if allowed || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command: %v %v", allowed, err)
	}
}

func ptr(value int64) *int64 { return &value }
