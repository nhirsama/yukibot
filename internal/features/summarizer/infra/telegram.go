package infra

import (
	"crypto/rand"
	"encoding/binary"

	"github.com/gotd/td/tg"
)

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
