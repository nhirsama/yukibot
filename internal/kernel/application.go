package kernel

import "context"

// Application is the top-level run loop.
type Application struct {
	Lifecycle  *LifecycleManager
	Supervisor *TaskSupervisor
	Shutdown   *ShutdownCoordinator
	log        Logger
}

// NewApplication wires the run loop. A nil shutdown coordinator is replaced.
func NewApplication(lifecycle *LifecycleManager, supervisor *TaskSupervisor, shutdown *ShutdownCoordinator, logger Logger) *Application {
	if shutdown == nil {
		shutdown = NewShutdownCoordinator()
	}
	return &Application{Lifecycle: lifecycle, Supervisor: supervisor, Shutdown: shutdown, log: orLogger(logger)}
}

// RequestShutdown forwards the first reason to the coordinator.
func (a *Application) RequestShutdown(reason string) {
	if reason == "" {
		reason = "requested"
	}
	a.Shutdown.Request(reason)
}

// Run starts the lifecycle, waits for shutdown or a critical task failure, then stops.
func (a *Application) Run(ctx context.Context, installSignalHandlers bool) (err error) {
	if installSignalHandlers {
		a.Shutdown.InstallSignalHandlers()
	}
	defer func() {
		if installSignalHandlers {
			a.Shutdown.RemoveSignalHandlers()
		}
		stopErr := a.Lifecycle.Stop(context.WithoutCancel(ctx))
		if stopErr != nil {
			err = stopErr
		}
	}()
	if err = a.Lifecycle.Start(ctx); err != nil {
		return err
	}
	return a.wait(ctx)
}

func (a *Application) wait(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownDone := make(chan struct{})
	failureDone := make(chan struct{})
	go func() {
		_, _ = a.Shutdown.Wait(wctx)
		close(shutdownDone)
	}()
	go func() {
		_ = a.Supervisor.FailureEvent().Wait(wctx)
		close(failureDone)
	}()
	select {
	case <-failureDone:
		if a.Supervisor.FailureEvent().IsSet() {
			a.Shutdown.Request("critical_task_failed")
		}
	case <-shutdownDone:
	case <-ctx.Done():
		a.Shutdown.Request("context_canceled")
	}
	reason, err := a.Shutdown.Wait(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	a.log.Info("application shutdown requested", "reason", reason)
	return nil
}
