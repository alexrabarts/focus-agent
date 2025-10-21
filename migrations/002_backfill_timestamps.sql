-- Backfill missing created_at timestamps
-- For tasks with NULL or 0 created_at, use updated_at if available, otherwise use current time

UPDATE tasks
SET created_at = COALESCE(
    NULLIF(created_at, 0),  -- Use existing created_at if not 0
    updated_at,              -- Fall back to updated_at
    CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)  -- Fall back to current time
)
WHERE created_at IS NULL OR created_at = 0;

-- Backfill missing updated_at timestamps
UPDATE tasks
SET updated_at = COALESCE(
    NULLIF(updated_at, 0),  -- Use existing updated_at if not 0
    created_at,              -- Fall back to created_at
    CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)  -- Fall back to current time
)
WHERE updated_at IS NULL OR updated_at = 0;
