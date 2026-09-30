-- name: ListSummaryRules :many
SELECT id, source_chat_id, source_topic_id, source_username,
       destination_chat_id, destination_topic_id, destination_username,
       window_seconds, enabled
FROM summarizer_rules
ORDER BY id;

-- name: InsertSummaryRule :one
INSERT INTO summarizer_rules (
    source_chat_id, source_topic_id, source_username,
    destination_chat_id, destination_topic_id, destination_username,
    window_seconds, enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: UpdateSummaryRule :execrows
UPDATE summarizer_rules
SET source_chat_id = $2,
    source_topic_id = $3,
    source_username = $4,
    destination_chat_id = $5,
    destination_topic_id = $6,
    destination_username = $7,
    window_seconds = $8,
    enabled = $9,
    updated_at = now()
WHERE id = $1;

-- name: DeleteSummaryRule :execrows
DELETE FROM summarizer_rules WHERE id = $1;

-- name: InsertSummaryRun :exec
INSERT INTO summarizer_runs (
    rule_id, started_at, completed_at, first_message_id, last_message_id,
    message_count, provider, model, prompt_version, output_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetSummaryModelConfig :one
SELECT provider, model, api_key, base_url, input_token_limit, output_token_limit,
       temperature, timeout, max_retries, prompt_preset, custom_prompt, max_concurrency
FROM summarizer_model_config
WHERE id = 1;

-- name: UpsertSummaryModelConfig :exec
INSERT INTO summarizer_model_config (
    id, provider, model, api_key, base_url, input_token_limit, output_token_limit,
    temperature, timeout, max_retries, prompt_preset, custom_prompt, max_concurrency
) VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
    provider = EXCLUDED.provider,
    model = EXCLUDED.model,
    api_key = EXCLUDED.api_key,
    base_url = EXCLUDED.base_url,
    input_token_limit = EXCLUDED.input_token_limit,
    output_token_limit = EXCLUDED.output_token_limit,
    temperature = EXCLUDED.temperature,
    timeout = EXCLUDED.timeout,
    max_retries = EXCLUDED.max_retries,
    prompt_preset = EXCLUDED.prompt_preset,
    custom_prompt = EXCLUDED.custom_prompt,
    max_concurrency = EXCLUDED.max_concurrency,
    updated_at = now();

-- name: ClearSummaryModelConfig :execrows
DELETE FROM summarizer_model_config WHERE id = 1;
