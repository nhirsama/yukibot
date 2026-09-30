package infra

import (
	"context"
	"sort"
	"time"

	"github.com/gotd/td/tg"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

// LatestMessageID returns the newest message ID, or zero when the chat is empty.
func (g *Gateway) LatestMessageID(ctx context.Context, source forwarder.SourceEndpoint) (int, error) {
	var latest int
	err := g.slot(ctx, source.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.sourcePeer(ctx, source)
		if err != nil {
			return err
		}
		box, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer.InputPeer(),
			Limit: 1,
		})
		if err != nil {
			return err
		}
		g.observeBox(ctx, box)
		messages, _, _ := splitMessages(box)
		latest = newestMessageID(messages)
		return nil
	})
	if err != nil {
		return 0, translate(err, false)
	}
	return latest, nil
}

// FetchMessagesAfter reads messages newer than afterMessageID, oldest first.
// The request matches Telethon's reverse iter_messages: offset_id is after+1
// (or 1 when after is 0), add_offset is -limit, then ids at or below after are dropped.
func (g *Gateway) FetchMessagesAfter(ctx context.Context, source forwarder.SourceEndpoint, afterMessageID int, limit int) ([]forwarder.IncomingMessage, error) {
	if afterMessageID < 0 {
		return nil, forwarder.NewValueError("after_message_id must not be negative")
	}
	if limit <= 0 {
		return nil, forwarder.NewValueError("limit must be positive")
	}
	var fetched []forwarder.IncomingMessage
	err := g.slot(ctx, source.ChatID, func() error {
		api, err := g.api()
		if err != nil {
			return err
		}
		peer, err := g.sourcePeer(ctx, source)
		if err != nil {
			return err
		}
		offsetID := afterMessageID + 1
		if afterMessageID == 0 {
			offsetID = 1
		}
		box, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:      peer.InputPeer(),
			OffsetID:  offsetID,
			AddOffset: -limit,
			Limit:     limit,
		})
		if err != nil {
			return err
		}
		g.observeBox(ctx, box)
		messages, _, _ := splitMessages(box)
		kept := make([]tg.MessageClass, 0, len(messages))
		for _, message := range messages {
			if messageClassID(message) > afterMessageID {
				kept = append(kept, message)
			}
		}
		sort.SliceStable(kept, func(i, j int) bool {
			return messageClassID(kept[i]) < messageClassID(kept[j])
		})
		if len(kept) > limit {
			kept = kept[:limit]
		}
		observed := time.Now().UTC()
		fetched = make([]forwarder.IncomingMessage, 0, len(kept))
		for _, message := range kept {
			normalized, ok := telegram.Normalize(message, observed)
			if !ok {
				continue
			}
			validated, err := forwarder.NewIncomingMessage(normalized)
			if err != nil {
				return err
			}
			fetched = append(fetched, validated)
		}
		return nil
	})
	if err != nil {
		return nil, translate(err, false)
	}
	return fetched, nil
}

func newestMessageID(messages []tg.MessageClass) int {
	latest := 0
	for _, message := range messages {
		if id := messageClassID(message); id > latest {
			latest = id
		}
	}
	return latest
}
