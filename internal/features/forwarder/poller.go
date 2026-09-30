package forwarder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// SourcePollerConfig tunes public-source polling. Zero limits use the Python defaults.
type SourcePollerConfig struct {
	BatchSize    int
	MaxBatches   int
	ScheduleTick time.Duration
	Clock        func() time.Time
	Logger       *slog.Logger
}

// SourcePoller publishes new public-source messages on the normal receive contract.
type SourcePoller struct {
	routes       RouteRepository
	cursors      PollCursorRepository
	telegram     TelegramSourceGateway
	bus          EventPublisher
	batchSize    int
	maxBatches   int
	scheduleTick time.Duration
	clock        func() time.Time
	log          *slog.Logger

	mu       sync.Mutex
	nextDue  map[int64]time.Time
	stopping bool
	stop     chan struct{}
}

// NewSourcePoller rejects non-positive explicit limits. Zero values use the defaults.
func NewSourcePoller(routes RouteRepository, cursors PollCursorRepository, telegram TelegramSourceGateway, bus EventPublisher, cfg SourcePollerConfig) (*SourcePoller, error) {
	if routes == nil || cursors == nil || telegram == nil || bus == nil {
		return nil, valueErr("source poller dependencies are required")
	}
	if cfg.BatchSize < 0 || cfg.MaxBatches < 0 {
		return nil, valueErr("poll batch limits must be positive")
	}
	if cfg.ScheduleTick < 0 {
		return nil, valueErr("schedule_tick must be positive")
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxBatches == 0 {
		cfg.MaxBatches = 10
	}
	if cfg.ScheduleTick == 0 {
		cfg.ScheduleTick = time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &SourcePoller{
		routes:       routes,
		cursors:      cursors,
		telegram:     telegram,
		bus:          bus,
		batchSize:    cfg.BatchSize,
		maxBatches:   cfg.MaxBatches,
		scheduleTick: cfg.ScheduleTick,
		clock:        cfg.Clock,
		log:          loggerOrDiscard(cfg.Logger),
		nextDue:      map[int64]time.Time{},
		stop:         make(chan struct{}, 1),
	}, nil
}

// Prepare clears a previous stop request before the feature starts the loop.
func (p *SourcePoller) Prepare() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopping = false
	select {
	case <-p.stop:
	default:
	}
}

// RequestStop asks Run to return after the current poll.
func (p *SourcePoller) RequestStop() {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	select {
	case p.stop <- struct{}{}:
	default:
	}
}

// Run polls until RequestStop or ctx is cancelled.
func (p *SourcePoller) Run(ctx context.Context) error {
	for {
		if p.isStopped() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := p.PollOnce(ctx); err != nil {
			return err
		}
		if p.isStopped() {
			return nil
		}
		timer := time.NewTimer(p.scheduleTick)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-p.stop:
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// PollOnce polls every due source at the current clock time.
func (p *SourcePoller) PollOnce(ctx context.Context) (int, error) {
	return p.PollOnceAt(ctx, p.clock())
}

// PollOnceAt polls every due source using now instead of the clock.
func (p *SourcePoller) PollOnceAt(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	routes, err := p.routes.ListAll(ctx)
	if err != nil {
		return 0, err
	}
	sources := orderedPollingSources(routes)
	active := map[int64]struct{}{}
	for _, source := range sources {
		active[source.ChatID] = struct{}{}
	}
	p.mu.Lock()
	for chatID := range p.nextDue {
		if _, ok := active[chatID]; !ok {
			delete(p.nextDue, chatID)
		}
	}
	p.mu.Unlock()

	published := 0
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return published, err
		}
		if !source.IsPolled() {
			continue
		}
		p.mu.Lock()
		due, ok := p.nextDue[source.ChatID]
		p.mu.Unlock()
		if ok && due.After(now) {
			continue
		}
		interval := source.PollEvery
		count, backlog, err := p.pollSource(ctx, source)
		if err != nil {
			if isCancel(err) {
				return published, err
			}
			var retry RetryAfter
			if errors.As(err, &retry) {
				p.mu.Lock()
				p.nextDue[source.ChatID] = now.Add(retry.Delay)
				p.mu.Unlock()
				p.log.Warn("source poll rate limited",
					"feature", "forwarder",
					"chat_id", source.ChatID,
					"retry_after", retry.Delay.Seconds(),
				)
				continue
			}
			p.mu.Lock()
			p.nextDue[source.ChatID] = now.Add(interval)
			p.mu.Unlock()
			p.log.Error("source poll failed",
				"feature", "forwarder",
				"chat_id", source.ChatID,
				"error_type", typeName(err),
				"error", err,
			)
			continue
		}
		published += count
		next := now.Add(interval)
		if backlog {
			next = now
		}
		p.mu.Lock()
		p.nextDue[source.ChatID] = next
		p.mu.Unlock()
		p.log.Info("source poll completed",
			"feature", "forwarder",
			"chat_id", source.ChatID,
			"published_messages", count,
			"backlog", backlog,
		)
	}
	return published, nil
}

func (p *SourcePoller) pollSource(ctx context.Context, source SourceEndpoint) (int, bool, error) {
	if err := p.telegram.EnsureSource(ctx, source, false); err != nil {
		return 0, false, err
	}
	cursor, ok, err := p.cursors.Get(ctx, source.ChatID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		latest, err := p.telegram.LatestMessageID(ctx, source)
		if err != nil {
			return 0, false, err
		}
		stored, err := NewPollCursor(source.ChatID, latest)
		if err != nil {
			return 0, false, err
		}
		if err := p.cursors.Save(ctx, stored); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}

	published := 0
	after := cursor.LastMessageID
	for batch := 0; batch < p.maxBatches; batch++ {
		messages, err := p.telegram.FetchMessagesAfter(ctx, source, after, p.batchSize)
		if err != nil {
			return published, false, err
		}
		if len(messages) == 0 {
			return published, false, nil
		}
		if err := validatePollBatch(source.ChatID, after, messages); err != nil {
			return published, false, err
		}
		for _, message := range messages {
			if err := p.bus.Publish(ctx, contracts.TelegramMessageReceived{Message: message}); err != nil {
				return published, false, err
			}
		}
		after = messages[len(messages)-1].Ref.MessageID
		stored, err := NewPollCursor(source.ChatID, after)
		if err != nil {
			return published, false, err
		}
		if err := p.cursors.Save(ctx, stored); err != nil {
			return published, false, err
		}
		published += len(messages)
		if len(messages) < p.batchSize {
			return published, false, nil
		}
	}
	return published, true, nil
}

func (p *SourcePoller) isStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopping
}

func orderedPollingSources(routes []Route) []SourceEndpoint {
	selected := map[int64]SourceEndpoint{}
	order := make([]int64, 0)
	for _, route := range routes {
		if !route.Enabled || !route.Source.IsPolled() {
			continue
		}
		existing, ok := selected[route.Source.ChatID]
		if !ok {
			order = append(order, route.Source.ChatID)
			selected[route.Source.ChatID] = route.Source
			continue
		}
		if route.Source.PollEvery < existing.PollEvery {
			selected[route.Source.ChatID] = route.Source
		}
	}
	out := make([]SourceEndpoint, 0, len(order))
	for _, chatID := range order {
		out = append(out, selected[chatID])
	}
	return out
}

func validatePollBatch(chatID int64, afterMessageID int, messages []IncomingMessage) error {
	previous := afterMessageID
	for _, message := range messages {
		if message.Ref.ChatID != chatID || message.Ref.MessageID <= previous {
			return valueErr("Telegram polling returned an invalid or unordered message batch")
		}
		previous = message.Ref.MessageID
	}
	return nil
}
