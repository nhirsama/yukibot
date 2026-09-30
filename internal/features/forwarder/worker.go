package forwarder

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// ProcessingReport is a forwarding or synchronization result the runner can persist.
type ProcessingReport interface {
	Errors() []error
}

// MessageForwarder is the use-case surface executed by ForwardJobProcessor.
type MessageForwarder interface {
	ForwardMessage(ctx context.Context, message IncomingMessage) (ForwardingReport, error)
	ForwardAlbum(ctx context.Context, messages []IncomingMessage) (ForwardingReport, error)
	SynchronizeEdit(ctx context.Context, message IncomingMessage) (SyncReport, error)
	SynchronizeDelete(ctx context.Context, event MessagesDeleted) (SyncReport, error)
}

// JobProcessor executes one claimed batch.
type JobProcessor interface {
	Execute(ctx context.Context, jobs []ForwardJob) (ProcessingReport, error)
}

// ForwardJobProcessor dispatches claimed jobs to ForwarderService.
type ForwardJobProcessor struct {
	service MessageForwarder
}

// NewForwardJobProcessor returns a processor for service.
func NewForwardJobProcessor(service MessageForwarder) *ForwardJobProcessor {
	return &ForwardJobProcessor{service: service}
}

// Execute runs one ordered batch. A multi-message receive batch is an album.
func (p *ForwardJobProcessor) Execute(ctx context.Context, jobs []ForwardJob) (ProcessingReport, error) {
	if len(jobs) == 0 {
		return nil, valueErr("at least one forwarding job is required")
	}
	kind := jobs[0].Kind
	for _, job := range jobs[1:] {
		if job.Kind != kind {
			return nil, valueErr("a claimed job batch must contain one operation kind")
		}
	}
	switch kind {
	case ForwardJobReceive:
		messages := make([]IncomingMessage, len(jobs))
		for i, job := range jobs {
			message, err := receivedMessage(job)
			if err != nil {
				return nil, err
			}
			messages[i] = message
		}
		if len(messages) == 1 {
			return p.service.ForwardMessage(ctx, messages[0])
		}
		return p.service.ForwardAlbum(ctx, messages)
	case ForwardJobEdit:
		if len(jobs) != 1 {
			return nil, valueErr(string(kind) + " jobs cannot be processed as a batch")
		}
		event, err := editedEvent(jobs[0])
		if err != nil {
			return nil, err
		}
		return p.service.SynchronizeEdit(ctx, event.Message)
	case ForwardJobDelete:
		if len(jobs) != 1 {
			return nil, valueErr(string(kind) + " jobs cannot be processed as a batch")
		}
		event, err := deletedEvent(jobs[0])
		if err != nil {
			return nil, err
		}
		return p.service.SynchronizeDelete(ctx, event)
	default:
		return nil, valueErr("a claimed job batch must contain one operation kind")
	}
}

func receivedMessage(job ForwardJob) (IncomingMessage, error) {
	switch event := job.Event.(type) {
	case contracts.TelegramMessageReceived:
		return event.Message, nil
	case *contracts.TelegramMessageReceived:
		if event != nil {
			return event.Message, nil
		}
	}
	return IncomingMessage{}, valueErr("receive job contains the wrong event type")
}

func editedEvent(job ForwardJob) (contracts.TelegramMessageEdited, error) {
	switch event := job.Event.(type) {
	case contracts.TelegramMessageEdited:
		return event, nil
	case *contracts.TelegramMessageEdited:
		if event != nil {
			return *event, nil
		}
	}
	return contracts.TelegramMessageEdited{}, valueErr("edit job contains the wrong event type")
}

func deletedEvent(job ForwardJob) (contracts.TelegramMessagesDeleted, error) {
	switch event := job.Event.(type) {
	case contracts.TelegramMessagesDeleted:
		return event, nil
	case *contracts.TelegramMessagesDeleted:
		if event != nil {
			return *event, nil
		}
	}
	return contracts.TelegramMessagesDeleted{}, valueErr("delete job contains the wrong event type")
}

// ForwardJobRunnerConfig tunes retries. Zero values use the Python defaults.
type ForwardJobRunnerConfig struct {
	MaxAttempts  int
	RetryBase    time.Duration
	RetryMax     time.Duration
	PollInterval time.Duration
	Clock        func() time.Time
	Logger       *slog.Logger
}

// ForwardJobRunner claims one ordered batch at a time and persists every transition.
type ForwardJobRunner struct {
	repository   ForwardJobRepository
	processor    JobProcessor
	maxAttempts  int
	retryBase    time.Duration
	retryMax     time.Duration
	pollInterval time.Duration
	clock        func() time.Time
	log          *slog.Logger

	mu       sync.Mutex
	stopping bool
	wake     chan struct{}
}

// NewForwardJobRunner validates retry settings and applies Python defaults.
func NewForwardJobRunner(repository ForwardJobRepository, processor JobProcessor, cfg ForwardJobRunnerConfig) (*ForwardJobRunner, error) {
	if repository == nil || processor == nil {
		return nil, valueErr("job runner dependencies are required")
	}
	if cfg.MaxAttempts < 0 {
		return nil, valueErr("max_attempts must be positive")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.RetryBase < 0 || cfg.RetryMax < 0 {
		return nil, valueErr("retry delays must be non-negative and ordered")
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 60 * time.Second
	}
	if cfg.RetryMax < cfg.RetryBase {
		return nil, valueErr("retry delays must be non-negative and ordered")
	}
	if cfg.PollInterval < 0 {
		return nil, valueErr("poll_interval must be positive")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &ForwardJobRunner{
		repository:   repository,
		processor:    processor,
		maxAttempts:  cfg.MaxAttempts,
		retryBase:    cfg.RetryBase,
		retryMax:     cfg.RetryMax,
		pollInterval: cfg.PollInterval,
		clock:        cfg.Clock,
		log:          loggerOrDiscard(cfg.Logger),
		wake:         make(chan struct{}, 1),
	}, nil
}

// Prepare returns interrupted processing jobs to pending without resetting attempts.
func (r *ForwardJobRunner) Prepare(ctx context.Context) (int, error) {
	r.mu.Lock()
	r.stopping = false
	r.mu.Unlock()
	return r.repository.RecoverIncomplete(ctx)
}

// Enqueue persists jobs and wakes the loop, including duplicate-key updates.
func (r *ForwardJobRunner) Enqueue(ctx context.Context, jobs []PendingForwardJob) (int, error) {
	inserted, err := r.repository.Enqueue(ctx, jobs)
	if err != nil {
		return 0, err
	}
	r.Wake()
	return inserted, nil
}

// Wake asks the loop to look for due work.
func (r *ForwardJobRunner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// RequestStop ends Run after the current batch.
func (r *ForwardJobRunner) RequestStop() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.Wake()
}

// Run claims due batches until RequestStop.
func (r *ForwardJobRunner) Run(ctx context.Context) error {
	for {
		if r.isStopping() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := r.ProcessOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		if r.isStopping() {
			return nil
		}
		select {
		case <-r.wake:
		default:
		}
		if r.isStopping() {
			return nil
		}
		timer := time.NewTimer(r.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ProcessOnce claims and finishes at most one due batch.
func (r *ForwardJobRunner) ProcessOnce(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	jobs, err := r.repository.ClaimDue(ctx, r.clock())
	if err != nil {
		return false, err
	}
	if len(jobs) == 0 {
		return false, nil
	}
	ids := make([]int, len(jobs))
	attempts := 0
	for i, job := range jobs {
		ids[i] = job.ID
		if job.Attempts > attempts {
			attempts = job.Attempts
		}
	}
	report, execErr := r.processor.Execute(ctx, jobs)
	var failures []error
	if execErr != nil {
		if isCancel(execErr) {
			return false, execErr
		}
		failures = []error{execErr}
	} else if report != nil {
		failures = report.Errors()
	}
	if len(failures) == 0 {
		if err := r.repository.MarkSucceeded(ctx, ids); err != nil {
			return false, err
		}
		r.logCompletion(jobs, attempts, report)
		return true, nil
	}
	summary := errorSummary(failures)
	if attempts >= r.maxAttempts || allPermanent(failures) {
		if err := r.repository.MarkFailed(ctx, ids, summary); err != nil {
			return false, err
		}
		r.log.Error("forwarder job failed permanently",
			"feature", "forwarder",
			"job_ids", ids,
			"operation", string(jobs[0].Kind),
			"attempt", attempts,
			"error_type", typeName(failures[0]),
		)
		return true, nil
	}
	delay := retryDelay(failures, attempts, r.retryBase, r.retryMax)
	if err := r.repository.Reschedule(ctx, ids, r.clock().Add(delay), summary); err != nil {
		return false, err
	}
	r.log.Warn("forwarder job scheduled for retry",
		"feature", "forwarder",
		"job_ids", ids,
		"operation", string(jobs[0].Kind),
		"attempt", attempts,
		"retry_after", delay.Seconds(),
		"error_type", typeName(failures[0]),
	)
	return true, nil
}

func (r *ForwardJobRunner) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

func (r *ForwardJobRunner) logCompletion(jobs []ForwardJob, attempts int, report ProcessingReport) {
	attrs := []any{
		"feature", "forwarder",
		"operation", string(jobs[0].Kind),
		"attempt", attempts,
	}
	switch concrete := report.(type) {
	case ForwardingReport:
		attrs = append(attrs,
			"matched_routes", concrete.MatchedRoutes,
			"delivered_messages", concrete.DeliveredMessages(),
			"deduplicated_messages", concrete.DeduplicatedMessages(),
		)
	case SyncReport:
		attrs = append(attrs, "synchronized", concrete.Synchronized)
	}
	r.log.Info("forwarder job completed", attrs...)
}

func allPermanent(errs []error) bool {
	if len(errs) == 0 {
		return false
	}
	for _, err := range errs {
		if !isPermanent(err) {
			return false
		}
	}
	return true
}

func isPermanent(err error) bool {
	var mismatch DeliveryResultMismatch
	var missing MessageNotFound
	var unsupported NativeForwardUnsupported
	var partial PartialDeliveryState
	var permanent PermanentDeliveryError
	return errors.As(err, &mismatch) ||
		errors.As(err, &missing) ||
		errors.As(err, &unsupported) ||
		errors.As(err, &partial) ||
		errors.As(err, &permanent)
}

func retryDelay(errs []error, attempts int, base, limit time.Duration) time.Duration {
	var requested time.Duration
	found := false
	for _, err := range errs {
		var retry RetryAfter
		if errors.As(err, &retry) && (!found || retry.Delay > requested) {
			requested = retry.Delay
			found = true
		}
	}
	if found {
		return requested
	}
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	delay := base
	for i := 0; i < shift; i++ {
		if delay >= limit || delay > delay*2 {
			return limit
		}
		delay *= 2
	}
	if delay > limit {
		return limit
	}
	return delay
}

func errorSummary(errs []error) string {
	parts := make([]string, len(errs))
	for i, err := range errs {
		parts[i] = typeName(err) + ": " + err.Error()
	}
	summary := strings.Join(parts, "; ")
	runes := []rune(summary)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return summary
}

func loggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return logger
}
