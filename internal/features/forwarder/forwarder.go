package forwarder

import (
	"context"
	"time"
)

// BackgroundReportHandler receives an album report flushed off the caller goroutine.
type BackgroundReportHandler func(context.Context, ForwardingReport) error

// ForwarderConfig tunes album assembly. A zero delay uses 800ms.
type ForwarderConfig struct {
	AlbumDelay time.Duration
	OnReport   BackgroundReportHandler
	OnError    func(error)
}

// Forwarder adds album assembly in front of ForwarderService.
type Forwarder struct {
	service  *ForwarderService
	albums   *AlbumBuffer[albumKey, IncomingMessage]
	onReport BackgroundReportHandler
}

type albumKey struct {
	chatID int64
	group  string
	kind   string
}

// NewForwarder rejects a negative album delay.
func NewForwarder(service *ForwarderService, cfg ForwarderConfig) (*Forwarder, error) {
	if service == nil {
		return nil, valueErr("forwarder service is required")
	}
	if cfg.AlbumDelay < 0 {
		return nil, valueErr("flush_delay must not be negative")
	}
	if cfg.AlbumDelay == 0 {
		cfg.AlbumDelay = 800 * time.Millisecond
	}
	forwarder := &Forwarder{service: service, onReport: cfg.OnReport}
	buffer, err := NewAlbumBuffer[albumKey, IncomingMessage](forwarder.flushAlbum, cfg.AlbumDelay, func(left, right IncomingMessage) bool {
		return left.Ref.MessageID < right.Ref.MessageID
	}, cfg.OnError)
	if err != nil {
		return nil, err
	}
	forwarder.albums = buffer
	return forwarder, nil
}

// HandleMessage forwards one message, or buffers it until the album is quiet.
func (f *Forwarder) HandleMessage(ctx context.Context, message IncomingMessage) (ForwardingReport, error) {
	if isNilValue(message.GroupedID) {
		return f.service.ForwardMessage(ctx, message)
	}
	key := albumKey{
		chatID: message.Ref.ChatID,
		group:  formatGroupedID(message.GroupedID),
		kind:   groupKind(message.GroupedID),
	}
	if err := f.albums.Add(ctx, key, message); err != nil {
		return ForwardingReport{}, err
	}
	return ForwardingReport{Buffered: true}, nil
}

// HandleEdit synchronizes one edited message.
func (f *Forwarder) HandleEdit(ctx context.Context, message IncomingMessage) (SyncReport, error) {
	return f.service.SynchronizeEdit(ctx, message)
}

// HandleDelete synchronizes one deletion event.
func (f *Forwarder) HandleDelete(ctx context.Context, event MessagesDeleted) (SyncReport, error) {
	return f.service.SynchronizeDelete(ctx, event)
}

// Close rejects later messages. Pending albums are flushed when flush is true.
func (f *Forwarder) Close(ctx context.Context, flush bool) error {
	return f.albums.Close(ctx, flush)
}

func (f *Forwarder) flushAlbum(ctx context.Context, messages []IncomingMessage) error {
	report, err := f.service.ForwardAlbum(ctx, messages)
	if err != nil {
		return err
	}
	if f.onReport != nil {
		return f.onReport(ctx, report)
	}
	return nil
}

func groupKind(value any) string {
	return reflectGroupKind(value)
}
