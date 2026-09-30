package forwarder

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// AlbumBuffer collects items by key and flushes after a quiet period.
type AlbumBuffer[K comparable, T any] struct {
	callback func(context.Context, []T) error
	delay    time.Duration
	less     func(a, b T) bool
	onError  func(error)
	log      *slog.Logger

	mu     sync.Mutex
	groups map[K][]T
	order  []K
	slots  map[K]*albumSlot
	closed bool
}

type albumSlot struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewAlbumBuffer rejects a negative flush delay.
func NewAlbumBuffer[K comparable, T any](callback func(context.Context, []T) error, flushDelay time.Duration, less func(a, b T) bool, onError func(error)) (*AlbumBuffer[K, T], error) {
	if flushDelay < 0 {
		return nil, valueErr("flush_delay must not be negative")
	}
	if callback == nil {
		return nil, valueErr("album callback is required")
	}
	return &AlbumBuffer[K, T]{
		callback: callback,
		delay:    flushDelay,
		less:     less,
		onError:  onError,
		log:      slog.New(slog.DiscardHandler),
		groups:   map[K][]T{},
		slots:    map[K]*albumSlot{},
	}, nil
}

// Add appends an item and restarts the sliding flush timer for its key.
func (b *AlbumBuffer[K, T]) Add(ctx context.Context, key K, item T) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errAlbumClosed
	}
	if _, ok := b.groups[key]; !ok {
		b.order = append(b.order, key)
	}
	b.groups[key] = append(b.groups[key], item)
	if previous := b.slots[key]; previous != nil {
		previous.cancel()
	}
	timerCtx, cancel := context.WithCancel(context.Background())
	slot := &albumSlot{cancel: cancel, done: make(chan struct{})}
	b.slots[key] = slot
	delay := b.delay
	b.mu.Unlock()

	go func() {
		defer close(slot.done)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timerCtx.Done():
			return
		case <-timer.C:
		}
		b.flushFromTimer(key, slot)
	}()
	return nil
}

// Flush sends the current items for key immediately.
func (b *AlbumBuffer[K, T]) Flush(ctx context.Context, key K) error {
	items := b.take(key, nil)
	if len(items) == 0 {
		return nil
	}
	return b.invoke(ctx, items, false)
}

// Close rejects later adds. When flush is true, pending groups are delivered.
func (b *AlbumBuffer[K, T]) Close(ctx context.Context, flush bool) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	slots := make([]*albumSlot, 0, len(b.slots))
	for _, slot := range b.slots {
		slots = append(slots, slot)
		slot.cancel()
	}
	b.slots = map[K]*albumSlot{}
	var groups [][]T
	if flush {
		groups = make([][]T, 0, len(b.order))
		for _, key := range b.order {
			if items, ok := b.groups[key]; ok {
				groups = append(groups, items)
			}
		}
	}
	b.groups = map[K][]T{}
	b.order = nil
	b.mu.Unlock()

	for _, slot := range slots {
		select {
		case <-slot.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, items := range groups {
		if err := b.invoke(ctx, items, false); err != nil {
			return err
		}
	}
	return nil
}

// PendingGroups is the number of keys that have not been flushed.
func (b *AlbumBuffer[K, T]) PendingGroups() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.groups)
}

func (b *AlbumBuffer[K, T]) flushFromTimer(key K, slot *albumSlot) {
	items := b.take(key, slot)
	if len(items) == 0 {
		return
	}
	_ = b.invoke(context.Background(), items, true)
}

func (b *AlbumBuffer[K, T]) take(key K, owner *albumSlot) []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	if owner != nil && b.slots[key] != owner {
		return nil
	}
	if slot := b.slots[key]; slot != nil && slot != owner {
		slot.cancel()
	}
	delete(b.slots, key)
	items := b.groups[key]
	delete(b.groups, key)
	b.order = removeKey(b.order, key)
	return items
}

func (b *AlbumBuffer[K, T]) invoke(ctx context.Context, items []T, fromTimer bool) error {
	if b.less != nil {
		sort.SliceStable(items, func(i, j int) bool { return b.less(items[i], items[j]) })
	}
	err := b.callback(ctx, items)
	if err == nil {
		return nil
	}
	if fromTimer {
		if b.onError != nil {
			b.onError(err)
		} else {
			b.log.Error("album callback failed", "error", err)
		}
		return nil
	}
	return err
}

func removeKey[K comparable](order []K, key K) []K {
	next := order[:0]
	for _, item := range order {
		if item != key {
			next = append(next, item)
		}
	}
	return next
}

var errAlbumClosed = valueErr("album buffer is closed")
