package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/embeddings"
)

// Neighbor represents a task with feedback and its similarity score
type Neighbor struct {
	TaskID           string
	Vote             int     // -1 (thumbs down) or +1 (thumbs up)
	Similarity       float64 // Cosine similarity to target task
	FeedbackReason   string  // Optional reason text
	OriginalScore    float64 // The score before user feedback
	AdjustedScore    float64 // The score after user adjustment
}

// KNNScorer handles K-NN based priority scoring using embeddings and user feedback
type KNNScorer struct {
	db     *db.DB
	embClient *embeddings.Client
	k      int // Number of neighbors to consider
}

// NewKNNScorer creates a new K-NN scorer
func NewKNNScorer(database *db.DB, embeddingsClient *embeddings.Client, k int) *KNNScorer {
	if k <= 0 {
		k = 5 // Default to 5 neighbors
	}
	return &KNNScorer{
		db:     database,
		embClient: embeddingsClient,
		k:      k,
	}
}

// GetFeedbackCount returns the total number of feedback items
func (knn *KNNScorer) GetFeedbackCount() (int, error) {
	query := `SELECT COUNT(*) FROM priority_feedback`
	var count int
	err := knn.db.QueryRow(query).Scan(&count)
	return count, err
}

// FindNearestNeighbors finds the K most similar tasks with feedback
func (knn *KNNScorer) FindNearestNeighbors(ctx context.Context, targetTaskID string) ([]Neighbor, error) {
	// Get target task's embedding
	targetEmb, err := embeddings.GetTaskEmbedding(knn.db, targetTaskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get target embedding: %w", err)
	}

	// Query all tasks with embeddings and feedback
	query := `
		SELECT
			te.task_id,
			te.embedding,
			pf.user_vote,
			pf.reason,
			pf.original_score,
			pf.adjusted_score
		FROM task_embeddings te
		INNER JOIN priority_feedback pf ON te.task_id = pf.task_id
		WHERE te.task_id != ?
	`

	rows, err := knn.db.Query(query, targetTaskID)
	if err != nil {
		return nil, fmt.Errorf("failed to query neighbors: %w", err)
	}
	defer rows.Close()

	var neighbors []Neighbor
	for rows.Next() {
		var neighbor Neighbor
		var embeddingJSON string

		err := rows.Scan(
			&neighbor.TaskID,
			&embeddingJSON,
			&neighbor.Vote,
			&neighbor.FeedbackReason,
			&neighbor.OriginalScore,
			&neighbor.AdjustedScore,
		)
		if err != nil {
			log.Printf("Failed to scan neighbor: %v", err)
			continue
		}

		// Parse embedding
		var embedding []float64
		if err := json.Unmarshal([]byte(embeddingJSON), &embedding); err != nil {
			log.Printf("Failed to unmarshal embedding: %v", err)
			continue
		}

		// Calculate similarity
		similarity, err := embeddings.CosineSimilarity(targetEmb.Embedding, embedding)
		if err != nil {
			log.Printf("Failed to calculate similarity: %v", err)
			continue
		}

		neighbor.Similarity = similarity
		neighbors = append(neighbors, neighbor)
	}

	if len(neighbors) == 0 {
		return nil, nil // No neighbors with feedback yet
	}

	// Sort by similarity (descending)
	sort.Slice(neighbors, func(i, j int) bool {
		return neighbors[i].Similarity > neighbors[j].Similarity
	})

	// Take top K neighbors
	if len(neighbors) > knn.k {
		neighbors = neighbors[:knn.k]
	}

	return neighbors, nil
}

// CalculateKNNAdjustment calculates a priority score adjustment based on K-NN feedback
// Returns an adjustment value to add to the base score
func (knn *KNNScorer) CalculateKNNAdjustment(ctx context.Context, taskID string) (float64, error) {
	neighbors, err := knn.FindNearestNeighbors(ctx, taskID)
	if err != nil {
		return 0, err
	}

	if len(neighbors) == 0 {
		return 0, nil // No adjustment if no feedback available
	}

	// Calculate weighted vote
	// Each neighbor contributes: vote * similarity
	// vote = +1 (thumbs up) or -1 (thumbs down)
	var weightedSum float64
	var totalWeight float64

	for _, neighbor := range neighbors {
		weight := neighbor.Similarity
		if weight < 0 {
			weight = 0 // Ignore negative similarities
		}

		weightedSum += float64(neighbor.Vote) * weight
		totalWeight += weight
	}

	if totalWeight == 0 {
		return 0, nil
	}

	// Normalized adjustment: [-1.0, +1.0]
	// Scale to a reasonable adjustment range (e.g., ±2 points on a 1-10 scale)
	adjustment := (weightedSum / totalWeight) * 2.0

	return adjustment, nil
}

// ScoreWithKNN scores a task using K-NN based on similar tasks with feedback
// Returns adjusted score and whether K-NN was used
func (knn *KNNScorer) ScoreWithKNN(ctx context.Context, task *db.Task, baseScore float64) (float64, bool, error) {
	// Check if task has embedding
	hasEmb, err := embeddings.HasTaskEmbedding(knn.db, task.ID)
	if err != nil {
		return baseScore, false, err
	}

	if !hasEmb {
		// Generate embedding if missing
		taskEmb, err := embeddings.GenerateTaskEmbedding(ctx, knn.embClient, task)
		if err != nil {
			log.Printf("Failed to generate embedding for task %s: %v", task.ID, err)
			return baseScore, false, nil // Fallback to base score
		}

		if err := embeddings.SaveTaskEmbedding(knn.db, taskEmb); err != nil {
			log.Printf("Failed to save embedding for task %s: %v", task.ID, err)
		}
	}

	// Calculate K-NN adjustment
	adjustment, err := knn.CalculateKNNAdjustment(ctx, task.ID)
	if err != nil {
		log.Printf("Failed to calculate K-NN adjustment: %v", err)
		return baseScore, false, nil // Fallback to base score
	}

	// Apply adjustment
	finalScore := baseScore + adjustment

	// Clamp to [1, 10] range
	if finalScore < 1.0 {
		finalScore = 1.0
	} else if finalScore > 10.0 {
		finalScore = 10.0
	}

	return finalScore, adjustment != 0, nil
}

// SaveFeedback records user feedback on a task's priority score
func SaveFeedback(database *db.DB, taskID string, vote int, reason string, originalScore, adjustedScore float64) error {
	// Generate feedback ID with timestamp
	feedbackID := fmt.Sprintf("fb_%s_%d", taskID, time.Now().Unix())

	query := `
		INSERT INTO priority_feedback
		(id, task_id, user_vote, reason, original_score, adjusted_score, feedback_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err := database.Exec(
		query,
		feedbackID,
		taskID,
		vote,
		reason,
		originalScore,
		adjustedScore,
		time.Now().Unix(),
	)

	if err != nil {
		return fmt.Errorf("failed to insert feedback: %w", err)
	}

	return nil
}
