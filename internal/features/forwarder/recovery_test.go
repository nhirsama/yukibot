package forwarder

import (
	"context"
	"testing"
	"time"
)

func TestCheckRefreshesJoinedMetadataAndClassifiesUniqueChats(t *testing.T) {
	routes := mustRoutes(
		mustRoute(1, mustSource(-1001, SourceConfig{Username: "old_source"}), mustDestination(-2001, DestinationConfig{})),
		mustRoute(2, mustSource(-3001, SourceConfig{Username: "polled", PollEvery: 300 * time.Second, Polled: true}), mustDestination(-4001, DestinationConfig{})),
	)
	accesses := NewInMemoryChatAccessRepository([]ChatAccess{{
		ChatID: -1001, Title: "Old title", Username: "old_source", InviteLink: "https://t.me/old_source",
	}})
	gateway := newRecoveryGateway([]ChatInspection{
		{
			Access: ChatAccess{ChatID: -1001, Title: "Renamed source", Username: "new_source", InviteLink: "https://t.me/new_source"},
			Joined: true,
		},
		{
			Access: ChatAccess{ChatID: -2001, Title: "Private target", InviteLink: "https://t.me/+private"},
			Joined: true,
		},
	})
	rebuilder, err := NewMembershipRebuilder(gateway, MembershipRebuilderConfig{
		RandomInterval: func(time.Duration, time.Duration) time.Duration { return 300 * time.Second },
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewMembershipRecoveryService(routes, accesses, gateway, rebuilder)

	report, err := service.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]MembershipItem{}
	for _, item := range report.Items {
		byID[item.Access.ChatID] = item
	}
	if byID[-1001].State != MembershipJoined || byID[-1001].Access.Title != "Renamed source" || byID[-1001].Access.JoinReference() != "https://t.me/new_source" {
		t.Fatalf("source %#v", byID[-1001])
	}
	if byID[-2001].State != MembershipJoined || byID[-2001].Access.InviteLink != "https://t.me/+private" {
		t.Fatalf("target %#v", byID[-2001])
	}
	if byID[-3001].State != MembershipNotRequired || byID[-4001].State != MembershipUnavailable {
		t.Fatalf("polled %#v dest %#v", byID[-3001], byID[-4001])
	}
	if report.Updated != 2 {
		t.Fatalf("updated %d", report.Updated)
	}
	if len(gateway.inspected) != 1 || len(gateway.inspected[0]) != 4 ||
		gateway.inspected[0][0] != -4001 || gateway.inspected[0][1] != -3001 ||
		gateway.inspected[0][2] != -2001 || gateway.inspected[0][3] != -1001 {
		t.Fatalf("inspected %#v", gateway.inspected)
	}
	stored, err := accesses.GetMany(context.Background(), []int64{-1001, -2001})
	if err != nil {
		t.Fatal(err)
	}
	saved := map[int64]ChatAccess{}
	for _, item := range stored {
		saved[item.ChatID] = item
	}
	if saved[-1001].Username != "new_source" || saved[-2001].Title != "Private target" {
		t.Fatalf("stored %#v", saved)
	}
}

func TestCheckPreservesRecordedPrivateInvite(t *testing.T) {
	routes := mustRoutes(mustRoute(1, mustSource(-1001, SourceConfig{}), mustDestination(-2001, DestinationConfig{})))
	original := ChatAccess{ChatID: -1001, Title: "Private", InviteLink: "https://t.me/+existing"}
	accesses := NewInMemoryChatAccessRepository([]ChatAccess{original})
	gateway := newRecoveryGateway([]ChatInspection{
		{Access: ChatAccess{ChatID: -1001, Title: "Private"}, Joined: true, MetadataError: "FloodWaitError"},
		{Access: ChatAccess{ChatID: -2001, Title: "Target"}, Joined: true},
	})
	rebuilder, err := NewMembershipRebuilder(gateway, MembershipRebuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service := NewMembershipRecoveryService(routes, accesses, gateway, rebuilder)
	ctx := context.Background()

	if _, err := service.Check(ctx, false); err != nil {
		t.Fatal(err)
	}
	stored, err := accesses.GetMany(ctx, []int64{-1001})
	if err != nil || len(stored) != 1 || stored[0] != original {
		t.Fatalf("%#v %v", stored, err)
	}

	gateway.inspections[-1001] = ChatInspection{Access: ChatAccess{ChatID: -1001, Title: "Private"}, Joined: true}
	if _, err := service.Check(ctx, false); err != nil {
		t.Fatal(err)
	}
	stored, err = accesses.GetMany(ctx, []int64{-1001})
	if err != nil || len(stored) != 1 || stored[0] != original {
		t.Fatalf("%#v %v", stored, err)
	}
}

func TestRebuildQueuesMissingChatsWithMinimumSpacing(t *testing.T) {
	routes := mustRoutes(
		mustRoute(1, mustSource(-1001, SourceConfig{PollEvery: 300 * time.Second, Polled: true}), mustDestination(-2001, DestinationConfig{Username: "target_one"})),
		mustRoute(2, mustSource(-1002, SourceConfig{PollEvery: 300 * time.Second, Polled: true}), mustDestination(-2002, DestinationConfig{Username: "target_two"})),
		mustRoute(3, mustSource(-1003, SourceConfig{PollEvery: 300 * time.Second, Polled: true}), mustDestination(-2003, DestinationConfig{})),
	)
	gateway := newRecoveryGateway(nil)
	accesses := NewInMemoryChatAccessRepository(nil)
	rebuilder, err := NewMembershipRebuilder(gateway, MembershipRebuilderConfig{
		Clock:          func() time.Time { return time.Unix(100, 0).UTC() },
		RandomInterval: func(time.Duration, time.Duration) time.Duration { return 300 * time.Second },
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewMembershipRecoveryService(routes, accesses, gateway, rebuilder)
	ctx := context.Background()

	report, err := service.Rebuild(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Count(MembershipMissing) != 2 || report.Count(MembershipUnavailable) != 1 || rebuilder.Progress().Total != 2 {
		t.Fatalf("report missing %d unavailable %d total %d", report.Count(MembershipMissing), report.Count(MembershipUnavailable), rebuilder.Progress().Total)
	}
	processed, err := rebuilder.ProcessOnceAt(ctx, time.Unix(100, 0).UTC())
	if err != nil || !processed || len(gateway.joined) != 1 || gateway.joined[0] != -2002 {
		t.Fatalf("processed %v joined %#v err %v", processed, gateway.joined, err)
	}
	progress := rebuilder.Progress()
	if !progress.NextAttemptAt.Equal(time.Unix(400, 0).UTC()) {
		t.Fatalf("next %s", progress.NextAttemptAt)
	}
	processed, err = rebuilder.ProcessOnceAt(ctx, time.Unix(399, 0).UTC())
	if err != nil || processed {
		t.Fatalf("early processed %v err %v", processed, err)
	}
	processed, err = rebuilder.ProcessOnceAt(ctx, time.Unix(400, 0).UTC())
	if err != nil || !processed || len(gateway.joined) != 2 || gateway.joined[1] != -2001 {
		t.Fatalf("processed %v joined %#v err %v", processed, gateway.joined, err)
	}
	progress = rebuilder.Progress()
	if progress.Active || progress.Joined != 2 {
		t.Fatalf("progress %#v", progress)
	}
}
