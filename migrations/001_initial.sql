-- Focus Agent Database Schema
-- DuckDB with FTS extension

-- Messages table for email storage
CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR PRIMARY KEY,
    thread_id VARCHAR NOT NULL,
    from_addr VARCHAR,
    to_addr VARCHAR,
    subject VARCHAR,
    snippet VARCHAR,
    body VARCHAR,
    ts BIGINT NOT NULL, -- Unix timestamp
    last_msg_id VARCHAR,
    labels VARCHAR, -- JSON array
    sensitivity VARCHAR, -- low, medium, high
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    updated_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)
);

CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
CREATE INDEX IF NOT EXISTS idx_messages_from ON messages(from_addr);

-- Threads table for conversation tracking
CREATE TABLE IF NOT EXISTS threads (
    id VARCHAR PRIMARY KEY,
    last_history_id VARCHAR,
    summary VARCHAR,
    summary_hash VARCHAR,
    task_count INTEGER DEFAULT 0,
    priority_score DOUBLE DEFAULT 0,
    relevant_to_user BOOLEAN DEFAULT false,
    next_followup_ts BIGINT,
    last_synced BIGINT,
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    updated_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)
);

CREATE INDEX IF NOT EXISTS idx_threads_followup ON threads(next_followup_ts);
CREATE INDEX IF NOT EXISTS idx_threads_priority ON threads(priority_score DESC);

-- Tasks table for unified task management
CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR PRIMARY KEY,
    source VARCHAR NOT NULL, -- gmail, gcal, gtasks, manual
    source_id VARCHAR, -- Original ID from source system
    title VARCHAR NOT NULL,
    description VARCHAR,
    due_ts BIGINT,
    project VARCHAR,
    impact INTEGER CHECK (impact >= 1 AND impact <= 5),
    urgency INTEGER CHECK (urgency >= 1 AND urgency <= 5),
    effort VARCHAR CHECK (effort IN ('S', 'M', 'L')),
    stakeholder VARCHAR,
    score DOUBLE,
    status VARCHAR DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed', 'cancelled')),
    metadata VARCHAR, -- JSON object for source-specific data
    matched_priorities VARCHAR, -- JSON string storing which priorities matched
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    updated_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    completed_at BIGINT
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(due_ts);
CREATE INDEX IF NOT EXISTS idx_tasks_score ON tasks(score DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_source ON tasks(source, source_id);

-- Documents table for Drive documents
CREATE TABLE IF NOT EXISTS docs (
    id VARCHAR PRIMARY KEY,
    title VARCHAR NOT NULL,
    link VARCHAR NOT NULL,
    mime_type VARCHAR,
    meeting_id VARCHAR, -- Calendar event ID if related
    summary VARCHAR,
    owner VARCHAR,
    updated_ts BIGINT,
    last_synced BIGINT,
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)
);

CREATE INDEX IF NOT EXISTS idx_docs_meeting ON docs(meeting_id);
CREATE INDEX IF NOT EXISTS idx_docs_updated ON docs(updated_ts);

-- Events table for Calendar
CREATE TABLE IF NOT EXISTS events (
    id VARCHAR PRIMARY KEY,
    title VARCHAR NOT NULL,
    start_ts BIGINT NOT NULL,
    end_ts BIGINT NOT NULL,
    location VARCHAR,
    description VARCHAR,
    attendees VARCHAR, -- JSON array
    meeting_link VARCHAR,
    status VARCHAR,
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    updated_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)
);

CREATE INDEX IF NOT EXISTS idx_events_start ON events(start_ts);
CREATE INDEX IF NOT EXISTS idx_events_end ON events(end_ts);

-- Preferences table for learned user preferences
CREATE TABLE IF NOT EXISTS prefs (
    key VARCHAR PRIMARY KEY,
    val VARCHAR NOT NULL,
    updated_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT)
);

-- Usage table for tracking API usage
CREATE SEQUENCE IF NOT EXISTS usage_seq;
CREATE TABLE IF NOT EXISTS usage (
    id INTEGER PRIMARY KEY DEFAULT nextval('usage_seq'),
    ts BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    service VARCHAR NOT NULL, -- gemini, gmail, drive, etc.
    action VARCHAR NOT NULL,
    tokens INTEGER DEFAULT 0,
    cost DOUBLE DEFAULT 0,
    duration_ms INTEGER,
    error VARCHAR
);

CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage(ts);
CREATE INDEX IF NOT EXISTS idx_usage_service ON usage(service);

-- LLM cache table
CREATE TABLE IF NOT EXISTS llm_cache (
    hash VARCHAR PRIMARY KEY,
    prompt VARCHAR NOT NULL,
    response VARCHAR NOT NULL,
    model VARCHAR,
    tokens INTEGER,
    created_at BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    expires_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_llm_cache_expires ON llm_cache(expires_at);

-- Sync state table for tracking incremental syncs
CREATE TABLE IF NOT EXISTS sync_state (
    service VARCHAR PRIMARY KEY,
    state VARCHAR NOT NULL, -- JSON object with service-specific state
    last_sync BIGINT DEFAULT CAST(epoch(current_timestamp::TIMESTAMP) AS BIGINT),
    next_sync BIGINT,
    error_count INTEGER DEFAULT 0,
    last_error VARCHAR
);

-- Migration tracking table
CREATE TABLE IF NOT EXISTS migration_versions (
    version INTEGER PRIMARY KEY,
    name VARCHAR NOT NULL,
    applied_at BIGINT NOT NULL
);
