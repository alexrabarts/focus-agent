package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Message represents an email message
type Message struct {
	ID          string    `json:"id"`
	ThreadID    string    `json:"thread_id"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Subject     string    `json:"subject"`
	Snippet     string    `json:"snippet"`
	Body        string    `json:"body"`
	Timestamp   time.Time `json:"timestamp"`
	LastMsgID   string    `json:"last_msg_id"`
	Labels      []string  `json:"labels"`
	Sensitivity string    `json:"sensitivity"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Thread represents an email conversation
type Thread struct {
	ID             string     `json:"id"`
	LastHistoryID  string     `json:"last_history_id"`
	Summary        string     `json:"summary"`
	SummaryHash    string     `json:"summary_hash"`
	TaskCount      int        `json:"task_count"`
	PriorityScore  float64    `json:"priority_score"`
	RelevantToUser bool       `json:"relevant_to_user"`
	NextFollowupTS *time.Time `json:"next_followup_ts"`
	LastSynced     time.Time  `json:"last_synced"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Task represents a work item
type Task struct {
	ID                 string     `json:"id"`
	Source             string     `json:"source"`
	SourceID           string     `json:"source_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	DueTS              *time.Time `json:"due_ts"`
	Project            string     `json:"project"`
	Impact             int        `json:"impact"`
	Urgency            int        `json:"urgency"`
	Effort             string     `json:"effort"`
	Stakeholder        string     `json:"stakeholder"`
	Score              float64    `json:"score"`
	Status             string     `json:"status"`
	Metadata           string     `json:"metadata"`
	MatchedPriorities  string     `json:"matched_priorities"` // JSON string storing which priorities matched
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at"`
}

// PriorityMatches represents which priority areas matched for a task
type PriorityMatches struct {
	OKRs           []string `json:"okrs"`
	FocusAreas     []string `json:"focus_areas"`
	Projects       []string `json:"projects"`
	KeyStakeholder bool     `json:"key_stakeholder"`
}

// Document represents a Drive document
type Document struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Link       string    `json:"link"`
	MimeType   string    `json:"mime_type"`
	MeetingID  string    `json:"meeting_id"`
	Summary    string    `json:"summary"`
	Owner      string    `json:"owner"`
	UpdatedTS  time.Time `json:"updated_ts"`
	LastSynced time.Time `json:"last_synced"`
	CreatedAt  time.Time `json:"created_at"`
}

// Event represents a calendar event
type Event struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	StartTS     time.Time `json:"start_ts"`
	EndTS       time.Time `json:"end_ts"`
	Location    string    `json:"location"`
	Description string    `json:"description"`
	Attendees   []string  `json:"attendees"`
	MeetingLink string    `json:"meeting_link"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SyncState tracks incremental sync state
type SyncState struct {
	Service    string    `json:"service"`
	State      string    `json:"state"`
	LastSync   time.Time `json:"last_sync"`
	NextSync   time.Time `json:"next_sync"`
	ErrorCount int       `json:"error_count"`
	LastError  string    `json:"last_error"`
}

// Usage tracks API usage
type Usage struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Service    string    `json:"service"`
	Action     string    `json:"action"`
	Tokens     int       `json:"tokens"`
	Cost       float64   `json:"cost"`
	DurationMS int       `json:"duration_ms"`
	Error      string    `json:"error"`
}

// LLMCache stores cached LLM responses
type LLMCache struct {
	Hash      string    `json:"hash"`
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Model     string    `json:"model"`
	Tokens    int       `json:"tokens"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SaveMessage inserts or updates a message
func (db *DB) SaveMessage(msg *Message) error {
	labelsJSON, _ := json.Marshal(msg.Labels)

	query := `
		INSERT INTO messages (id, thread_id, from_addr, to_addr, subject, snippet, body, ts, last_msg_id, labels, sensitivity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			snippet = excluded.snippet,
			body = excluded.body,
			last_msg_id = excluded.last_msg_id,
			labels = excluded.labels,
			sensitivity = excluded.sensitivity
	`

	_, err := db.Exec(query,
		msg.ID, msg.ThreadID, msg.From, msg.To, msg.Subject, msg.Snippet, msg.Body,
		msg.Timestamp.Unix(), msg.LastMsgID, string(labelsJSON), msg.Sensitivity,
	)
	return err
}

// SaveThread inserts or updates a thread
func (db *DB) SaveThread(thread *Thread) error {
	var nextFollowup *int64
	if thread.NextFollowupTS != nil {
		ts := thread.NextFollowupTS.Unix()
		nextFollowup = &ts
	}

	// During sync, we only update sync-related fields
	// AI-generated fields (priority_score, summary, etc.) and indexed fields are preserved
	// Note: DuckDB doesn't allow updating indexed columns in ON CONFLICT DO UPDATE
	query := `
		INSERT INTO threads (id, last_history_id, summary, summary_hash, task_count, priority_score, relevant_to_user, next_followup_ts, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_history_id = COALESCE(excluded.last_history_id, threads.last_history_id),
			summary = COALESCE(NULLIF(excluded.summary, ''), threads.summary),
			summary_hash = COALESCE(NULLIF(excluded.summary_hash, ''), threads.summary_hash),
			task_count = CASE WHEN excluded.task_count > 0 THEN excluded.task_count ELSE threads.task_count END,
			last_synced = excluded.last_synced
	`

	_, err := db.Exec(query,
		thread.ID, thread.LastHistoryID, thread.Summary, thread.SummaryHash,
		thread.TaskCount, thread.PriorityScore, thread.RelevantToUser, nextFollowup, thread.LastSynced.Unix(),
	)
	return err
}

// SaveTask inserts or updates a task
func (db *DB) SaveTask(task *Task) error {
	var dueTS *int64
	if task.DueTS != nil {
		ts := task.DueTS.Unix()
		dueTS = &ts
	}

	var completedTS *int64
	if task.CompletedAt != nil {
		ts := task.CompletedAt.Unix()
		completedTS = &ts
	}

	// Set created_at if not already set (zero value)
	now := time.Now()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	createdTS := task.CreatedAt.Unix()

	// Always update updated_at
	task.UpdatedAt = now
	updatedTS := task.UpdatedAt.Unix()

	// Note: DuckDB doesn't allow updating indexed columns in ON CONFLICT DO UPDATE
	// Indexed columns: status, due_ts, score, source, source_id
	query := `
		INSERT INTO tasks (id, source, source_id, title, description, due_ts, project, impact, urgency, effort, stakeholder, score, status, metadata, completed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			project = excluded.project,
			impact = excluded.impact,
			urgency = excluded.urgency,
			effort = excluded.effort,
			stakeholder = excluded.stakeholder,
			metadata = excluded.metadata,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at
	`

	_, err := db.Exec(query,
		task.ID, task.Source, task.SourceID, task.Title, task.Description, dueTS,
		task.Project, task.Impact, task.Urgency, task.Effort, task.Stakeholder,
		task.Score, task.Status, task.Metadata, completedTS, createdTS, updatedTS,
	)
	return err
}

// GetPendingTasks returns all pending tasks sorted by score (highest first)
func (db *DB) GetPendingTasks(limit int) ([]*Task, error) {
	query := `
		SELECT id, source, source_id, title, description, due_ts, project,
		       impact, urgency, effort, stakeholder, score, status, metadata,
		       matched_priorities, created_at, updated_at, completed_at
		FROM tasks
		WHERE status = 'pending'
		ORDER BY score DESC
		LIMIT ?
	`

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		var dueTS, createdTS, updatedTS, completedTS sql.NullInt64
		var matchedPriorities sql.NullString

		err := rows.Scan(
			&task.ID, &task.Source, &task.SourceID, &task.Title, &task.Description,
			&dueTS, &task.Project, &task.Impact, &task.Urgency, &task.Effort,
			&task.Stakeholder, &task.Score, &task.Status, &task.Metadata,
			&matchedPriorities, &createdTS, &updatedTS, &completedTS,
		)
		if err != nil {
			return nil, err
		}

		if dueTS.Valid {
			t := time.Unix(dueTS.Int64, 0)
			task.DueTS = &t
		}
		if matchedPriorities.Valid {
			task.MatchedPriorities = matchedPriorities.String
		}
		if createdTS.Valid {
			task.CreatedAt = time.Unix(createdTS.Int64, 0)
		}
		if updatedTS.Valid {
			task.UpdatedAt = time.Unix(updatedTS.Int64, 0)
		}
		if completedTS.Valid {
			t := time.Unix(completedTS.Int64, 0)
			task.CompletedAt = &t
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// GetUpcomingEvents returns events in the next N hours
func (db *DB) GetUpcomingEvents(hours int) ([]*Event, error) {
	now := time.Now()
	endTime := now.Add(time.Duration(hours) * time.Hour)

	query := `
		SELECT id, title, start_ts, end_ts, location, description,
		       attendees, meeting_link, status
		FROM events
		WHERE start_ts >= ? AND start_ts <= ?
		ORDER BY start_ts ASC
	`

	rows, err := db.Query(query, now.Unix(), endTime.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		event := &Event{}
		var startTS, endTS int64
		var attendeesJSON string

		err := rows.Scan(
			&event.ID, &event.Title, &startTS, &endTS,
			&event.Location, &event.Description, &attendeesJSON,
			&event.MeetingLink, &event.Status,
		)
		if err != nil {
			return nil, err
		}

		event.StartTS = time.Unix(startTS, 0)
		event.EndTS = time.Unix(endTS, 0)

		if attendeesJSON != "" {
			json.Unmarshal([]byte(attendeesJSON), &event.Attendees)
		}

		events = append(events, event)
	}

	return events, nil
}

// GetSyncState retrieves sync state for a service
func (db *DB) GetSyncState(service string) (*SyncState, error) {
	state := &SyncState{Service: service}

	query := `SELECT state, last_sync, next_sync, error_count, last_error
	          FROM sync_state WHERE service = ?`

	var lastSync, nextSync int64
	err := db.QueryRow(query, service).Scan(
		&state.State, &lastSync, &nextSync, &state.ErrorCount, &state.LastError,
	)

	if err == sql.ErrNoRows {
		// Return empty state for new service
		return &SyncState{Service: service, State: "{}"}, nil
	} else if err != nil {
		return nil, err
	}

	state.LastSync = time.Unix(lastSync, 0)
	state.NextSync = time.Unix(nextSync, 0)

	return state, nil
}

// SaveSyncState updates sync state for a service
func (db *DB) SaveSyncState(state *SyncState) error {
	query := `
		INSERT INTO sync_state (service, state, last_sync, next_sync, error_count, last_error)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(service) DO UPDATE SET
			state = excluded.state,
			last_sync = excluded.last_sync,
			next_sync = excluded.next_sync,
			error_count = excluded.error_count,
			last_error = excluded.last_error
	`

	_, err := db.Exec(query,
		state.Service, state.State, state.LastSync.Unix(), state.NextSync.Unix(),
		state.ErrorCount, state.LastError,
	)
	return err
}

// LogUsage records API usage
func (db *DB) LogUsage(service, action string, tokens int, cost float64, duration time.Duration, err error) error {
	var errStr string
	if err != nil {
		errStr = err.Error()
	}

	query := `INSERT INTO usage (service, action, tokens, cost, duration_ms, error) VALUES (?, ?, ?, ?, ?, ?)`
	_, dbErr := db.Exec(query, service, action, tokens, cost, duration.Milliseconds(), errStr)
	return dbErr
}

// GetCachedResponse retrieves a cached LLM response
func (db *DB) GetCachedResponse(hash string) (*LLMCache, error) {
	cache := &LLMCache{}

	query := `SELECT prompt, response, model, tokens, created_at, expires_at
	          FROM llm_cache WHERE hash = ? AND expires_at > ?`

	var createdTS, expiresTS int64
	err := db.QueryRow(query, hash, time.Now().Unix()).Scan(
		&cache.Prompt, &cache.Response, &cache.Model, &cache.Tokens,
		&createdTS, &expiresTS,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	cache.Hash = hash
	cache.CreatedAt = time.Unix(createdTS, 0)
	cache.ExpiresAt = time.Unix(expiresTS, 0)

	return cache, nil
}

// SaveCachedResponse stores an LLM response in cache
func (db *DB) SaveCachedResponse(cache *LLMCache) error {
	query := `
		INSERT INTO llm_cache (hash, prompt, response, model, tokens, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET
			response = excluded.response,
			tokens = excluded.tokens,
			expires_at = excluded.expires_at
	`

	_, err := db.Exec(query,
		cache.Hash, cache.Prompt, cache.Response, cache.Model,
		cache.Tokens, cache.ExpiresAt.Unix(),
	)
	return err
}

// CleanExpiredCache removes expired cache entries
func (db *DB) CleanExpiredCache() error {
	query := `DELETE FROM llm_cache WHERE expires_at < ?`
	_, err := db.Exec(query, time.Now().Unix())
	return err
}

// GetPreference retrieves a user preference
func (db *DB) GetPreference(key string) (string, error) {
	var val string
	query := `SELECT val FROM prefs WHERE key = ?`
	err := db.QueryRow(query, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetPreference stores a user preference
func (db *DB) SetPreference(key, val string) error {
	query := `INSERT INTO prefs (key, val) VALUES (?, ?)
	          ON CONFLICT(key) DO UPDATE SET val = excluded.val`
	_, err := db.Exec(query, key, val)
	return err
}

// SearchMessages performs full-text search on messages
func (db *DB) SearchMessages(query string, limit int) ([]*Message, error) {
	searchQuery := `
		SELECT m.id, m.thread_id, m.from_addr, m.to_addr, m.subject,
		       m.snippet, m.body, m.ts, m.labels
		FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`

	rows, err := db.Query(searchQuery, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		var ts int64
		var labelsJSON string

		err := rows.Scan(
			&msg.ID, &msg.ThreadID, &msg.From, &msg.To, &msg.Subject,
			&msg.Snippet, &msg.Body, &ts, &labelsJSON,
		)
		if err != nil {
			return nil, err
		}

		msg.Timestamp = time.Unix(ts, 0)
		if labelsJSON != "" {
			json.Unmarshal([]byte(labelsJSON), &msg.Labels)
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

// SaveEvent saves an event to the database
func (db *DB) SaveEvent(event *Event) error {
	attendeesJSON, _ := json.Marshal(event.Attendees)

	// Note: DuckDB doesn't allow updating indexed columns in ON CONFLICT DO UPDATE
	// Indexed columns: start_ts, end_ts
	query := `
		INSERT INTO events (id, title, start_ts, end_ts, location, description, attendees, meeting_link, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			location = excluded.location,
			description = excluded.description,
			attendees = excluded.attendees,
			meeting_link = excluded.meeting_link,
			status = excluded.status
	`

	_, err := db.Exec(query,
		event.ID, event.Title, event.StartTS.Unix(), event.EndTS.Unix(),
		event.Location, event.Description, string(attendeesJSON),
		event.MeetingLink, event.Status,
	)
	return err
}

// SaveDocument saves a document to the database
func (db *DB) SaveDocument(doc *Document) error {
	// Note: DuckDB doesn't allow updating indexed columns in ON CONFLICT DO UPDATE
	// Indexed columns: meeting_id, updated_ts
	query := `
		INSERT INTO docs (id, title, link, mime_type, meeting_id, summary, owner, updated_ts, last_synced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			link = excluded.link,
			mime_type = excluded.mime_type,
			summary = excluded.summary,
			owner = excluded.owner,
			last_synced = excluded.last_synced
	`

	_, err := db.Exec(query,
		doc.ID, doc.Title, doc.Link, doc.MimeType, doc.MeetingID,
		doc.Summary, doc.Owner, doc.UpdatedTS.Unix(), doc.LastSynced.Unix(),
	)
	return err
}

// GetThreadsWithSummaries returns threads that have AI-generated summaries
func (db *DB) GetThreadsWithSummaries(limit int) ([]*Thread, error) {
	query := `
		SELECT t.id, t.last_history_id, t.summary, t.summary_hash, t.task_count,
		       t.priority_score, t.relevant_to_user, t.next_followup_ts, t.last_synced, t.created_at, t.updated_at
		FROM threads t
		WHERE t.summary IS NOT NULL AND t.summary != ''
		ORDER BY t.priority_score DESC, t.last_synced DESC
		LIMIT ?
	`

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []*Thread
	for rows.Next() {
		thread := &Thread{}
		var nextFollowupTS, lastSyncedTS, createdTS, updatedTS sql.NullInt64

		err := rows.Scan(
			&thread.ID, &thread.LastHistoryID, &thread.Summary, &thread.SummaryHash,
			&thread.TaskCount, &thread.PriorityScore, &thread.RelevantToUser, &nextFollowupTS, &lastSyncedTS, &createdTS, &updatedTS,
		)
		if err != nil {
			return nil, err
		}

		if nextFollowupTS.Valid {
			t := time.Unix(nextFollowupTS.Int64, 0)
			thread.NextFollowupTS = &t
		}
		if lastSyncedTS.Valid {
			thread.LastSynced = time.Unix(lastSyncedTS.Int64, 0)
		}
		if createdTS.Valid {
			thread.CreatedAt = time.Unix(createdTS.Int64, 0)
		}
		if updatedTS.Valid {
			thread.UpdatedAt = time.Unix(updatedTS.Int64, 0)
		}

		threads = append(threads, thread)
	}

	return threads, nil
}

// GetThreadByID returns a specific thread by ID
func (db *DB) GetThreadByID(id string) (*Thread, error) {
	thread := &Thread{}

	query := `
		SELECT id, last_history_id, summary, summary_hash, task_count,
		       next_followup_ts, last_synced, created_at, updated_at
		FROM threads
		WHERE id = ?
	`

	var nextFollowupTS, lastSyncedTS, createdTS, updatedTS sql.NullInt64
	err := db.QueryRow(query, id).Scan(
		&thread.ID, &thread.LastHistoryID, &thread.Summary, &thread.SummaryHash,
		&thread.TaskCount, &nextFollowupTS, &lastSyncedTS, &createdTS, &updatedTS,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("thread not found: %s", id)
	} else if err != nil {
		return nil, err
	}

	if nextFollowupTS.Valid {
		t := time.Unix(nextFollowupTS.Int64, 0)
		thread.NextFollowupTS = &t
	}
	if lastSyncedTS.Valid {
		thread.LastSynced = time.Unix(lastSyncedTS.Int64, 0)
	}
	if createdTS.Valid {
		thread.CreatedAt = time.Unix(createdTS.Int64, 0)
	}
	if updatedTS.Valid {
		thread.UpdatedAt = time.Unix(updatedTS.Int64, 0)
	}

	return thread, nil
}

// GetThreadMessages returns all messages for a thread
func (db *DB) GetThreadMessages(threadID string) ([]*Message, error) {
	query := `
		SELECT id, thread_id, from_addr, to_addr, subject, snippet, body, ts,
		       last_msg_id, labels, sensitivity, created_at, updated_at
		FROM messages
		WHERE thread_id = ?
		ORDER BY ts ASC
	`

	rows, err := db.Query(query, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		msg := &Message{}
		var ts, createdTS, updatedTS int64
		var labelsJSON string

		err := rows.Scan(
			&msg.ID, &msg.ThreadID, &msg.From, &msg.To, &msg.Subject,
			&msg.Snippet, &msg.Body, &ts, &msg.LastMsgID, &labelsJSON,
			&msg.Sensitivity, &createdTS, &updatedTS,
		)
		if err != nil {
			return nil, err
		}

		msg.Timestamp = time.Unix(ts, 0)
		msg.CreatedAt = time.Unix(createdTS, 0)
		msg.UpdatedAt = time.Unix(updatedTS, 0)

		if labelsJSON != "" {
			json.Unmarshal([]byte(labelsJSON), &msg.Labels)
		}

		messages = append(messages, msg)
	}

	return messages, nil
}
// RecalculateThreadPriorities updates priority scores for all threads based on their tasks
func (db *DB) RecalculateThreadPriorities() error {
	// Get all threads with summaries
	query := `SELECT id FROM threads WHERE summary IS NOT NULL AND summary != ''`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query threads: %w", err)
	}
	defer rows.Close()

	var threadIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		threadIDs = append(threadIDs, id)
	}

	// For each thread, calculate priority from its tasks
	updateQuery := `
		UPDATE threads
		SET priority_score = (
			SELECT COALESCE(MAX(score), 0)
			FROM tasks
			WHERE source = 'ai' AND source_id = ?
		)
		WHERE id = ?
	`

	updated := 0
	for _, threadID := range threadIDs {
		if _, err := db.Exec(updateQuery, threadID, threadID); err != nil {
			// Log but continue
			continue
		}
		updated++
	}

	return nil
}
