package infra

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

const approvalPendingText = "入群申请已提交, 审批通过后请重新执行路由命令"

func randomID() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

func entitiesFrom(users []tg.UserClass, chats []tg.ChatClass) tg.Entities {
	entities := tg.Entities{
		Users:    map[int64]*tg.User{},
		Chats:    map[int64]*tg.Chat{},
		Channels: map[int64]*tg.Channel{},
	}
	for _, user := range users {
		concrete, ok := user.(*tg.User)
		if ok && concrete != nil {
			entities.Users[concrete.ID] = concrete
		}
	}
	for _, chat := range chats {
		switch concrete := chat.(type) {
		case *tg.Chat:
			if concrete != nil {
				entities.Chats[concrete.ID] = concrete
			}
		case *tg.Channel:
			if concrete != nil {
				entities.Channels[concrete.ID] = concrete
			}
		}
	}
	return entities
}

func updatesEntities(updates tg.UpdatesClass) ([]tg.UserClass, []tg.ChatClass) {
	switch updates := updates.(type) {
	case *tg.Updates:
		return updates.Users, updates.Chats
	case *tg.UpdatesCombined:
		return updates.Users, updates.Chats
	default:
		return nil, nil
	}
}

func splitMessages(box tg.MessagesMessagesClass) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass) {
	switch box := box.(type) {
	case *tg.MessagesMessages:
		return box.Messages, box.Users, box.Chats
	case *tg.MessagesMessagesSlice:
		return box.Messages, box.Users, box.Chats
	case *tg.MessagesChannelMessages:
		return box.Messages, box.Users, box.Chats
	default:
		return nil, nil, nil
	}
}

func messageClassID(message tg.MessageClass) int {
	switch message := message.(type) {
	case *tg.Message:
		if message == nil {
			return 0
		}
		return message.ID
	case *tg.MessageService:
		if message == nil {
			return 0
		}
		return message.ID
	default:
		return 0
	}
}

func (g *Gateway) observeUpdates(ctx context.Context, updates tg.UpdatesClass) {
	if g == nil || g.client == nil || updates == nil {
		return
	}
	users, chats := updatesEntities(updates)
	g.client.Observe(ctx, entitiesFrom(users, chats))
}

func (g *Gateway) observeBox(ctx context.Context, box tg.MessagesMessagesClass) {
	if g == nil || g.client == nil || box == nil {
		return
	}
	_, users, chats := splitMessages(box)
	g.client.Observe(ctx, entitiesFrom(users, chats))
}

func inviteHash(link string) string {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	if parsed.Scheme == "tg" && parsed.Host == "join" {
		values := parsed.Query()["invite"]
		if len(values) == 0 || values[0] == "" {
			return ""
		}
		return values[0]
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || !telegramHost(parsed.Host) {
		return ""
	}
	path := strings.Trim(parsed.Path, "/")
	if strings.HasPrefix(path, "+") && len(path) > 1 {
		return path[1:]
	}
	const prefix = "joinchat/"
	if strings.HasPrefix(path, prefix) && len(path) > len(prefix) {
		return path[len(prefix):]
	}
	return ""
}

func publicUsername(reference string) string {
	if strings.HasPrefix(reference, "@") && len(reference) > 1 {
		return reference[1:]
	}
	parsed, err := url.Parse(reference)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !telegramHost(parsed.Host) {
		return ""
	}
	path := strings.Trim(parsed.Path, "/")
	if path == "" || strings.Contains(path, "/") || strings.HasPrefix(path, "+") || strings.HasPrefix(path, "joinchat") {
		return ""
	}
	return path
}

func telegramHost(host string) bool {
	switch strings.ToLower(host) {
	case "t.me", "telegram.me", "www.t.me", "www.telegram.me":
		return true
	default:
		return false
	}
}

func metadataName(err error) string {
	if err == nil {
		return ""
	}
	if rpc, ok := tgerr.As(err); ok && rpc.Type != "" {
		return rpc.Type
	}
	name := fmt.Sprintf("%T", err)
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return strings.TrimPrefix(name, "*")
}

// translate maps MTProto and transport failures onto the forwarder error types.
// context.Canceled passes through. A deadline or other network error waits one second.
func translate(err error, nativeForward bool) error {
	if err == nil {
		return nil
	}
	var (
		notFound    forwarder.MessageNotFound
		notModified forwarder.MessageNotModified
		unsupported forwarder.NativeForwardUnsupported
		permanent   forwarder.PermanentDeliveryError
		retry       forwarder.RetryAfter
		mismatch    forwarder.DeliveryResultMismatch
		partial     forwarder.PartialDeliveryState
		value       forwarder.ValueError
	)
	switch {
	case errors.As(err, &notFound), errors.As(err, &notModified), errors.As(err, &unsupported),
		errors.As(err, &permanent), errors.As(err, &retry), errors.As(err, &mismatch),
		errors.As(err, &partial), errors.As(err, &value):
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return forwarder.RetryAfter{Delay: time.Second}
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		if wait < time.Second {
			wait = time.Second
		}
		return forwarder.RetryAfter{Delay: wait}
	}
	if rpc, ok := tgerr.AsType(err, tg.ErrSlowmodeWait); ok {
		wait := time.Duration(rpc.Argument) * time.Second
		if wait < time.Second {
			wait = time.Second
		}
		return forwarder.RetryAfter{Delay: wait}
	}
	if tg.IsMessageNotModified(err) {
		return forwarder.MessageNotModified{}
	}
	if tg.IsMessageIDInvalid(err) || tgerr.Is(err, "MSG_ID_INVALID") {
		return forwarder.MessageNotFound{}
	}
	if nativeForward && tgerr.Is(err, "CHAT_FORWARDS_RESTRICTED", "CHAT_SEND_MEDIA_FORBIDDEN", "USER_BANNED_IN_CHANNEL") {
		return forwarder.NativeForwardUnsupported{}
	}
	if rpc, ok := tgerr.As(err); ok {
		return forwarder.NewPermanentDeliveryError(fmt.Sprintf("Telegram RPC %s: %s", rpc.Type, err.Error()))
	}
	var network net.Error
	if errors.As(err, &network) {
		return forwarder.RetryAfter{Delay: time.Second}
	}
	return err
}
