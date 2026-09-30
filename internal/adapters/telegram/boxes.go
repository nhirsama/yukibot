package telegram

import (
	"time"

	"github.com/gotd/td/tg"
)

func splitDialogs(box tg.MessagesDialogsClass) (dialogs []tg.DialogClass, messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass) {
	switch box := box.(type) {
	case *tg.MessagesDialogs:
		return box.Dialogs, box.Messages, box.Users, box.Chats
	case *tg.MessagesDialogsSlice:
		return box.Dialogs, box.Messages, box.Users, box.Chats
	case *tg.MessagesDialogsNotModified:
		return nil, nil, nil, nil
	default:
		return nil, nil, nil, nil
	}
}

func splitMessages(box tg.MessagesMessagesClass) (messages []tg.MessageClass, users []tg.UserClass, chats []tg.ChatClass) {
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

func messageDate(messages []tg.MessageClass, id int) int {
	for _, message := range messages {
		switch concrete := message.(type) {
		case *tg.Message:
			if concrete.ID == id {
				return concrete.Date
			}
		case *tg.MessageService:
			if concrete.ID == id {
				return concrete.Date
			}
		}
	}
	return 0
}

// SentMessageIDs collects ids from a send or forward result.
func SentMessageIDs(updates tg.UpdatesClass) []int {
	var ids []int
	collect := func(update tg.UpdateClass) {
		switch update := update.(type) {
		case *tg.UpdateMessageID:
			if update.ID > 0 {
				ids = append(ids, update.ID)
			}
		case *tg.UpdateNewMessage:
			if id := messageID(update.Message); id > 0 {
				ids = append(ids, id)
			}
		case *tg.UpdateNewChannelMessage:
			if id := messageID(update.Message); id > 0 {
				ids = append(ids, id)
			}
		}
	}
	switch updates := updates.(type) {
	case *tg.UpdateShortSentMessage:
		if updates.ID > 0 {
			ids = append(ids, updates.ID)
		}
	case *tg.UpdateShort:
		collect(updates.Update)
	case *tg.Updates:
		for _, update := range updates.Updates {
			collect(update)
		}
	case *tg.UpdatesCombined:
		for _, update := range updates.Updates {
			collect(update)
		}
	}
	return uniquePositive(ids)
}

// TopicIDFromUpdates returns the forum topic id created by a service message.
func TopicIDFromUpdates(updates tg.UpdatesClass) (int, bool) {
	var topic int
	found := false
	walk := func(message tg.MessageClass) {
		service, ok := message.(*tg.MessageService)
		if !ok || service == nil {
			return
		}
		if _, ok := service.Action.(*tg.MessageActionTopicCreate); ok && service.ID > 0 {
			topic = service.ID
			found = true
		}
	}
	switch updates := updates.(type) {
	case *tg.Updates:
		for _, update := range updates.Updates {
			switch update := update.(type) {
			case *tg.UpdateNewMessage:
				walk(update.Message)
			case *tg.UpdateNewChannelMessage:
				walk(update.Message)
			}
		}
	case *tg.UpdatesCombined:
		for _, update := range updates.Updates {
			switch update := update.(type) {
			case *tg.UpdateNewMessage:
				walk(update.Message)
			case *tg.UpdateNewChannelMessage:
				walk(update.Message)
			}
		}
	}
	if found {
		return topic, true
	}
	ids := SentMessageIDs(updates)
	if len(ids) == 0 {
		return 0, false
	}
	return ids[0], true
}

func messageID(message tg.MessageClass) int {
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

func uniquePositive(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
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

func unixTime(seconds int) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0).UTC()
}
