package telegram

import (
	"context"
	"strings"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// Title returns a cached chat title.
func (c *Client) Title(chatID int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	title, ok := c.titles[chatID]
	return title, ok && title != ""
}

// Username returns a cached public username without the @ prefix.
func (c *Client) Username(chatID int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name, ok := c.usernames[chatID]
	return name, ok && name != ""
}

// IsForum reports whether the cached chat is a forum.
func (c *Client) IsForum(chatID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.forums[chatID]
}

// Joined reports whether the chat was present in the loaded dialogs.
func (c *Client) Joined(chatID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.joined[chatID]
	return ok
}

// Remember caches title, username, and forum flags from a resolved peer.
func (c *Client) Remember(peer peers.Peer) {
	if c == nil || peer == nil {
		return
	}
	id := int64(peer.TDLibPeerID())
	title := strings.TrimSpace(peer.VisibleName())
	username, _ := peer.Username()
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	forum := false
	if channel, ok := peer.(peers.Channel); ok && channel.Raw() != nil {
		forum = channel.Raw().GetForum()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if title != "" {
		c.titles[id] = title
	}
	if username != "" {
		c.usernames[id] = username
	}
	c.forums[id] = forum
}

// MarkJoined records that this account is a member of the chat.
func (c *Client) MarkJoined(chatID int64) {
	if c == nil || chatID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.joined[chatID] = struct{}{}
}

// Observe applies users and chats carried by an update.
func (c *Client) Observe(ctx context.Context, entities tg.Entities) {
	users := make([]tg.UserClass, 0, len(entities.Users))
	for _, user := range entities.Users {
		users = append(users, user)
	}
	chats := make([]tg.ChatClass, 0, len(entities.Chats)+len(entities.Channels))
	for _, chat := range entities.Chats {
		chats = append(chats, chat)
	}
	for _, channel := range entities.Channels {
		chats = append(chats, channel)
	}
	c.mu.Lock()
	manager := c.manager
	c.mu.Unlock()
	if manager != nil {
		_ = manager.Apply(ctx, users, chats)
	}
	c.noteUsers(users)
	c.noteChats(chats)
}

func (c *Client) noteUsers(users []tg.UserClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, user := range users {
		concrete, ok := user.(*tg.User)
		if !ok || concrete == nil {
			continue
		}
		if title := visibleUser(concrete); title != "" {
			c.titles[concrete.ID] = title
		}
		if name, ok := concrete.GetUsername(); ok && strings.TrimSpace(name) != "" {
			c.usernames[concrete.ID] = strings.TrimPrefix(strings.TrimSpace(name), "@")
		}
	}
}

func (c *Client) noteChats(chats []tg.ChatClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, chat := range chats {
		id, ok := ChatDialogID(chat)
		if !ok {
			continue
		}
		switch concrete := chat.(type) {
		case *tg.Chat:
			if title := strings.TrimSpace(concrete.Title); title != "" {
				c.titles[id] = title
			}
		case *tg.ChatForbidden:
			if title := strings.TrimSpace(concrete.Title); title != "" {
				c.titles[id] = title
			}
		case *tg.Channel:
			if title := strings.TrimSpace(concrete.Title); title != "" {
				c.titles[id] = title
			}
			if name, ok := concrete.GetUsername(); ok && strings.TrimSpace(name) != "" {
				c.usernames[id] = strings.TrimPrefix(strings.TrimSpace(name), "@")
			}
			c.forums[id] = concrete.GetForum()
		case *tg.ChannelForbidden:
			if title := strings.TrimSpace(concrete.Title); title != "" {
				c.titles[id] = title
			}
		}
	}
}

func visibleUser(user *tg.User) string {
	if user == nil {
		return ""
	}
	first, _ := user.GetFirstName()
	last, _ := user.GetLastName()
	parts := make([]string, 0, 2)
	if strings.TrimSpace(first) != "" {
		parts = append(parts, strings.TrimSpace(first))
	}
	if strings.TrimSpace(last) != "" {
		parts = append(parts, strings.TrimSpace(last))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if name, ok := user.GetUsername(); ok {
		return strings.TrimSpace(name)
	}
	return ""
}

func (c *Client) loadDialogs(ctx context.Context) error {
	api, err := c.API()
	if err != nil {
		return err
	}
	manager, err := c.Peers()
	if err != nil {
		return err
	}
	var offsetDate, offsetID int
	var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
	for page := 0; page < 20; page++ {
		box, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
			Limit:      100,
		})
		if err != nil {
			return err
		}
		dialogs, messages, users, chats := splitDialogs(box)
		if err := manager.Apply(ctx, users, chats); err != nil {
			return err
		}
		c.noteUsers(users)
		c.noteChats(chats)
		if len(dialogs) == 0 {
			return nil
		}
		var last *tg.Dialog
		for _, dialog := range dialogs {
			concrete, ok := dialog.(*tg.Dialog)
			if !ok || concrete == nil {
				continue
			}
			last = concrete
			if id, ok := DialogID(concrete.Peer); ok {
				c.mu.Lock()
				c.joined[id] = struct{}{}
				c.mu.Unlock()
			}
		}
		if len(dialogs) < 100 || last == nil {
			return nil
		}
		offsetID = last.TopMessage
		offsetDate = messageDate(messages, last.TopMessage)
		id, ok := DialogID(last.Peer)
		if !ok {
			return nil
		}
		peer, err := manager.ResolveTDLibID(ctx, constant.TDLibPeerID(id))
		if err != nil || peer == nil {
			return nil
		}
		offsetPeer = peer.InputPeer()
		c.Remember(peer)
	}
	return nil
}
