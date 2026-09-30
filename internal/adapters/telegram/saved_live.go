//go:build live

package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// SavedDriver sends into Saved Messages and replays the stored message through
// the update handler. History omits the out flag, so replay marks it outgoing:
// that is the update a phone session produces for the account's own command.
type SavedDriver struct {
	client *Client
	api    *tg.Client
	sent   []int

	mu   sync.Mutex
	last time.Time
}

// NewSavedDriver binds a connected client.
func NewSavedDriver(client *Client) (*SavedDriver, error) {
	api, err := client.API()
	if err != nil {
		return nil, err
	}
	return &SavedDriver{client: client, api: api}, nil
}

// Send writes text to Saved Messages and returns the message id.
func (d *SavedDriver) Send(ctx context.Context, text string) (int, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	randomID := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if randomID == 0 {
		randomID = 1
	}
	updates, err := call(ctx, d, func() (tg.UpdatesClass, error) {
		return d.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerSelf{},
			Message:  text,
			RandomID: randomID,
		})
	})
	if err != nil {
		return 0, err
	}
	ids := SentMessageIDs(updates)
	if len(ids) == 0 {
		return 0, errNoSentMessage
	}
	d.sent = append(d.sent, ids[0])
	return ids[0], nil
}

// Replay loads the stored message and delivers it as an outgoing update.
func (d *SavedDriver) Replay(ctx context.Context, messageID int) error {
	message, ok, err := d.message(ctx, messageID)
	if err != nil {
		return err
	}
	if !ok {
		return errNoSentMessage
	}
	message.SetOut(true)
	return d.client.Handle(ctx, &tg.UpdateShort{
		Update: &tg.UpdateNewMessage{Message: message},
		Date:   message.Date,
	})
}

// WaitReply polls Saved Messages for a reply to messageID.
func (d *SavedDriver) WaitReply(ctx context.Context, messageID int, timeout time.Duration) (string, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		reply, ok, err := d.findReply(ctx, messageID)
		if err != nil || ok {
			return reply, ok, err
		}
		if time.Now().After(deadline) {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Delete removes the messages this driver sent or observed.
func (d *SavedDriver) Delete(ctx context.Context) {
	d.deleteIDs(ctx, d.sent)
}

// Sweep removes recent Saved Messages whose text is one of the given commands,
// plus replies to those messages. It only matches that exact text.
func (d *SavedDriver) Sweep(ctx context.Context, texts []string) error {
	want := map[string]struct{}{}
	for _, text := range texts {
		want[text] = struct{}{}
	}
	messages, err := d.recent(ctx, 100)
	if err != nil {
		return err
	}
	roots := map[int]struct{}{}
	var ids []int
	for _, message := range messages {
		if _, ok := want[message.Message]; !ok {
			continue
		}
		roots[message.ID] = struct{}{}
		ids = append(ids, message.ID)
	}
	for _, message := range messages {
		header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
		if !ok || header == nil {
			continue
		}
		if _, root := roots[header.ReplyToMsgID]; root {
			ids = append(ids, message.ID)
		}
	}
	d.deleteIDs(ctx, ids)
	return nil
}

func (d *SavedDriver) deleteIDs(ctx context.Context, raw []int) {
	if len(raw) == 0 {
		return
	}
	seen := map[int]struct{}{}
	ids := make([]int, 0, len(raw))
	for _, id := range raw {
		if _, ok := seen[id]; ok || id <= 0 {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for len(ids) > 0 {
		n := len(ids)
		if n > 80 {
			n = 80
		}
		batch := ids[:n]
		ids = ids[n:]
		request := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: batch}
		request.SetFlags()
		_, _ = call(ctx, d, func() (*tg.MessagesAffectedMessages, error) {
			return d.api.MessagesDeleteMessages(ctx, request)
		})
	}
}

func (d *SavedDriver) findReply(ctx context.Context, messageID int) (string, bool, error) {
	messages, err := d.recent(ctx, 40)
	if err != nil {
		return "", false, err
	}
	for _, message := range messages {
		header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
		if !ok || header == nil || header.ReplyToMsgID != messageID {
			continue
		}
		d.sent = append(d.sent, message.ID)
		return message.Message, true, nil
	}
	return "", false, nil
}

func (d *SavedDriver) message(ctx context.Context, messageID int) (*tg.Message, bool, error) {
	messages, err := d.recent(ctx, 40)
	if err != nil {
		return nil, false, err
	}
	for _, message := range messages {
		if message.ID == messageID {
			return message, true, nil
		}
	}
	return nil, false, nil
}

func (d *SavedDriver) recent(ctx context.Context, limit int) ([]*tg.Message, error) {
	raw, err := call(ctx, d, func() (tg.MessagesMessagesClass, error) {
		return d.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  &tg.InputPeerSelf{},
			Limit: limit,
		})
	})
	if err != nil {
		return nil, err
	}
	var classes []tg.MessageClass
	switch box := raw.(type) {
	case *tg.MessagesMessages:
		classes = box.Messages
	case *tg.MessagesMessagesSlice:
		classes = box.Messages
	case *tg.MessagesChannelMessages:
		classes = box.Messages
	default:
		return nil, nil
	}
	out := make([]*tg.Message, 0, len(classes))
	for _, class := range classes {
		message, ok := class.(*tg.Message)
		if ok {
			out = append(out, message)
		}
	}
	return out, nil
}

func (d *SavedDriver) pace(ctx context.Context) error {
	d.mu.Lock()
	wait := time.Until(d.last.Add(900 * time.Millisecond))
	if wait < 0 {
		wait = 0
	}
	d.last = time.Now().Add(wait)
	d.mu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func call[T any](ctx context.Context, d *SavedDriver, fn func() (T, error)) (T, error) {
	var zero T
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if err := d.pace(ctx); err != nil {
			return zero, err
		}
		value, err := fn()
		if err == nil {
			return value, nil
		}
		last = err
		waited, waitErr := tgerr.FloodWait(ctx, err)
		if !waited {
			return zero, waitErr
		}
	}
	return zero, last
}

var errNoSentMessage = errString("telegram did not return a sent message")

type errString string

func (e errString) Error() string { return string(e) }
