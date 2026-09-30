package summarizer

import (
	"context"
	"errors"
	"testing"
)

type recordedSubscription struct {
	unregistered bool
}

func (s *recordedSubscription) Unregister() { s.unregistered = true }

type recordedRegistrar struct {
	name, summary, help string
	sub                 *recordedSubscription
}

func (r *recordedRegistrar) Register(name, summary, helpText string, handler CommandHandler) (CommandSubscription, error) {
	if handler == nil {
		return nil, errors.New("handler is required")
	}
	r.name, r.summary, r.help = name, summary, helpText
	r.sub = &recordedSubscription{}
	return r.sub, nil
}

func TestLifecycleResetsOnlyAfterRegistration(t *testing.T) {
	registrar := &recordedRegistrar{}
	resets := 0
	lifecycle := NewLifecycle(registrar, func(context.Context, ControlCommand) (CommandResult, error) {
		return CommandResult{}, nil
	}, func(context.Context) error {
		resets++
		return nil
	})
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resets != 0 {
		t.Fatalf("reset before start = %d", resets)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registrar.name != CommandName || registrar.summary != CommandSummary || registrar.help != SummaryHelp {
		t.Fatalf("registered %+v", registrar)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !registrar.sub.unregistered || resets != 1 {
		t.Fatalf("unregistered=%v resets=%d", registrar.sub.unregistered, resets)
	}
	if err := lifecycle.Stop(context.Background()); err != nil || resets != 1 {
		t.Fatalf("second stop resets=%d err=%v", resets, err)
	}
}
