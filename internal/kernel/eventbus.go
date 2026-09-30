package kernel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"sync"
	"time"
)

// Handler processes one exact event type.
type Handler func(context.Context, any) error

// DispatchFailure is one handler error isolated from its siblings.
type DispatchFailure struct {
	HandlerName string
	Err         error
}

// DispatchReport is the outcome of one publish call.
type DispatchReport struct {
	EventType    string
	HandlerCount int
	Failures     []DispatchFailure
}

// Succeeded is the number of handlers that did not fail.
func (r DispatchReport) Succeeded() int { return r.HandlerCount - len(r.Failures) }

// Subscription removes one handler. Unsubscribe is idempotent.
type Subscription struct {
	active bool
	unsub  func()
}

// Active reports whether the subscription still receives events.
func (s *Subscription) Active() bool { return s != nil && s.active }

// Unsubscribe removes the handler. A second call does nothing.
func (s *Subscription) Unsubscribe() {
	if s == nil || !s.active {
		return
	}
	s.active = false
	if s.unsub != nil {
		s.unsub()
	}
}

type registration struct {
	ptr     uintptr
	name    string
	handler Handler
}

// EventBus dispatches in-process events by exact dynamic type.
type EventBus struct {
	mu       sync.Mutex
	handlers map[reflect.Type][]registration
	log      Logger
}

// NewEventBus returns an empty bus.
func NewEventBus(logger Logger) *EventBus {
	return &EventBus{handlers: map[reflect.Type][]registration{}, log: orLogger(logger)}
}

// Subscribe registers handler for the exact type T.
func Subscribe[T any](bus *EventBus, handler func(context.Context, T) error) (*Subscription, error) {
	if handler == nil {
		return nil, errors.New("handler must not be nil")
	}
	typ := reflect.TypeOf((*T)(nil)).Elem()
	ptr := reflect.ValueOf(handler).Pointer()
	name := handlerName(ptr)
	wrapped := func(ctx context.Context, event any) error {
		return handler(ctx, event.(T))
	}
	return bus.subscribe(typ, ptr, name, wrapped)
}

func (b *EventBus) subscribe(typ reflect.Type, ptr uintptr, name string, handler Handler) (*Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.handlers[typ]
	for _, existing := range list {
		if existing.ptr == ptr {
			return nil, fmt.Errorf("handler %q is already subscribed to %s", name, typ.Name())
		}
	}
	b.handlers[typ] = append(list, registration{ptr: ptr, name: name, handler: handler})
	sub := &Subscription{active: true}
	sub.unsub = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		current := b.handlers[typ]
		next := current[:0]
		for _, existing := range current {
			if existing.ptr != ptr {
				next = append(next, existing)
			}
		}
		if len(next) == 0 {
			delete(b.handlers, typ)
			return
		}
		b.handlers[typ] = next
	}
	return sub, nil
}

// Publish runs a snapshot of the exact-type handlers concurrently.
// context.Canceled from a handler is returned after every sibling finishes
// and is not recorded as a DispatchFailure.
func (b *EventBus) Publish(ctx context.Context, event any) (DispatchReport, error) {
	typ := reflect.TypeOf(event)
	b.mu.Lock()
	snapshot := append([]registration(nil), b.handlers[typ]...)
	b.mu.Unlock()
	report := DispatchReport{EventType: typ.Name(), HandlerCount: len(snapshot)}
	if len(snapshot) == 0 {
		return report, nil
	}
	failures := make([]DispatchFailure, len(snapshot))
	failed := make([]bool, len(snapshot))
	var canceled error
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(snapshot))
	for i, reg := range snapshot {
		go func(i int, reg registration) {
			defer wg.Done()
			started := time.Now()
			err := reg.handler(ctx, event)
			elapsed := float64(time.Since(started).Microseconds()) / 1000
			duration := math.Round(elapsed*1000) / 1000
			if errors.Is(err, context.Canceled) {
				mu.Lock()
				if canceled == nil {
					canceled = err
				}
				mu.Unlock()
				return
			}
			if err != nil {
				b.log.Error("event handler failed",
					"event_type", typ.Name(),
					"handler", reg.name,
					"duration_ms", duration,
					"error_type", typeName(err),
				)
				failures[i] = DispatchFailure{HandlerName: reg.name, Err: err}
				failed[i] = true
				return
			}
			b.log.Debug("event handled", "event_type", typ.Name(), "handler", reg.name, "duration_ms", duration)
		}(i, reg)
	}
	wg.Wait()
	if canceled != nil {
		return DispatchReport{}, canceled
	}
	for i, ok := range failed {
		if ok {
			report.Failures = append(report.Failures, failures[i])
		}
	}
	return report, nil
}

func handlerName(ptr uintptr) string {
	fn := runtime.FuncForPC(ptr)
	if fn == nil {
		return fmt.Sprintf("handler@%x", ptr)
	}
	return fn.Name()
}

func typeName(err error) string {
	if err == nil {
		return ""
	}
	return reflect.TypeOf(err).String()
}
