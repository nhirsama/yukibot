package management

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestRepositoryAdminsModulesAndAccountReceipts(t *testing.T) {
	ctx := context.Background()
	owner := Owner{ID: 999}
	repository := NewMemoryRepository(owner)
	other := repository.WithIdentity(Owner{ID: 1000})

	admin, err := repository.IsAdmin(ctx, 123)
	if err != nil || admin {
		t.Fatalf("admin before add: %v %v", admin, err)
	}
	if err := repository.AddAdmin(ctx, 123, 999); err != nil {
		t.Fatal(err)
	}
	if err := repository.AddAdmin(ctx, 123, 1); err != nil {
		t.Fatal(err)
	}
	grantedBy, ok, err := repository.GrantedBy(ctx, 123)
	if err != nil || !ok || grantedBy != 999 {
		t.Fatalf("granted by %d %v %v", grantedBy, ok, err)
	}
	ids, err := repository.ListAdmins(ctx)
	if err != nil || len(ids) != 1 || ids[0] != 123 {
		t.Fatalf("admins %v %v", ids, err)
	}
	shared, err := other.ListAdmins(ctx)
	if err != nil || len(shared) != 1 || shared[0] != 123 {
		t.Fatalf("shared admins %v %v", shared, err)
	}
	if err := repository.RemoveAdmin(ctx, 123); err != nil {
		t.Fatal(err)
	}
	if err := repository.RemoveAdmin(ctx, 123); err != nil {
		t.Fatal(err)
	}
	ids, err = repository.ListAdmins(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("admins after remove %v %v", ids, err)
	}

	enabled, err := repository.GetEnabled(ctx, "forwarder")
	if err != nil || enabled != nil {
		t.Fatalf("missing flag %v %v", enabled, err)
	}
	if err := repository.SetEnabled(ctx, "forwarder", false); err != nil {
		t.Fatal(err)
	}
	enabled, err = repository.GetEnabled(ctx, "forwarder")
	if err != nil || enabled == nil || *enabled {
		t.Fatalf("disabled flag %v %v", enabled, err)
	}
	if err := repository.SetEnabled(ctx, "forwarder", true); err != nil {
		t.Fatal(err)
	}
	enabled, err = other.GetEnabled(ctx, "forwarder")
	if err != nil || enabled == nil || !*enabled {
		t.Fatalf("shared flag %v %v", enabled, err)
	}

	processed, err := repository.IsProcessed(ctx, -1001, 10)
	if err != nil || processed {
		t.Fatalf("receipt before mark %v %v", processed, err)
	}
	if err := repository.MarkProcessed(ctx, -1001, 10); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkProcessed(ctx, -1001, 10); err != nil {
		t.Fatal(err)
	}
	processed, err = repository.IsProcessed(ctx, -1001, 10)
	if err != nil || !processed {
		t.Fatalf("receipt %v %v", processed, err)
	}
	processed, err = repository.IsProcessed(ctx, -1002, 10)
	if err != nil || processed {
		t.Fatalf("other chat %v %v", processed, err)
	}
	processed, err = other.IsProcessed(ctx, -1001, 10)
	if err != nil || processed {
		t.Fatalf("other account saw receipt %v %v", processed, err)
	}
	if err := other.MarkProcessed(ctx, -1001, 10); err != nil {
		t.Fatal(err)
	}
	processed, err = other.IsProcessed(ctx, -1001, 10)
	if err != nil || !processed {
		t.Fatalf("other account receipt %v %v", processed, err)
	}
	processed, err = repository.IsProcessed(ctx, -1001, 10)
	if err != nil || !processed {
		t.Fatalf("owner receipt remained %v %v", processed, err)
	}
}

func TestRepositoryIdentityRequiredForReceipts(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository(Owner{})
	if _, err := repository.IsProcessed(ctx, 1, 1); err == nil || err.Error() != "Telegram account identity is not available" {
		t.Fatalf("is processed %v", err)
	}
	if err := repository.MarkProcessed(ctx, 1, 1); err == nil {
		t.Fatal("mark stored a receipt without an account")
	}
	repository = NewMemoryRepository(nil)
	if _, err := repository.IsProcessed(ctx, 1, 1); err == nil {
		t.Fatal("nil identity was accepted")
	}
}

func TestRepositoryCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repository := NewMemoryRepository(Owner{ID: 1})
	if _, err := repository.IsAdmin(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("is admin %v", err)
	}
	if err := repository.SetEnabled(ctx, "forwarder", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("set enabled %v", err)
	}
}

func TestRepositoryConcurrentAdds(t *testing.T) {
	repository := NewMemoryRepository(Owner{ID: 1})
	var group sync.WaitGroup
	for id := int64(1); id <= 32; id++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := repository.AddAdmin(context.Background(), id, 1); err != nil {
				t.Error(err)
			}
			if _, err := repository.IsAdmin(context.Background(), id); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	ids, err := repository.ListAdmins(context.Background())
	if err != nil || len(ids) != 32 {
		t.Fatalf("ids %v %v", ids, err)
	}
}
