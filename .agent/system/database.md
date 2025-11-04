# Database Documentation

## Database Engine

**Engine:** DuckDB (NOT SQLite)
**Version:** v1.4.1 (via `github.com/marcboeker/go-duckdb/v2`)
**Previous Version:** v1.1.3 (upgraded to fix UPDATE constraint errors on indexed columns)

## Database Locations

- **Local Development:** `~/.focus-agent/data.db`
- **Production:** `/srv/focus-agent/data.duckdb`

## Important Notes

### DuckDB vs SQLite

- **Use `duckdb` CLI to query DuckDB files, NOT `sqlite3`**
- DuckDB and SQLite are different database engines with incompatible file formats

### Single-Writer Limitation

**DuckDB is single-writer:**
- Only one process can write to the database at a time
- **Always stop API server before running migration scripts that write**
- Read operations can be concurrent

### Querying from Command Line

```bash
# Correct way to query DuckDB
duckdb ~/.focus-agent/data.db "SELECT * FROM tasks LIMIT 10"

# WRONG - will not work
sqlite3 ~/.focus-agent/data.db "SELECT * FROM tasks LIMIT 10"
```

## Schema Overview

### Core Tables

- **`messages`** - Email storage with FTS5 indexing
- **`threads`** - Conversation tracking with AI summaries
- **`tasks`** - Unified task management with scoring
- **`events`** - Calendar events
- **`docs`** - Google Drive documents
- **`llm_cache`** - AI response caching (24-hour TTL)
- **`usage`** - API usage tracking
- **`brief_tasks`** - Task mappings for daily briefs (24-hour validity)

### Task Schema

```sql
-- Key fields for task prioritization
CREATE TABLE tasks (
    id VARCHAR PRIMARY KEY,
    title VARCHAR NOT NULL,
    description TEXT,
    status VARCHAR DEFAULT 'pending',
    due_ts TIMESTAMP,
    impact INTEGER,           -- 1-5 scale
    urgency INTEGER,          -- 1-5 scale
    effort VARCHAR,           -- S/M/L
    stakeholder VARCHAR,
    project VARCHAR,
    score REAL,              -- Calculated priority score
    source VARCHAR,          -- 'gmail', 'google_tasks', etc.
    source_id VARCHAR,       -- Thread ID or external ID
    created_at BIGINT,
    updated_at BIGINT
);
```

## Migrations

Migrations are handled in code (not external tools):
- Always use `IF NOT EXISTS` / `IF EXISTS` for idempotency
- Located in: `internal/db/migrations.go`
- Run automatically on startup

## Backup Procedures

```bash
# Backup before schema changes
cp ~/.focus-agent/data.db ~/.focus-agent/data.db.backup-$(date +%Y%m%d-%H%M)

# Restore from backup
cp ~/.focus-agent/data.db.backup-[timestamp] ~/.focus-agent/data.db
```

## Common Queries

### Task Analysis

```sql
-- Check due date coverage
SELECT
  COUNT(*) as total,
  SUM(CASE WHEN due_ts IS NOT NULL THEN 1 ELSE 0 END) as with_due_date,
  ROUND(SUM(CASE WHEN due_ts IS NOT NULL THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 1) as pct
FROM tasks WHERE source = 'gmail';

-- Check score distribution
SELECT
  ROUND(score, 1) as score_bucket,
  COUNT(*) as count
FROM tasks
WHERE status = 'pending'
GROUP BY score_bucket
ORDER BY score_bucket DESC;

-- Find tasks without stakeholders
SELECT COUNT(*)
FROM tasks
WHERE stakeholder IS NULL OR stakeholder = '' OR stakeholder = 'N/A';
```

### Thread Processing

```sql
-- Queue: threads waiting for AI processing
SELECT DISTINCT t.id, m.subject, m.from_addr, m.ts
FROM threads t
JOIN messages m ON t.id = m.thread_id
WHERE t.summary IS NULL OR t.summary = ''
GROUP BY t.id
ORDER BY m.ts DESC
LIMIT 100;
```
