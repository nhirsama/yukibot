package telegram

import (
	"context"
	"sync"
	"time"
)

// SlidingWindow limits events to maxEvents per period.
type SlidingWindow struct {
	maxEvents int
	period    time.Duration
	mu        sync.Mutex
	stamps    []time.Time
	now       func() time.Time
}

// NewSlidingWindow returns a limiter. now defaults to time.Now.
func NewSlidingWindow(maxEvents int, period time.Duration, now func() time.Time) *SlidingWindow {
	if now == nil {
		now = time.Now
	}
	return &SlidingWindow{maxEvents: maxEvents, period: period, now: now}
}

// Wait blocks until the event fits in the window.
func (w *SlidingWindow) Wait(ctx context.Context) error {
	for {
		w.mu.Lock()
		now := w.now()
		cutoff := now.Add(-w.period)
		kept := w.stamps[:0]
		for _, stamp := range w.stamps {
			if stamp.After(cutoff) {
				kept = append(kept, stamp)
			}
		}
		w.stamps = kept
		if len(w.stamps) < w.maxEvents {
			w.stamps = append(w.stamps, now)
			w.mu.Unlock()
			return nil
		}
		wait := w.period - now.Sub(w.stamps[0])
		w.mu.Unlock()
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RequestLimiter serializes one chat and applies a global concurrency cap
// plus a per-chat sliding window of 20 calls per second.
type RequestLimiter struct {
	global   chan struct{}
	mu       sync.Mutex
	chats    map[int64]*sync.Mutex
	windows  map[int64]*SlidingWindow
	perChat  int
	interval time.Duration
}

// NewRequestLimiter uses the production defaults: 4 in flight, 20 messages per second per chat.
func NewRequestLimiter() *RequestLimiter {
	return newLimiter(4, 20, time.Second)
}

func newLimiter(concurrency, perSecond int, period time.Duration) *RequestLimiter {
	return &RequestLimiter{
		global:   make(chan struct{}, concurrency),
		chats:    map[int64]*sync.Mutex{},
		windows:  map[int64]*SlidingWindow{},
		perChat:  perSecond,
		interval: period,
	}
}

// Slot runs fn while holding the per-chat lock, one global slot, and the chat window.
func (l *RequestLimiter) Slot(ctx context.Context, chatID int64, fn func() error) error {
	lock := l.chatLock(chatID)
	lock.Lock()
	defer lock.Unlock()
	select {
	case l.global <- struct{}{}:
		defer func() { <-l.global }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := l.window(chatID).Wait(ctx); err != nil {
		return err
	}
	return fn()
}

func (l *RequestLimiter) chatLock(chatID int64) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, ok := l.chats[chatID]
	if !ok {
		lock = &sync.Mutex{}
		l.chats[chatID] = lock
	}
	return lock
}

func (l *RequestLimiter) window(chatID int64) *SlidingWindow {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, ok := l.windows[chatID]
	if !ok {
		window = NewSlidingWindow(l.perChat, l.interval, nil)
		l.windows[chatID] = window
	}
	return window
}
