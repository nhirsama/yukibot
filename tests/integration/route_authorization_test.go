package integration

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/features/management"
	"github.com/nhirsama/yukibot/internal/kernel"
)

// Exercise normalization, authorization, dispatch and the real /route add
// handler together. No Telegram connection or production database is needed.
func TestRouteAddAuthorization(t *testing.T) {
	const ownerID, adminID = int64(999), int64(123)
	tests := []struct {
		name    string
		peer    tg.PeerClass
		from    tg.PeerClass
		out     bool
		forward *tg.MessageFwdHeader
		allow   bool
	}{
		{name: "owner saved messages without out or from", peer: &tg.PeerUser{UserID: ownerID}, allow: true},
		{name: "owner explicit sender without out", peer: &tg.PeerChat{ChatID: 10}, from: &tg.PeerUser{UserID: ownerID}, allow: true},
		{name: "owner outgoing private message", peer: &tg.PeerUser{UserID: 456}, out: true, allow: true},
		{name: "admin private message without from", peer: &tg.PeerUser{UserID: adminID}, allow: true},
		{name: "admin group message", peer: &tg.PeerChat{ChatID: 10}, from: &tg.PeerUser{UserID: adminID}, allow: true},
		{name: "stranger private message", peer: &tg.PeerUser{UserID: 456}},
		{name: "anonymous group message", peer: &tg.PeerChat{ChatID: 10}},
		{name: "channel post without sender", peer: &tg.PeerChannel{ChannelID: adminID}},
		{name: "send as channel", peer: &tg.PeerUser{UserID: adminID}, from: &tg.PeerChannel{ChannelID: adminID}},
		{name: "explicit sender overrides private peer", peer: &tg.PeerUser{UserID: adminID}, from: &tg.PeerUser{UserID: 456}},
		{name: "forward from admin does not grant access", peer: &tg.PeerUser{UserID: 456}, forward: &tg.MessageFwdHeader{FromID: &tg.PeerUser{UserID: adminID}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			owner := management.Owner{ID: ownerID}
			admins := management.NewMemoryRepository(owner)
			must(t, admins.AddAdmin(ctx, adminID, ownerID))
			routes, err := forwarder.NewInMemoryRouteRepository(nil)
			must(t, err)
			service, err := forwarder.NewForwarderManagementService(routes, forwarder.ForwarderManagementConfig{})
			must(t, err)
			commands, err := forwarder.NewForwarderCommands(service, nil)
			must(t, err)
			registry := kernel.NewCommandRegistry()
			_, err = registry.Register("/route", "routes", forwarder.RouteHelp, func(ctx context.Context, command kernel.ControlCommand) (kernel.CommandResult, error) {
				result, err := commands.Handle(ctx, forwarder.ControlCommand{
					Name: command.Name, RawArguments: command.RawArguments,
					ChatID: command.ChatID, MessageID: command.MessageID,
					ActorID: command.ActorID, Outgoing: command.Outgoing,
				})
				return kernel.TextResult(result.Text), err
			})
			must(t, err)
			dispatcher := kernel.NewCommandDispatcher(registry, management.NewAuthorizer(admins, owner), admins, nil)
			raw := &tg.Message{
				ID: 1, PeerID: tc.peer, FromID: tc.from, Out: tc.out,
				Message: "/route add -1001 -2001",
			}
			if tc.forward != nil {
				raw.SetFwdFrom(*tc.forward)
			}
			message, ok := telegram.Normalize(raw, time.Now())
			if !ok {
				t.Fatal("normalization failed")
			}
			result, err := dispatcher.Dispatch(ctx, message.Text, message.Ref.ChatID, message.Ref.MessageID, message.SenderID, message.Outgoing)
			must(t, err)
			want := "Permission denied."
			wantRoutes := 0
			if tc.allow {
				want = "Route 1 is configured."
				wantRoutes = 1
			}
			if !result.Consumed || result.Response == nil || *result.Response != want {
				t.Fatalf("response=%+v, want %q (actor=%v outgoing=%v)", result, want, message.SenderID, message.Outgoing)
			}
			stored, err := routes.ListAll(ctx)
			must(t, err)
			if len(stored) != wantRoutes {
				t.Fatalf("stored %d routes, want %d", len(stored), wantRoutes)
			}
		})
	}
}
