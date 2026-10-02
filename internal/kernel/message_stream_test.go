package kernel

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func testStream(t *testing.T, capacity int) *MessageStream {
	t.Helper()
	supervisor := NewTaskSupervisor(nil)
	stream, err := NewMessageStream(capacity, time.Second, supervisor, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stream.Stop(context.Background())
		_ = supervisor.Stop(time.Second)
	})
	return stream
}

func TestMessageStreamFIFOConsumptionAndFailureIsolation(t *testing.T) {
	ctx := context.Background()
	stream := testStream(t, 8)
	var first, second []int
	if err := stream.Subscribe("control", func(_ context.Context, raw any) (bool, error) {
		n := raw.(int)
		first = append(first, n)
		switch n {
		case 2:
			return true, nil
		case 3:
			return false, errors.New("failed")
		case 4:
			panic("subscriber panic")
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Subscribe("data", func(_ context.Context, raw any) (bool, error) {
		second = append(second, raw.(int))
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		if err := stream.Publish(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stream.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, []int{1, 2, 3, 4, 5}) || !reflect.DeepEqual(second, []int{1, 5}) {
		t.Fatalf("first=%v data=%v", first, second)
	}
	if err := stream.Publish(ctx, 6); !errors.Is(err, ErrStreamClosed) {
		t.Fatal(err)
	}
}

func TestMessageStreamBackpressureAndClose(t *testing.T) {
	stream := testStream(t, 1)
	if err := stream.Publish(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := stream.Publish(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full queue must block: %v", err)
	}
	waiting := make(chan error, 1)
	go func() { waiting <- stream.Publish(context.Background(), 3) }()
	if err := stream.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked publisher was not released")
	}
}

func TestMessageStreamDeliveryAcknowledgment(t *testing.T) {
	stream := testStream(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	if err := stream.Subscribe("storage", func(ctx context.Context, _ any) (bool, error) {
		close(entered)
		select {
		case <-release:
			return true, errors.New("storage failed")
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- stream.PublishAndWait(context.Background(), 1) }()
	<-entered
	select {
	case err := <-result:
		t.Fatalf("acknowledged before delivery: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || err.Error() != "storage failed" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing delivery acknowledgment")
	}
}

func TestMessageStreamCanceledProducerDoesNotDispatch(t *testing.T) {
	stream := testStream(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var called bool
	if err := stream.Subscribe("data", func(context.Context, any) (bool, error) {
		called = true
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	// Enqueue with an acknowledgment before starting the consumer, then cancel.
	if err := stream.enqueue(ctx, streamEntry{event: 1, producer: ctx, ack: make(chan error, 1)}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := stream.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stream.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("canceled history event reached subscribers")
	}
}

func TestMessageStreamDrainDeadlineCancelsHandler(t *testing.T) {
	stream := testStream(t, 1)
	entered, canceled := make(chan struct{}), make(chan struct{})
	if err := stream.Subscribe("slow", func(ctx context.Context, _ any) (bool, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return true, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stream.Publish(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := stream.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("handler was not canceled")
	}
}

func TestMessageStreamSubscriptionsFreezeAtStart(t *testing.T) {
	stream := testStream(t, 1)
	handler := func(context.Context, any) (bool, error) { return false, nil }
	if err := stream.Subscribe("test", handler); err != nil {
		t.Fatal(err)
	}
	if err := stream.Subscribe("test", handler); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := stream.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stream.Subscribe("late", handler); err == nil {
		t.Fatal("late subscription accepted")
	}
}
