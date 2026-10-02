package bootstrap

import (
	"context"
	"errors"

	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/kernel"
)

// queuedPublisher preserves feature subscription ports but sends both live
// updates and history through the same queue with producer-assigned provenance.
type queuedPublisher struct {
	busAdapter
	stream *kernel.MessageStream
	origin contracts.EventOrigin
}

func (p queuedPublisher) Publish(ctx context.Context, event any) error {
	switch event.(type) {
	case contracts.TelegramMessageReceived, contracts.TelegramMessageEdited, contracts.TelegramMessagesDeleted:
	default:
		return errors.New("unsupported ingress event")
	}
	envelope := contracts.TelegramEventEnvelope{Origin: p.origin, Event: event}
	if p.origin == contracts.OriginHistory {
		return p.stream.PublishAndWait(ctx, envelope)
	}
	if p.origin != contracts.OriginLive {
		return errors.New("unsupported ingress origin")
	}
	return p.stream.Publish(ctx, envelope)
}

// distributeMessages is downstream of the control subscriber, never parallel
// with authorization. Consumed/denied/failed commands cannot reach this handler.
func distributeMessages(bus *kernel.EventBus) kernel.StreamHandler {
	return func(ctx context.Context, raw any) (bool, error) {
		envelope, ok := raw.(contracts.TelegramEventEnvelope)
		if !ok || (envelope.Origin != contracts.OriginLive && envelope.Origin != contracts.OriginHistory) {
			return true, errors.New("invalid ingress envelope")
		}
		switch envelope.Event.(type) {
		case contracts.TelegramMessageReceived, contracts.TelegramMessageEdited, contracts.TelegramMessagesDeleted:
		default:
			return true, errors.New("invalid ingress payload")
		}
		report, err := bus.Publish(ctx, envelope.Event)
		if err != nil {
			return true, err
		}
		if envelope.Origin == contracts.OriginHistory && report.HandlerCount == 0 {
			return true, errors.New("history event has no active subscribers")
		}
		var failures []error
		for _, failure := range report.Failures {
			failures = append(failures, failure.Err)
		}
		return true, errors.Join(failures...)
	}
}
