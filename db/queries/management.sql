-- name: IsAdmin :one
SELECT EXISTS(SELECT 1 FROM management_admins WHERE user_id = $1) AS is_admin;

-- name: ListAdmins :many
SELECT user_id, granted_by FROM management_admins ORDER BY user_id;

-- name: AddAdmin :exec
INSERT INTO management_admins (user_id, granted_by)
VALUES ($1, $2)
ON CONFLICT (user_id) DO NOTHING;

-- name: RemoveAdmin :exec
DELETE FROM management_admins WHERE user_id = $1;

-- name: GetModuleEnabled :one
SELECT enabled FROM management_modules WHERE name = $1;

-- name: SetModuleEnabled :exec
INSERT INTO management_modules (name, enabled)
VALUES ($1, $2)
ON CONFLICT (name) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now();

-- name: IsCommandProcessed :one
SELECT EXISTS(
    SELECT 1 FROM management_command_receipts
    WHERE account_id = $1 AND chat_id = $2 AND message_id = $3
) AS processed;

-- name: MarkCommandProcessed :exec
INSERT INTO management_command_receipts (account_id, chat_id, message_id)
VALUES ($1, $2, $3)
ON CONFLICT (account_id, chat_id, message_id) DO NOTHING;
