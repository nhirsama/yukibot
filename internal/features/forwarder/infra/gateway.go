package infra

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

// Gateway is the gotd adapter for the forwarder Telegram ports.
// InspectChats uses the dialog membership recorded at startup and after joins.
type Gateway struct {
	client *telegram.Client
}

var (
	_ forwarder.TelegramGateway         = (*Gateway)(nil)
	_ forwarder.TelegramSourceGateway   = (*Gateway)(nil)
	_ forwarder.TelegramRecoveryGateway = (*Gateway)(nil)
)

// New returns a gateway bound to the process Telegram client.
func New(client *telegram.Client) *Gateway {
	return &Gateway{client: client}
}

func (g *Gateway) slot(ctx context.Context, chatID int64, fn func() error) error {
	if g == nil || g.client == nil || g.client.Limiter() == nil {
		return fn()
	}
	return g.client.Limiter().Slot(ctx, chatID, fn)
}

func (g *Gateway) api() (*tg.Client, error) {
	if g == nil || g.client == nil {
		return nil, forwarder.NewPermanentDeliveryError("telegram client is not available")
	}
	return g.client.API()
}

// ChatTitle returns a cached chat title.
func (g *Gateway) ChatTitle(chatID int64) (string, bool) {
	if g == nil || g.client == nil {
		return "", false
	}
	return g.client.Title(chatID)
}

// IsForum reports whether the cached chat is a forum.
func (g *Gateway) IsForum(chatID int64) bool {
	return g != nil && g.client != nil && g.client.IsForum(chatID)
}

// SourceTitle joins the cached chat title with the forum topic title.
func (g *Gateway) SourceTitle(ctx context.Context, source forwarder.SourceEndpoint) (string, bool, error) {
	group, hasGroup := g.ChatTitle(source.ChatID)
	if !source.HasTopic {
		return group, hasGroup, nil
	}
	topic, hasTopic := g.topicTitle(ctx, source)
	if !hasTopic {
		return group, hasGroup, nil
	}
	if hasGroup {
		return group + "/" + topic, true, nil
	}
	return topic, true, nil
}

func (g *Gateway) topicTitle(ctx context.Context, source forwarder.SourceEndpoint) (title string, ok bool) {
	topicID := source.TopicID
	if topicID <= 0 {
		topicID = 1
	}
	_ = g.slot(ctx, source.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, source.ChatID)
		if err != nil {
			return err
		}
		channel, isChannel := peer.(peers.Channel)
		if !isChannel {
			return nil
		}
		box, err := api.ChannelsGetForumTopicsByID(ctx, &tg.ChannelsGetForumTopicsByIDRequest{
			Channel: channel.InputChannel(),
			Topics:  []int{topicID},
		})
		if err != nil || box == nil {
			return err
		}
		g.observeChats(ctx, nil, box.Chats)
		for _, item := range box.Topics {
			concrete, isTopic := item.(*tg.ForumTopic)
			if !isTopic || concrete == nil {
				continue
			}
			if name := strings.TrimSpace(concrete.GetTitle()); name != "" {
				title = name
				ok = true
				return nil
			}
		}
		return nil
	})
	return title, ok
}

// ResolveChat resolves an invite, numeric ID, or public username.
func (g *Gateway) ResolveChat(ctx context.Context, reference string) (forwarder.ChatIdentity, error) {
	normalized := strings.TrimSpace(reference)
	if hash := inviteHash(normalized); hash != "" {
		identity, err := g.resolveInvite(ctx, hash, normalized)
		if err != nil {
			return forwarder.ChatIdentity{}, translate(err, false)
		}
		return identity, nil
	}
	if chatID, err := parseChatID(normalized); err == nil {
		identity, err := g.resolveID(ctx, chatID)
		if err != nil {
			return forwarder.ChatIdentity{}, translate(err, false)
		}
		return identity, nil
	}
	username := publicUsername(normalized)
	if username == "" {
		return forwarder.ChatIdentity{}, forwarder.NewValueError("频道引用必须是 ID、@用户名或 Telegram 邀请链接")
	}
	identity, err := g.resolveUsername(ctx, username, "")
	if err != nil {
		return forwarder.ChatIdentity{}, translate(err, false)
	}
	return identity, nil
}

func parseChatID(reference string) (int64, error) {
	return strconv.ParseInt(reference, 10, 64)
}

func (g *Gateway) resolveInvite(ctx context.Context, hash, reference string) (forwarder.ChatIdentity, error) {
	api, err := g.api()
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	checked, err := api.MessagesCheckChatInvite(ctx, hash)
	if err != nil {
		if tg.IsInviteRequestSent(err) {
			return forwarder.ChatIdentity{}, forwarder.NewPermanentDeliveryError(approvalPendingText)
		}
		return forwarder.ChatIdentity{}, err
	}
	if already, ok := checked.(*tg.ChatInviteAlready); ok && already != nil && already.Chat != nil {
		return g.identityFromChat(ctx, already.Chat, reference)
	}
	updates, err := api.MessagesImportChatInvite(ctx, hash)
	if err != nil {
		if tg.IsInviteRequestSent(err) {
			return forwarder.ChatIdentity{}, forwarder.NewPermanentDeliveryError(approvalPendingText)
		}
		return forwarder.ChatIdentity{}, err
	}
	g.observeUpdates(ctx, updates)
	_, chats := updatesEntities(updates)
	if len(chats) != 1 {
		return forwarder.ChatIdentity{}, forwarder.NewValueError("Telegram invite did not return exactly one chat")
	}
	return g.identityFromChat(ctx, chats[0], reference)
}

func (g *Gateway) identityFromChat(ctx context.Context, chat tg.ChatClass, invite string) (forwarder.ChatIdentity, error) {
	peer, err := g.peerFromChat(ctx, chat)
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	chatID := int64(peer.TDLibPeerID())
	username, _ := peer.Username()
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	return forwarder.NewChatIdentityOptional(chatID, username, invite)
}

func (g *Gateway) resolveID(ctx context.Context, chatID int64) (forwarder.ChatIdentity, error) {
	manager, err := g.manager()
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(chatID))
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	g.client.Remember(peer)
	username, _ := peer.Username()
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	return forwarder.NewChatIdentityOptional(chatID, username, "")
}

func (g *Gateway) resolveUsername(ctx context.Context, username, invite string) (forwarder.ChatIdentity, error) {
	manager, err := g.manager()
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	peer, err := manager.ResolveDomain(ctx, strings.TrimPrefix(username, "@"))
	if err != nil {
		return forwarder.ChatIdentity{}, err
	}
	g.client.Remember(peer)
	chatID := int64(peer.TDLibPeerID())
	resolved, _ := peer.Username()
	resolved = strings.TrimPrefix(strings.TrimSpace(resolved), "@")
	if resolved == "" {
		resolved = strings.TrimPrefix(username, "@")
	}
	return forwarder.NewChatIdentityOptional(chatID, resolved, invite)
}

// EnsureSource resolves the source and optionally joins a channel or supergroup.
func (g *Gateway) EnsureSource(ctx context.Context, source forwarder.SourceEndpoint, join bool) error {
	peer, err := g.sourcePeer(ctx, source)
	if err != nil {
		return translate(err, false)
	}
	if !join {
		return nil
	}
	err = g.joinPeer(ctx, peer)
	if tg.IsUserAlreadyParticipant(err) {
		g.client.MarkJoined(source.ChatID)
		return nil
	}
	if tg.IsInviteRequestSent(err) {
		return forwarder.NewPermanentDeliveryError(approvalPendingText)
	}
	return translate(err, false)
}

// InspectChats reports membership from the startup dialog cache.
// A private chat without a username exports an invite link; that failure stays on the row.
func (g *Gateway) InspectChats(ctx context.Context, chatIDs []int64) ([]forwarder.ChatInspection, error) {
	inspections := make([]forwarder.ChatInspection, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		if g.client == nil || !g.client.Joined(chatID) {
			inspections = append(inspections, forwarder.ChatInspection{Access: forwarder.ChatAccess{ChatID: chatID}})
			continue
		}
		title, _ := g.ChatTitle(chatID)
		username, _ := g.client.Username(chatID)
		link := ""
		metadata := ""
		if username != "" {
			link = "https://t.me/" + username
		} else if chatID < 0 {
			exported, err := g.exportInvite(ctx, chatID)
			if err != nil {
				metadata = metadataName(err)
			} else {
				link = exported
			}
		}
		inspections = append(inspections, forwarder.ChatInspection{
			Access:        forwarder.ChatAccess{ChatID: chatID, Title: title, Username: username, InviteLink: link},
			Joined:        true,
			MetadataError: metadata,
		})
	}
	return inspections, nil
}

func (g *Gateway) exportInvite(ctx context.Context, chatID int64) (string, error) {
	var link string
	err := g.slot(ctx, chatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, chatID)
		if err != nil {
			return err
		}
		exported, err := api.MessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: peer.InputPeer()})
		if err != nil {
			return err
		}
		invite, ok := exported.(*tg.ChatInviteExported)
		if ok && invite != nil {
			link = strings.TrimSpace(invite.Link)
		}
		return nil
	})
	return link, err
}

// JoinChat joins a missing chat by username or invite link.
func (g *Gateway) JoinChat(ctx context.Context, access forwarder.ChatAccess) (forwarder.RebuildJoinResult, error) {
	if g.client != nil && g.client.Joined(access.ChatID) {
		return forwarder.RebuildAlreadyJoined, nil
	}
	result, err := g.joinChat(ctx, access)
	if tg.IsInviteRequestSent(err) {
		return forwarder.RebuildApprovalPending, nil
	}
	if tg.IsUserAlreadyParticipant(err) {
		if g.client != nil {
			g.client.MarkJoined(access.ChatID)
		}
		return forwarder.RebuildAlreadyJoined, nil
	}
	if err != nil {
		return "", translate(err, false)
	}
	return result, nil
}

func (g *Gateway) joinChat(ctx context.Context, access forwarder.ChatAccess) (forwarder.RebuildJoinResult, error) {
	if username := strings.TrimPrefix(strings.TrimSpace(access.Username), "@"); username != "" {
		manager, err := g.manager()
		if err != nil {
			return "", err
		}
		peer, err := manager.ResolveDomain(ctx, username)
		if err != nil {
			return "", err
		}
		if int64(peer.TDLibPeerID()) != access.ChatID {
			return "", forwarder.NewValueError(fmt.Sprintf("join reference resolves to chat %d, expected %d", peer.TDLibPeerID(), access.ChatID))
		}
		if err := g.joinPeer(ctx, peer); err != nil {
			return "", err
		}
		return forwarder.RebuildJoined, nil
	}
	hash := inviteHash(access.InviteLink)
	if hash == "" {
		return "", forwarder.NewValueError(fmt.Sprintf("chat %d has no usable join reference", access.ChatID))
	}
	api, err := g.api()
	if err != nil {
		return "", err
	}
	updates, err := api.MessagesImportChatInvite(ctx, hash)
	if err != nil {
		return "", err
	}
	g.observeUpdates(ctx, updates)
	_, chats := updatesEntities(updates)
	found := false
	for _, chat := range chats {
		peer, err := g.peerFromChat(ctx, chat)
		if err != nil {
			return "", err
		}
		if int64(peer.TDLibPeerID()) == access.ChatID {
			found = true
		}
	}
	if !found {
		return "", forwarder.NewValueError(fmt.Sprintf("Telegram invite did not resolve to expected chat %d", access.ChatID))
	}
	return forwarder.RebuildJoined, nil
}

// CreateForumTopic creates a topic and returns its service-message ID.
func (g *Gateway) CreateForumTopic(ctx context.Context, destinationChatID int64, title string, randomID int64) (int, error) {
	var topicID int
	err := g.slot(ctx, destinationChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, destinationChatID)
		if err != nil {
			return err
		}
		channel, ok := peer.(peers.Channel)
		if !ok {
			return forwarder.NewPermanentDeliveryError(fmt.Sprintf("chat %d is not a forum channel", destinationChatID))
		}
		updates, err := api.ChannelsCreateForumTopic(ctx, &tg.ChannelsCreateForumTopicRequest{
			Channel:  channel.InputChannel(),
			Title:    title,
			RandomID: randomID,
		})
		if err != nil {
			return err
		}
		g.observeUpdates(ctx, updates)
		id, ok := telegram.TopicIDFromUpdates(updates)
		if !ok {
			return forwarder.MessageNotFound{}
		}
		topicID = id
		return nil
	})
	if err != nil {
		return 0, translate(err, false)
	}
	return topicID, nil
}

// EditForumTopic renames a topic. TOPIC_NOT_MODIFIED is success.
func (g *Gateway) EditForumTopic(ctx context.Context, destinationChatID int64, topicID int, title string) error {
	err := g.slot(ctx, destinationChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, destinationChatID)
		if err != nil {
			return err
		}
		channel, ok := peer.(peers.Channel)
		if !ok {
			return forwarder.NewPermanentDeliveryError(fmt.Sprintf("chat %d is not a forum channel", destinationChatID))
		}
		request := &tg.ChannelsEditForumTopicRequest{Channel: channel.InputChannel(), TopicID: topicID}
		request.SetTitle(title)
		updates, err := api.ChannelsEditForumTopic(ctx, request)
		if err != nil {
			if tg.IsTopicNotModified(err) {
				return nil
			}
			return err
		}
		g.observeUpdates(ctx, updates)
		return nil
	})
	return translate(err, false)
}

// DeleteMessage revokes one destination message.
func (g *Gateway) DeleteMessage(ctx context.Context, target forwarder.MessageRef) error {
	err := g.slot(ctx, target.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, target.ChatID)
		if err != nil {
			return err
		}
		if channel, ok := peer.(peers.Channel); ok {
			_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: channel.InputChannel(),
				ID:      []int{target.MessageID},
			})
			return err
		}
		request := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{target.MessageID}}
		request.SetFlags()
		_, err = api.MessagesDeleteMessages(ctx, request)
		return err
	})
	return translate(err, false)
}

// EditFromSource copies the source text, entities, and inline buttons onto the destination.
// An empty entity list or keyboard clears links and buttons that the source removed.
func (g *Gateway) EditFromSource(ctx context.Context, source forwarder.IncomingMessage, target forwarder.MessageRef) error {
	err := g.slot(ctx, target.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		native, err := g.getMessage(ctx, api, source.Ref)
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, target.ChatID)
		if err != nil {
			return err
		}
		text := native.Message
		entities, hasEntities := native.GetEntities()
		if strings.TrimSpace(text) == "" {
			text = source.Text
			if text == "" {
				text = source.Caption
			}
			hasEntities = false
		}
		request := &tg.MessagesEditMessageRequest{Peer: peer.InputPeer(), ID: target.MessageID}
		request.SetMessage(text)
		if !hasEntities {
			entities = []tg.MessageEntityClass{}
		}
		request.SetEntities(entities)
		if markup := copiedMarkup(native); markup != nil {
			request.SetReplyMarkup(markup)
		} else {
			request.SetReplyMarkup(&tg.ReplyInlineMarkup{})
		}
		if !keepsWebPreview(native) {
			request.SetNoWebpage(true)
		}
		updates, err := api.MessagesEditMessage(ctx, request)
		if err != nil {
			return err
		}
		g.observeUpdates(ctx, updates)
		return nil
	})
	return translate(err, false)
}

func (g *Gateway) manager() (*peers.Manager, error) {
	if g == nil || g.client == nil {
		return nil, forwarder.NewPermanentDeliveryError("telegram client is not available")
	}
	return g.client.Peers()
}

func (g *Gateway) cachedPeer(ctx context.Context, chatID int64) (peers.Peer, error) {
	manager, err := g.manager()
	if err != nil {
		return nil, err
	}
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(chatID))
	if err != nil || peer == nil {
		return nil, forwarder.NewPermanentDeliveryError(fmt.Sprintf("chat %d is not in the Telethon peer cache; open or join it first", chatID))
	}
	return peer, nil
}

func (g *Gateway) sourcePeer(ctx context.Context, source forwarder.SourceEndpoint) (peers.Peer, error) {
	manager, err := g.manager()
	if err != nil {
		return nil, err
	}
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(source.ChatID))
	if err == nil && peer != nil {
		g.client.Remember(peer)
		return peer, nil
	}
	last := err
	if source.Username != "" {
		resolved, resolveErr := manager.ResolveDomain(ctx, strings.TrimPrefix(source.Username, "@"))
		if resolveErr != nil {
			last = resolveErr
		} else if int64(resolved.TDLibPeerID()) != source.ChatID {
			last = forwarder.NewValueError(fmt.Sprintf("public username now resolves to a different chat than %d", source.ChatID))
		} else {
			g.client.Remember(resolved)
			return resolved, nil
		}
	}
	if last == nil {
		return nil, forwarder.NewPermanentDeliveryError(fmt.Sprintf("cannot resolve source chat %d", source.ChatID))
	}
	return nil, last
}

func (g *Gateway) peerFromChat(ctx context.Context, chat tg.ChatClass) (peers.Peer, error) {
	chatID, ok := telegram.ChatDialogID(chat)
	if !ok {
		return nil, forwarder.NewPermanentDeliveryError("Telegram chat has no dialog id")
	}
	manager, err := g.manager()
	if err != nil {
		return nil, err
	}
	if err := manager.Apply(ctx, nil, []tg.ChatClass{chat}); err != nil {
		return nil, err
	}
	g.observeChats(ctx, nil, []tg.ChatClass{chat})
	peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(chatID))
	if err != nil || peer == nil {
		return nil, err
	}
	g.client.Remember(peer)
	g.client.MarkJoined(chatID)
	return peer, nil
}

func (g *Gateway) observeChats(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) {
	if g == nil || g.client == nil {
		return
	}
	g.client.Observe(ctx, entitiesFrom(users, chats))
}

func (g *Gateway) joinPeer(ctx context.Context, peer peers.Peer) error {
	if peer == nil {
		return forwarder.NewPermanentDeliveryError("telegram peer is not available")
	}
	channel, ok := peer.(peers.Channel)
	if !ok {
		if _, isUser := peer.(peers.User); isUser {
			return forwarder.NewValueError("automatic joining is only supported for channels and supergroups")
		}
		g.client.Remember(peer)
		g.client.MarkJoined(int64(peer.TDLibPeerID()))
		return nil
	}
	if !channel.Left() {
		g.client.Remember(peer)
		g.client.MarkJoined(int64(peer.TDLibPeerID()))
		return nil
	}
	api, err := g.api()
	if err != nil {
		return err
	}
	updates, err := api.ChannelsJoinChannel(ctx, channel.InputChannel())
	if err != nil {
		return err
	}
	g.observeUpdates(ctx, updates)
	if err := channel.Sync(ctx); err != nil {
		return err
	}
	manager, err := g.manager()
	if err != nil {
		return err
	}
	refreshed, err := manager.ResolveTDLibID(ctx, peer.TDLibPeerID())
	if err != nil || refreshed == nil {
		return forwarder.NewPermanentDeliveryError("Telegram did not confirm channel membership")
	}
	if refreshedChannel, ok := refreshed.(peers.Channel); ok && refreshedChannel.Left() {
		return forwarder.NewPermanentDeliveryError("Telegram did not confirm channel membership")
	}
	g.client.Remember(refreshed)
	g.client.MarkJoined(int64(refreshed.TDLibPeerID()))
	return nil
}

func (g *Gateway) getMessage(ctx context.Context, api *tg.Client, ref forwarder.MessageRef) (*tg.Message, error) {
	peer, err := g.cachedPeer(ctx, ref.ChatID)
	if err != nil {
		return nil, err
	}
	query := []tg.InputMessageClass{&tg.InputMessageID{ID: ref.MessageID}}
	var box tg.MessagesMessagesClass
	if channel, ok := peer.(peers.Channel); ok {
		box, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: channel.InputChannel(), ID: query})
	} else {
		box, err = api.MessagesGetMessages(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	g.observeBox(ctx, box)
	messages, _, _ := splitMessages(box)
	if len(messages) == 0 {
		return nil, forwarder.MessageNotFound{}
	}
	message, ok := messages[0].(*tg.Message)
	if !ok || message == nil || message.ID <= 0 {
		return nil, forwarder.MessageNotFound{}
	}
	return message, nil
}
