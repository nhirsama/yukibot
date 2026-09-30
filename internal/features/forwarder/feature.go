package forwarder

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

const featureName = "forwarder"

// ForwarderFeatureConfig tunes lifecycle integration.
// A zero album delay is 800ms and a zero stop timeout is 15s.
type ForwarderFeatureConfig struct {
	Commands    CommandRegistrar
	Handler     CommandHandler
	AlbumDelay  time.Duration
	StopTimeout time.Duration
	Clock       func() time.Time
	Logger      *slog.Logger
	Poller      *SourcePoller
	Rebuilder   *MembershipRebuilder
}

// ForwarderFeature subscribes to message events and runs the durable worker.
type ForwarderFeature struct {
	bus         EventPublisher
	runner      ForwardRunner
	tasks       TaskStarter
	commands    CommandRegistrar
	handler     CommandHandler
	albumDelay  time.Duration
	stopTimeout time.Duration
	clock       func() time.Time
	log         *slog.Logger
	poller      *SourcePoller
	rebuilder   *MembershipRebuilder

	life        sync.Mutex
	subs        []EventSubscription
	commandSub  CommandSubscription
	worker      RunningTask
	pollTask    RunningTask
	rebuildTask RunningTask
}

// NewForwarderFeature validates dependencies and applies the Python defaults.
func NewForwarderFeature(bus EventPublisher, runner ForwardRunner, tasks TaskStarter, cfg ForwarderFeatureConfig) (*ForwarderFeature, error) {
	if bus == nil || runner == nil || tasks == nil {
		return nil, valueErr("forwarder feature dependencies are required")
	}
	if cfg.AlbumDelay < 0 {
		return nil, valueErr("album_delay must not be negative")
	}
	if cfg.StopTimeout < 0 {
		return nil, valueErr("stop_timeout must be positive")
	}
	if (cfg.Commands == nil) != (cfg.Handler == nil) {
		return nil, valueErr("command registry and handler must be provided together")
	}
	if cfg.AlbumDelay == 0 {
		cfg.AlbumDelay = 800 * time.Millisecond
	}
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 15 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &ForwarderFeature{
		bus:         bus,
		runner:      runner,
		tasks:       tasks,
		commands:    cfg.Commands,
		handler:     cfg.Handler,
		albumDelay:  cfg.AlbumDelay,
		stopTimeout: cfg.StopTimeout,
		clock:       cfg.Clock,
		log:         loggerOrDiscard(cfg.Logger),
		poller:      cfg.Poller,
		rebuilder:   cfg.Rebuilder,
	}, nil
}

// Name is the feature identifier.
func (f *ForwarderFeature) Name() string { return featureName }

// Start prepares the queue, subscribes, and launches background tasks.
// A second call while the worker is running does nothing.
func (f *ForwarderFeature) Start(ctx context.Context) error {
	f.life.Lock()
	defer f.life.Unlock()
	if f.worker != nil {
		return nil
	}
	recovered, err := f.runner.Prepare(ctx)
	if err != nil {
		return err
	}
	worker, err := f.tasks.Go("forwarder:worker", true, f.runner.Run)
	if err != nil {
		return err
	}
	f.worker = worker
	if err := f.attach(ctx); err != nil {
		f.shutdown(ctx)
		return err
	}
	f.runner.Wake()
	if recovered > 0 {
		f.log.Warn("recovered interrupted forwarder jobs",
			"feature", featureName,
			"job_count", recovered,
		)
	}
	return nil
}

// Stop unsubscribes and drains the worker. Background tasks are cancelled after the stop timeout.
func (f *ForwarderFeature) Stop(ctx context.Context) error {
	f.life.Lock()
	defer f.life.Unlock()
	f.shutdown(ctx)
	return nil
}

func (f *ForwarderFeature) attach(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	received, err := f.bus.SubscribeReceived(f.onReceived)
	if err != nil {
		return err
	}
	f.subs = append(f.subs, received)
	edited, err := f.bus.SubscribeEdited(f.onEdited)
	if err != nil {
		return err
	}
	f.subs = append(f.subs, edited)
	deleted, err := f.bus.SubscribeDeleted(f.onDeleted)
	if err != nil {
		return err
	}
	f.subs = append(f.subs, deleted)
	if f.commands != nil && f.handler != nil {
		sub, err := f.commands.Register("/route", "管理消息转发路由", RouteHelp, f.handler)
		if err != nil {
			return err
		}
		f.commandSub = sub
	}
	if f.poller != nil {
		f.poller.Prepare()
		task, err := f.tasks.Go("forwarder:source-poller", true, f.poller.Run)
		if err != nil {
			return err
		}
		f.pollTask = task
	}
	if f.rebuilder != nil {
		f.rebuilder.Prepare()
		task, err := f.tasks.Go("forwarder:membership-rebuilder", false, f.rebuilder.Run)
		if err != nil {
			return err
		}
		f.rebuildTask = task
	}
	return nil
}

func (f *ForwarderFeature) shutdown(ctx context.Context) {
	if f.rebuildTask != nil {
		if f.rebuilder != nil {
			f.rebuilder.RequestStop()
		}
		f.rebuildTask.Cancel()
		_ = f.rebuildTask.Wait(context.Background())
		f.rebuildTask = nil
	}
	if f.pollTask != nil {
		if f.poller != nil {
			f.poller.RequestStop()
		}
		f.waitTask(ctx, f.pollTask, "forwarder source poller did not stop before timeout")
		f.pollTask = nil
	}
	if f.commandSub != nil {
		f.commandSub.Unregister()
		f.commandSub = nil
	}
	for _, sub := range f.subs {
		sub.Unsubscribe()
	}
	f.subs = nil
	worker := f.worker
	f.worker = nil
	if worker == nil {
		return
	}
	f.runner.RequestStop()
	f.waitTask(ctx, worker, "forwarder worker did not drain before timeout")
}

func (f *ForwarderFeature) waitTask(ctx context.Context, task RunningTask, timeoutMessage string) {
	waitCtx, cancel := context.WithTimeout(ctx, f.stopTimeout)
	err := task.Wait(waitCtx)
	cancel()
	if err == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		f.log.Error(timeoutMessage,
			"feature", featureName,
			"timeout", f.stopTimeout.Seconds(),
		)
	}
	task.Cancel()
	_ = task.Wait(context.Background())
}

func (f *ForwarderFeature) onReceived(ctx context.Context, event contracts.TelegramMessageReceived) error {
	return f.enqueue(ctx, event)
}

func (f *ForwarderFeature) onEdited(ctx context.Context, event contracts.TelegramMessageEdited) error {
	return f.enqueue(ctx, event)
}

func (f *ForwarderFeature) onDeleted(ctx context.Context, event contracts.TelegramMessagesDeleted) error {
	return f.enqueue(ctx, event)
}

func (f *ForwarderFeature) enqueue(ctx context.Context, event any) error {
	jobs, err := PendingJobsForEvent(event, f.clock(), f.albumDelay)
	if err != nil {
		return err
	}
	inserted, err := f.runner.Enqueue(ctx, jobs)
	if err != nil {
		return err
	}
	f.log.Debug("forwarder event persisted",
		"feature", featureName,
		"event_type", eventName(event),
		"job_count", inserted,
		"deduplicated", inserted == 0,
	)
	return nil
}

func eventName(event any) string {
	if event == nil {
		return "nil"
	}
	kind := reflect.TypeOf(event)
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if name := kind.Name(); name != "" {
		return name
	}
	return kind.String()
}
