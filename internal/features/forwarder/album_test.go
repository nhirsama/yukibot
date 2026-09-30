package forwarder

import (
	"context"
	"testing"
	"time"
)

func TestAlbumBufferUsesSlidingWindowAndSortsItems(t *testing.T) {
	flushed := make(chan []int, 1)
	buffer, err := NewAlbumBuffer[string, int](func(ctx context.Context, items []int) error {
		flushed <- append([]int(nil), items...)
		return nil
	}, 15*time.Millisecond, func(a, b int) bool { return a < b }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, item := range []int{3, 1, 2} {
		if err := buffer.Add(ctx, "album", item); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-flushed:
		if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
			t.Fatalf("got %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("album was not flushed")
	}
	if buffer.PendingGroups() != 0 {
		t.Fatalf("pending = %d", buffer.PendingGroups())
	}
	if err := buffer.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func TestAlbumBufferFlushesPendingItemsOnClose(t *testing.T) {
	var flushed []int
	buffer, err := NewAlbumBuffer[string, int](func(ctx context.Context, items []int) error {
		flushed = append([]int(nil), items...)
		return nil
	}, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := buffer.Add(ctx, "album", 1); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(flushed) != 1 || flushed[0] != 1 {
		t.Fatalf("got %v", flushed)
	}
}
