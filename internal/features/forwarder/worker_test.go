package forwarder

import (
	"context"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/contracts"
)

type scriptRepo struct {
	jobs            []ForwardJob
	succeeded       []int
	failedIDs       []int
	failedText      string
	rescheduledIDs  []int
	rescheduledAt   time.Time
	rescheduledText string
}

func (r *scriptRepo) Enqueue(context.Context, []PendingForwardJob) (int, error) { return 0, nil }
func (r *scriptRepo) RecoverIncomplete(context.Context) (int, error)            { return 0, nil }
func (r *scriptRepo) ClaimDue(context.Context, time.Time) ([]ForwardJob, error) {
	jobs := r.jobs
	r.jobs = nil
	return jobs, nil
}
func (r *scriptRepo) MarkSucceeded(ctx context.Context, jobIDs []int) error {
	r.succeeded = append([]int(nil), jobIDs...)
	return nil
}
func (r *scriptRepo) MarkFailed(ctx context.Context, jobIDs []int, failure string) error {
	r.failedIDs = append([]int(nil), jobIDs...)
	r.failedText = failure
	return nil
}
func (r *scriptRepo) Reschedule(ctx context.Context, jobIDs []int, availableAt time.Time, failure string) error {
	r.rescheduledIDs = append([]int(nil), jobIDs...)
	r.rescheduledAt = availableAt
	r.rescheduledText = failure
	return nil
}

type scriptProc struct {
	report ProcessingReport
	err    error
}

func (p scriptProc) Execute(context.Context, []ForwardJob) (ProcessingReport, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.report, nil
}

func TestRunnerMarksSuccessRetryAndPermanentFailure(t *testing.T) {
	ctx := context.Background()
	clock := func() time.Time { return time.Unix(100, 0).UTC() }
	successRepo := &scriptRepo{jobs: []ForwardJob{mustJob(1, 10, 1), mustJob(2, 11, 1)}}
	runner, err := NewForwardJobRunner(successRepo, scriptProc{report: ForwardingReport{}}, ForwardJobRunnerConfig{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := runner.ProcessOnce(ctx)
	if err != nil || !processed || len(successRepo.succeeded) != 2 || successRepo.failedText != "" || !successRepo.rescheduledAt.IsZero() {
		t.Fatalf("processed %v err %v repo %#v", processed, err, successRepo)
	}

	retryRepo := &scriptRepo{jobs: []ForwardJob{mustJob(1, 10, 2)}}
	retry, err := NewRetryAfter(120 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runner, err = NewForwardJobRunner(retryRepo, scriptProc{err: retry}, ForwardJobRunnerConfig{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(retryRepo.rescheduledIDs) != 1 || !retryRepo.rescheduledAt.Equal(time.Unix(220, 0).UTC()) || !contains(retryRepo.rescheduledText, "RetryAfter") {
		t.Fatalf("retry %#v", retryRepo)
	}

	permanentRepo := &scriptRepo{jobs: []ForwardJob{mustJob(1, 10, 1)}}
	runner, err = NewForwardJobRunner(permanentRepo, scriptProc{err: NewPermanentDeliveryError("forbidden")}, ForwardJobRunnerConfig{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(permanentRepo.failedIDs) != 1 || !permanentRepo.rescheduledAt.IsZero() {
		t.Fatalf("permanent %#v", permanentRepo)
	}

	source := MessageRef{ChatID: -1001, MessageID: 10}
	mixedRepo := &scriptRepo{jobs: []ForwardJob{mustJob(1, 10, 1)}}
	mixed, err := NewRetryAfter(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	report := ForwardingReport{Failures: []DeliveryFailure{
		{RouteID: 1, Sources: []MessageRef{source}, Error: NewPermanentDeliveryError("forbidden")},
		{RouteID: 2, Sources: []MessageRef{source}, Error: mixed},
	}}
	runner, err = NewForwardJobRunner(mixedRepo, scriptProc{report: report}, ForwardJobRunnerConfig{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mixedRepo.rescheduledIDs) != 1 || !mixedRepo.rescheduledAt.Equal(time.Unix(105, 0).UTC()) || mixedRepo.failedText != "" {
		t.Fatalf("mixed %#v", mixedRepo)
	}
}

type recordForwarder struct {
	album  []int
	edited int
}

func (r *recordForwarder) ForwardMessage(context.Context, IncomingMessage) (ForwardingReport, error) {
	return ForwardingReport{}, nil
}
func (r *recordForwarder) ForwardAlbum(ctx context.Context, messages []IncomingMessage) (ForwardingReport, error) {
	for _, message := range messages {
		r.album = append(r.album, message.Ref.MessageID)
	}
	return ForwardingReport{}, nil
}
func (r *recordForwarder) SynchronizeEdit(ctx context.Context, message IncomingMessage) (SyncReport, error) {
	r.edited = message.Ref.MessageID
	return SyncReport{Operation: SyncEdit}, nil
}
func (r *recordForwarder) SynchronizeDelete(context.Context, MessagesDeleted) (SyncReport, error) {
	return SyncReport{Operation: SyncDelete}, nil
}

func TestProcessorDispatchesAlbumAndEdit(t *testing.T) {
	service := &recordForwarder{}
	processor := NewForwardJobProcessor(service)
	if _, err := processor.Execute(context.Background(), []ForwardJob{mustJob(1, 10, 1), mustJob(2, 11, 1)}); err != nil {
		t.Fatal(err)
	}
	edit := mustJob(3, 12, 1)
	edit.Kind = ForwardJobEdit
	edit.Event = contracts.TelegramMessageEdited{Message: textMessage(12, "edited")}
	if _, err := processor.Execute(context.Background(), []ForwardJob{edit}); err != nil {
		t.Fatal(err)
	}
	if len(service.album) != 2 || service.album[0] != 10 || service.album[1] != 11 || service.edited != 12 {
		t.Fatalf("service %#v", service)
	}
}

func mustJob(id, messageID, attempts int) ForwardJob {
	job, err := NewForwardJob(id, ForwardJobReceive, contracts.TelegramMessageReceived{Message: textMessage(messageID, "hello")}, attempts, nil)
	if err != nil {
		panic(err)
	}
	return job
}

func contains(value, part string) bool {
	return len(value) >= len(part) && (value == part || len(part) == 0 || (len(value) > 0 && (func() bool {
		for i := 0; i+len(part) <= len(value); i++ {
			if value[i:i+len(part)] == part {
				return true
			}
		}
		return false
	})()))
}
