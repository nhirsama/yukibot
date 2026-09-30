package store

import (
	"context"
	"time"

	"github.com/nhirsama/yukibot/internal/features/forwarder"
)

// Routes is the route repository port.
type Routes struct{ *Repository }

// Links is the message-link repository port.
type Links struct{ *Repository }

// Topics is the managed-topic repository port.
type Topics struct{ *Repository }

// Cursors is the poll-cursor repository port.
type Cursors struct{ *Repository }

// Access is the chat-access repository port.
type Access struct{ *Repository }

// Jobs is the durable job repository port.
type Jobs struct{ *Repository }

var (
	_ forwarder.RouteRepository         = Routes{}
	_ forwarder.MessageLinkRepository   = Links{}
	_ forwarder.ManagedTopicRepository  = Topics{}
	_ forwarder.PollCursorRepository    = Cursors{}
	_ forwarder.ChatAccessRepository    = Access{}
	_ forwarder.ForwardJobRepository    = Jobs{}
)

// Remove deletes one message link.
func (l Links) Remove(ctx context.Context, link forwarder.MessageLink) error {
	return l.Repository.RemoveLink(ctx, link)
}

// Get loads one managed topic.
func (t Topics) Get(ctx context.Context, sourceChatID int64, sourceTopicID *int, destinationChatID int64) (forwarder.ManagedTopic, bool, error) {
	return t.Repository.GetTopic(ctx, sourceChatID, sourceTopicID, destinationChatID)
}

// Save upserts one managed topic.
func (t Topics) Save(ctx context.Context, topic forwarder.ManagedTopic) error {
	return t.Repository.SaveTopic(ctx, topic)
}

// Get loads one poll cursor.
func (c Cursors) Get(ctx context.Context, sourceChatID int64) (forwarder.PollCursor, bool, error) {
	return c.Repository.GetCursor(ctx, sourceChatID)
}

// Save advances one poll cursor.
func (c Cursors) Save(ctx context.Context, cursor forwarder.PollCursor) error {
	return c.Repository.SaveCursor(ctx, cursor)
}

// Save upserts chat access, keeping a stored title when the new title is blank.
func (a Access) Save(ctx context.Context, access forwarder.ChatAccess) error {
	return a.Repository.SaveAccess(ctx, access)
}

// Enqueue inserts jobs and slides pending album groups.
func (j Jobs) Enqueue(ctx context.Context, jobs []forwarder.PendingForwardJob) (int, error) {
	return j.Repository.Enqueue(ctx, jobs)
}

// RecoverIncomplete returns processing jobs to pending without resetting attempts.
func (j Jobs) RecoverIncomplete(ctx context.Context) (int, error) {
	return j.Repository.RecoverIncomplete(ctx)
}

// ClaimDue is head-of-line: a future head blocks later due jobs.
func (j Jobs) ClaimDue(ctx context.Context, now time.Time) ([]forwarder.ForwardJob, error) {
	return j.Repository.ClaimDue(ctx, now)
}

// MarkSucceeded deletes claimed jobs.
func (j Jobs) MarkSucceeded(ctx context.Context, jobIDs []int) error {
	return j.Repository.MarkSucceeded(ctx, jobIDs)
}

// MarkFailed keeps the rows and stores the error.
func (j Jobs) MarkFailed(ctx context.Context, jobIDs []int, failure string) error {
	return j.Repository.MarkFailed(ctx, jobIDs, failure)
}

// Reschedule returns claimed jobs to pending at availableAt.
func (j Jobs) Reschedule(ctx context.Context, jobIDs []int, availableAt time.Time, failure string) error {
	return j.Repository.Reschedule(ctx, jobIDs, availableAt, failure)
}
