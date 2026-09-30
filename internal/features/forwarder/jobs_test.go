package forwarder

import (
	"context"
	"testing"
	"time"
)

func TestJobRepositoryHeadOfLineDedupRecoverAndDelete(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryForwardJobRepository()
	now := time.Unix(100, 0).UTC()
	group := "album:-1001:50"
	inserted, err := repo.Enqueue(ctx, []PendingForwardJob{
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(10 * time.Second), Event: "later"},
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:11", AvailableAt: now, Event: "due"},
	})
	if err != nil || inserted != 2 {
		t.Fatalf("inserted %d err %v", inserted, err)
	}
	blocked, err := repo.ClaimDue(ctx, now)
	if err != nil || len(blocked) != 0 {
		t.Fatalf("future head must block a later due job, got %#v %v", blocked, err)
	}

	album := NewInMemoryForwardJobRepository()
	if _, err := album.Enqueue(ctx, []PendingForwardJob{
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(time.Second), GroupKey: &group, Event: "a"},
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:11", AvailableAt: now.Add(5 * time.Second), GroupKey: &group, Event: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	slid, err := album.Enqueue(ctx, []PendingForwardJob{
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(8 * time.Second), GroupKey: &group, Event: "a"},
	})
	if err != nil || slid != 0 {
		t.Fatalf("duplicate insert = %d err %v", slid, err)
	}
	if jobs, err := album.ClaimDue(ctx, now.Add(5*time.Second)); err != nil || len(jobs) != 0 {
		t.Fatalf("duplicate key must slide the group, got %#v %v", jobs, err)
	}
	claimed, err := album.ClaimDue(ctx, now.Add(8*time.Second))
	if err != nil || len(claimed) != 2 {
		t.Fatalf("group claim got %#v %v", claimed, err)
	}
	if claimed[0].Attempts != 1 || claimed[1].Attempts != 1 {
		t.Fatalf("attempts = %d %d", claimed[0].Attempts, claimed[1].Attempts)
	}
	recovered, err := album.RecoverIncomplete(ctx)
	if err != nil || recovered != 2 {
		t.Fatalf("recovered %d err %v", recovered, err)
	}
	again, err := album.ClaimDue(ctx, now.Add(8*time.Second))
	if err != nil || len(again) != 2 || again[0].Attempts != 2 {
		t.Fatalf("recover must keep attempts, got %#v %v", again, err)
	}
	ids := []int{again[0].ID, again[1].ID}
	if err := album.MarkSucceeded(ctx, ids); err != nil {
		t.Fatal(err)
	}
	if jobs, err := album.ClaimDue(ctx, now.Add(8*time.Second)); err != nil || len(jobs) != 0 {
		t.Fatalf("success must delete jobs, got %#v %v", jobs, err)
	}
	reinserted, err := album.Enqueue(ctx, []PendingForwardJob{
		{Kind: ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now, GroupKey: &group, Event: "a"},
	})
	if err != nil || reinserted != 1 {
		t.Fatalf("deleted key must be insertable again, got %d %v", reinserted, err)
	}
}
