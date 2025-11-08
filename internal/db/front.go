package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// SaveFrontMetadata saves Front conversation metadata
func (db *DB) SaveFrontMetadata(metadata *FrontMetadata) error {
	tagsJSON, _ := json.Marshal(metadata.Tags)

	query := `
		INSERT INTO front_metadata (
			thread_id, conversation_id, status, assignee_id, assignee_name,
			tags, last_message_ts, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (thread_id) DO UPDATE SET
			status = excluded.status,
			assignee_id = excluded.assignee_id,
			assignee_name = excluded.assignee_name,
			tags = excluded.tags,
			last_message_ts = excluded.last_message_ts,
			updated_at = excluded.updated_at
	`

	_, err := db.Exec(query,
		metadata.ThreadID,
		metadata.ConversationID,
		metadata.Status,
		metadata.AssigneeID,
		metadata.AssigneeName,
		string(tagsJSON),
		metadata.LastMessageTS.Unix(),
		metadata.CreatedAt.Unix(),
		metadata.UpdatedAt.Unix(),
	)

	return err
}

// GetFrontMetadata retrieves Front metadata for a thread
func (db *DB) GetFrontMetadata(threadID string) (*FrontMetadata, error) {
	query := `
		SELECT conversation_id, status, assignee_id, assignee_name, tags,
		       last_message_ts, created_at, updated_at
		FROM front_metadata
		WHERE thread_id = ?
	`

	var metadata FrontMetadata
	var tagsJSON string
	var lastMsgTS, createdTS, updatedTS sql.NullInt64

	err := db.QueryRow(query, threadID).Scan(
		&metadata.ConversationID,
		&metadata.Status,
		&metadata.AssigneeID,
		&metadata.AssigneeName,
		&tagsJSON,
		&lastMsgTS,
		&createdTS,
		&updatedTS,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no Front metadata found for thread %s", threadID)
	} else if err != nil {
		return nil, err
	}

	metadata.ThreadID = threadID
	if tagsJSON != "" {
		json.Unmarshal([]byte(tagsJSON), &metadata.Tags)
	}
	if lastMsgTS.Valid {
		metadata.LastMessageTS = time.Unix(lastMsgTS.Int64, 0)
	}
	if createdTS.Valid {
		metadata.CreatedAt = time.Unix(createdTS.Int64, 0)
	}
	if updatedTS.Valid {
		metadata.UpdatedAt = time.Unix(updatedTS.Int64, 0)
	}

	return &metadata, nil
}

// SaveFrontComment saves an internal Front comment
func (db *DB) SaveFrontComment(comment *FrontComment) error {
	query := `
		INSERT INTO front_comments (
			id, thread_id, conversation_id, author_name, body, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING
	`

	_, err := db.Exec(query,
		comment.ID,
		comment.ThreadID,
		comment.ConversationID,
		comment.AuthorName,
		comment.Body,
		comment.CreatedAt.Unix(),
	)

	return err
}

// GetFrontComments retrieves all comments for a thread
func (db *DB) GetFrontComments(threadID string) ([]*FrontComment, error) {
	query := `
		SELECT id, conversation_id, author_name, body, created_at
		FROM front_comments
		WHERE thread_id = ?
		ORDER BY created_at ASC
	`

	rows, err := db.Query(query, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []*FrontComment
	for rows.Next() {
		var comment FrontComment
		var createdTS int64

		err := rows.Scan(
			&comment.ID,
			&comment.ConversationID,
			&comment.AuthorName,
			&comment.Body,
			&createdTS,
		)
		if err != nil {
			continue
		}

		comment.ThreadID = threadID
		comment.CreatedAt = time.Unix(createdTS, 0)
		comments = append(comments, &comment)
	}

	return comments, nil
}

// GetThreadsNeedingFrontEnrichment finds threads without Front metadata
func (db *DB) GetThreadsNeedingFrontEnrichment(limit int) ([]struct {
	ThreadID  string
	Subject   string
	Timestamp time.Time
}, error) {
	query := `
		SELECT t.id, m.subject, m.ts
		FROM threads t
		JOIN messages m ON t.id = m.thread_id
		LEFT JOIN front_metadata fm ON t.id = fm.thread_id
		WHERE fm.thread_id IS NULL
		GROUP BY t.id, m.subject, m.ts
		ORDER BY m.ts DESC
		LIMIT ?
	`

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []struct {
		ThreadID  string
		Subject   string
		Timestamp time.Time
	}

	for rows.Next() {
		var r struct {
			ThreadID  string
			Subject   string
			Timestamp time.Time
		}
		var ts int64

		if err := rows.Scan(&r.ThreadID, &r.Subject, &ts); err != nil {
			continue
		}

		r.Timestamp = time.Unix(ts, 0)
		results = append(results, r)
	}

	return results, nil
}
