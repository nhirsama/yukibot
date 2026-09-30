package contracts

import (
	"errors"
	"time"
)

// ContentType is the normalized Telegram message kind.
type ContentType string

const (
	ContentText      ContentType = "text"
	ContentPhoto     ContentType = "photo"
	ContentVideo     ContentType = "video"
	ContentDocument  ContentType = "document"
	ContentAudio     ContentType = "audio"
	ContentVoice     ContentType = "voice"
	ContentVideoNote ContentType = "video_note"
	ContentSticker   ContentType = "sticker"
	ContentAnimation ContentType = "animation"
	ContentPoll      ContentType = "poll"
	ContentLocation  ContentType = "location"
	ContentContact   ContentType = "contact"
	ContentVenue     ContentType = "venue"
	ContentDice      ContentType = "dice"
	ContentGame      ContentType = "game"
	ContentService   ContentType = "service"
	ContentOther     ContentType = "other"
)

// ServiceKind classifies a service message.
type ServiceKind string

const (
	ServiceMembersJoined ServiceKind = "members_joined"
	ServiceMemberLeft    ServiceKind = "member_left"
	ServiceMessagePinned ServiceKind = "message_pinned"
	ServiceTitleChanged  ServiceKind = "title_changed"
	ServiceTopicCreated  ServiceKind = "topic_created"
	ServiceTopicClosed   ServiceKind = "topic_closed"
	ServiceTopicReopened ServiceKind = "topic_reopened"
	ServiceOther         ServiceKind = "other"
)

// MessageRef identifies one message in a chat.
type MessageRef struct {
	ChatID    int64
	MessageID int
}

// NewMessageRef validates the reference.
func NewMessageRef(chatID int64, messageID int) (MessageRef, error) {
	if chatID == 0 {
		return MessageRef{}, errors.New("chat_id must not be zero")
	}
	if messageID <= 0 {
		return MessageRef{}, errors.New("message_id must be positive")
	}
	return MessageRef{ChatID: chatID, MessageID: messageID}, nil
}

// ServiceMessage is the normalized service payload.
type ServiceMessage struct {
	Kind        ServiceKind
	ActorName   string
	MemberNames []string
	NewTitle    string
}

// TelegramMessage is the adapter-neutral message passed to features.
type TelegramMessage struct {
	Ref              MessageRef
	ContentType      ContentType
	OccurredAt       time.Time
	SenderID         *int64
	TopicID          *int
	GroupedID        any
	Text             string
	Caption          string
	ReplyToMessageID *int
	Service          *ServiceMessage
	Outgoing         bool
	EditedAt         *time.Time
}

// Validate checks the message invariants from the Python contract.
func (m TelegramMessage) Validate() error {
	if _, err := NewMessageRef(m.Ref.ChatID, m.Ref.MessageID); err != nil {
		return err
	}
	if m.TopicID != nil && *m.TopicID < 0 {
		return errors.New("topic_id must not be negative")
	}
	if m.ReplyToMessageID != nil && *m.ReplyToMessageID <= 0 {
		return errors.New("reply_to_message_id must be positive")
	}
	if m.ContentType == ContentService && m.Service == nil {
		return errors.New("service details are required for service messages")
	}
	if m.Service != nil && m.ContentType != ContentService {
		return errors.New("service details are only valid for service messages")
	}
	return nil
}

// SearchableText is text, otherwise caption, otherwise empty.
func (m TelegramMessage) SearchableText() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}
