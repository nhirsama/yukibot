package telegram

import (
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// Normalize maps a Telegram message onto the adapter-neutral contract.
// Poll text is stored in Text as "[投票] …". Other media keeps the body in Caption.
func Normalize(message tg.MessageClass, fallback time.Time) (contracts.TelegramMessage, bool) {
	switch message := message.(type) {
	case *tg.MessageService:
		return normalizeService(message, fallback)
	case *tg.Message:
		return normalizeMessage(message, fallback)
	default:
		return contracts.TelegramMessage{}, false
	}
}

func normalizeService(message *tg.MessageService, fallback time.Time) (contracts.TelegramMessage, bool) {
	if message == nil {
		return contracts.TelegramMessage{}, false
	}
	chatID, ok := DialogID(message.PeerID)
	if !ok || message.ID <= 0 {
		return contracts.TelegramMessage{}, false
	}
	occurred := unixTime(message.Date)
	if occurred.IsZero() {
		occurred = fallback.UTC()
	}
	service := serviceMessage(message.Action)
	normalized := contracts.TelegramMessage{
		Ref:         contracts.MessageRef{ChatID: chatID, MessageID: message.ID},
		ContentType: contracts.ContentService,
		OccurredAt:  occurred,
		Outgoing:    message.Out,
		Service:     &service,
	}
	if sender, ok := senderID(message.FromID); ok {
		normalized.SenderID = &sender
	}
	if topic, ok := topicID(message.ReplyTo, message.Action, message.ID); ok {
		normalized.TopicID = &topic
	}
	if reply, ok := replyID(message.ReplyTo); ok {
		normalized.ReplyToMessageID = &reply
	}
	return normalized, true
}

func normalizeMessage(message *tg.Message, fallback time.Time) (contracts.TelegramMessage, bool) {
	if message == nil {
		return contracts.TelegramMessage{}, false
	}
	chatID, ok := DialogID(message.PeerID)
	if !ok || message.ID <= 0 {
		return contracts.TelegramMessage{}, false
	}
	occurred := unixTime(message.Date)
	if occurred.IsZero() {
		occurred = fallback.UTC()
	}
	content := contentType(message)
	body := message.Message
	if content == contracts.ContentPoll {
		if text := pollText(message); text != "" {
			body = text
		}
	}
	normalized := contracts.TelegramMessage{
		Ref:         contracts.MessageRef{ChatID: chatID, MessageID: message.ID},
		ContentType: content,
		OccurredAt:  occurred,
		Outgoing:    message.Out,
	}
	if content == contracts.ContentText || content == contracts.ContentPoll || content == contracts.ContentService {
		normalized.Text = body
	} else {
		normalized.Caption = body
	}
	if sender, ok := senderID(message.FromID); ok {
		normalized.SenderID = &sender
	}
	if topic, ok := topicID(message.ReplyTo, nil, message.ID); ok {
		normalized.TopicID = &topic
	}
	if reply, ok := replyID(message.ReplyTo); ok {
		normalized.ReplyToMessageID = &reply
	}
	if grouped, ok := message.GetGroupedID(); ok {
		normalized.GroupedID = grouped
	}
	if edited, ok := message.GetEditDate(); ok && edited > 0 {
		stamp := unixTime(edited)
		normalized.EditedAt = &stamp
	}
	return normalized, true
}

func contentType(message *tg.Message) contracts.ContentType {
	media, ok := message.GetMedia()
	if !ok || media == nil {
		return contracts.ContentText
	}
	switch media := media.(type) {
	case *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return contracts.ContentText
	case *tg.MessageMediaPoll:
		return contracts.ContentPoll
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive:
		return contracts.ContentLocation
	case *tg.MessageMediaContact:
		return contracts.ContentContact
	case *tg.MessageMediaVenue:
		return contracts.ContentVenue
	case *tg.MessageMediaDice:
		return contracts.ContentDice
	case *tg.MessageMediaGame:
		return contracts.ContentGame
	case *tg.MessageMediaPhoto:
		return contracts.ContentPhoto
	case *tg.MessageMediaDocument:
		return documentContent(media)
	default:
		return contracts.ContentOther
	}
}

func documentContent(media *tg.MessageMediaDocument) contracts.ContentType {
	document, ok := media.Document.(*tg.Document)
	if !ok || document == nil {
		return contracts.ContentDocument
	}
	sticker := false
	animated := false
	var audio *tg.DocumentAttributeAudio
	var video *tg.DocumentAttributeVideo
	for _, attribute := range document.Attributes {
		switch attribute := attribute.(type) {
		case *tg.DocumentAttributeSticker:
			sticker = true
		case *tg.DocumentAttributeAnimated:
			animated = true
		case *tg.DocumentAttributeAudio:
			audio = attribute
		case *tg.DocumentAttributeVideo:
			video = attribute
		}
	}
	if sticker {
		return contracts.ContentSticker
	}
	if animated {
		return contracts.ContentAnimation
	}
	if audio != nil {
		if audio.Voice {
			return contracts.ContentVoice
		}
		return contracts.ContentAudio
	}
	if video != nil {
		if video.RoundMessage {
			return contracts.ContentVideoNote
		}
		return contracts.ContentVideo
	}
	return contracts.ContentDocument
}

func pollText(message *tg.Message) string {
	media, ok := message.GetMedia()
	if !ok {
		return ""
	}
	pollMedia, ok := media.(*tg.MessageMediaPoll)
	if !ok || pollMedia.Poll.Question.Text == "" {
		return ""
	}
	question := strings.TrimSpace(pollMedia.Poll.Question.Text)
	if question == "" {
		return ""
	}
	options := make([]string, 0, len(pollMedia.Poll.Answers))
	for _, answer := range pollMedia.Poll.Answers {
		received, ok := answer.(*tg.PollAnswer)
		if !ok || received == nil {
			continue
		}
		text := strings.TrimSpace(received.Text.Text)
		if text != "" {
			options = append(options, text)
		}
	}
	if len(options) == 0 {
		return "[投票] " + question
	}
	return "[投票] " + question + " 选项: " + strings.Join(options, "; ")
}

func serviceMessage(action tg.MessageActionClass) contracts.ServiceMessage {
	service := contracts.ServiceMessage{Kind: contracts.ServiceOther}
	switch action := action.(type) {
	case *tg.MessageActionChatAddUser, *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		service.Kind = contracts.ServiceMembersJoined
	case *tg.MessageActionChatDeleteUser:
		service.Kind = contracts.ServiceMemberLeft
	case *tg.MessageActionPinMessage:
		service.Kind = contracts.ServiceMessagePinned
	case *tg.MessageActionChatEditTitle:
		service.Kind = contracts.ServiceTitleChanged
		service.NewTitle = action.Title
	case *tg.MessageActionTopicCreate:
		service.Kind = contracts.ServiceTopicCreated
		service.NewTitle = action.Title
	case *tg.MessageActionTopicEdit:
		if closed, ok := action.GetClosed(); ok {
			if closed {
				service.Kind = contracts.ServiceTopicClosed
			} else {
				service.Kind = contracts.ServiceTopicReopened
			}
		}
		if title, ok := action.GetTitle(); ok {
			service.NewTitle = title
		}
	}
	return service
}

func topicID(reply tg.MessageReplyHeaderClass, action tg.MessageActionClass, messageID int) (int, bool) {
	if header, ok := reply.(*tg.MessageReplyHeader); ok && header != nil && header.GetForumTopic() {
		if top, ok := header.GetReplyToTopID(); ok && top > 0 {
			return top, true
		}
		if id, ok := header.GetReplyToMsgID(); ok && id > 0 {
			return id, true
		}
		return 1, true
	}
	if _, ok := action.(*tg.MessageActionTopicCreate); ok {
		return messageID, true
	}
	return 0, false
}

func replyID(reply tg.MessageReplyHeaderClass) (int, bool) {
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return 0, false
	}
	id, ok := header.GetReplyToMsgID()
	return id, ok && id > 0
}

func senderID(peer tg.PeerClass) (int64, bool) {
	user, ok := peer.(*tg.PeerUser)
	if !ok || user == nil || user.UserID == 0 {
		return 0, false
	}
	return user.UserID, true
}
