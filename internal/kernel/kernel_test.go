package kernel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubFeature struct {
	name     string
	events   *[]string
	startErr error
	stopErr  error
	starts   int
	stops    int
}

func (f *stubFeature) Name() string { return f.name }

func (f *stubFeature) Start(context.Context) error {
	f.starts++
	if f.events != nil {
		*f.events = append(*f.events, "start:"+f.name)
	}
	return f.startErr
}

func (f *stubFeature) Stop(context.Context) error {
	f.stops++
	if f.events != nil {
		*f.events = append(*f.events, "stop:"+f.name)
	}
	return f.stopErr
}

func TestEventBusIsolatesFailuresAndUnsubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	type payload struct{ N int }
	var mu sync.Mutex
	received := []int{}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	okHandler := func(context.Context, payload) error {
		started <- struct{}{}
		<-release
		mu.Lock()
		received = append(received, 7)
		mu.Unlock()
		return nil
	}
	badHandler := func(context.Context, payload) error {
		started <- struct{}{}
		<-release
		return errors.New("boom")
	}
	sub, err := Subscribe(bus, okHandler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Subscribe(bus, badHandler); err != nil {
		t.Fatal(err)
	}
	if _, err := Subscribe(bus, okHandler); err == nil || !strings.Contains(err.Error(), "already subscribed") {
		t.Fatalf("duplicate: %v", err)
	}
	done := make(chan DispatchReport, 1)
	go func() {
		report, err := bus.Publish(context.Background(), payload{N: 7})
		if err != nil {
			t.Errorf("publish: %v", err)
		}
		done <- report
	}()
	<-started
	<-started
	close(release)
	report := <-done
	if report.HandlerCount != 2 || report.Succeeded() != 1 || len(report.Failures) != 1 {
		t.Fatalf("report %+v", report)
	}
	if report.Failures[0].Err.Error() != "boom" {
		t.Fatalf("failure %+v", report.Failures[0])
	}
	sub.Unsubscribe()
	sub.Unsubscribe()
	report, err = bus.Publish(context.Background(), payload{N: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.HandlerCount != 1 {
		t.Fatalf("after unsubscribe %+v", report)
	}
	type child struct{ payload }
	report, err = bus.Publish(context.Background(), child{payload{N: 1}})
	if err != nil || report.HandlerCount != 0 {
		t.Fatalf("subclass dispatch %+v %v", report, err)
	}
}

func TestLifecycleStartRollbackAndStop(t *testing.T) {
	events := []string{}
	one := &stubFeature{name: "one", events: &events}
	two := &stubFeature{name: "two", events: &events, startErr: errors.New("nope")}
	manager, err := NewLifecycleManager([]Feature{one, two}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Start(context.Background())
	var startErr *LifecycleStartError
	if !errors.As(err, &startErr) || startErr.FeatureName != "two" || startErr.Cause.Error() != "nope" {
		t.Fatalf("start: %v", err)
	}
	if manager.State() != StateFailed || len(manager.StartedFeatures()) != 0 {
		t.Fatalf("state %s started %v", manager.State(), manager.StartedFeatures())
	}
	if strings.Join(events, ",") != "start:one,start:two,stop:one" {
		t.Fatalf("events %v", events)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.State() != StateStopped {
		t.Fatalf("state %s", manager.State())
	}

	events = nil
	ok := &stubFeature{name: "one", events: &events}
	manager, err = NewLifecycleManager([]Feature{ok}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil || ok.starts != 1 {
		t.Fatalf("second start starts=%d err=%v", ok.starts, err)
	}
	if _, err := NewLifecycleManager([]Feature{ok, &stubFeature{name: "one"}}, nil); err == nil || !strings.Contains(err.Error(), "duplicate feature name") {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestLifecycleStopFailure(t *testing.T) {
	events := []string{}
	one := &stubFeature{name: "one", events: &events}
	two := &stubFeature{name: "two", events: &events, stopErr: errors.New("stop")}
	manager, err := NewLifecycleManager([]Feature{one, two}, nil)
	if err != nil || manager.Start(context.Background()) != nil {
		t.Fatal(err)
	}
	err = manager.Stop(context.Background())
	var stopErr *LifecycleStopError
	if !errors.As(err, &stopErr) || stopErr.Failures[0].Name != "two" {
		t.Fatalf("stop: %v", err)
	}
	if manager.State() != StateFailed {
		t.Fatalf("state %s", manager.State())
	}
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "stop:two") || !strings.Contains(joined, "stop:one") {
		t.Fatalf("events %v", events)
	}
}

func TestSupervisorCriticalFailureAndClose(t *testing.T) {
	supervisor := NewTaskSupervisor(nil)
	err := supervisor.Go("worker", true, func(context.Context) error {
		return errors.New("down")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := supervisor.FailureEvent().Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if supervisor.ActiveCount() != 0 || !supervisor.FailureEvent().IsSet() {
		t.Fatalf("active %d", supervisor.ActiveCount())
	}
	failures := supervisor.Failures()
	if len(failures) != 1 || failures[0].TaskName != "worker" || !failures[0].Critical {
		t.Fatalf("failures %+v", failures)
	}
	if err := supervisor.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	err = supervisor.Go("late", false, func(context.Context) error { return nil })
	if err == nil || err.Error() != "task supervisor is closed" {
		t.Fatalf("closed: %v", err)
	}
	if err := supervisor.Stop(-1); err == nil || !strings.Contains(err.Error(), "timeout must not be negative") {
		t.Fatalf("timeout: %v", err)
	}
	stopped := make(chan struct{})
	supervisor = NewTaskSupervisor(nil)
	_ = supervisor.Go("wait", false, func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	if err := supervisor.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("task was not canceled")
	}
	if supervisor.ActiveCount() != 0 || len(supervisor.Failures()) != 0 {
		t.Fatalf("cancel recorded as failure: %+v", supervisor.Failures())
	}
}

func TestApplicationShutdownAndCriticalFailure(t *testing.T) {
	feature := &stubFeature{name: "one"}
	manager, err := NewLifecycleManager([]Feature{feature}, nil)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := NewTaskSupervisor(nil)
	app := NewApplication(manager, supervisor, nil, nil)
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background(), false) }()
	time.Sleep(20 * time.Millisecond)
	app.RequestShutdown("test")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if app.Shutdown.Reason() != "test" || manager.State() != StateStopped || feature.stops != 1 {
		t.Fatalf("reason %s state %s stops %d", app.Shutdown.Reason(), manager.State(), feature.stops)
	}

	feature = &stubFeature{name: "one"}
	manager, _ = NewLifecycleManager([]Feature{feature}, nil)
	supervisor = NewTaskSupervisor(nil)
	app = NewApplication(manager, supervisor, nil, nil)
	go func() { done <- app.Run(context.Background(), false) }()
	time.Sleep(20 * time.Millisecond)
	_ = supervisor.Go("worker", true, func(context.Context) error { return errors.New("down") })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if app.Shutdown.Reason() != "critical_task_failed" || feature.stops != 1 {
		t.Fatalf("reason %s stops %d", app.Shutdown.Reason(), feature.stops)
	}
}

type memoryModules struct {
	flags map[string]*bool
}

func (m *memoryModules) GetEnabled(_ context.Context, name string) (*bool, error) {
	return m.flags[name], nil
}

func (m *memoryModules) SetEnabled(_ context.Context, name string, enabled bool) error {
	value := enabled
	m.flags[name] = &value
	return nil
}

func TestModuleControllerPersistsAndToggles(t *testing.T) {
	store := &memoryModules{flags: map[string]*bool{}}
	mod := &stubFeature{name: "forwarder"}
	controller, err := NewModuleController([]Feature{mod}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mod.starts != 1 || store.flags["forwarder"] == nil || !*store.flags["forwarder"] {
		t.Fatalf("start %+v starts %d", store.flags["forwarder"], mod.starts)
	}
	if _, err := controller.Disable(context.Background(), "forwarder"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Disable(context.Background(), "forwarder"); err != nil {
		t.Fatal(err)
	}
	if mod.stops != 1 {
		t.Fatalf("stops %d", mod.stops)
	}
	if _, err := controller.Enable(context.Background(), "forwarder"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Enable(context.Background(), "forwarder"); err != nil {
		t.Fatal(err)
	}
	if mod.starts != 2 {
		t.Fatalf("starts %d", mod.starts)
	}
	if _, err := controller.Enable(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing: %v", err)
	}
}

type memoryReceipts struct {
	mu   sync.Mutex
	seen map[[2]int64]struct{}
}

func (m *memoryReceipts) IsProcessed(_ context.Context, chatID int64, messageID int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.seen[[2]int64{chatID, int64(messageID)}]
	return ok, nil
}

func (m *memoryReceipts) MarkProcessed(_ context.Context, chatID int64, messageID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[[2]int64]struct{}{}
	}
	m.seen[[2]int64{chatID, int64(messageID)}] = struct{}{}
	return nil
}

type allowAll struct{ allow bool }

func (a allowAll) IsAuthorized(context.Context, ControlCommand) (bool, error) { return a.allow, nil }

type recordingLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recordingLogger) Info(msg string, _ ...any) {
	l.mu.Lock()
	l.msgs = append(l.msgs, msg)
	l.mu.Unlock()
}
func (l *recordingLogger) Error(msg string, _ ...any) { l.Info(msg) }
func (l *recordingLogger) Debug(msg string, _ ...any) { l.Info(msg) }

func TestCommandDispatchHelpDuplicateAndFailure(t *testing.T) {
	name, args, ok := SplitCommand("/route  add  7\nnext")
	if !ok || name != "/route" || args != " add  7\nnext" {
		t.Fatalf("split %q %q %v", name, args, ok)
	}
	if _, _, ok := SplitCommand("prefix /route"); ok {
		t.Fatal("embedded slash was recognized")
	}
	registry := NewCommandRegistry()
	if _, err := registry.Register("/help", "", "", nil); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("help: %v", err)
	}
	calls := 0
	_, err := registry.Register("/route", "管理消息转发路由", "detailed route help", func(_ context.Context, command ControlCommand) (CommandResult, error) {
		calls++
		if command.RawArguments != " add 1" && calls == 1 {
			t.Fatalf("args %q", command.RawArguments)
		}
		return TextResult("ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register("/route", "", "", nil); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("dup: %v", err)
	}
	logger := &recordingLogger{}
	receipts := &memoryReceipts{}
	dispatcher := NewCommandDispatcher(registry, allowAll{true}, receipts, logger)
	actor := int64(123)
	first, err := dispatcher.Dispatch(context.Background(), "/route  add 1", -1001, 10, &actor, false)
	if err != nil || !first.Consumed || first.Duplicate || first.Response == nil || *first.Response != "ok" {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := dispatcher.Dispatch(context.Background(), "/route other", -1001, 10, &actor, false)
	if err != nil || !second.Consumed || !second.Duplicate || second.Response != nil || calls != 1 {
		t.Fatalf("replay %+v calls %d", second, calls)
	}
	denied := NewCommandDispatcher(registry, allowAll{false}, &memoryReceipts{}, nil)
	dispatch, err := denied.Dispatch(context.Background(), "/route list", -1001, 11, &actor, false)
	if err != nil || dispatch.Response == nil || *dispatch.Response != "Permission denied." {
		t.Fatalf("denied %+v", dispatch)
	}
	help, err := dispatcher.Dispatch(context.Background(), "/help", -1001, 12, &actor, true)
	if err != nil || help.Response == nil || !strings.Contains(*help.Response, "/help - 列出命令或查看详细帮助") || !strings.Contains(*help.Response, "/route - 管理消息转发路由") {
		t.Fatalf("help %v", help.Response)
	}
	detail, err := dispatcher.Dispatch(context.Background(), "/help /route", -1001, 13, &actor, true)
	if err != nil || detail.Response == nil || *detail.Response != "detailed route help" {
		t.Fatalf("detail %+v", detail)
	}
	unknown, err := dispatcher.Dispatch(context.Background(), "/unknown value", -1001, 14, &actor, true)
	if err != nil || unknown.Consumed {
		t.Fatalf("unknown %+v", unknown)
	}
	_, err = registry.Register("/boom", "boom", "boom", func(context.Context, ControlCommand) (CommandResult, error) {
		return CommandResult{}, errors.New("sensitive detail")
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := dispatcher.Dispatch(context.Background(), "/boom", -1001, 15, &actor, true)
	if err != nil || failed.Response == nil || *failed.Response != "Command failed. Check the application logs." {
		t.Fatalf("failed %+v", failed)
	}
	if !strings.Contains(strings.Join(logger.msgs, "\n"), "control command failed") {
		t.Fatalf("logs %v", logger.msgs)
	}
}

func TestShutdownFirstReasonWins(t *testing.T) {
	shutdown := NewShutdownCoordinator()
	shutdown.Request("")
	shutdown.Request("later")
	if shutdown.Reason() != "" {
		t.Fatalf("reason %q", shutdown.Reason())
	}
	reason, err := shutdown.Wait(context.Background())
	if err != nil || reason != "requested" {
		t.Fatalf("wait %q %v", reason, err)
	}
}
