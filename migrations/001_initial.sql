-- Focus Agent Database Schema
-- SQLite with WAL mode, FTS5, JSON1

-- Enable WAL mode for better concurrency
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

-- Messages table for email storage
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    from_addr TEXT,
    to_addr TEXT,
    subject TEXT,
    snippet TEXT,
    body TEXT,
    ts INTEGER NOT NULL, -- Unix timestamp
    last_msg_id TEXT,
    labels TEXT, -- JSON array
    sensitivity TEXT, -- low, medium, high
    created_at INTEGER DEFAULT (strftime('%s','now')),
    updated_at INTEGER DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
CREATE INDEX IF NOT EXISTS idx_messages_from ON messages(from_addr);

-- Threads table for conversation tracking
CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    last_history_id TEXT,
    summary TEXT,
    summary_hash TEXT,
    task_count INTEGER DEFAULT 0,
    next_followup_ts INTEGER,
    last_synced INTEGER,
    created_at INTEGER DEFAULT (strftime('%s','now')),
    updated_at INTEGER DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_threads_followup ON threads(next_followup_ts);

-- Tasks table for unified task management
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL, -- gmail, gcal, gtasks, manual
    source_id TEXT, -- Original ID from source system
    title TEXT NOT NULL,
    description TEXT,
    due_ts INTEGER,
    project TEXT,
    impact INTEGER CHECK (impact >= 1 AND impact <= 5),
    urgency INTEGER CHECK (urgency >= 1 AND urgency <= 5),
    effort TEXT CHECK (effort IN ('S', 'M', 'L')),
    stakeholder TEXT,
    score REAL,
    status TEXT DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed', 'cancelled')),
    metadata TEXT, -- JSON object for source-specific data
    created_at INTEGER DEFAULT (strftime('%s','now')),
    updated_at INTEGER DEFAULT (strftime('%s','now')),
    completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(due_ts);
CREATE INDEX IF NOT EXISTS idx_tasks_score ON tasks(score DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_source ON tasks(source, source_id);

-- Documents table for Drive documents
CREATE TABLE IF NOT EXISTS docs (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    link TEXT NOT NULL,
    mime_type TEXT,
    meeting_id TEXT, -- Calendar event ID if related
    summary TEXT,
    owner TEXT,
    updated_ts INTEGER,
    last_synced INTEGER,
    created_at INTEGER DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_docs_meeting ON docs(meeting_id);
CREATE INDEX IF NOT EXISTS idx_docs_updated ON docs(updated_ts);

-- Events table for Calendar
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    start_ts INTEGER NOT NULL,
    end_ts INTEGER NOT NULL,
    location TEXT,
    description TEXT,
    attendees TEXT, -- JSON array
    meeting_link TEXT,
    status TEXT,
    created_at INTEGER DEFAULT (strftime('%s','now')),
    updated_at INTEGER DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_events_start ON events(start_ts);
CREATE INDEX IF NOT EXISTS idx_events_end ON events(end_ts);

-- Preferences table for learned user preferences
CREATE TABLE IF NOT EXISTS prefs (
    key TEXT PRIMARY KEY,
    val TEXT NOT NULL,
    updated_at INTEGER DEFAULT (strftime('%s','now'))
);

-- Usage table for tracking API usage
CREATE TABLE IF NOT EXISTS usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER DEFAULT (strftime('%s','now')),
    service TEXT NOT NULL, -- gemini, gmail, drive, etc.
    action TEXT NOT NULL,
    tokens INTEGER DEFAULT 0,
    cost REAL DEFAULT 0,
    duration_ms INTEGER,
    error TEXT
);

CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage(ts);
CREATE INDEX IF NOT EXISTS idx_usage_service ON usage(service);

-- LLM cache table
CREATE TABLE IF NOT EXISTS llm_cache (
    hash TEXT PRIMARY KEY,
    prompt TEXT NOT NULL,
    response TEXT NOT NULL,
    model TEXT,
    tokens INTEGER,
    created_at INTEGER DEFAULT (strftime('%s','now')),
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_llm_cache_expires ON llm_cache(expires_at);

-- Sync state table for tracking incremental syncs
CREATE TABLE IF NOT EXISTS sync_state (
    service TEXT PRIMARY KEY,
    state TEXT NOT NULL, -- JSON object with service-specific state
    last_sync INTEGER DEFAULT (strftime('%s','now')),
    next_sync INTEGER,
    error_count INTEGER DEFAULT 0,
    last_error TEXT
);

-- Full-text search for messages
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    subject,
    snippet,
    body,
    from_addr,
    content=messages,
    content_rowid=rowid
);

-- Triggers to keep FTS index updated
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, subject, snippet, body, from_addr)
    VALUES (new.rowid, new.subject, new.snippet, new.body, new.from_addr);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
    UPDATE messages_fts SET
        subject = new.subject,
        snippet = new.snippet,
        body = new.body,
        from_addr = new.from_addr
    WHERE rowid = new.rowid;
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
END;

-- Full-text search for tasks
CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(
    title,
    description,
    project,
    content=tasks,
    content_rowid=rowid
);

-- Triggers for tasks FTS
CREATE TRIGGER IF NOT EXISTS tasks_ai AFTER INSERT ON tasks BEGIN
    INSERT INTO tasks_fts(rowid, title, description, project)
    VALUES (new.rowid, new.title, new.description, new.project);
END;

CREATE TRIGGER IF NOT EXISTS tasks_au AFTER UPDATE ON tasks BEGIN
    UPDATE tasks_fts SET
        title = new.title,
        description = new.description,
        project = new.project
    WHERE rowid = new.rowid;
END;

CREATE TRIGGER IF NOT EXISTS tasks_ad AFTER DELETE ON tasks BEGIN
    DELETE FROM tasks_fts WHERE rowid = old.rowid;
END;

-- Update timestamp triggers
CREATE TRIGGER IF NOT EXISTS messages_update_ts AFTER UPDATE ON messages BEGIN
    UPDATE messages SET updated_at = strftime('%s','now') WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS threads_update_ts AFTER UPDATE ON threads BEGIN
    UPDATE threads SET updated_at = strftime('%s','now') WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS tasks_update_ts AFTER UPDATE ON tasks BEGIN
    UPDATE tasks SET updated_at = strftime('%s','now') WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS events_update_ts AFTER UPDATE ON events BEGIN
    UPDATE events SET updated_at = strftime('%s','now') WHERE id = new.id;
END;