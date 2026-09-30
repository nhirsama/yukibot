package infra

import (
	"context"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

// DeliverMessage forwards or copies one source message.
// Copy reuses the Telegram file reference and does not download the media.
func (g *Gateway) DeliverMessage(ctx context.Context, message forwarder.IncomingMessage, destination forwarder.DestinationEndpoint, mode forwarder.ForwardMode, replyToMessageID *int) (forwarder.MessageRef, error) {
	var sent forwarder.MessageRef
	err := g.slot(ctx, destination.ChatID, func() error {
		ref, err := g.deliverOne(ctx, message, destination, mode, replyToMessageID)
		sent = ref
		return err
	})
	if err != nil {
		return forwarder.MessageRef{}, translate(err, mode == forwarder.ForwardModeForward)
	}
	return sent, nil
}

// DeliverAlbum forwards or copies an album. The destination count must match the source.
func (g *Gateway) DeliverAlbum(ctx context.Context, messages []forwarder.IncomingMessage, destination forwarder.DestinationEndpoint, mode forwarder.ForwardMode, replyToMessageID *int) ([]forwarder.MessageRef, error) {
	var sent []forwarder.MessageRef
	err := g.slot(ctx, destination.ChatID, func() error {
		refs, err := g.deliverAlbum(ctx, messages, destination, mode, replyToMessageID)
		sent = refs
		return err
	})
	if err != nil {
		return nil, translate(err, mode == forwarder.ForwardModeForward)
	}
	return sent, nil
}

// SendText sends a plain message, optionally into a destination topic.
func (g *Gateway) SendText(ctx context.Context, text string, destination forwarder.DestinationEndpoint, replyToMessageID *int) (forwarder.MessageRef, error) {
	var sent forwarder.MessageRef
	err := g.slot(ctx, destination.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.cachedPeer(ctx, destination.ChatID)
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
		if reply := inputReply(destination, replyToMessageID); reply != nil {
			request.SetReplyTo(reply)
		}
		updates, err := api.MessagesSendMessage(ctx, request)
		if err != nil {
			return err
		}
		g.observeUpdates(ctx, updates)
		ref, err := oneRef(destination.ChatID, updates)
		sent = ref
		return err
	})
	if err != nil {
		return forwarder.MessageRef{}, translate(err, false)
	}
	return sent, nil
}

func (g *Gateway) deliverOne(ctx context.Context, message forwarder.IncomingMessage, destination forwarder.DestinationEndpoint, mode forwarder.ForwardMode, replyToMessageID *int) (forwarder.MessageRef, error) {
	api, err := g.api()
	if err != nil {
		return forwarder.MessageRef{}, err
	}
	native, err := g.getMessage(ctx, api, message.Ref)
	if err != nil {
		return forwarder.MessageRef{}, err
	}
	target, err := g.cachedPeer(ctx, destination.ChatID)
	if err != nil {
		return forwarder.MessageRef{}, err
	}
	if mode == forwarder.ForwardModeForward {
		updates, err := g.forwardNative(ctx, api, []*tg.Message{native}, target, destination, replyToMessageID)
		if err != nil {
			return forwarder.MessageRef{}, err
		}
		return oneRef(destination.ChatID, updates)
	}
	updates, err := g.copyOne(ctx, api, native, target, destination, replyToMessageID)
	if err != nil {
		return forwarder.MessageRef{}, err
	}
	return oneRef(destination.ChatID, updates)
}

func (g *Gateway) deliverAlbum(ctx context.Context, messages []forwarder.IncomingMessage, destination forwarder.DestinationEndpoint, mode forwarder.ForwardMode, replyToMessageID *int) ([]forwarder.MessageRef, error) {
	api, err := g.api()
	if err != nil {
		return nil, err
	}
	native := make([]*tg.Message, len(messages))
	for i, message := range messages {
		item, err := g.getMessage(ctx, api, message.Ref)
		if err != nil {
			return nil, err
		}
		native[i] = item
	}
	target, err := g.cachedPeer(ctx, destination.ChatID)
	if err != nil {
		return nil, err
	}
	var updates tg.UpdatesClass
	if mode == forwarder.ForwardModeForward {
		updates, err = g.forwardNative(ctx, api, native, target, destination, replyToMessageID)
	} else {
		updates, err = g.copyAlbum(ctx, api, messages, native, target, destination, replyToMessageID)
	}
	if err != nil {
		return nil, err
	}
	return manyRefs(destination.ChatID, updates, len(messages))
}

func (g *Gateway) forwardNative(ctx context.Context, api *tg.Client, messages []*tg.Message, target peers.Peer, destination forwarder.DestinationEndpoint, replyToMessageID *int) (tg.UpdatesClass, error) {
	if replyToMessageID != nil {
		return nil, forwarder.NativeForwardUnsupported{}
	}
	if len(messages) == 0 || messages[0] == nil {
		return nil, forwarder.MessageNotFound{}
	}
	sourceChat, ok := telegram.DialogID(messages[0].PeerID)
	if !ok {
		return nil, forwarder.NewPermanentDeliveryError("source message has no chat")
	}
	ids := make([]int, len(messages))
	for i, message := range messages {
		if message == nil || message.ID <= 0 {
			return nil, forwarder.MessageNotFound{}
		}
		if message.Noforwards {
			return nil, forwarder.NativeForwardUnsupported{}
		}
		chatID, ok := telegram.DialogID(message.PeerID)
		if !ok || chatID != sourceChat {
			return nil, forwarder.NewValueError("native forwarded messages must share a source chat")
		}
		ids[i] = message.ID
	}
	source, err := g.cachedPeer(ctx, sourceChat)
	if err != nil {
		return nil, err
	}
	randoms, err := randomIDs(len(ids))
	if err != nil {
		return nil, err
	}
	request := &tg.MessagesForwardMessagesRequest{
		FromPeer: source.InputPeer(),
		ID:       ids,
		RandomID: randoms,
		ToPeer:   target.InputPeer(),
	}
	if destination.HasTopic && destination.TopicID > 0 {
		request.SetTopMsgID(destination.TopicID)
	}
	updates, err := api.MessagesForwardMessages(ctx, request)
	if err != nil {
		return nil, err
	}
	g.observeUpdates(ctx, updates)
	return updates, nil
}

func (g *Gateway) copyOne(ctx context.Context, api *tg.Client, source *tg.Message, target peers.Peer, destination forwarder.DestinationEndpoint, replyToMessageID *int) (tg.UpdatesClass, error) {
	media, err := inputMedia(source)
	if err != nil {
		return nil, err
	}
	random, err := randomID()
	if err != nil {
		return nil, err
	}
	text, entities := caption(source)
	if media != nil {
		request := &tg.MessagesSendMediaRequest{
			Peer:     target.InputPeer(),
			Media:    media,
			Message:  text,
			RandomID: random,
		}
		if len(entities) > 0 {
			request.SetEntities(entities)
		}
		if markup := copiedMarkup(source); markup != nil {
			request.SetReplyMarkup(markup)
		}
		if reply := inputReply(destination, replyToMessageID); reply != nil {
			request.SetReplyTo(reply)
		}
		updates, err := api.MessagesSendMedia(ctx, request)
		if err != nil {
			return nil, err
		}
		g.observeUpdates(ctx, updates)
		return updates, nil
	}
	if text == "" {
		return nil, forwarder.NewPermanentDeliveryError("this Telegram media type cannot be copied")
	}
	request := &tg.MessagesSendMessageRequest{
		Peer:     target.InputPeer(),
		Message:  text,
		RandomID: random,
	}
	if len(entities) > 0 {
		request.SetEntities(entities)
	}
	if markup := copiedMarkup(source); markup != nil {
		request.SetReplyMarkup(markup)
	}
	if reply := inputReply(destination, replyToMessageID); reply != nil {
		request.SetReplyTo(reply)
	}
	updates, err := api.MessagesSendMessage(ctx, request)
	if err != nil {
		return nil, err
	}
	g.observeUpdates(ctx, updates)
	return updates, nil
}

func (g *Gateway) copyAlbum(ctx context.Context, api *tg.Client, messages []forwarder.IncomingMessage, native []*tg.Message, target peers.Peer, destination forwarder.DestinationEndpoint, replyToMessageID *int) (tg.UpdatesClass, error) {
	multi := make([]tg.InputSingleMedia, len(messages))
	for i, message := range messages {
		switch message.ContentType {
		case contracts.ContentPhoto, contracts.ContentVideo, contracts.ContentAnimation:
		default:
			return nil, forwarder.NewPermanentDeliveryError(string(message.ContentType) + " cannot be copied as an album item")
		}
		media, err := inputMedia(native[i])
		if err != nil {
			return nil, err
		}
		if media == nil {
			return nil, forwarder.NewPermanentDeliveryError("album item has no downloadable file")
		}
		random, err := randomID()
		if err != nil {
			return nil, err
		}
		text, entities := caption(native[i])
		item := tg.InputSingleMedia{Media: media, RandomID: random, Message: text}
		if len(entities) > 0 {
			item.SetEntities(entities)
		}
		multi[i] = item
	}
	request := &tg.MessagesSendMultiMediaRequest{
		Peer:       target.InputPeer(),
		MultiMedia: multi,
	}
	if reply := inputReply(destination, replyToMessageID); reply != nil {
		request.SetReplyTo(reply)
	}
	updates, err := api.MessagesSendMultiMedia(ctx, request)
	if err != nil {
		return nil, err
	}
	g.observeUpdates(ctx, updates)
	return updates, nil
}

func inputMedia(message *tg.Message) (tg.InputMediaClass, error) {
	if message == nil {
		return nil, forwarder.MessageNotFound{}
	}
	media, ok := message.GetMedia()
	if !ok || media == nil {
		return nil, nil
	}
	switch media := media.(type) {
	case *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return nil, nil
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok || photo == nil {
			return nil, forwarder.NewPermanentDeliveryError("this Telegram media type cannot be copied")
		}
		return &tg.InputMediaPhoto{ID: photo.AsInput()}, nil
	case *tg.MessageMediaDocument:
		document, ok := media.Document.(*tg.Document)
		if !ok || document == nil {
			return nil, forwarder.NewPermanentDeliveryError("this Telegram media type cannot be copied")
		}
		return &tg.InputMediaDocument{ID: document.AsInput()}, nil
	default:
		return nil, forwarder.NewPermanentDeliveryError("this Telegram media type cannot be copied")
	}
}

func keepsWebPreview(message *tg.Message) bool {
	if message == nil {
		return false
	}
	media, ok := message.GetMedia()
	if !ok || media == nil {
		return false
	}
	_, ok = media.(*tg.MessageMediaWebPage)
	return ok
}

func copiedMarkup(message *tg.Message) tg.ReplyMarkupClass {
	if message == nil {
		return nil
	}
	markup, ok := message.GetReplyMarkup()
	if !ok || markup == nil {
		return nil
	}
	return markup
}

func caption(message *tg.Message) (string, []tg.MessageEntityClass) {
	if message == nil {
		return "", nil
	}
	entities, ok := message.GetEntities()
	if !ok {
		return message.Message, nil
	}
	return message.Message, entities
}

// inputReply puts an ordinary reply, or the destination topic, on InputReplyToMessage.
// TopMsgID is set only when the topic is greater than 1 and differs from the reply id.
func inputReply(destination forwarder.DestinationEndpoint, replyToMessageID *int) tg.InputReplyToClass {
	replyID := 0
	if replyToMessageID != nil && *replyToMessageID > 0 {
		replyID = *replyToMessageID
	} else if destination.HasTopic && destination.TopicID > 0 {
		replyID = destination.TopicID
	}
	if replyID <= 0 {
		return nil
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: replyID}
	if destination.HasTopic && destination.TopicID > 1 && destination.TopicID != replyID {
		reply.SetTopMsgID(destination.TopicID)
	}
	return reply
}

func oneRef(chatID int64, updates tg.UpdatesClass) (forwarder.MessageRef, error) {
	ids := telegram.SentMessageIDs(updates)
	if len(ids) == 0 {
		return forwarder.MessageRef{}, forwarder.MessageNotFound{}
	}
	return contracts.NewMessageRef(chatID, ids[len(ids)-1])
}

func manyRefs(chatID int64, updates tg.UpdatesClass, count int) ([]forwarder.MessageRef, error) {
	ids := telegram.SentMessageIDs(updates)
	if len(ids) != count {
		return nil, forwarder.NewDeliveryResultMismatch(
			"sent album did not return one destination reference per item",
		)
	}
	refs := make([]forwarder.MessageRef, len(ids))
	for i, id := range ids {
		ref, err := contracts.NewMessageRef(chatID, id)
		if err != nil {
			return nil, err
		}
		refs[i] = ref
	}
	return refs, nil
}

func randomIDs(count int) ([]int64, error) {
	ids := make([]int64, count)
	for i := range ids {
		id, err := randomID()
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}
