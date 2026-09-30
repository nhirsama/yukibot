package kernel

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// ShutdownCoordinator records the first shutdown reason.
type ShutdownCoordinator struct {
	mu        sync.Mutex
	reason    string
	requested bool
	waiters   []chan string
	signals   chan os.Signal
	stop      chan struct{}
}

// NewShutdownCoordinator returns a coordinator that has not been requested.
func NewShutdownCoordinator() *ShutdownCoordinator {
	return &ShutdownCoordinator{}
}

// Reason returns the first reason, or empty when shutdown has not been requested.
func (s *ShutdownCoordinator) Reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// Requested reports whether Request has been called.
func (s *ShutdownCoordinator) Requested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested
}

// Request records the first reason and wakes waiters. Later calls are ignored.
func (s *ShutdownCoordinator) Request(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return
	}
	s.requested = true
	s.reason = reason
	for _, waiter := range s.waiters {
		waiter <- reason
	}
	s.waiters = nil
}

// Wait blocks until shutdown is requested. An empty stored reason is returned as "requested".
func (s *ShutdownCoordinator) Wait(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.requested {
		reason := s.reason
		s.mu.Unlock()
		if reason == "" {
			return "requested", nil
		}
		return reason, nil
	}
	ch := make(chan string, 1)
	s.waiters = append(s.waiters, ch)
	s.mu.Unlock()
	select {
	case reason := <-ch:
		if reason == "" {
			return "requested", nil
		}
		return reason, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// InstallSignalHandlers requests shutdown on SIGINT and SIGTERM.
func (s *ShutdownCoordinator) InstallSignalHandlers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signals != nil {
		return
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	s.signals = ch
	s.stop = make(chan struct{})
	go func() {
		for {
			select {
			case <-s.stop:
				return
			case sig := <-ch:
				switch sig {
				case syscall.SIGINT:
					s.Request("SIGINT")
				case syscall.SIGTERM:
					s.Request("SIGTERM")
				}
			}
		}
	}()
}

// RemoveSignalHandlers stops delivering signals to this coordinator.
func (s *ShutdownCoordinator) RemoveSignalHandlers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signals == nil {
		return
	}
	signal.Stop(s.signals)
	close(s.stop)
	s.signals = nil
	s.stop = nil
}
