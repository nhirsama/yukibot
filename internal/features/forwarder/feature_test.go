package forwarder

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

type featureRunner struct {
	mu        sync.Mutex
	recovered int
	prepares  int
	wakes     int
	enqueued  []PendingForwardJob
	stopped   chan struct{}
	once      sync.Once
}

func newFeatureRunner() *featureRunner {
	return &featureRunner{recovered: 2, stopped: make(chan struct{})}
}

func (r *featureRunner) Prepare(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.prepares++
	r.mu.Unlock()
	return r.recovered, nil
}

func (r *featureRunner) Wake() {
	r.mu.Lock()
	r.wakes++
	r.mu.Unlock()
}

func (r *featureRunner) Enqueue(ctx context.Context, jobs []PendingForwardJob) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.enqueued = append(r.enqueued, jobs...)
	r.wakes++
	r.mu.Unlock()
	return len(jobs), nil
}

func (r *featureRunner) RequestStop() {
	r.once.Do(func() { close(r.stopped) })
}

func (r *featureRunner) Run(ctx context.Context) error {
	select {
	case <-r.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *featureRunner) snapshot() (int, int, []PendingForwardJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := append([]PendingForwardJob(nil), r.enqueued...)
	return r.prepares, r.wakes, jobs
}

type recordingCommands struct {
	name    string
	summary string
	help    string
	active  int
	handler CommandHandler
}

type recordingCommandSub struct {
	commands *recordingCommands
	once     sync.Once
}

func (s *recordingCommandSub) Unregister() {
	s.once.Do(func() { s.commands.active-- })
}

func (c *recordingCommands) Register(name, summary, helpText string, handler CommandHandler) (CommandSubscription, error) {
	c.name = name
	c.summary = summary
	c.help = helpText
	c.handler = handler
	c.active++
	return &recordingCommandSub{commands: c}, nil
}

func TestFeaturePersistsContractEventsAndUnsubscribesOnStop(t *testing.T) {
	bus := newMemBus()
	runner := newFeatureRunner()
	tasks := &memTasks{}
	commands := &recordingCommands{}
	feature, err := NewForwarderFeature(bus, runner, tasks, ForwarderFeatureConfig{
		Commands:   commands,
		Handler:    func(context.Context, ControlCommand) (CommandResult, error) { return CommandResult{}, nil },
		AlbumDelay: 500 * time.Millisecond,
		Clock:      func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if commands.active != 1 || commands.name != "/route" || commands.summary != "管理消息转发路由" || commands.help != RouteHelp {
		t.Fatalf("command %#v", commands)
	}

	now := time.Now().UTC()
	sender := int64(42)
	topic := 7
	grouped := 50
	message := contracts.TelegramMessage{
		Ref:         MessageRef{ChatID: -1001, MessageID: 10},
		ContentType: contracts.ContentText,
		OccurredAt:  now,
		SenderID:    &sender,
		TopicID:     &topic,
		GroupedID:   grouped,
		Text:        "hello",
		EditedAt:    &now,
	}
	received := contracts.TelegramMessageReceived{Message: message}
	edited := contracts.TelegramMessageEdited{Message: message}
	chatID := int64(-1001)
	deleted := contracts.TelegramMessagesDeleted{MessageIDs: []int{10, 11}, OccurredAt: now, ChatID: &chatID}
	if err := bus.Publish(ctx, received); err != nil || bus.handlerCount(1) != 1 {
		t.Fatalf("received handlers %d err %v", bus.handlerCount(1), err)
	}
	if err := bus.Publish(ctx, edited); err != nil || bus.handlerCount(2) != 1 {
		t.Fatalf("edited handlers %d err %v", bus.handlerCount(2), err)
	}
	if err := bus.Publish(ctx, deleted); err != nil || bus.handlerCount(3) != 1 {
		t.Fatalf("deleted handlers %d err %v", bus.handlerCount(3), err)
	}

	prepares, wakes, jobs := runner.snapshot()
	if prepares != 1 || wakes != 4 || len(jobs) != 4 {
		t.Fatalf("prepares %d wakes %d jobs %d", prepares, wakes, len(jobs))
	}
	if jobs[0].Event != received || jobs[0].GroupKey == nil || *jobs[0].GroupKey != "album:-1001:50" {
		t.Fatalf("receive job %#v", jobs[0])
	}
	if !jobs[0].AvailableAt.Equal(time.Unix(100, 0).UTC().Add(500 * time.Millisecond)) {
		t.Fatalf("available %s", jobs[0].AvailableAt)
	}
	if jobs[1].Event != edited {
		t.Fatalf("edit job %#v", jobs[1])
	}
	if jobs[2].DeduplicationKey != "delete:-1001:10" || jobs[3].DeduplicationKey != "delete:-1001:11" {
		t.Fatalf("delete keys %q %q", jobs[2].DeduplicationKey, jobs[3].DeduplicationKey)
	}

	if err := feature.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.stopped:
	default:
		t.Fatal("runner was not stopped")
	}
	if tasks.activeCount() != 0 {
		t.Fatalf("active %d", tasks.activeCount())
	}
	if bus.handlerCount(1) != 0 || bus.handlerCount(2) != 0 || bus.handlerCount(3) != 0 || commands.active != 0 {
		t.Fatalf("handlers remain received %d commands %d", bus.handlerCount(1), commands.active)
	}
}

func TestFeatureStartsPollerAndRebuilderTasks(t *testing.T) {
	bus := newMemBus()
	runner := newFeatureRunner()
	tasks := &memTasks{}
	poller, err := NewSourcePoller(mustRoutes(), NewInMemoryPollCursorRepository(nil), newFakeSources(), bus, SourcePollerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	rebuilder, err := NewMembershipRebuilder(newRecoveryGateway(nil), MembershipRebuilderConfig{
		RandomInterval: func(time.Duration, time.Duration) time.Duration { return 300 * time.Second },
	})
	if err != nil {
		t.Fatal(err)
	}
	feature, err := NewForwarderFeature(bus, runner, tasks, ForwarderFeatureConfig{
		AlbumDelay:  500 * time.Millisecond,
		StopTimeout: 2 * time.Second,
		Poller:      poller,
		Rebuilder:   rebuilder,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := feature.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tasks.started) != 3 ||
		tasks.started[0] != (startedTask{name: "forwarder:worker", critical: true}) ||
		tasks.started[1] != (startedTask{name: "forwarder:source-poller", critical: true}) ||
		tasks.started[2] != (startedTask{name: "forwarder:membership-rebuilder", critical: false}) {
		t.Fatalf("tasks %#v", tasks.started)
	}
	if err := feature.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if tasks.activeCount() != 0 {
		t.Fatalf("active %d", tasks.activeCount())
	}
}
