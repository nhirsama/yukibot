package kernel

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrStreamClosed = errors.New("message stream is closed")

// StreamHandler observes events in subscription order. Consuming an event
// prevents later subscribers from seeing it. An error also stops propagation.
type StreamHandler func(context.Context, any) (consumed bool, err error)

type streamSubscriber struct {
	name   string
	handle StreamHandler
}

type streamEntry struct {
	event any
	// A synchronous producer can cancel pending work without advancing its cursor.
	producer context.Context
	ack      chan error
}

// MessageStream is a bounded, single-consumer FIFO with ordered subscriptions.
// It is an in-memory ingress queue, not a durable broker. Subscribers are fixed
// before Start; feature-level subscriptions remain on the downstream EventBus.
type MessageStream struct {
	mu               sync.Mutex
	changed          chan struct{}
	entries          []streamEntry
	head, size       int
	subs             []streamSubscriber
	started, closing bool
	task             *Running
	supervisor       *TaskSupervisor
	drain            time.Duration
	log              Logger
}

func NewMessageStream(capacity int, drain time.Duration, supervisor *TaskSupervisor, logger Logger) (*MessageStream, error) {
	if capacity <= 0 || drain <= 0 || supervisor == nil {
		return nil, errors.New("message stream requires positive capacity, drain timeout and supervisor")
	}
	return &MessageStream{
		changed: make(chan struct{}), entries: make([]streamEntry, capacity),
		supervisor: supervisor, drain: drain, log: orLogger(logger),
	}, nil
}

func (s *MessageStream) Name() string { return "message-stream" }

// Subscribe installs a named subscriber before the consumer is started.
func (s *MessageStream) Subscribe(name string, handler StreamHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closing || name == "" || handler == nil {
		return errors.New("invalid or late message stream subscription")
	}
	for _, sub := range s.subs {
		if sub.name == name {
			return errors.New("duplicate message stream subscription")
		}
	}
	s.subs = append(s.subs, streamSubscriber{name: name, handle: handler})
	return nil
}

func (s *MessageStream) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return ErrStreamClosed
	}
	if s.started {
		return nil
	}
	task, err := s.supervisor.Start("message-stream:dispatch", true, s.run)
	if err != nil {
		return err
	}
	s.task, s.started = task, true
	return nil
}

// Publish acknowledges acceptance, not processing. Full queues apply backpressure.
// Events may be buffered before Start so module-owned producers can start first.
func (s *MessageStream) Publish(ctx context.Context, event any) error {
	return s.enqueue(ctx, streamEntry{event: event})
}

// PublishAndWait also waits for delivery. Pollers use this before saving cursors;
// acceptance into volatile memory must never be mistaken for durable delivery.
func (s *MessageStream) PublishAndWait(ctx context.Context, event any) error {
	ack := make(chan error, 1)
	if err := s.enqueue(ctx, streamEntry{event: event, producer: ctx, ack: ack}); err != nil {
		return err
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *MessageStream) enqueue(ctx context.Context, entry streamEntry) error {
	if entry.event == nil {
		return errors.New("message stream event must not be nil")
	}
	for {
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		if s.closing {
			s.mu.Unlock()
			return ErrStreamClosed
		}
		if s.size < len(s.entries) {
			s.entries[(s.head+s.size)%len(s.entries)] = entry
			s.size++
			s.signal()
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Stop rejects new events, drains accepted work while subscribers are alive,
// and cancels the consumer at the deadline. The stream is not restartable.
func (s *MessageStream) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.signal()
	task := s.task
	if task == nil {
		s.discardPending(ErrStreamClosed)
	}
	s.mu.Unlock()
	if task == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.drain)
	defer cancel()
	if err := task.Wait(waitCtx); err != nil {
		task.Cancel()
		return err
	}
	return nil
}

func (s *MessageStream) run(ctx context.Context) error {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closing = true
		s.discardPending(ErrStreamClosed)
		s.signal()
	}()
	for {
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		if s.size == 0 {
			if s.closing {
				s.mu.Unlock()
				return nil
			}
			changed := s.changed
			s.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
			}
			continue
		}
		entry := s.pop()
		s.signal()
		s.mu.Unlock()
		err := s.deliver(ctx, entry)
		if entry.ack != nil {
			entry.ack <- err
		}
	}
}

func (s *MessageStream) deliver(ctx context.Context, entry streamEntry) error {
	if entry.producer != nil {
		if err := entry.producer.Err(); err != nil {
			return err
		}
		child, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(entry.producer, cancel)
		defer stop()
		defer cancel()
		ctx = child
	}
	for _, sub := range s.subs {
		consumed, err := callStreamHandler(ctx, sub.handle, entry.event)
		if err != nil {
			// Fail closed: an authorization/reply failure must not leak a
			// command (possibly carrying an API key) into ordinary forwarding.
			s.log.Error("message stream subscriber failed", "subscriber", sub.name, "error_type", typeName(err))
			return err
		}
		if consumed {
			return nil
		}
	}
	return nil
}

func callStreamHandler(ctx context.Context, handler StreamHandler, event any) (consumed bool, err error) {
	defer func() {
		if recover() != nil {
			consumed, err = true, errors.New("message stream subscriber panicked")
		}
	}()
	return handler(ctx, event)
}

// All ring-buffer and notification helpers require s.mu.
func (s *MessageStream) signal() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *MessageStream) pop() streamEntry {
	entry := s.entries[s.head]
	s.entries[s.head] = streamEntry{}
	s.head = (s.head + 1) % len(s.entries)
	s.size--
	return entry
}

func (s *MessageStream) discardPending(err error) {
	for s.size > 0 {
		if entry := s.pop(); entry.ack != nil {
			entry.ack <- err
		}
	}
}
