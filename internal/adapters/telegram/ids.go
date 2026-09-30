package telegram

import (
	"github.com/gotd/td/constant"
	"github.com/gotd/td/tg"
)

// DialogID converts a Telegram peer to the TDLib dialog ID used by the domain.
func DialogID(peer tg.PeerClass) (int64, bool) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID, true
	case *tg.PeerChat:
		return -p.ChatID, true
	case *tg.PeerChannel:
		return ChannelDialogID(p.ChannelID), true
	default:
		return 0, false
	}
}

// ChatDialogID converts a user, chat, or channel object to a dialog ID.
func ChatDialogID(chat tg.ChatClass) (int64, bool) {
	switch c := chat.(type) {
	case *tg.Chat:
		return -c.ID, true
	case *tg.ChatForbidden:
		return -c.ID, true
	case *tg.Channel:
		return ChannelDialogID(c.ID), true
	case *tg.ChannelForbidden:
		return ChannelDialogID(c.ID), true
	default:
		return 0, false
	}
}

// ChannelDialogID is the TDLib ID for a channel or supergroup.
func ChannelDialogID(channelID int64) int64 {
	var id constant.TDLibPeerID
	id.Channel(channelID)
	return int64(id)
}
