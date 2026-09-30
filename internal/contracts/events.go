package contracts

import (
	"errors"
	"time"
)

// TelegramMessageReceived is a new normalized message.
type TelegramMessageReceived struct {
	Message TelegramMessage
}

// TelegramMessageEdited is an edit of a normalized message.
type TelegramMessageEdited struct {
	Message TelegramMessage
}

// TelegramMessagesDeleted lists messages removed from a chat.
type TelegramMessagesDeleted struct {
	MessageIDs []int
	OccurredAt time.Time
	ChatID     *int64
}

// Validate checks delete-event invariants.
func (e TelegramMessagesDeleted) Validate() error {
	if len(e.MessageIDs) == 0 {
		return errors.New("message_ids must not be empty")
	}
	for _, id := range e.MessageIDs {
		if id <= 0 {
			return errors.New("message_ids must be positive")
		}
	}
	if e.ChatID != nil && *e.ChatID == 0 {
		return errors.New("chat_id must not be zero")
	}
	return nil
}
