package management

import (
	"context"
	"testing"

	"github.com/nhirsama/yukibot/internal/kernel"
)

func TestFeatureRegistersAndUnregistersAdmin(t *testing.T) {
	ctx := context.Background()
	registry := kernel.NewCommandRegistry()
	commands := NewCommands(NewService(NewMemoryRepository(Owner{ID: 999}), &fakeModules{}, Owner{ID: 999}))
	feature := NewFeature(registry, commands)
	if feature.Name() != "management" {
		t.Fatalf("name %s", feature.Name())
	}
	if _, ok := registry.Get("/admin"); ok {
		t.Fatal("registered before start")
	}
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	registration, ok := registry.Get(CommandName)
	if !ok || registration.Summary != CommandSummary || registration.HelpText != AdminHelp {
		t.Fatalf("registration %+v %v", registration, ok)
	}
	result, err := registration.Handler(ctx, kernel.ControlCommand{RawArguments: "help", Outgoing: true})
	if err != nil || result.Text == nil || *result.Text != AdminHelp {
		t.Fatalf("handler %v %v", result.Text, err)
	}
	if err := feature.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := feature.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("/admin"); ok {
		t.Fatal("still registered")
	}
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("/admin"); !ok {
		t.Fatal("did not register again")
	}
}

func TestFeatureStartRequiresRegistry(t *testing.T) {
	feature := NewFeature(nil, nil)
	if err := feature.Start(context.Background()); err == nil {
		t.Fatal("nil registry started")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	feature = NewFeature(kernel.NewCommandRegistry(), NewCommands(NewService(NewMemoryRepository(Owner{ID: 1}), &fakeModules{}, Owner{ID: 1})))
	if err := feature.Start(canceled); err == nil {
		t.Fatal("canceled start succeeded")
	}
}
