package contracts

// EventOrigin is assigned by trusted producers, never parsed from message text.
type EventOrigin string

const (
	OriginLive    EventOrigin = "telegram-live"
	OriginHistory EventOrigin = "history-poll"
)

// TelegramEventEnvelope carries normalized receive/edit/delete events through
// one ingress stream. Historical messages must not execute control commands.
type TelegramEventEnvelope struct {
	Origin EventOrigin
	Event  any
}
