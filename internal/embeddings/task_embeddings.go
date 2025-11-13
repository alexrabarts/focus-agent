package embeddings

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/db"
)

// TaskEmbedding represents a task's embedding data
type TaskEmbedding struct {
	TaskID           string
	Embedding        []float64
	EmbeddingContent string
	Model            string
	GeneratedAt      time.Time
}

// BuildEmbeddingContent creates the text to embed for a task
// Combines: title + description + source context + matched priorities (if any)
func BuildEmbeddingContent(task *db.Task) string {
	var parts []string

	// Add title (most important)
	if task.Title != "" {
		parts = append(parts, fmt.Sprintf("Task: %s", task.Title))
	}

	// Add description
	if task.Description != "" {
		parts = append(parts, fmt.Sprintf("Description: %s", task.Description))
	}

	// Add source context
	sourceContext := formatSourceContext(task.Source, task.Project)
	if sourceContext != "" {
		parts = append(parts, fmt.Sprintf("Source: %s", sourceContext))
	}

	// Add matched priorities if available (strategic reasoning proxy)
	if task.MatchedPriorities != "" && task.MatchedPriorities != "{}" {
		var matches map[string]interface{}
		if err := json.Unmarshal([]byte(task.MatchedPriorities), &matches); err == nil && len(matches) > 0 {
			var priorities []string
			for k := range matches {
				priorities = append(priorities, k)
			}
			if len(priorities) > 0 {
				parts = append(parts, fmt.Sprintf("Aligned with priorities: %s", strings.Join(priorities, ", ")))
			}
		}
	}

	return strings.Join(parts, "\n")
}

// formatSourceContext creates a human-readable source description
func formatSourceContext(source, project string) string {
	var parts []string

	switch source {
	case "gmail":
		parts = append(parts, "Email inbox")
	case "gcal":
		parts = append(parts, "Calendar")
	case "gtasks":
		parts = append(parts, "Google Tasks")
	case "front":
		parts = append(parts, "Front inbox")
	default:
		if source != "" {
			parts = append(parts, source)
		}
	}

	if project != "" {
		parts = append(parts, fmt.Sprintf("project %s", project))
	}

	return strings.Join(parts, " - ")
}

// HashContent generates a stable hash for embedding content
// Used to detect when content has changed and embedding needs regeneration
func HashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// GenerateTaskEmbedding generates an embedding for a task
func GenerateTaskEmbedding(ctx context.Context, client *Client, task *db.Task) (*TaskEmbedding, error) {
	content := BuildEmbeddingContent(task)
	if content == "" {
		return nil, fmt.Errorf("no content to embed for task %s", task.ID)
	}

	embedding, err := client.GenerateWithRetry(ctx, content, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embedding: %w", err)
	}

	return &TaskEmbedding{
		TaskID:           task.ID,
		Embedding:        embedding,
		EmbeddingContent: content,
		Model:            client.model,
		GeneratedAt:      time.Now(),
	}, nil
}

// GenerateTaskEmbeddingAsync generates an embedding asynchronously
func GenerateTaskEmbeddingAsync(ctx context.Context, client *Client, database *db.DB, task *db.Task) {
	go func() {
		// Use background context with timeout
		embedCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		embedding, err := GenerateTaskEmbedding(embedCtx, client, task)
		if err != nil {
			log.Printf("Failed to generate embedding for task %s: %v", task.ID, err)
			return
		}

		// Store embedding in database
		if err := SaveTaskEmbedding(database, embedding); err != nil {
			log.Printf("Failed to save embedding for task %s: %v", task.ID, err)
			return
		}

		log.Printf("Generated embedding for task %s", task.ID)
	}()
}

// SaveTaskEmbedding saves a task embedding to the database
func SaveTaskEmbedding(database *db.DB, embedding *TaskEmbedding) error {
	// Convert []float64 to array literal format for DuckDB
	embeddingJSON, err := json.Marshal(embedding.Embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	query := `
		INSERT OR REPLACE INTO task_embeddings
		(task_id, embedding, embedding_content, model, generated_at)
		VALUES (?, ?::FLOAT[768], ?, ?, ?)
	`

	_, err = database.Exec(
		query,
		embedding.TaskID,
		string(embeddingJSON),
		embedding.EmbeddingContent,
		embedding.Model,
		embedding.GeneratedAt.Unix(),
	)

	if err != nil {
		return fmt.Errorf("failed to insert embedding: %w", err)
	}

	return nil
}

// GetTaskEmbedding retrieves a task's embedding from the database
func GetTaskEmbedding(database *db.DB, taskID string) (*TaskEmbedding, error) {
	query := `
		SELECT task_id, embedding, embedding_content, model, generated_at
		FROM task_embeddings
		WHERE task_id = ?
	`

	var embedding TaskEmbedding
	var embeddingJSON string
	var generatedAtUnix int64

	err := database.QueryRow(query, taskID).Scan(
		&embedding.TaskID,
		&embeddingJSON,
		&embedding.EmbeddingContent,
		&embedding.Model,
		&generatedAtUnix,
	)

	if err != nil {
		return nil, err
	}

	// Parse embedding JSON
	if err := json.Unmarshal([]byte(embeddingJSON), &embedding.Embedding); err != nil {
		return nil, fmt.Errorf("failed to unmarshal embedding: %w", err)
	}

	embedding.GeneratedAt = time.Unix(generatedAtUnix, 0)

	return &embedding, nil
}

// HasTaskEmbedding checks if a task already has an embedding
func HasTaskEmbedding(database *db.DB, taskID string) (bool, error) {
	query := `SELECT COUNT(*) FROM task_embeddings WHERE task_id = ?`
	var count int
	err := database.QueryRow(query, taskID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
