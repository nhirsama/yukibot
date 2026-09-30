package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"unicode"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// CommandOutcome is the control-plane result for one message.
type CommandOutcome struct {
	Consumed  bool
	Response  *string
	Duplicate bool
}

// CommandPlane is the kernel dispatcher as seen by the Telegram adapter.
type CommandPlane interface {
	Dispatch(ctx context.Context, text string, chatID int64, messageID int, actorID *int64, outgoing bool) (CommandOutcome, error)
	Recognizes(text string) bool
}

// CommandRouter delivers command replies and swallows the bot's own responses.
type CommandRouter struct {
	plane  CommandPlane
	client *Client
	log    *slog.Logger
	mu     sync.Mutex
	seen   map[[2]int64]struct{}
	order  [][2]int64
}

// NewCommandRouter returns a router. A nil logger discards records.
func NewCommandRouter(plane CommandPlane, client *Client, logger *slog.Logger) *CommandRouter {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &CommandRouter{plane: plane, client: client, log: logger, seen: map[[2]int64]struct{}{}}
}

// Route reports whether the message was consumed by the control plane.
// Edited commands are recognized and not executed.
func (r *CommandRouter) Route(ctx context.Context, message contracts.TelegramMessage, execute bool) (bool, error) {
	if r == nil || r.plane == nil {
		return false, nil
	}
	key := [2]int64{message.Ref.ChatID, int64(message.Ref.MessageID)}
	if message.Outgoing && r.seenResponse(key) {
		r.log.Info("telegram control response consumed", "chat_id", message.Ref.ChatID, "message_id", message.Ref.MessageID)
		return true, nil
	}
	if !execute {
		return r.plane.Recognizes(message.Text), nil
	}
	outcome, err := r.plane.Dispatch(ctx, message.Text, message.Ref.ChatID, message.Ref.MessageID, message.SenderID, message.Outgoing)
	if err != nil {
		return false, err
	}
	if !outcome.Consumed {
		return false, nil
	}
	name, _, _ := splitCommand(message.Text)
	r.log.Info("telegram control command consumed",
		"command", name,
		"chat_id", message.Ref.ChatID,
		"message_id", message.Ref.MessageID,
		"actor_id", message.SenderID,
		"outgoing", message.Outgoing,
		"duplicate", outcome.Duplicate,
		"has_response", outcome.Response != nil && *outcome.Response != "",
	)
	if outcome.Response != nil && *outcome.Response != "" {
		sentID, err := r.reply(ctx, message, *outcome.Response)
		if err != nil {
			return true, err
		}
		r.remember(message.Ref.ChatID, sentID)
		r.log.Info("telegram control response sent",
			"command", name,
			"chat_id", message.Ref.ChatID,
			"message_id", message.Ref.MessageID,
			"response_message_id", sentID,
		)
	}
	return true, nil
}

func (r *CommandRouter) reply(ctx context.Context, message contracts.TelegramMessage, text string) (int, error) {
	api, err := r.client.API()
	if err != nil {
		return 0, err
	}
	manager, err := r.client.Peers()
	if err != nil {
		return 0, err
	}
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(message.Ref.ChatID))
	if err != nil || peer == nil {
		return 0, fmt.Errorf("command chat %d is not in the peer cache", message.Ref.ChatID)
	}
	r.client.Remember(peer)
	randomID, err := randomID()
	if err != nil {
		return 0, err
	}
	request := &tg.MessagesSendMessageRequest{
		Peer:     peer.InputPeer(),
		Message:  text,
		RandomID: randomID,
	}
	request.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: message.Ref.MessageID})
	var sent int
	err = r.client.Limiter().Slot(ctx, message.Ref.ChatID, func() error {
		updates, err := api.MessagesSendMessage(ctx, request)
		if err != nil {
			return err
		}
		ids := SentMessageIDs(updates)
		if len(ids) == 0 {
			return fmt.Errorf("telegram did not return a sent message")
		}
		sent = ids[0]
		return nil
	})
	return sent, err
}

func (r *CommandRouter) seenResponse(key [2]int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seen[key]
	return ok
}

func (r *CommandRouter) remember(chatID int64, messageID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]int64{chatID, int64(messageID)}
	if _, ok := r.seen[key]; ok {
		return
	}
	r.seen[key] = struct{}{}
	r.order = append(r.order, key)
	if len(r.order) > 4096 {
		delete(r.seen, r.order[0])
		r.order = r.order[1:]
	}
}

// splitCommand splits the first token and preserves the remainder, including leading spaces.
func splitCommand(text string) (string, string, bool) {
	if text == "" || text[0] != '/' {
		return "", "", false
	}
	for index, character := range text {
		if unicode.IsSpace(character) {
			return text[:index], text[index+len(string(character)):], true
		}
	}
	return text, "", true
}

func randomID() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id, nil
}
