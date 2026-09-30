-- name: ListRoutesForSourceChat :many
SELECT id, source_chat_id, source_topic_id, destination_chat_id, destination_topic_id,
       mode, filter_json, enabled, fallback_to_copy, source_username, destination_username,
       poll_interval_seconds
FROM forwarder_routes
WHERE source_chat_id = $1
ORDER BY id;

-- name: ListRoutes :many
SELECT id, source_chat_id, source_topic_id, destination_chat_id, destination_topic_id,
       mode, filter_json, enabled, fallback_to_copy, source_username, destination_username,
       poll_interval_seconds
FROM forwarder_routes
ORDER BY id;

-- name: InsertRoute :one
INSERT INTO forwarder_routes (
    source_chat_id, source_topic_id, destination_chat_id, destination_topic_id,
    mode, filter_json, enabled, fallback_to_copy, source_username, destination_username,
    poll_interval_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id;

-- name: InsertRouteWithID :one
INSERT INTO forwarder_routes (
    id, source_chat_id, source_topic_id, destination_chat_id, destination_topic_id,
    mode, filter_json, enabled, fallback_to_copy, source_username, destination_username,
    poll_interval_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

-- name: SyncRouteIDSequence :one
SELECT setval(pg_get_serial_sequence('forwarder_routes', 'id'),
              (SELECT COALESCE(MAX(id), 1) FROM forwarder_routes));

-- name: UpdateRoute :execrows
UPDATE forwarder_routes
SET source_chat_id = $2,
    source_topic_id = $3,
    destination_chat_id = $4,
    destination_topic_id = $5,
    mode = $6,
    filter_json = $7,
    enabled = $8,
    fallback_to_copy = $9,
    source_username = $10,
    destination_username = $11,
    poll_interval_seconds = $12
WHERE id = $1;

-- name: DeleteRoute :execrows
DELETE FROM forwarder_routes WHERE id = $1;

-- name: UpsertMessageLink :exec
INSERT INTO forwarder_message_links (
    route_id, source_chat_id, source_message_id, destination_chat_id, destination_message_id, delivery_mode
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (route_id, source_chat_id, source_message_id) DO UPDATE
SET destination_chat_id = EXCLUDED.destination_chat_id,
    destination_message_id = EXCLUDED.destination_message_id,
    delivery_mode = EXCLUDED.delivery_mode;

-- name: GetMessageLink :one
SELECT route_id, source_chat_id, source_message_id, destination_chat_id, destination_message_id, delivery_mode
FROM forwarder_message_links
WHERE route_id = $1 AND source_chat_id = $2 AND source_message_id = $3;

-- name: FindLinksBySource :many
SELECT route_id, source_chat_id, source_message_id, destination_chat_id, destination_message_id, delivery_mode
FROM forwarder_message_links
WHERE source_chat_id = $1 AND source_message_id = $2
ORDER BY route_id;

-- name: FindLinksByMessageID :many
SELECT route_id, source_chat_id, source_message_id, destination_chat_id, destination_message_id, delivery_mode
FROM forwarder_message_links
WHERE source_message_id = $1
ORDER BY route_id, source_chat_id;

-- name: DeleteMessageLink :exec
DELETE FROM forwarder_message_links
WHERE route_id = $1 AND source_chat_id = $2 AND source_message_id = $3;

-- name: GetManagedTopic :one
SELECT source_chat_id, source_topic_id, destination_chat_id, topic_id, title
FROM forwarder_managed_topics
WHERE source_chat_id = $1 AND source_topic_id = $2 AND destination_chat_id = $3;

-- name: UpsertManagedTopic :exec
INSERT INTO forwarder_managed_topics (source_chat_id, source_topic_id, destination_chat_id, topic_id, title)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (source_chat_id, source_topic_id, destination_chat_id) DO UPDATE
SET topic_id = EXCLUDED.topic_id, title = EXCLUDED.title, updated_at = now();

-- name: GetPollCursor :one
SELECT source_chat_id, last_message_id FROM forwarder_poll_cursors WHERE source_chat_id = $1;

-- name: UpsertPollCursor :exec
INSERT INTO forwarder_poll_cursors (source_chat_id, last_message_id)
VALUES ($1, $2)
ON CONFLICT (source_chat_id) DO UPDATE
SET last_message_id = GREATEST(forwarder_poll_cursors.last_message_id, EXCLUDED.last_message_id),
    updated_at = now();

-- name: ListChatAccess :many
SELECT chat_id, title, username, invite_link
FROM forwarder_chat_access
WHERE chat_id = ANY($1::bigint[])
ORDER BY chat_id;

-- name: UpsertChatAccess :exec
INSERT INTO forwarder_chat_access (chat_id, title, username, invite_link)
VALUES ($1, $2, $3, $4)
ON CONFLICT (chat_id) DO UPDATE
SET title = COALESCE(EXCLUDED.title, forwarder_chat_access.title),
    username = EXCLUDED.username,
    invite_link = EXCLUDED.invite_link,
    updated_at = now();

-- name: InsertChatAccessIgnore :exec
INSERT INTO forwarder_chat_access (chat_id, username, invite_link)
VALUES ($1, $2, $3)
ON CONFLICT (chat_id) DO NOTHING;

-- name: DeleteChatAccess :exec
DELETE FROM forwarder_chat_access WHERE chat_id = $1;

-- name: CountRouteChatRefs :one
SELECT COUNT(*)::bigint
FROM forwarder_routes
WHERE source_chat_id = $1 OR destination_chat_id = $1;

-- name: InsertJob :execrows
INSERT INTO forwarder_jobs (kind, deduplication_key, group_key, payload_json, available_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (deduplication_key) DO NOTHING;

-- name: SlidePendingGroup :exec
UPDATE forwarder_jobs
SET available_at = $1, updated_at = now()
WHERE group_key = $2 AND state = 'pending';

-- name: RecoverProcessingJobs :execrows
UPDATE forwarder_jobs
SET state = 'pending', updated_at = now()
WHERE state = 'processing';

-- name: SelectHeadPendingJob :one
SELECT id, kind, group_key, payload_json, attempts, available_at
FROM forwarder_jobs
WHERE state = 'pending'
ORDER BY id
LIMIT 1
FOR UPDATE;

-- name: SelectDueGroupJobs :many
SELECT id, kind, group_key, payload_json, attempts, available_at
FROM forwarder_jobs
WHERE state = 'pending' AND group_key = $1 AND available_at <= $2
ORDER BY id
FOR UPDATE;

-- name: MarkJobsProcessing :exec
UPDATE forwarder_jobs
SET state = 'processing', attempts = attempts + 1, updated_at = now()
WHERE id = ANY($1::bigint[]) AND state = 'pending';

-- name: DeleteProcessingJobs :exec
DELETE FROM forwarder_jobs
WHERE id = ANY($1::bigint[]) AND state = 'processing';

-- name: FailProcessingJobs :exec
UPDATE forwarder_jobs
SET state = 'failed', last_error = $2, updated_at = now()
WHERE id = ANY($1::bigint[]) AND state = 'processing';

-- name: RescheduleProcessingJobs :exec
UPDATE forwarder_jobs
SET state = 'pending', available_at = $2, last_error = $3, updated_at = now()
WHERE id = ANY($1::bigint[]) AND state = 'processing';
