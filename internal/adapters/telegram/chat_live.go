//go:build live

package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// LiveMessage is the part of a Telegram message the live checks compare.
type LiveMessage struct {
	ID        int
	Text      string
	Kind      string
	Forwarded bool
	Grouped   bool
	Command   bool
	Buttons   []string
	Links     []string
	Preview   bool
}

// LiveButton is one inline URL button on a test message.
type LiveButton struct {
	Text string
	URL  string
}

// DialogInfo is one chat from the recent dialog list.
type DialogInfo struct {
	ID         int64
	Title      string
	Kind       string
	Username   string
	Top        time.Time
	TopMessage int
	Creator    bool
	CanPost    bool
	Noforwards bool
}

// LiveChat is a paced RPC helper for the logged-in session.
type LiveChat struct {
	client *Client
	api    *tg.Client

	mu   sync.Mutex
	last time.Time
}

// NewLiveChat binds a connected client.
func NewLiveChat(client *Client) (*LiveChat, error) {
	api, err := client.API()
	if err != nil {
		return nil, err
	}
	return &LiveChat{client: client, api: api}, nil
}

// Dialogs returns the newest dialogs, up to two pages.
func (l *LiveChat) Dialogs(ctx context.Context) ([]DialogInfo, error) {
	return l.dialogs(ctx, 0, 2)
}

// DialogPages reads one dialog folder. Folder 1 is the archive.
func (l *LiveChat) DialogPages(ctx context.Context, folder, pages int) ([]DialogInfo, error) {
	return l.dialogs(ctx, folder, pages)
}

func (l *LiveChat) dialogs(ctx context.Context, folder, pages int) ([]DialogInfo, error) {
	if pages <= 0 {
		pages = 1
	}
	var offsetDate, offsetID int
	var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
	var out []DialogInfo
	for page := 0; page < pages; page++ {
		request := &tg.MessagesGetDialogsRequest{
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
			Limit:      100,
		}
		if folder > 0 {
			request.SetFolderID(folder)
		}
		box, err := liveCall(ctx, l, func() (tg.MessagesDialogsClass, error) {
			return l.api.MessagesGetDialogs(ctx, request)
		})
		if err != nil {
			return nil, err
		}
		dialogs, messages, _, chats := splitDialogs(box)
		byID := map[int64]tg.ChatClass{}
		for _, chat := range chats {
			id, ok := ChatDialogID(chat)
			if ok {
				byID[id] = chat
			}
		}
		var last *tg.Dialog
		for _, dialog := range dialogs {
			concrete, ok := dialog.(*tg.Dialog)
			if !ok || concrete == nil {
				continue
			}
			last = concrete
			id, ok := DialogID(concrete.Peer)
			if !ok {
				continue
			}
			info := DialogInfo{
				ID:         id,
				Top:        time.Unix(int64(messageDate(messages, concrete.TopMessage)), 0).UTC(),
				TopMessage: concrete.TopMessage,
			}
			if chat, ok := byID[id]; ok {
				if classified, ok := classifyChat(chat); ok {
					classified.Top = info.Top
					classified.TopMessage = info.TopMessage
					info = classified
				}
			} else if _, isUser := concrete.Peer.(*tg.PeerUser); isUser {
				info.Kind = "user"
			}
			if info.Kind == "" {
				continue
			}
			out = append(out, info)
		}
		if len(dialogs) < 100 || last == nil {
			break
		}
		offsetID = last.TopMessage
		offsetDate = messageDate(messages, last.TopMessage)
		id, ok := DialogID(last.Peer)
		if !ok {
			break
		}
		peer, err := l.peer(ctx, id)
		if err != nil || peer == nil {
			break
		}
		offsetPeer = peer.InputPeer()
	}
	return out, nil
}

// Recent returns the newest messages without Telegram types.
func (l *LiveChat) Recent(ctx context.Context, chatID int64, limit int) ([]LiveMessage, error) {
	history, err := l.history(ctx, chatID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]LiveMessage, len(history))
	for i, message := range history {
		out[i] = liveMessage(message)
	}
	return out, nil
}

// MaxID is the newest message id, or 0 when the chat is empty.
func (l *LiveChat) MaxID(ctx context.Context, chatID int64) (int, error) {
	history, err := l.history(ctx, chatID, 1)
	if err != nil || len(history) == 0 {
		return 0, err
	}
	return history[0].ID, nil
}

// History returns the newest messages in a chat.
func (l *LiveChat) history(ctx context.Context, chatID int64, limit int) ([]*tg.Message, error) {
	if limit <= 0 {
		limit = 20
	}
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return nil, err
	}
	box, err := liveCall(ctx, l, func() (tg.MessagesMessagesClass, error) {
		return l.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer.InputPeer(),
			Limit: limit,
		})
	})
	if err != nil {
		return nil, err
	}
	classes, _, _ := splitMessages(box)
	return concreteMessages(classes), nil
}

// Send writes text and returns the new message id.
func (l *LiveChat) Send(ctx context.Context, chatID int64, text string) (int, error) {
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return 0, err
	}
	randomID, err := liveRandomID()
	if err != nil {
		return 0, err
	}
	updates, err := liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     peer.InputPeer(),
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
	return ids[0], nil
}

// Edit replaces the text of one message.
func (l *LiveChat) Edit(ctx context.Context, chatID int64, messageID int, text string) error {
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return err
	}
	request := &tg.MessagesEditMessageRequest{Peer: peer.InputPeer(), ID: messageID}
	request.SetMessage(text)
	_, err = liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.MessagesEditMessage(ctx, request)
	})
	return err
}

// EditLinked replaces the text, one text link, and one URL button.
func (l *LiveChat) EditLinked(ctx context.Context, chatID int64, messageID int, text, linkLabel, linkURL string, button LiveButton) error {
	offset := strings.Index(text, linkLabel)
	if offset < 0 || linkLabel == "" || button.Text == "" {
		return errString("link label or button is missing")
	}
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return err
	}
	request := &tg.MessagesEditMessageRequest{Peer: peer.InputPeer(), ID: messageID}
	request.SetMessage(text)
	request.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: offset, Length: len(linkLabel), URL: linkURL},
	})
	request.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonURL{Text: button.Text, URL: button.URL}},
	}}})
	_, err = liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.MessagesEditMessage(ctx, request)
	})
	return err
}

// EditPlain replaces the text and clears inline buttons, text links, and the link preview.
func (l *LiveChat) EditPlain(ctx context.Context, chatID int64, messageID int, text string) error {
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return err
	}
	request := &tg.MessagesEditMessageRequest{Peer: peer.InputPeer(), ID: messageID}
	request.SetMessage(text)
	request.SetNoWebpage(true)
	request.SetEntities([]tg.MessageEntityClass{})
	request.SetReplyMarkup(&tg.ReplyInlineMarkup{})
	_, err = liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.MessagesEditMessage(ctx, request)
	})
	return err
}

// SendLinked writes text with one text link and one URL button.
func (l *LiveChat) SendLinked(ctx context.Context, chatID int64, text, linkLabel, linkURL string, button LiveButton) (int, error) {
	offset := strings.Index(text, linkLabel)
	if offset < 0 || linkLabel == "" || button.Text == "" {
		return 0, errString("link label or button is missing")
	}
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return 0, err
	}
	randomID, err := liveRandomID()
	if err != nil {
		return 0, err
	}
	request := &tg.MessagesSendMessageRequest{
		Peer:     peer.InputPeer(),
		Message:  text,
		RandomID: randomID,
	}
	request.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: offset, Length: len(linkLabel), URL: linkURL},
	})
	request.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonURL{Text: button.Text, URL: button.URL}},
	}}})
	updates, err := liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.MessagesSendMessage(ctx, request)
	})
	if err != nil {
		return 0, err
	}
	ids := SentMessageIDs(updates)
	if len(ids) == 0 {
		return 0, errNoSentMessage
	}
	return ids[0], nil
}

// CreateChannel creates a broadcast channel and returns its dialog.
func (l *LiveChat) CreateChannel(ctx context.Context, title string) (DialogInfo, error) {
	request := &tg.ChannelsCreateChannelRequest{Title: title, About: "temporary yukibot test"}
	request.SetBroadcast(true)
	updates, err := liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.ChannelsCreateChannel(ctx, request)
	})
	if err != nil {
		return DialogInfo{}, err
	}
	l.observe(ctx, updates)
	for _, chat := range liveUpdateChats(updates) {
		info, ok := classifyChat(chat)
		if ok && info.Kind == "channel" && info.Title == title {
			return info, nil
		}
	}
	return DialogInfo{}, errString("created channel was not returned")
}

// DeleteChannel deletes a channel owned by the logged-in account.
func (l *LiveChat) DeleteChannel(ctx context.Context, chatID int64) error {
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return err
	}
	channel, ok := peer.(peers.Channel)
	if !ok {
		return errString("chat is not a channel")
	}
	_, err = liveCall(ctx, l, func() (tg.UpdatesClass, error) {
		return l.api.ChannelsDeleteChannel(ctx, channel.InputChannel())
	})
	return err
}

// Message loads one message without Telegram types.
func (l *LiveChat) Message(ctx context.Context, chatID int64, messageID int) (LiveMessage, bool, error) {
	message, ok, err := l.Fetch(ctx, chatID, messageID)
	if err != nil || !ok {
		return LiveMessage{}, ok, err
	}
	return liveMessage(message), true, nil
}

// WaitMatch polls one message until match reports success.
func (l *LiveChat) WaitMatch(ctx context.Context, chatID int64, messageID int, timeout time.Duration, match func(LiveMessage) bool) (LiveMessage, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		message, ok, err := l.Message(ctx, chatID, messageID)
		if err != nil {
			return LiveMessage{}, false, err
		}
		if ok && match(message) {
			return message, true, nil
		}
		if time.Now().After(deadline) {
			return LiveMessage{}, false, nil
		}
		if err := sleepLive(ctx, 400*time.Millisecond); err != nil {
			return LiveMessage{}, false, err
		}
	}
}

func (l *LiveChat) observe(ctx context.Context, updates tg.UpdatesClass) {
	users, chats := liveUpdateEntities(updates)
	l.client.Observe(ctx, liveEntities(users, chats))
}

func liveUpdateEntities(updates tg.UpdatesClass) ([]tg.UserClass, []tg.ChatClass) {
	switch updates := updates.(type) {
	case *tg.Updates:
		return updates.Users, updates.Chats
	case *tg.UpdatesCombined:
		return updates.Users, updates.Chats
	default:
		return nil, nil
	}
}

func liveUpdateChats(updates tg.UpdatesClass) []tg.ChatClass {
	_, chats := liveUpdateEntities(updates)
	return chats
}

func liveEntities(users []tg.UserClass, chats []tg.ChatClass) tg.Entities {
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

// Delete revokes messages in one chat.
func (l *LiveChat) Delete(ctx context.Context, chatID int64, ids []int) error {
	ids = positiveIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return err
	}
	if channel, ok := peer.(peers.Channel); ok {
		_, err = liveCall(ctx, l, func() (*tg.MessagesAffectedMessages, error) {
			return l.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: channel.InputChannel(),
				ID:      ids,
			})
		})
		return err
	}
	request := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: ids}
	request.SetFlags()
	_, err = liveCall(ctx, l, func() (*tg.MessagesAffectedMessages, error) {
		return l.api.MessagesDeleteMessages(ctx, request)
	})
	return err
}

// Replay delivers a stored message through the update handler as an incoming
// new, edit, or delete. Same-session sends are outgoing, which the forwarder ignores.
func (l *LiveChat) Replay(ctx context.Context, chatID int64, messageID int, kind string) error {
	classes, users, chats, err := l.fetch(ctx, chatID, messageID)
	if err != nil && kind != "delete" {
		return err
	}
	var message *tg.Message
	for _, class := range classes {
		if candidate, ok := class.(*tg.Message); ok && candidate.ID == messageID {
			message = candidate
			break
		}
	}
	if message == nil && kind != "delete" {
		return errNoSentMessage
	}
	if message != nil {
		message.SetOut(false)
	}
	update, err := liveUpdate(chatID, messageID, message, kind)
	if err != nil {
		return err
	}
	date := int(time.Now().Unix())
	if message != nil && message.Date > 0 {
		date = message.Date
	}
	return l.client.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   users,
		Chats:   chats,
		Date:    date,
	})
}

// Fetch returns one message. The bool is false when Telegram no longer has it.
func (l *LiveChat) Fetch(ctx context.Context, chatID int64, messageID int) (*tg.Message, bool, error) {
	classes, _, _, err := l.fetch(ctx, chatID, messageID)
	if err != nil {
		return nil, false, err
	}
	for _, class := range classes {
		message, ok := class.(*tg.Message)
		if ok && message.ID == messageID {
			return message, true, nil
		}
	}
	return nil, false, nil
}

// WaitAfter polls history until a message newer than afterID matches.
// A zero ID means the timeout expired.
func (l *LiveChat) WaitAfter(ctx context.Context, chatID int64, afterID int, timeout time.Duration, match func(LiveMessage) bool) (LiveMessage, error) {
	deadline := time.Now().Add(timeout)
	for {
		history, err := l.Recent(ctx, chatID, 40)
		if err != nil {
			return LiveMessage{}, err
		}
		for _, message := range history {
			if message.ID <= afterID {
				continue
			}
			if match == nil || match(message) {
				return message, nil
			}
		}
		if time.Now().After(deadline) {
			return LiveMessage{}, nil
		}
		if err := sleepLive(ctx, 400*time.Millisecond); err != nil {
			return LiveMessage{}, err
		}
	}
}

// WaitText polls one message until its body contains text.
func (l *LiveChat) WaitText(ctx context.Context, chatID int64, messageID int, text string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		message, ok, err := l.Fetch(ctx, chatID, messageID)
		if err != nil {
			return false, err
		}
		if ok && message.Message == text {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleepLive(ctx, 400*time.Millisecond); err != nil {
			return false, err
		}
	}
}

// WaitGone polls until a message is no longer available.
func (l *LiveChat) WaitGone(ctx context.Context, chatID int64, messageID int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		_, ok, err := l.Fetch(ctx, chatID, messageID)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleepLive(ctx, 400*time.Millisecond); err != nil {
			return false, err
		}
	}
}

func (l *LiveChat) fetch(ctx context.Context, chatID int64, messageID int) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass, error) {
	peer, err := l.peer(ctx, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	query := []tg.InputMessageClass{&tg.InputMessageID{ID: messageID}}
	if channel, ok := peer.(peers.Channel); ok {
		box, err := liveCall(ctx, l, func() (tg.MessagesMessagesClass, error) {
			return l.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: channel.InputChannel(),
				ID:      query,
			})
		})
		if err != nil {
			return nil, nil, nil, err
		}
		messages, users, chats := splitMessages(box)
		return messages, users, chats, nil
	}
	box, err := liveCall(ctx, l, func() (tg.MessagesMessagesClass, error) {
		return l.api.MessagesGetMessages(ctx, query)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	messages, users, chats := splitMessages(box)
	return messages, users, chats, nil
}

func (l *LiveChat) peer(ctx context.Context, chatID int64) (peers.Peer, error) {
	manager, err := l.client.Peers()
	if err != nil {
		return nil, err
	}
	return manager.ResolveTDLibID(ctx, constant.TDLibPeerID(chatID))
}

func (l *LiveChat) pace(ctx context.Context) error {
	l.mu.Lock()
	wait := time.Until(l.last.Add(750 * time.Millisecond))
	if wait < 0 {
		wait = 0
	}
	l.last = time.Now().Add(wait)
	l.mu.Unlock()
	return sleepLive(ctx, wait)
}

func classifyChat(chat tg.ChatClass) (DialogInfo, bool) {
	switch chat := chat.(type) {
	case *tg.Channel:
		info := DialogInfo{
			ID:         ChannelDialogID(chat.ID),
			Title:      chat.Title,
			Kind:       "channel",
			Creator:    chat.Creator,
			CanPost:    chat.Creator,
			Noforwards: chat.Noforwards,
		}
		if chat.Megagroup {
			info.Kind = "supergroup"
			info.CanPost = !chat.Left
		}
		if rights, ok := chat.GetAdminRights(); ok && rights.PostMessages {
			info.CanPost = true
		}
		if chat.Left {
			info.CanPost = false
		}
		if name, ok := chat.GetUsername(); ok {
			info.Username = name
		}
		return info, true
	case *tg.Chat:
		return DialogInfo{
			ID:      -chat.ID,
			Title:   chat.Title,
			Kind:    "group",
			Creator: chat.Creator,
			CanPost: !chat.Left && !chat.Deactivated,
		}, true
	default:
		return DialogInfo{}, false
	}
}

func liveUpdate(chatID int64, messageID int, message *tg.Message, kind string) (tg.UpdateClass, error) {
	channel := constant.TDLibPeerID(chatID).IsChannel()
	switch kind {
	case "new":
		if message == nil {
			return nil, errNoSentMessage
		}
		if channel {
			return &tg.UpdateNewChannelMessage{Message: message}, nil
		}
		return &tg.UpdateNewMessage{Message: message}, nil
	case "edit":
		if message == nil {
			return nil, errNoSentMessage
		}
		if channel {
			return &tg.UpdateEditChannelMessage{Message: message}, nil
		}
		return &tg.UpdateEditMessage{Message: message}, nil
	case "delete":
		if channel {
			return &tg.UpdateDeleteChannelMessages{
				ChannelID: constant.TDLibPeerID(chatID).ToPlain(),
				Messages:  []int{messageID},
				PtsCount:  1,
			}, nil
		}
		return &tg.UpdateDeleteMessages{Messages: []int{messageID}}, nil
	default:
		return nil, errString("unknown replay kind")
	}
}

func liveMessage(message *tg.Message) LiveMessage {
	if message == nil {
		return LiveMessage{}
	}
	text := message.Message
	_, forwarded := message.GetFwdFrom()
	_, grouped := message.GetGroupedID()
	return LiveMessage{
		ID:        message.ID,
		Text:      text,
		Kind:      liveKind(message),
		Forwarded: forwarded,
		Grouped:   grouped,
		Command:   strings.HasPrefix(strings.TrimSpace(text), "/"),
		Buttons:   buttonLabels(message),
		Links:     linkURLs(message),
		Preview:   hasWebPreview(message),
	}
}

func hasWebPreview(message *tg.Message) bool {
	media, ok := message.GetMedia()
	if !ok || media == nil {
		return false
	}
	_, ok = media.(*tg.MessageMediaWebPage)
	return ok
}

func buttonLabels(message *tg.Message) []string {
	markup, ok := message.GetReplyMarkup()
	if !ok || markup == nil {
		return nil
	}
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok {
		return []string{fmt.Sprintf("%T", markup)}
	}
	var labels []string
	for _, row := range inline.Rows {
		for _, button := range row.Buttons {
			labels = append(labels, buttonText(button))
		}
	}
	return labels
}

func buttonText(button tg.KeyboardButtonClass) string {
	switch button := button.(type) {
	case *tg.KeyboardButtonURL:
		return button.Text
	case *tg.KeyboardButton:
		return button.Text
	case *tg.KeyboardButtonCallback:
		return button.Text
	case *tg.KeyboardButtonURLAuth:
		return button.Text
	case *tg.KeyboardButtonWebView:
		return button.Text
	case *tg.KeyboardButtonSimpleWebView:
		return button.Text
	default:
		if button == nil {
			return ""
		}
		return fmt.Sprintf("%T", button)
	}
}

func linkURLs(message *tg.Message) []string {
	entities, ok := message.GetEntities()
	if !ok {
		return nil
	}
	var urls []string
	for _, entity := range entities {
		switch entity := entity.(type) {
		case *tg.MessageEntityTextURL:
			urls = append(urls, entity.URL)
		case *tg.MessageEntityURL:
			start := entity.Offset
			end := entity.Offset + entity.Length
			if start >= 0 && end <= len(message.Message) && start < end {
				urls = append(urls, message.Message[start:end])
			}
		}
	}
	return urls
}

func liveKind(message *tg.Message) string {
	if message.Media == nil {
		return "text"
	}
	switch message.Media.(type) {
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return "document"
	default:
		return "media"
	}
}

// SameDelivery reports whether destination looks like a copy or forward of source.
func SameDelivery(source, destination LiveMessage) bool {
	if text := strings.TrimSpace(source.Text); text != "" {
		return strings.Contains(destination.Text, text)
	}
	return source.Kind != "text" && destination.Kind == source.Kind
}

func concreteMessages(classes []tg.MessageClass) []*tg.Message {
	out := make([]*tg.Message, 0, len(classes))
	for _, class := range classes {
		message, ok := class.(*tg.Message)
		if ok {
			out = append(out, message)
		}
	}
	return out
}

func positiveIDs(ids []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func liveRandomID() (int64, error) {
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

func sleepLive(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
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

func liveCall[T any](ctx context.Context, chat *LiveChat, fn func() (T, error)) (T, error) {
	var zero T
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if err := chat.pace(ctx); err != nil {
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
