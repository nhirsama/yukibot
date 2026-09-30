package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

type refJSON struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id"`
}

type serviceJSON struct {
	Kind        string   `json:"kind"`
	ActorName   string   `json:"actor_name"`
	MemberNames []string `json:"member_names"`
	NewTitle    string   `json:"new_title"`
}

type messageJSON struct {
	Ref              refJSON         `json:"ref"`
	ContentType      string          `json:"content_type"`
	OccurredAt       time.Time       `json:"occurred_at"`
	SenderID         *int64          `json:"sender_id"`
	TopicID          *int            `json:"topic_id"`
	GroupedID        json.RawMessage `json:"grouped_id"`
	Text             *string         `json:"text"`
	Caption          *string         `json:"caption"`
	ReplyToMessageID *int            `json:"reply_to_message_id"`
	Service          *serviceJSON    `json:"service"`
	Outgoing         bool            `json:"outgoing"`
	EditedAt         *time.Time      `json:"edited_at"`
}

type deletedJSON struct {
	MessageIDs []int      `json:"message_ids"`
	OccurredAt time.Time  `json:"occurred_at"`
	ChatID     *int64     `json:"chat_id"`
}

func encodeEvent(event any) ([]byte, error) {
	switch ev := event.(type) {
	case contracts.TelegramMessageReceived:
		return encodeMessage(ev.Message)
	case contracts.TelegramMessageEdited:
		return encodeMessage(ev.Message)
	case contracts.TelegramMessagesDeleted:
		return json.Marshal(deletedJSON{MessageIDs: ev.MessageIDs, OccurredAt: ev.OccurredAt.UTC(), ChatID: ev.ChatID})
	default:
		return nil, fmt.Errorf("unsupported forwarding event %T", event)
	}
}

func decodeEvent(kind forwarder.ForwardJobKind, payload []byte) (any, error) {
	if kind == forwarder.ForwardJobDelete {
		var decoded deletedJSON
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return nil, err
		}
		event := contracts.TelegramMessagesDeleted{MessageIDs: decoded.MessageIDs, OccurredAt: decoded.OccurredAt.UTC(), ChatID: decoded.ChatID}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		return event, nil
	}
	message, err := decodeMessage(payload)
	if err != nil {
		return nil, err
	}
	if kind == forwarder.ForwardJobReceive {
		return contracts.TelegramMessageReceived{Message: message}, nil
	}
	return contracts.TelegramMessageEdited{Message: message}, nil
}

func encodeMessage(message contracts.TelegramMessage) ([]byte, error) {
	payload := messageJSON{
		Ref:              refJSON{ChatID: message.Ref.ChatID, MessageID: message.Ref.MessageID},
		ContentType:      string(message.ContentType),
		OccurredAt:       message.OccurredAt.UTC(),
		SenderID:         message.SenderID,
		TopicID:          message.TopicID,
		ReplyToMessageID: message.ReplyToMessageID,
		Outgoing:         message.Outgoing,
	}
	if message.Text != "" {
		payload.Text = &message.Text
	}
	if message.Caption != "" {
		payload.Caption = &message.Caption
	}
	if message.EditedAt != nil {
		edited := message.EditedAt.UTC()
		payload.EditedAt = &edited
	}
	if message.Service != nil {
		names := message.Service.MemberNames
		if names == nil {
			names = []string{}
		}
		payload.Service = &serviceJSON{
			Kind:        string(message.Service.Kind),
			ActorName:   message.Service.ActorName,
			MemberNames: names,
			NewTitle:    message.Service.NewTitle,
		}
	}
	grouped, err := encodeGrouped(message.GroupedID)
	if err != nil {
		return nil, err
	}
	payload.GroupedID = grouped
	return json.Marshal(payload)
}

func decodeMessage(payload []byte) (contracts.TelegramMessage, error) {
	var decoded messageJSON
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return contracts.TelegramMessage{}, err
	}
	ref, err := contracts.NewMessageRef(decoded.Ref.ChatID, decoded.Ref.MessageID)
	if err != nil {
		return contracts.TelegramMessage{}, err
	}
	message := contracts.TelegramMessage{
		Ref:              ref,
		ContentType:      contracts.ContentType(decoded.ContentType),
		OccurredAt:       decoded.OccurredAt.UTC(),
		SenderID:         decoded.SenderID,
		TopicID:          decoded.TopicID,
		ReplyToMessageID: decoded.ReplyToMessageID,
		Outgoing:         decoded.Outgoing,
		Text:             derefString(decoded.Text),
		Caption:          derefString(decoded.Caption),
	}
	if decoded.EditedAt != nil {
		edited := decoded.EditedAt.UTC()
		message.EditedAt = &edited
	}
	if decoded.Service != nil {
		message.Service = &contracts.ServiceMessage{
			Kind:        contracts.ServiceKind(decoded.Service.Kind),
			ActorName:   decoded.Service.ActorName,
			MemberNames: decoded.Service.MemberNames,
			NewTitle:    decoded.Service.NewTitle,
		}
	}
	grouped, err := decodeGrouped(decoded.GroupedID)
	if err != nil {
		return contracts.TelegramMessage{}, err
	}
	message.GroupedID = grouped
	if err := message.Validate(); err != nil {
		return contracts.TelegramMessage{}, err
	}
	return message, nil
}

func encodeGrouped(value any) (json.RawMessage, error) {
	if value == nil {
		return []byte("null"), nil
	}
	switch n := value.(type) {
	case int:
		return []byte(strconv.Itoa(n)), nil
	case int64:
		return []byte(strconv.FormatInt(n, 10)), nil
	case string:
		return json.Marshal(n)
	default:
		return json.Marshal(value)
	}
}

func decodeGrouped(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil, err
	}
	return number, nil
}
