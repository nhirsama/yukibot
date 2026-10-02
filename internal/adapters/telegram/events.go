package telegram

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// Publisher accepts normalized events. The composition root adapts the kernel bus.
type Publisher interface {
	Publish(ctx context.Context, event any) error
}

// RunningTask is one supervised goroutine.
type RunningTask interface {
	Cancel()
	Wait(ctx context.Context) error
}

// TaskStarter launches the update watcher. The composition root adapts the supervisor.
type TaskStarter interface {
	Go(name string, critical bool, fn func(context.Context) error) (RunningTask, error)
}

// EventSource is the update pump. Handlers are registered at construction, before Run.
type EventSource struct {
	client  *Client
	publish Publisher
	tasks   TaskStarter
	drain   time.Duration

	mu        sync.Mutex
	accepting bool
	started   bool
	pump      RunningTask
	inflight  sync.WaitGroup
}

// NewEventSource registers user and channel new, edit, and delete handlers.
func NewEventSource(client *Client, publish Publisher, tasks TaskStarter, drain time.Duration) *EventSource {
	if drain <= 0 {
		drain = 15 * time.Second
	}
	source := &EventSource{client: client, publish: publish, tasks: tasks, drain: drain}
	if client != nil && client.Dispatcher() != nil {
		disp := client.Dispatcher()
		disp.OnNewMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
			return source.onMessage(ctx, entities, update.Message, false)
		})
		disp.OnNewChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
			return source.onMessage(ctx, entities, update.Message, false)
		})
		disp.OnEditMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
			return source.onMessage(ctx, entities, update.Message, true)
		})
		disp.OnEditChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
			return source.onMessage(ctx, entities, update.Message, true)
		})
		disp.OnDeleteMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteMessages) error {
			return source.onDelete(ctx, nil, update.Messages)
		})
		disp.OnDeleteChannelMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
			chatID := ChannelDialogID(update.ChannelID)
			return source.onDelete(ctx, &chatID, update.Messages)
		})
	}
	return source
}

// Name is the lifecycle name.
func (s *EventSource) Name() string { return "telegram" }

// Start begins accepting updates and watches the client as a critical task.
func (s *EventSource) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	s.accepting = true
	pump, err := s.tasks.Go("telegram:updates", true, s.watch)
	if err != nil {
		s.accepting = false
		return err
	}
	s.pump = pump
	s.started = true
	return nil
}

// Stop stops accepting updates, cancels the watcher, and drains in-flight handlers.
func (s *EventSource) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.accepting = false
	pump := s.pump
	s.pump = nil
	s.started = false
	s.mu.Unlock()
	if pump != nil {
		pump.Cancel()
		_ = pump.Wait(ctx)
	}
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	timer := time.NewTimer(s.drain)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil
}

func (s *EventSource) watch(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.client.Wait(context.Background())
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		s.mu.Lock()
		accepting := s.accepting
		s.mu.Unlock()
		if errors.Is(err, context.Canceled) || err == nil && !accepting {
			return nil
		}
		if err == nil && accepting {
			return errors.New("telegram update pump stopped")
		}
		return err
	}
}

func (s *EventSource) onMessage(ctx context.Context, entities tg.Entities, message tg.MessageClass, edited bool) error {
	if !s.beginEvent() {
		return nil
	}
	defer s.inflight.Done()
	if s.client != nil {
		s.client.Observe(ctx, entities)
	}
	normalized, ok := Normalize(message, time.Now().UTC())
	if !ok {
		return nil
	}
	if err := normalized.Validate(); err != nil {
		return nil
	}
	if s.publish == nil {
		return nil
	}
	if edited {
		return s.publish.Publish(ctx, contracts.TelegramMessageEdited{Message: normalized})
	}
	return s.publish.Publish(ctx, contracts.TelegramMessageReceived{Message: normalized})
}

func (s *EventSource) onDelete(ctx context.Context, chatID *int64, ids []int) error {
	if len(ids) == 0 || s.publish == nil || !s.beginEvent() {
		return nil
	}
	defer s.inflight.Done()
	event := contracts.TelegramMessagesDeleted{
		MessageIDs: append([]int(nil), ids...),
		OccurredAt: time.Now().UTC(),
		ChatID:     chatID,
	}
	if err := event.Validate(); err != nil {
		return nil
	}
	return s.publish.Publish(ctx, event)
}

// Admission and WaitGroup.Add share the shutdown lock: Stop cannot start
// waiting between the acceptance check and registration of an in-flight event.
func (s *EventSource) beginEvent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return false
	}
	s.inflight.Add(1)
	return true
}
