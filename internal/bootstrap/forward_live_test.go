//go:build live

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/config"
	"github.com/nhirsama/yukibot/internal/kernel"
)

// TestLiveForward exercises copy, edit sync, and native forward
// between the new group and the newest channel.
// Delete synchronization is disabled.
// Production routes are disabled for the run and restored afterwards.
// Same-session sends are replayed as incoming because the forwarder ignores
// the account's own messages.
func TestLiveForward(t *testing.T) {
	chdirModule(t)
	if others := conflictingBots(); len(others) > 0 {
		t.Fatalf("another yukibot process is running:\n%s", strings.Join(others, "\n"))
	}
	settings, err := config.Load()
	mustLive(t, err)

	state := &liveForward{url: settings.DatabaseURL}
	t.Cleanup(func() { state.finish(t) })
	state.pause(t)

	app, client, err := assemble(settings)
	mustLive(t, err)
	startCtx, startCancel := context.WithTimeout(context.Background(), 90*time.Second)
	err = app.Lifecycle.Start(startCtx)
	startCancel()
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = app.Lifecycle.Stop(stopCtx)
		stopCancel()
		t.Fatal(redactLive(err.Error()))
	}
	state.app = app
	state.client = client

	live, err := telegram.NewLiveChat(client)
	mustLive(t, err)
	state.live = live
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	dialogs, err := live.Dialogs(ctx)
	mustLive(t, err)
	known := state.routeChats(t)
	group, channel, listed := chooseLiveChats(dialogs, known)
	for _, line := range listed {
		t.Log(line)
	}
	if group.ID == 0 || channel.ID == 0 {
		t.Fatal("could not find the new group and a channel")
	}
	t.Logf("group %q (%s, creator=%t) channel %q (%s, can_post=%t)", group.Title, group.Kind, group.Creator, channel.Title, channel.Kind, channel.CanPost)

	editDest := channel
	editDestName := "channel"
	if !channel.CanPost {
		self, err := client.Identity().UserID()
		mustLive(t, err)
		editDest = telegram.DialogInfo{ID: self, Title: "Saved Messages", Kind: "saved", CanPost: true}
		editDestName = "saved messages, because the channel is not writable"
	}
	t.Logf("edit/delete destination: %s", editDestName)

	channelRoute := state.insertRoute(t, channel.ID, group.ID, "copy")
	editRoute := state.insertRoute(t, group.ID, editDest.ID, "copy")
	var problems []string
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		problems = append(problems, msg)
		t.Logf("FAIL %s", msg)
	}

	source, ok, err := pickSourceMessage(ctx, live, channel.ID, 0)
	mustLive(t, err)
	if !ok {
		t.Fatal("the channel has no message that can be replayed")
	}
	groupMark, err := live.MaxID(ctx, group.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, channel.ID, source.ID, "new"))
	copied, err := live.WaitAfter(ctx, group.ID, groupMark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return telegram.SameDelivery(source, message)
	})
	mustLive(t, err)
	if copied.ID == 0 {
		fail("channel copy did not arrive in the group\n%s", state.jobs())
	} else {
		state.track(group.ID, copied.ID)
		t.Logf("copy: channel message %d -> group message %d (%s)", source.ID, copied.ID, source.Kind)
	}

	nonce := liveNonce()
	original := "yukibot-live-copy-" + nonce
	edited := "yukibot-live-edited-" + nonce
	sentID, err := live.Send(ctx, group.ID, original)
	mustLive(t, err)
	state.track(group.ID, sentID)
	destMark, err := live.MaxID(ctx, editDest.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, group.ID, sentID, "new"))
	delivered, err := live.WaitAfter(ctx, editDest.ID, destMark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return message.Text == original
	})
	mustLive(t, err)
	if delivered.ID == 0 {
		fail("copy from the group did not arrive\n%s", state.jobs())
	} else {
		state.track(editDest.ID, delivered.ID)
		t.Logf("copy: group message %d -> %s message %d", sentID, editDest.Kind, delivered.ID)
		mustLive(t, live.Edit(ctx, group.ID, sentID, edited))
		mustLive(t, live.Replay(ctx, group.ID, sentID, "edit"))
		changed, err := live.WaitText(ctx, editDest.ID, delivered.ID, edited, 45*time.Second)
		mustLive(t, err)
		if !changed {
			fail("edit sync did not change the destination\n%s", state.jobs())
		} else {
			t.Log("edit sync: destination text updated")
		}
	}

	state.setMode(t, editRoute, "forward")
	forwardedText := "yukibot-live-forward-" + nonce
	forwardID, err := live.Send(ctx, group.ID, forwardedText)
	mustLive(t, err)
	state.track(group.ID, forwardID)
	destMark, err = live.MaxID(ctx, editDest.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, group.ID, forwardID, "new"))
	native, err := live.WaitAfter(ctx, editDest.ID, destMark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return message.Text == forwardedText
	})
	mustLive(t, err)
	if native.ID == 0 {
		fail("native forward did not arrive\n%s", state.jobs())
	} else {
		state.track(editDest.ID, native.ID)
		if !native.Forwarded {
			fail("destination message arrived without a forward header\n%s", state.jobs())
		} else {
			t.Logf("native forward: group message %d -> %s message %d", forwardID, editDest.Kind, native.ID)
		}
	}

	second, ok, err := pickSourceMessage(ctx, live, channel.ID, source.ID)
	mustLive(t, err)
	if !ok {
		second = source
	}
	state.setMode(t, channelRoute, "forward")
	groupMark, err = live.MaxID(ctx, group.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, channel.ID, second.ID, "new"))
	channelForward, err := live.WaitAfter(ctx, group.ID, groupMark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return telegram.SameDelivery(second, message)
	})
	mustLive(t, err)
	if channelForward.ID == 0 {
		fail("native forward from the channel did not arrive\n%s", state.jobs())
	} else {
		state.track(group.ID, channelForward.ID)
		if !channelForward.Forwarded {
			fail("channel forward arrived without a forward header\n%s", state.jobs())
		} else {
			t.Logf("native forward: channel message %d -> group message %d", second.ID, channelForward.ID)
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%d checks failed:\n%s", len(problems), strings.Join(problems, "\n\n"))
	}
}

// TestLiveChannelForward checks native forward on a fresh route.
// The combined test copies first, and that stored link makes a later forward of the same message a no-op.
func TestLiveChannelForward(t *testing.T) {
	chdirModule(t)
	if others := conflictingBots(); len(others) > 0 {
		t.Fatalf("another yukibot process is running:\n%s", strings.Join(others, "\n"))
	}
	settings, err := config.Load()
	mustLive(t, err)

	state := &liveForward{url: settings.DatabaseURL}
	t.Cleanup(func() { state.finish(t) })
	state.pause(t)

	app, client, err := assemble(settings)
	mustLive(t, err)
	startCtx, startCancel := context.WithTimeout(context.Background(), 90*time.Second)
	err = app.Lifecycle.Start(startCtx)
	startCancel()
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = app.Lifecycle.Stop(stopCtx)
		stopCancel()
		t.Fatal(redactLive(err.Error()))
	}
	state.app = app
	state.client = client

	live, err := telegram.NewLiveChat(client)
	mustLive(t, err)
	state.live = live
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dialogs, err := live.Dialogs(ctx)
	mustLive(t, err)
	group, channel, _ := chooseLiveChats(dialogs, state.routeChats(t))
	if group.ID == 0 || channel.ID == 0 {
		t.Fatal("could not find the new group and a channel")
	}
	t.Logf("group %q (%s) channel %q noforwards=%t", group.Title, group.Kind, channel.Title, channel.Noforwards)
	self, err := client.Identity().UserID()
	mustLive(t, err)
	sweepProbes(t, ctx, live, group.ID)
	sweepProbes(t, ctx, live, self)

	state.insertRoute(t, channel.ID, group.ID, "forward")
	source, ok, err := pickTextMessage(ctx, live, channel.ID)
	mustLive(t, err)
	if !ok {
		t.Fatal("the channel has no text message that can be forwarded")
	}
	mark, err := live.MaxID(ctx, group.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, channel.ID, source.ID, "new"))
	delivered, err := live.WaitAfter(ctx, group.ID, mark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return telegram.SameDelivery(source, message)
	})
	mustLive(t, err)
	if delivered.ID == 0 {
		t.Fatalf("native forward from the channel did not arrive\n%s", state.jobs())
	}
	state.track(group.ID, delivered.ID)
	if !delivered.Forwarded {
		t.Fatalf("channel forward arrived without a forward header (noforwards=%t)\n%s", channel.Noforwards, state.jobs())
	}
	t.Logf("native forward: channel message %d -> group message %d", source.ID, delivered.ID)
}

// TestLiveButtonEdit creates two channels, copies a post with a text link and a URL button,
// then removes both and checks the copy follows.
// A basic group cannot keep inline buttons sent by a user account, so the destination is a channel.
func TestLiveButtonEdit(t *testing.T) {
	chdirModule(t)
	if others := conflictingBots(); len(others) > 0 {
		t.Fatalf("another yukibot process is running:\n%s", strings.Join(others, "\n"))
	}
	settings, err := config.Load()
	mustLive(t, err)

	state := &liveForward{url: settings.DatabaseURL}
	t.Cleanup(func() { state.finish(t) })
	state.pause(t)

	app, client, err := assemble(settings)
	mustLive(t, err)
	startCtx, startCancel := context.WithTimeout(context.Background(), 90*time.Second)
	err = app.Lifecycle.Start(startCtx)
	startCancel()
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = app.Lifecycle.Stop(stopCtx)
		stopCancel()
		t.Fatal(redactLive(err.Error()))
	}
	state.app = app
	state.client = client

	live, err := telegram.NewLiveChat(client)
	mustLive(t, err)
	state.live = live
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	sourceChannel, err := live.CreateChannel(ctx, "yukibot-live-buttons")
	mustLive(t, err)
	state.trackChannel(sourceChannel.ID)
	destChannel, err := live.CreateChannel(ctx, "yukibot-live-buttons-copy")
	mustLive(t, err)
	state.trackChannel(destChannel.ID)
	t.Logf("channel %q -> channel %q", sourceChannel.Title, destChannel.Title)
	state.insertRoute(t, sourceChannel.ID, destChannel.ID, "copy")

	nonce := liveNonce()
	original := "yukibot-live-btn-" + nonce + " open"
	edited := "yukibot-live-btn-" + nonce + " plain"
	button := telegram.LiveButton{Text: "docs", URL: "https://example.com/docs"}
	sentID, err := live.SendLinked(ctx, sourceChannel.ID, original, "open", "https://example.com/open", button)
	mustLive(t, err)
	state.track(sourceChannel.ID, sentID)
	posted, ok, err := live.Message(ctx, sourceChannel.ID, sentID)
	mustLive(t, err)
	if !ok || !hasLink(posted, "https://example.com/open") {
		t.Fatalf("source post did not keep the text link links=%v", posted.Links)
	}
	if !hasButton(posted, "docs") {
		err = live.EditLinked(ctx, sourceChannel.ID, sentID, original, "open", "https://example.com/open", button)
		if err != nil && !strings.Contains(err.Error(), "MESSAGE_NOT_MODIFIED") {
			mustLive(t, err)
		}
		posted, ok, err = live.Message(ctx, sourceChannel.ID, sentID)
		mustLive(t, err)
	}
	buttonKept := ok && hasButton(posted, "docs")
	if buttonKept {
		t.Log("source channel kept the inline button")
	} else {
		t.Log("telegram dropped the inline button on a user-authored channel post; checking text-link removal")
	}
	mark, err := live.MaxID(ctx, destChannel.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, sourceChannel.ID, sentID, "new"))
	copied, err := live.WaitAfter(ctx, destChannel.ID, mark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return message.Text == original
	})
	mustLive(t, err)
	if copied.ID == 0 {
		t.Fatalf("button copy did not arrive\n%s", state.jobs())
	}
	state.track(destChannel.ID, copied.ID)
	if !hasLink(copied, "https://example.com/open") || (buttonKept && !hasButton(copied, "docs")) {
		t.Fatalf("copy dropped button=%v links=%v preview=%t\n%s", copied.Buttons, copied.Links, copied.Preview, state.jobs())
	}
	t.Logf("copy kept the text link (button=%t): channel %d -> channel %d", hasButton(copied, "docs"), sentID, copied.ID)

	mustLive(t, live.EditPlain(ctx, sourceChannel.ID, sentID, edited))
	source, ok, err := live.Message(ctx, sourceChannel.ID, sentID)
	mustLive(t, err)
	if !ok || source.Text != edited || len(source.Buttons) != 0 || len(source.Links) != 0 || source.Preview {
		t.Fatalf("source edit left text_ok=%t buttons=%v links=%v preview=%t", ok && source.Text == edited, source.Buttons, source.Links, source.Preview)
	}
	mustLive(t, live.Replay(ctx, sourceChannel.ID, sentID, "edit"))
	updated, ok, err := live.WaitMatch(ctx, destChannel.ID, copied.ID, 45*time.Second, func(message telegram.LiveMessage) bool {
		return message.Text == edited && len(message.Buttons) == 0 && len(message.Links) == 0 && !message.Preview
	})
	mustLive(t, err)
	if !ok {
		current, _, _ := live.Message(ctx, destChannel.ID, copied.ID)
		t.Fatalf("edit left buttons=%v links=%v preview=%t text_matches=%t\n%s", current.Buttons, current.Links, current.Preview, current.Text == edited, state.jobs())
	}
	t.Logf("edit cleared links and buttons on channel message %d", updated.ID)

	dialogs, err := live.Dialogs(ctx)
	mustLive(t, err)
	var sample telegram.LiveMessage
	var sampleChat int64
	var sampleTitle string
	for _, dialog := range dialogs {
		if dialog.Kind == "user" || dialog.ID == sourceChannel.ID || dialog.ID == destChannel.ID {
			continue
		}
		recent, err := live.Recent(ctx, dialog.ID, 30)
		if err != nil {
			t.Logf("scan %s: %s", dialog.Kind, redactLive(err.Error()))
			continue
		}
		buttoned := 0
		for _, message := range recent {
			if len(message.Buttons) == 0 {
				continue
			}
			buttoned++
			if sample.ID == 0 {
				sample = message
				sampleChat = dialog.ID
				sampleTitle = dialog.Title
			}
		}
		t.Logf("scan %s %q buttoned=%d/%d", dialog.Kind, dialog.Title, buttoned, len(recent))
	}
	if sample.ID == 0 {
		t.Log("no recent dialog message has an inline button")
		return
	}
	state.insertRoute(t, sampleChat, destChannel.ID, "copy")
	mark, err = live.MaxID(ctx, destChannel.ID)
	mustLive(t, err)
	mustLive(t, live.Replay(ctx, sampleChat, sample.ID, "new"))
	delivered, err := live.WaitAfter(ctx, destChannel.ID, mark, 60*time.Second, func(message telegram.LiveMessage) bool {
		return len(message.Buttons) > 0 || message.Text == sample.Text
	})
	mustLive(t, err)
	if delivered.ID == 0 {
		t.Fatalf("buttoned post from %q was not copied\n%s", sampleTitle, state.jobs())
	}
	state.track(destChannel.ID, delivered.ID)
	if len(delivered.Buttons) == 0 {
		t.Fatalf("copy of an existing buttoned post dropped %d buttons\n%s", len(sample.Buttons), state.jobs())
	}
	t.Logf("copy kept %d inline buttons from %q", len(delivered.Buttons), sampleTitle)
}

func hasButton(message telegram.LiveMessage, label string) bool {
	for _, button := range message.Buttons {
		if button == label {
			return true
		}
	}
	return false
}

func hasLink(message telegram.LiveMessage, url string) bool {
	for _, link := range message.Links {
		if link == url {
			return true
		}
	}
	return false
}

func sweepProbes(t *testing.T, ctx context.Context, live *telegram.LiveChat, chatID int64) {
	t.Helper()
	recent, err := live.Recent(ctx, chatID, 30)
	mustLive(t, err)
	var ids []int
	for _, message := range recent {
		if strings.Contains(message.Text, "yukibot-live-") {
			ids = append(ids, message.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	t.Logf("removing %d leftover probe messages", len(ids))
	mustLive(t, live.Delete(ctx, chatID, ids))
}

func pickTextMessage(ctx context.Context, live *telegram.LiveChat, chatID int64) (telegram.LiveMessage, bool, error) {
	history, err := live.Recent(ctx, chatID, 30)
	if err != nil {
		return telegram.LiveMessage{}, false, err
	}
	for _, message := range history {
		if message.Command || message.Grouped {
			continue
		}
		if strings.TrimSpace(message.Text) != "" {
			return message, true, nil
		}
	}
	return telegram.LiveMessage{}, false, nil
}

type liveForward struct {
	url              string
	pausedRoutes     []int64
	summarizerPaused bool
	routes           []int64
	jobFloor         int64
	app              *kernel.Application
	client           *telegram.Client
	live             *telegram.LiveChat
	own              map[int64][]int
	channels         []int64
	done             bool
}

func (s *liveForward) pause(t *testing.T) {
	t.Helper()
	floor := s.query(t, `SELECT COALESCE(MAX(id), 0) FROM forwarder_jobs`)
	parsed, err := strconv.ParseInt(floor, 10, 64)
	mustLive(t, err)
	s.jobFloor = parsed
	paused := s.query(t, `UPDATE forwarder_routes SET enabled = false WHERE enabled RETURNING id`)
	s.pausedRoutes = parseIDs(paused)
	name := s.query(t, `UPDATE management_modules SET enabled = false, updated_at = now() WHERE name = 'summarizer' AND enabled RETURNING name`)
	s.summarizerPaused = strings.TrimSpace(name) == "summarizer"
	t.Logf("paused %d routes", len(s.pausedRoutes))
}

func (s *liveForward) insertRoute(t *testing.T, source, destination int64, mode string) int64 {
	t.Helper()
	if mode != "copy" && mode != "forward" {
		t.Fatalf("bad mode %s", mode)
	}
	query := fmt.Sprintf(`INSERT INTO forwarder_routes (
    source_chat_id, destination_chat_id, mode, filter_json, enabled, fallback_to_copy
) VALUES (
    %d, %d, '%s', '{"keywords":[],"allowed_content_types":[],"blocked_content_types":[],"include_service_messages":false}'::jsonb, true, true
) RETURNING id`, source, destination, mode)
	raw := s.query(t, query)
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	mustLive(t, err)
	s.routes = append(s.routes, id)
	return id
}

func (s *liveForward) setMode(t *testing.T, id int64, mode string) {
	t.Helper()
	if mode != "copy" && mode != "forward" {
		t.Fatalf("bad mode %s", mode)
	}
	s.query(t, fmt.Sprintf(`UPDATE forwarder_routes SET mode = '%s' WHERE id = %d`, mode, id))
}

func (s *liveForward) routeChats(t *testing.T) map[int64]struct{} {
	t.Helper()
	raw := s.query(t, `SELECT source_chat_id FROM forwarder_routes UNION SELECT destination_chat_id FROM forwarder_routes`)
	out := map[int64]struct{}{}
	for _, id := range parseIDs(raw) {
		out[id] = struct{}{}
	}
	return out
}

func (s *liveForward) track(chatID int64, messageID int) {
	if s.own == nil {
		s.own = map[int64][]int{}
	}
	s.own[chatID] = append(s.own[chatID], messageID)
}

func (s *liveForward) trackChannel(chatID int64) {
	s.channels = append(s.channels, chatID)
}

func (s *liveForward) query(t *testing.T, sql string) string {
	t.Helper()
	out, err := psqlText(s.url, sql)
	if err != nil {
		t.Fatal(redactLive(err.Error()))
	}
	return out
}

func (s *liveForward) jobs() string {
	out, err := psqlText(s.url, fmt.Sprintf(`SELECT id::text || ' ' || kind || ' ' || state || ' ' || left(COALESCE(last_error, ''), 180) FROM forwarder_jobs WHERE id > %d ORDER BY id`, s.jobFloor))
	if err != nil {
		return redactLive(err.Error())
	}
	if strings.TrimSpace(out) == "" {
		return "no new forwarder jobs"
	}
	return redactLive(out)
}

func (s *liveForward) finish(t *testing.T) {
	if s.done {
		return
	}
	s.done = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if len(s.routes) > 0 {
		_, _ = psqlText(s.url, fmt.Sprintf(`UPDATE forwarder_routes SET enabled = false WHERE id IN (%s)`, joinIDs(s.routes)))
	}
	if s.jobFloor > 0 {
		_, _ = psqlText(s.url, fmt.Sprintf(`DELETE FROM forwarder_jobs WHERE id > %d AND state = 'pending'`, s.jobFloor))
	}
	if s.live != nil {
		s.collectLinks()
		s.deleteTracked(t, ctx)
		_ = sleepLiveWait(ctx, 1500*time.Millisecond)
		s.collectLinks()
		s.deleteTracked(t, ctx)
		for _, id := range s.channels {
			if err := s.live.DeleteChannel(ctx, id); err != nil {
				t.Logf("delete channel %d: %s", id, redactLive(err.Error()))
			}
		}
	}
	if s.app != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := s.app.Lifecycle.Stop(stopCtx); err != nil {
			t.Logf("stop: %s", redactLive(err.Error()))
		}
		stopCancel()
	}
	if len(s.routes) > 0 {
		if _, err := psqlText(s.url, fmt.Sprintf(`DELETE FROM forwarder_routes WHERE id IN (%s)`, joinIDs(s.routes))); err != nil {
			t.Logf("cleanup routes: %s", redactLive(err.Error()))
		}
	}
	if s.jobFloor > 0 {
		if _, err := psqlText(s.url, fmt.Sprintf(`DELETE FROM forwarder_jobs WHERE id > %d`, s.jobFloor)); err != nil {
			t.Logf("cleanup jobs: %s", redactLive(err.Error()))
		}
	}
	if len(s.pausedRoutes) > 0 {
		if _, err := psqlText(s.url, fmt.Sprintf(`UPDATE forwarder_routes SET enabled = true WHERE id IN (%s)`, joinIDs(s.pausedRoutes))); err != nil {
			t.Errorf("restore routes: %s", redactLive(err.Error()))
		}
	}
	if s.summarizerPaused {
		if _, err := psqlText(s.url, `UPDATE management_modules SET enabled = true, updated_at = now() WHERE name = 'summarizer'`); err != nil {
			t.Errorf("restore summarizer: %s", redactLive(err.Error()))
		}
	}
}

func (s *liveForward) collectLinks() {
	raw, err := psqlText(s.url, s.linkQuery())
	if err != nil {
		return
	}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		chatID, chatErr := strconv.ParseInt(fields[0], 10, 64)
		messageID, messageErr := strconv.Atoi(fields[1])
		if chatErr == nil && messageErr == nil {
			s.track(chatID, messageID)
		}
	}
}

func (s *liveForward) deleteTracked(t *testing.T, ctx context.Context) {
	for chatID, ids := range s.own {
		if err := s.live.Delete(ctx, chatID, ids); err != nil {
			t.Logf("cleanup delete chat %d: %s", chatID, redactLive(err.Error()))
		}
	}
}

func sleepLiveWait(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *liveForward) linkQuery() string {
	if len(s.routes) == 0 {
		return `SELECT destination_chat_id::text, destination_message_id::text FROM forwarder_message_links WHERE false`
	}
	return fmt.Sprintf(`SELECT destination_chat_id::text, destination_message_id::text FROM forwarder_message_links WHERE route_id IN (%s)`, joinIDs(s.routes))
}

func chooseLiveChats(dialogs []telegram.DialogInfo, known map[int64]struct{}) (telegram.DialogInfo, telegram.DialogInfo, []string) {
	cutoff := time.Now().Add(-24 * time.Hour)
	var lines []string
	var group, channel, anyChannel telegram.DialogInfo
	for _, dialog := range dialogs {
		if dialog.Kind == "user" {
			continue
		}
		_, routed := known[dialog.ID]
		lines = append(lines, fmt.Sprintf("dialog %s %q routed=%t creator=%t can_post=%t age=%s", dialog.Kind, dialog.Title, routed, dialog.Creator, dialog.CanPost, time.Since(dialog.Top).Round(time.Minute)))
		if dialog.Kind == "channel" && (anyChannel.ID == 0 || dialog.Top.After(anyChannel.Top)) {
			anyChannel = dialog
		}
		if routed || dialog.Top.Before(cutoff) {
			continue
		}
		if group.ID == 0 && dialog.Creator && (dialog.Kind == "group" || dialog.Kind == "supergroup") && dialog.CanPost {
			group = dialog
		}
		if channel.ID == 0 && dialog.Kind == "channel" {
			channel = dialog
		}
	}
	if channel.ID == 0 {
		channel = anyChannel
	}
	return group, channel, lines
}

func pickSourceMessage(ctx context.Context, live *telegram.LiveChat, chatID int64, skip int) (telegram.LiveMessage, bool, error) {
	history, err := live.Recent(ctx, chatID, 30)
	if err != nil {
		return telegram.LiveMessage{}, false, err
	}
	var fallback telegram.LiveMessage
	foundFallback := false
	for _, message := range history {
		if message.ID == skip || message.Command {
			continue
		}
		if message.Grouped {
			if !foundFallback {
				fallback = message
				foundFallback = true
			}
			continue
		}
		if strings.TrimSpace(message.Text) != "" || message.Kind != "text" {
			return message, true, nil
		}
	}
	return fallback, foundFallback, nil
}

func liveNonce() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "probe"
	}
	return hex.EncodeToString(buf[:])
}

func parseIDs(raw string) []int64 {
	var ids []int64
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, err := strconv.ParseInt(line, 10, 64)
		if err == nil && id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func psqlText(url, sql string) (string, error) {
	cmd := exec.Command("psql", url, "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c", sql)
	cmd.Env = append(os.Environ(), "PGCLIENTENCODING=UTF8")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("%s", redactLive(text+" "+err.Error()))
	}
	return text, nil
}

func conflictingBots() []string {
	out, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		return nil
	}
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "go test") {
			continue
		}
		if strings.Contains(line, "cmd/yukibot") || strings.Contains(line, "/yukibot ") || strings.HasSuffix(strings.TrimSpace(line), "/yukibot") {
			hits = append(hits, strings.TrimSpace(line))
		}
	}
	return hits
}
