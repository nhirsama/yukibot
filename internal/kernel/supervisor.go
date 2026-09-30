package kernel

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TaskFailure is a completed background task that returned an error.
type TaskFailure struct {
	TaskName string
	Err      error
	Critical bool
}

// Event is a level-triggered signal. Clear is safe for tests.
type Event struct {
	mu  sync.Mutex
	set bool
	ch  chan struct{}
}

// NewEvent returns an unset event.
func NewEvent() *Event { return &Event{ch: make(chan struct{})} }

// Set wakes current and future waiters. Further sets are no-ops until Clear.
func (e *Event) Set() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.set {
		e.set = true
		close(e.ch)
	}
}

// Clear re-arms the event. It does not wake waiters by itself.
func (e *Event) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.set {
		e.set = false
		e.ch = make(chan struct{})
	}
}

// IsSet reports whether Set has been called since the last Clear.
func (e *Event) IsSet() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.set
}

// Wait blocks until the event is set or ctx ends.
func (e *Event) Wait(ctx context.Context) error {
	e.mu.Lock()
	ch := e.ch
	set := e.set
	e.mu.Unlock()
	if set {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type supervised struct {
	name     string
	critical bool
	cancel   context.CancelFunc
	done     chan struct{}
}

// TaskSupervisor owns background goroutines.
type TaskSupervisor struct {
	mu       sync.Mutex
	tasks    map[*supervised]struct{}
	critical map[*supervised]struct{}
	failures []TaskFailure
	failure  *Event
	closed   bool
	log      Logger
}

// NewTaskSupervisor returns an open supervisor.
func NewTaskSupervisor(logger Logger) *TaskSupervisor {
	return &TaskSupervisor{
		tasks:    map[*supervised]struct{}{},
		critical: map[*supervised]struct{}{},
		failure:  NewEvent(),
		log:      orLogger(logger),
	}
}

// ActiveCount is the number of tasks not yet finished.
func (s *TaskSupervisor) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

// Failures returns a snapshot of recorded task errors.
func (s *TaskSupervisor) Failures() []TaskFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskFailure, len(s.failures))
	copy(out, s.failures)
	return out
}

// FailureEvent is set when a critical task fails. Callers may Clear it.
func (s *TaskSupervisor) FailureEvent() *Event { return s.failure }

// Running is one supervised goroutine that the owner can cancel and wait for.
type Running struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

// Cancel requests the task to stop. It is safe to call more than once.
func (r *Running) Cancel() {
	if r != nil && r.cancel != nil {
		r.cancel()
	}
}

// Wait blocks until the task finishes or ctx ends.
func (r *Running) Wait(ctx context.Context) error {
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Go starts fn. Cancellation (context.Canceled) is not a failure.
func (s *TaskSupervisor) Go(name string, critical bool, fn func(context.Context) error) error {
	_, err := s.Start(name, critical, fn)
	return err
}

// Start is Go with a handle the caller can cancel before process shutdown.
func (s *TaskSupervisor) Start(name string, critical bool, fn func(context.Context) error) (*Running, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, &SupervisorClosedError{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &supervised{name: name, critical: critical, cancel: cancel, done: make(chan struct{})}
	s.tasks[task] = struct{}{}
	if critical {
		s.critical[task] = struct{}{}
	}
	go func() {
		defer close(task.done)
		err := fn(ctx)
		s.finish(task, err)
	}()
	return &Running{cancel: cancel, done: task.done}, nil
}

func (s *TaskSupervisor) finish(task *supervised, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, task)
	_, critical := s.critical[task]
	delete(s.critical, task)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	s.failures = append(s.failures, TaskFailure{TaskName: task.name, Err: err, Critical: critical})
	if critical {
		s.failure.Set()
	}
	s.log.Error("background task failed", "task", task.name, "critical", critical, "error_type", typeName(err))
}

// Stop cancels every task and waits up to timeout.
func (s *TaskSupervisor) Stop(timeout time.Duration) error {
	if timeout < 0 {
		return errors.New("timeout must not be negative")
	}
	s.mu.Lock()
	if s.closed && len(s.tasks) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	tasks := make([]*supervised, 0, len(s.tasks))
	for task := range s.tasks {
		tasks = append(tasks, task)
	}
	s.mu.Unlock()
	for _, task := range tasks {
		task.cancel()
	}
	if len(tasks) == 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		for _, task := range tasks {
			<-task.done
		}
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		pending := 0
		for _, task := range tasks {
			select {
			case <-task.done:
			default:
				pending++
			}
		}
		s.log.Error("tasks did not stop before timeout", "task_count", pending, "timeout", timeout.Seconds())
		return nil
	}
}

// SupervisorLifecycle adapts the supervisor to Feature.
type SupervisorLifecycle struct {
	supervisor *TaskSupervisor
	timeout    time.Duration
}

// NewSupervisorLifecycle uses timeout when the process stops.
func NewSupervisorLifecycle(supervisor *TaskSupervisor, timeout time.Duration) *SupervisorLifecycle {
	return &SupervisorLifecycle{supervisor: supervisor, timeout: timeout}
}

func (s *SupervisorLifecycle) Name() string { return "task-supervisor" }

func (s *SupervisorLifecycle) Start(context.Context) error { return nil }

func (s *SupervisorLifecycle) Stop(context.Context) error { return s.supervisor.Stop(s.timeout) }
