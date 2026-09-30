package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/storage/dbsql"
)

// Pool begins transactions and serves sqlc. *pgxpool.Pool and *database.DB both qualify.
type Pool interface {
	dbsql.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Repository is the PostgreSQL adapter for every forwarder repository port.
type Repository struct {
	pool Pool
	q    *dbsql.Queries
}

// NewRepository binds the repositories to a pool.
func NewRepository(pool Pool) *Repository {
	return &Repository{pool: pool, q: dbsql.New(pool)}
}

func (r *Repository) ListForSourceChat(ctx context.Context, chatID int64) ([]forwarder.Route, error) {
	rows, err := r.q.ListRoutesForSourceChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	return routesFrom(rows)
}

func (r *Repository) ListAll(ctx context.Context) ([]forwarder.Route, error) {
	rows, err := r.q.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	return routesFrom(rows)
}

func (r *Repository) Add(ctx context.Context, route forwarder.Route) error {
	return r.tx(ctx, func(q *dbsql.Queries) error {
		rows, err := q.ListRoutes(ctx)
		if err != nil {
			return err
		}
		existing, err := routesFrom(rows)
		if err != nil {
			return err
		}
		for _, item := range existing {
			if item.ID == route.ID {
				return forwarder.NewValueError("route " + strconv.Itoa(route.ID) + " already exists")
			}
		}
		if err := forwarder.AssertAcyclicRoutes(append(existing, route)); err != nil {
			return err
		}
		params, err := routeParams(route)
		if err != nil {
			return err
		}
		if _, err := q.InsertRouteWithID(ctx, withRouteID(route.ID, params)); err != nil {
			return err
		}
		_, err = q.SyncRouteIDSequence(ctx)
		return err
	})
}

func (r *Repository) AddAuto(ctx context.Context, draft forwarder.RouteDraft) (forwarder.Route, error) {
	var created forwarder.Route
	err := r.tx(ctx, func(q *dbsql.Queries) error {
		rows, err := q.ListRoutes(ctx)
		if err != nil {
			return err
		}
		existing, err := routesFrom(rows)
		if err != nil {
			return err
		}
		probe, err := draft.Bind(1)
		if err != nil {
			return err
		}
		if err := forwarder.AssertAcyclicRoutes(append(existing, probe)); err != nil {
			return err
		}
		params, err := routeParams(probe)
		if err != nil {
			return err
		}
		id, err := q.InsertRoute(ctx, params)
		if err != nil {
			return err
		}
		routeID, err := fitInt(id, "route id")
		if err != nil {
			return err
		}
		created, err = draft.Bind(routeID)
		return err
	})
	return created, err
}

func (r *Repository) Replace(ctx context.Context, route forwarder.Route) error {
	return r.tx(ctx, func(q *dbsql.Queries) error {
		rows, err := q.ListRoutes(ctx)
		if err != nil {
			return err
		}
		existing, err := routesFrom(rows)
		if err != nil {
			return err
		}
		found := false
		proposed := make([]forwarder.Route, 0, len(existing))
		for _, item := range existing {
			if item.ID == route.ID {
				found = true
				proposed = append(proposed, route)
				continue
			}
			proposed = append(proposed, item)
		}
		if !found {
			return forwarder.KeyError{ID: route.ID}
		}
		if err := forwarder.AssertAcyclicRoutes(proposed); err != nil {
			return err
		}
		params, err := routeParams(route)
		if err != nil {
			return err
		}
		updated, err := q.UpdateRoute(ctx, updateParams(route, params))
		if err != nil {
			return err
		}
		if updated == 0 {
			return forwarder.KeyError{ID: route.ID}
		}
		return nil
	})
}

func (r *Repository) Remove(ctx context.Context, routeID int) (bool, error) {
	deleted, err := r.q.DeleteRoute(ctx, int64(routeID))
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

func (r *Repository) SaveMany(ctx context.Context, links []forwarder.MessageLink) error {
	return r.tx(ctx, func(q *dbsql.Queries) error {
		for _, link := range links {
			if err := q.UpsertMessageLink(ctx, dbsql.UpsertMessageLinkParams{
				RouteID:              int64(link.RouteID),
				SourceChatID:         link.Source.ChatID,
				SourceMessageID:      int64(link.Source.MessageID),
				DestinationChatID:    link.Destination.ChatID,
				DestinationMessageID: int64(link.Destination.MessageID),
				DeliveryMode:         string(link.DeliveryMode),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) Get(ctx context.Context, routeID int, source forwarder.MessageRef) (forwarder.MessageLink, bool, error) {
	row, err := r.q.GetMessageLink(ctx, dbsql.GetMessageLinkParams{
		RouteID:         int64(routeID),
		SourceChatID:    source.ChatID,
		SourceMessageID: int64(source.MessageID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return forwarder.MessageLink{}, false, nil
	}
	if err != nil {
		return forwarder.MessageLink{}, false, err
	}
	link, err := linkFrom(row.RouteID, row.SourceChatID, row.SourceMessageID, row.DestinationChatID, row.DestinationMessageID, row.DeliveryMode)
	return link, err == nil, err
}

func (r *Repository) FindAll(ctx context.Context, source forwarder.MessageRef) ([]forwarder.MessageLink, error) {
	rows, err := r.q.FindLinksBySource(ctx, dbsql.FindLinksBySourceParams{
		SourceChatID:    source.ChatID,
		SourceMessageID: int64(source.MessageID),
	})
	if err != nil {
		return nil, err
	}
	return linksFromRows(len(rows), func(i int) (int64, int64, int64, int64, int64, string) {
		row := rows[i]
		return row.RouteID, row.SourceChatID, row.SourceMessageID, row.DestinationChatID, row.DestinationMessageID, row.DeliveryMode
	})
}

func (r *Repository) FindBySourceMessageID(ctx context.Context, messageID int) ([]forwarder.MessageLink, error) {
	rows, err := r.q.FindLinksByMessageID(ctx, int64(messageID))
	if err != nil {
		return nil, err
	}
	return linksFromRows(len(rows), func(i int) (int64, int64, int64, int64, int64, string) {
		row := rows[i]
		return row.RouteID, row.SourceChatID, row.SourceMessageID, row.DestinationChatID, row.DestinationMessageID, row.DeliveryMode
	})
}

func (r *Repository) RemoveLink(ctx context.Context, link forwarder.MessageLink) error {
	return r.q.DeleteMessageLink(ctx, dbsql.DeleteMessageLinkParams{
		RouteID:         int64(link.RouteID),
		SourceChatID:    link.Source.ChatID,
		SourceMessageID: int64(link.Source.MessageID),
	})
}

// Remove is the message-link method. Route removal is RemoveRoute via the route methods above.
// The interface method name Remove is shared, so this repository cannot implement both
// with one method. See Routes and Links wrappers.

func (r *Repository) GetTopic(ctx context.Context, sourceChatID int64, sourceTopicID *int, destinationChatID int64) (forwarder.ManagedTopic, bool, error) {
	stored := int64(0)
	if sourceTopicID != nil {
		stored = int64(forwarder.NormalizeGeneralTopic(sourceTopicID))
	}
	row, err := r.q.GetManagedTopic(ctx, dbsql.GetManagedTopicParams{
		SourceChatID:      sourceChatID,
		SourceTopicID:     stored,
		DestinationChatID: destinationChatID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return forwarder.ManagedTopic{}, false, nil
	}
	if err != nil {
		return forwarder.ManagedTopic{}, false, err
	}
	topic, err := topicFromRow(row)
	return topic, err == nil, err
}

func (r *Repository) SaveTopic(ctx context.Context, topic forwarder.ManagedTopic) error {
	sourceTopic := int64(0)
	if topic.HasSourceTopic {
		sourceTopic = int64(topic.SourceTopicID)
	}
	return r.q.UpsertManagedTopic(ctx, dbsql.UpsertManagedTopicParams{
		SourceChatID:      topic.SourceChatID,
		SourceTopicID:     sourceTopic,
		DestinationChatID: topic.DestinationChatID,
		TopicID:           int64(topic.TopicID),
		Title:             topic.Title,
	})
}

func (r *Repository) GetCursor(ctx context.Context, sourceChatID int64) (forwarder.PollCursor, bool, error) {
	row, err := r.q.GetPollCursor(ctx, sourceChatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return forwarder.PollCursor{}, false, nil
	}
	if err != nil {
		return forwarder.PollCursor{}, false, err
	}
	cursor, err := forwarder.NewPollCursor(row.SourceChatID, int(row.LastMessageID))
	return cursor, err == nil, err
}

func (r *Repository) SaveCursor(ctx context.Context, cursor forwarder.PollCursor) error {
	return r.q.UpsertPollCursor(ctx, dbsql.UpsertPollCursorParams{
		SourceChatID:  cursor.SourceChatID,
		LastMessageID: int64(cursor.LastMessageID),
	})
}

func (r *Repository) GetMany(ctx context.Context, chatIDs []int64) ([]forwarder.ChatAccess, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListChatAccess(ctx, chatIDs)
	if err != nil {
		return nil, err
	}
	out := make([]forwarder.ChatAccess, 0, len(rows))
	for _, row := range rows {
		out = append(out, forwarder.ChatAccess{
			ChatID:     row.ChatID,
			Title:      derefString(row.Title),
			Username:   derefString(row.Username),
			InviteLink: derefString(row.InviteLink),
		})
	}
	return out, nil
}

func (r *Repository) SaveAccess(ctx context.Context, access forwarder.ChatAccess) error {
	return r.q.UpsertChatAccess(ctx, dbsql.UpsertChatAccessParams{
		ChatID:     access.ChatID,
		Title:      optString(access.Title),
		Username:   optString(access.Username),
		InviteLink: optString(access.InviteLink),
	})
}

func (r *Repository) Enqueue(ctx context.Context, jobs []forwarder.PendingForwardJob) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}
	inserted := 0
	err := r.tx(ctx, func(q *dbsql.Queries) error {
		for _, job := range jobs {
			payload, err := encodeEvent(job.Event)
			if err != nil {
				return err
			}
			count, err := q.InsertJob(ctx, dbsql.InsertJobParams{
				Kind:             string(job.Kind),
				DeduplicationKey: job.DeduplicationKey,
				GroupKey:         job.GroupKey,
				PayloadJson:      payload,
				AvailableAt:      timestamptz(job.AvailableAt),
			})
			if err != nil {
				return err
			}
			inserted += int(count)
			if job.GroupKey != nil {
				if err := q.SlidePendingGroup(ctx, dbsql.SlidePendingGroupParams{
					AvailableAt: timestamptz(job.AvailableAt),
					GroupKey:    job.GroupKey,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return inserted, err
}

func (r *Repository) RecoverIncomplete(ctx context.Context) (int, error) {
	count, err := r.q.RecoverProcessingJobs(ctx)
	return int(count), err
}

func (r *Repository) ClaimDue(ctx context.Context, now time.Time) ([]forwarder.ForwardJob, error) {
	var claimed []forwarder.ForwardJob
	err := r.tx(ctx, func(q *dbsql.Queries) error {
		head, err := q.SelectHeadPendingJob(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !head.AvailableAt.Valid || head.AvailableAt.Time.After(now) {
			return nil
		}
		selected := []dbsql.SelectHeadPendingJobRow{head}
		if head.GroupKey != nil {
			group, err := q.SelectDueGroupJobs(ctx, dbsql.SelectDueGroupJobsParams{
				GroupKey:    head.GroupKey,
				AvailableAt: timestamptz(now),
			})
			if err != nil {
				return err
			}
			selected = selected[:0]
			for _, row := range group {
				selected = append(selected, dbsql.SelectHeadPendingJobRow(row))
			}
		}
		if len(selected) == 0 {
			return nil
		}
		ids := make([]int64, len(selected))
		for i, row := range selected {
			ids[i] = row.ID
		}
		if err := q.MarkJobsProcessing(ctx, ids); err != nil {
			return err
		}
		claimed = make([]forwarder.ForwardJob, 0, len(selected))
		for _, row := range selected {
			event, err := decodeEvent(forwarder.ForwardJobKind(row.Kind), row.PayloadJson)
			if err != nil {
				return err
			}
			id, err := fitInt(row.ID, "job id")
			if err != nil {
				return err
			}
			job, err := forwarder.NewForwardJob(id, forwarder.ForwardJobKind(row.Kind), event, int(row.Attempts)+1, row.GroupKey)
			if err != nil {
				return err
			}
			claimed = append(claimed, job)
		}
		return nil
	})
	return claimed, err
}

func (r *Repository) MarkSucceeded(ctx context.Context, jobIDs []int) error {
	if len(jobIDs) == 0 {
		return nil
	}
	return r.q.DeleteProcessingJobs(ctx, int64s(jobIDs))
}

func (r *Repository) MarkFailed(ctx context.Context, jobIDs []int, failure string) error {
	if len(jobIDs) == 0 {
		return nil
	}
	return r.q.FailProcessingJobs(ctx, dbsql.FailProcessingJobsParams{Column1: int64s(jobIDs), LastError: &failure})
}

func (r *Repository) Reschedule(ctx context.Context, jobIDs []int, availableAt time.Time, failure string) error {
	if len(jobIDs) == 0 {
		return nil
	}
	return r.q.RescheduleProcessingJobs(ctx, dbsql.RescheduleProcessingJobsParams{
		Column1:     int64s(jobIDs),
		AvailableAt: timestamptz(availableAt),
		LastError:   &failure,
	})
}

func (r *Repository) tx(ctx context.Context, fn func(*dbsql.Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(r.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func routesFrom(rows []dbsql.ForwarderRoute) ([]forwarder.Route, error) {
	out := make([]forwarder.Route, 0, len(rows))
	for _, row := range rows {
		route, err := routeFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, nil
}

func linkFrom(routeID, sourceChat, sourceMessage, destChat, destMessage int64, mode string) (forwarder.MessageLink, error) {
	source, err := contracts.NewMessageRef(sourceChat, int(sourceMessage))
	if err != nil {
		return forwarder.MessageLink{}, err
	}
	destination, err := contracts.NewMessageRef(destChat, int(destMessage))
	if err != nil {
		return forwarder.MessageLink{}, err
	}
	id, err := fitInt(routeID, "route id")
	if err != nil {
		return forwarder.MessageLink{}, err
	}
	return forwarder.NewMessageLink(id, source, destination, forwarder.ForwardMode(mode))
}

func linksFromRows(n int, at func(int) (int64, int64, int64, int64, int64, string)) ([]forwarder.MessageLink, error) {
	out := make([]forwarder.MessageLink, 0, n)
	for i := 0; i < n; i++ {
		routeID, sourceChat, sourceMessage, destChat, destMessage, mode := at(i)
		link, err := linkFrom(routeID, sourceChat, sourceMessage, destChat, destMessage, mode)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, nil
}

func topicFromRow(row dbsql.GetManagedTopicRow) (forwarder.ManagedTopic, error) {
	var sourceTopic *int
	if row.SourceTopicID != 0 {
		value := int(row.SourceTopicID)
		sourceTopic = &value
	}
	return forwarder.NewManagedTopic(row.SourceChatID, row.DestinationChatID, int(row.TopicID), row.Title, sourceTopic)
}

func int64s(values []int) []int64 {
	out := make([]int64, len(values))
	for i, value := range values {
		out[i] = int64(value)
	}
	return out
}
