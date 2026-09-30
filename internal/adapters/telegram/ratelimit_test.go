package telegram

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSameChatIsSerialized(t *testing.T) {
	limiter := newLimiter(2, 100, time.Second)
	var mu sync.Mutex
	current, maxSeen := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = limiter.Slot(context.Background(), 1, func() error {
				mu.Lock()
				current++
				if current > maxSeen {
					maxSeen = current
				}
				mu.Unlock()
				time.Sleep(30 * time.Millisecond)
				mu.Lock()
				current--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("same chat overlapped, max=%d", maxSeen)
	}
}

func TestDifferentChatsOverlap(t *testing.T) {
	limiter := newLimiter(2, 100, time.Second)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for _, chatID := range []int64{1, 2} {
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			_ = limiter.Slot(context.Background(), chatID, func() error {
				started <- struct{}{}
				<-release
				return nil
			})
		}(chatID)
	}
	<-started
	<-started
	close(release)
	wg.Wait()
}
