package infra

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
)

const pageSize = 100

var linkPattern = regexp.MustCompile(`(?i)https?://[^\s<>()]+`)

var mediaLabels = map[contracts.ContentType]string{
	contracts.ContentPhoto:     "图片",
	contracts.ContentVideo:     "视频",
	contracts.ContentDocument:  "文件",
	contracts.ContentAudio:     "音频",
	contracts.ContentVoice:     "语音",
	contracts.ContentVideoNote: "视频消息",
	contracts.ContentSticker:   "贴纸",
	contracts.ContentAnimation: "动画",
	contracts.ContentLocation:  "位置",
	contracts.ContentContact:   "联系人",
	contracts.ContentVenue:     "地点",
	contracts.ContentGame:      "游戏",
}

// Gateway is the gotd adapter for the summarizer Telegram port.
type Gateway struct {
	client *telegram.Client
}

var _ summarizer.Telegram = (*Gateway)(nil)

// New returns a gateway bound to the process Telegram client.
func New(client *telegram.Client) *Gateway {
	return &Gateway{client: client}
}

// ResolveEndpoint resolves an id, @username, public link, or topic link.
func (g *Gateway) ResolveEndpoint(ctx context.Context, reference string) (summarizer.SummaryEndpoint, error) {
	parsed, err := summarizer.ParseEndpointReference(reference)
	if err != nil {
		return summarizer.SummaryEndpoint{}, err
	}
	peer, err := g.resolveParsed(ctx, parsed)
	if err != nil {
		if isValue(err) {
			return summarizer.SummaryEndpoint{}, err
		}
		return summarizer.SummaryEndpoint{}, &summarizer.SummarizerError{Msg: "无法解析 Telegram 聊天: " + err.Error()}
	}
	chatID := int64(peer.TDLibPeerID())
	if parsed.Numeric && chatID != parsed.ChatID {
		return summarizer.SummaryEndpoint{}, &summarizer.ValueError{Msg: fmt.Sprintf("Telegram reference resolves to chat %d, expected %d", chatID, parsed.ChatID)}
	}
	g.client.Remember(peer)
	if parsed.HasTopic && !g.client.IsForum(chatID) {
		return summarizer.SummaryEndpoint{}, &summarizer.ValueError{Msg: "指定了话题 ID, 但目标聊天不是论坛群组"}
	}
	var topic *int
	if parsed.HasTopic {
		topicID := parsed.TopicID
		topic = &topicID
	}
	var username *string
	if name, ok := peer.Username(); ok {
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		if name != "" {
			username = &name
		}
	}
	return summarizer.NewSummaryEndpoint(chatID, topic, username)
}

// FetchRecent reads newest-first history until since, then keeps the last limit raw messages.
func (g *Gateway) FetchRecent(ctx context.Context, source summarizer.SummaryEndpoint, since time.Time, limit *int) (summarizer.FetchedSummaryMessages, error) {
	if since.Location() == nil {
		return summarizer.FetchedSummaryMessages{}, &summarizer.ValueError{Msg: "since must be timezone-aware"}
	}
	if limit != nil && *limit <= 0 {
		return summarizer.FetchedSummaryMessages{}, &summarizer.ValueError{Msg: "limit must be positive"}
	}
	if source.TopicID < 0 {
		return summarizer.FetchedSummaryMessages{}, &summarizer.ValueError{Msg: "topic_id must be positive"}
	}
	peer, err := g.endpointPeer(ctx, source)
	if err != nil {
		return summarizer.FetchedSummaryMessages{}, err
	}
	var collected []tg.MessageClass
	var users = map[int64]*tg.User{}
	var titles = map[int64]string{}
	err = g.slot(ctx, source.ChatID, func() error {
		raw, err := g.recent(ctx, peer, source.TopicID, since, limit, users, titles)
		collected = raw
		return err
	})
	if err != nil {
		if isValue(err) {
			return summarizer.FetchedSummaryMessages{}, err
		}
		return summarizer.FetchedSummaryMessages{}, &summarizer.SummarizerError{Msg: "读取 Telegram 历史消息失败: " + err.Error()}
	}
	observed := time.Now().UTC()
	messages := make([]summarizer.SummaryMessage, 0, len(collected))
	for _, message := range collected {
		summary, err := summaryMessage(message, source.ChatID, observed, users, titles)
		if err != nil {
			return summarizer.FetchedSummaryMessages{}, err
		}
		if summary != nil {
			messages = append(messages, *summary)
		}
	}
	return summarizer.FetchedSummaryMessages{
		Source:    source,
		ChatKind:  chatKind(peer, source.ChatID),
		ChatTitle: chatTitle(g.client, peer, source.ChatID),
		Messages:  messages,
	}, nil
}

// SendText sends the summary, replying to the destination topic when one is set.
func (g *Gateway) SendText(ctx context.Context, destination summarizer.SummaryEndpoint, text string) (contracts.MessageRef, error) {
	peer, err := g.endpointPeer(ctx, destination)
	if err != nil {
		return contracts.MessageRef{}, err
	}
	var sent contracts.MessageRef
	err = g.slot(ctx, destination.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		random, err := randomID()
		if err != nil {
			return err
		}
		request := &tg.MessagesSendMessageRequest{
			Peer:     peer.InputPeer(),
			Message:  text,
			RandomID: random,
		}
		if destination.TopicID > 0 {
			request.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: destination.TopicID})
		}
		updates, err := api.MessagesSendMessage(ctx, request)
		if err != nil {
			return err
		}
		users, chats := updatesEntities(updates)
		g.observe(ctx, users, chats)
		ids := telegram.SentMessageIDs(updates)
		if len(ids) == 0 {
			return fmt.Errorf("telegram did not return a message id")
		}
		ref, err := contracts.NewMessageRef(destination.ChatID, ids[len(ids)-1])
		sent = ref
		return err
	})
	if err != nil {
		return contracts.MessageRef{}, &summarizer.SummarizerError{Msg: "发送 Telegram 总结失败: " + err.Error()}
	}
	return sent, nil
}

func (g *Gateway) recent(ctx context.Context, peer peers.Peer, topicID int, since time.Time, limit *int, users map[int64]*tg.User, titles map[int64]string) ([]tg.MessageClass, error) {
	api, err := g.api()
	if err != nil {
		return nil, err
	}
	remaining := -1
	if limit != nil {
		remaining = *limit
	}
	var collected []tg.MessageClass
	offset := 0
	for {
		batch := pageSize
		if remaining >= 0 && remaining < batch {
			batch = remaining
		}
		if batch == 0 {
			break
		}
		box, err := g.historyPage(ctx, api, peer, topicID, offset, batch)
		if err != nil {
			return nil, err
		}
		messages, pageUsers, pageChats := splitMessages(box)
		g.observe(ctx, pageUsers, pageChats)
		indexEntities(pageUsers, pageChats, users, titles)
		if len(messages) == 0 {
			break
		}
		stop := false
		oldest := 0
		for _, message := range messages {
			if id := messageClassID(message); id > 0 {
				oldest = id
			}
			if stamp, ok := messageTime(message); ok && stamp.Before(since) {
				stop = true
				break
			}
			collected = append(collected, message)
			if remaining > 0 {
				remaining--
				if remaining == 0 {
					stop = true
					break
				}
			}
		}
		if stop || len(messages) < batch || oldest == 0 || oldest == offset {
			break
		}
		offset = oldest
	}
	if topicID > 0 && !containsID(collected, topicID) {
		message, err := g.messageByID(ctx, api, peer, topicID, users, titles)
		if err != nil {
			return nil, err
		}
		if message != nil {
			if stamp, ok := messageTime(message); !ok || !stamp.Before(since) {
				collected = append(collected, message)
			}
		}
	}
	sort.SliceStable(collected, func(i, j int) bool {
		return messageClassID(collected[i]) < messageClassID(collected[j])
	})
	if limit != nil && len(collected) > *limit {
		collected = collected[len(collected)-*limit:]
	}
	return collected, nil
}

func (g *Gateway) historyPage(ctx context.Context, api *tg.Client, peer peers.Peer, topicID, offset, limit int) (tg.MessagesMessagesClass, error) {
	if topicID > 0 {
		return api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer:     peer.InputPeer(),
			MsgID:    topicID,
			OffsetID: offset,
			Limit:    limit,
		})
	}
	return api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     peer.InputPeer(),
		OffsetID: offset,
		Limit:    limit,
	})
}

func (g *Gateway) messageByID(ctx context.Context, api *tg.Client, peer peers.Peer, messageID int, users map[int64]*tg.User, titles map[int64]string) (tg.MessageClass, error) {
	query := []tg.InputMessageClass{&tg.InputMessageID{ID: messageID}}
	var (
		box tg.MessagesMessagesClass
		err error
	)
	if channel, ok := peer.(peers.Channel); ok {
		box, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: channel.InputChannel(), ID: query})
	} else {
		box, err = api.MessagesGetMessages(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	messages, pageUsers, pageChats := splitMessages(box)
	g.observe(ctx, pageUsers, pageChats)
	indexEntities(pageUsers, pageChats, users, titles)
	if len(messages) == 0 || messageClassID(messages[0]) != messageID {
		return nil, nil
	}
	return messages[0], nil
}

func (g *Gateway) resolveParsed(ctx context.Context, parsed summarizer.EndpointReference) (peers.Peer, error) {
	manager, err := g.manager()
	if err != nil {
		return nil, err
	}
	if parsed.Numeric {
		return manager.ResolveTDLibID(ctx, constant.TDLibPeerID(parsed.ChatID))
	}
	return manager.ResolveDomain(ctx, strings.TrimPrefix(parsed.Username, "@"))
}

func (g *Gateway) endpointPeer(ctx context.Context, endpoint summarizer.SummaryEndpoint) (peers.Peer, error) {
	manager, err := g.manager()
	if err != nil {
		return nil, &summarizer.SummarizerError{Msg: fmt.Sprintf("无法访问 Telegram 聊天 %d: %s", endpoint.ChatID, err.Error())}
	}
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(endpoint.ChatID))
	if err == nil && peer != nil {
		g.client.Remember(peer)
		return peer, nil
	}
	last := err
	if endpoint.Username != "" {
		resolved, resolveErr := manager.ResolveDomain(ctx, strings.TrimPrefix(endpoint.Username, "@"))
		if resolveErr != nil {
			last = resolveErr
		} else if int64(resolved.TDLibPeerID()) != endpoint.ChatID {
			last = fmt.Errorf("Telegram username now resolves to a different chat than %d", endpoint.ChatID)
		} else {
			g.client.Remember(resolved)
			return resolved, nil
		}
	}
	if last == nil {
		last = fmt.Errorf("peer is not available")
	}
	return nil, &summarizer.SummarizerError{Msg: fmt.Sprintf("无法访问 Telegram 聊天 %d: %s", endpoint.ChatID, last.Error())}
}

func (g *Gateway) slot(ctx context.Context, chatID int64, fn func() error) error {
	if g == nil || g.client == nil || g.client.Limiter() == nil {
		return fn()
	}
	return g.client.Limiter().Slot(ctx, chatID, fn)
}

func (g *Gateway) api() (*tg.Client, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("telegram client is not available")
	}
	return g.client.API()
}

func (g *Gateway) manager() (*peers.Manager, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("telegram client is not available")
	}
	return g.client.Peers()
}

func (g *Gateway) observe(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) {
	if g == nil || g.client == nil {
		return
	}
	g.client.Observe(ctx, entitiesFrom(users, chats))
}

func summaryMessage(message tg.MessageClass, chatID int64, observed time.Time, users map[int64]*tg.User, titles map[int64]string) (*summarizer.SummaryMessage, error) {
	normalized, ok := telegram.Normalize(message, observed)
	if !ok || normalized.ContentType == contracts.ContentService {
		return nil, nil
	}
	text := strings.TrimSpace(normalized.Text)
	if text == "" {
		text = strings.TrimSpace(normalized.Caption)
	}
	if text == "" {
		return nil, nil
	}
	if label, ok := mediaLabels[normalized.ContentType]; ok {
		text = "[" + label + "] " + text
	}
	native, _ := message.(*tg.Message)
	sender := senderName(native, normalized.SenderID, users, titles)
	grouped := summarizer.GroupedID{}
	switch value := normalized.GroupedID.(type) {
	case int64:
		grouped = summarizer.NumericGroupedID(value)
	case int:
		grouped = summarizer.NumericGroupedID(int64(value))
	}
	built, err := summarizer.NewSummaryMessage(summarizer.SummaryMessage{
		Refs:             []contracts.MessageRef{{ChatID: chatID, MessageID: normalized.Ref.MessageID}},
		OccurredAt:       normalized.OccurredAt,
		SenderName:       sender,
		Text:             text,
		SenderID:         normalized.SenderID,
		ReplyToMessageID: normalized.ReplyToMessageID,
		GroupedID:        grouped,
		ForwardedFrom:    forwardedFrom(native),
		Links:            messageLinks(normalized, native),
		Outgoing:         normalized.Outgoing,
	})
	if err != nil {
		return nil, err
	}
	return &built, nil
}

func senderName(message *tg.Message, senderID *int64, users map[int64]*tg.User, titles map[int64]string) string {
	if message != nil {
		switch from := message.FromID.(type) {
		case *tg.PeerUser:
			if user := users[from.UserID]; user != nil {
				if name := visibleUser(user); name != "" {
					return name
				}
			}
		case *tg.PeerChat:
			if title := strings.TrimSpace(titles[from.ChatID]); title != "" {
				return title
			}
		case *tg.PeerChannel:
			if title := strings.TrimSpace(titles[from.ChannelID]); title != "" {
				return title
			}
			if title := strings.TrimSpace(titles[telegram.ChannelDialogID(from.ChannelID)]); title != "" {
				return title
			}
		}
		if author, ok := message.GetPostAuthor(); ok && strings.TrimSpace(author) != "" {
			return strings.TrimSpace(author)
		}
	}
	if senderID != nil {
		return strconv.FormatInt(*senderID, 10)
	}
	return "未知发送者"
}

func visibleUser(user *tg.User) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func forwardedFrom(message *tg.Message) string {
	if message == nil {
		return ""
	}
	header, ok := message.GetFwdFrom()
	if !ok {
		return ""
	}
	if name, ok := header.GetFromName(); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if name, ok := header.GetPostAuthor(); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if name, ok := header.GetSavedFromName(); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "Telegram 转发消息"
}

func messageLinks(normalized contracts.TelegramMessage, message *tg.Message) []string {
	seen := map[string]struct{}{}
	var links []string
	add := func(link string) {
		link = strings.TrimSpace(link)
		if link == "" {
			return
		}
		if _, ok := seen[link]; ok {
			return
		}
		seen[link] = struct{}{}
		links = append(links, link)
	}
	for _, link := range linkPattern.FindAllString(normalized.Text+" "+normalized.Caption, -1) {
		add(link)
	}
	if message != nil {
		entities, ok := message.GetEntities()
		if ok {
			for _, entity := range entities {
				if textURL, ok := entity.(*tg.MessageEntityTextURL); ok && textURL != nil {
					add(textURL.URL)
				}
			}
		}
	}
	if links == nil {
		return []string{}
	}
	return links
}

func chatKind(peer peers.Peer, chatID int64) summarizer.SummaryChatKind {
	if chatID > 0 {
		return summarizer.ChatPrivate
	}
	if channel, ok := peer.(peers.Channel); ok && channel.Raw() != nil && channel.Raw().Broadcast && !channel.Raw().Megagroup {
		return summarizer.ChatChannel
	}
	return summarizer.ChatGroup
}

func chatTitle(client *telegram.Client, peer peers.Peer, chatID int64) string {
	if client != nil {
		if title, ok := client.Title(chatID); ok && strings.TrimSpace(title) != "" {
			return strings.TrimSpace(title)
		}
	}
	if peer != nil {
		if title := strings.TrimSpace(peer.VisibleName()); title != "" {
			return title
		}
	}
	return strconv.FormatInt(chatID, 10)
}

func indexEntities(users []tg.UserClass, chats []tg.ChatClass, userIndex map[int64]*tg.User, titles map[int64]string) {
	for _, user := range users {
		concrete, ok := user.(*tg.User)
		if ok && concrete != nil {
			userIndex[concrete.ID] = concrete
		}
	}
	for _, chat := range chats {
		switch concrete := chat.(type) {
		case *tg.Chat:
			if concrete != nil {
				titles[concrete.ID] = concrete.Title
			}
		case *tg.Channel:
			if concrete != nil {
				titles[concrete.ID] = concrete.Title
				titles[telegram.ChannelDialogID(concrete.ID)] = concrete.Title
			}
		}
	}
}

func containsID(messages []tg.MessageClass, messageID int) bool {
	for _, message := range messages {
		if messageClassID(message) == messageID {
			return true
		}
	}
	return false
}

func messageTime(message tg.MessageClass) (time.Time, bool) {
	switch message := message.(type) {
	case *tg.Message:
		if message == nil || message.Date == 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(message.Date), 0).UTC(), true
	case *tg.MessageService:
		if message == nil || message.Date == 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(message.Date), 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func isValue(err error) bool {
	_, ok := err.(*summarizer.ValueError)
	return ok
}
