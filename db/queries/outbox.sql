-- name: GetOutboxEvent :one
SELECT id, case_id, analysis_id, event_type, payload, status, attempt_count,
       published_at, last_error, created_at, updated_at
FROM outbox_events
WHERE id = $1;

-- name: ListPendingOutboxEventsForUpdate :many
SELECT id, case_id, analysis_id, event_type, payload, status, attempt_count,
       published_at, last_error, created_at, updated_at
FROM outbox_events
WHERE status = 'PENDING'
ORDER BY created_at, id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: CreateOutboxEvent :one
INSERT INTO outbox_events (id, case_id, analysis_id, event_type, payload)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, case_id, analysis_id, event_type, payload, status, attempt_count,
          published_at, last_error, created_at, updated_at;

-- name: MarkOutboxEventPublished :one
UPDATE outbox_events
SET status = 'PUBLISHED', published_at = $2, updated_at = now(), last_error = NULL
WHERE id = $1
RETURNING id, case_id, analysis_id, event_type, payload, status, attempt_count,
          published_at, last_error, created_at, updated_at;

-- name: RecordOutboxAttempt :one
UPDATE outbox_events
SET attempt_count = attempt_count + 1, last_error = $2, updated_at = now()
WHERE id = $1
RETURNING id, case_id, analysis_id, event_type, payload, status, attempt_count,
          published_at, last_error, created_at, updated_at;
