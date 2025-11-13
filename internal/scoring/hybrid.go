package scoring

import (
	"context"
	"log"

	"github.com/alexrabarts/focus-agent/internal/db"
)

// ScoringPhase determines which scoring strategy to use based on feedback count
type ScoringPhase int

const (
	// PhaseBootstrap: 0-20 feedback items - use LLM only, collect feedback
	PhaseBootstrap ScoringPhase = iota
	// PhaseHybrid: 20-100 feedback items - blend LLM + K-NN
	PhaseHybrid
	// PhaseKNN: 100+ feedback items - primarily K-NN with LLM fallback
	PhaseKNN
)

// Thresholds for phase transitions
const (
	BootstrapThreshold = 20
	HybridThreshold    = 100
)

// HybridScorer combines LLM-based scoring with K-NN feedback learning
type HybridScorer struct {
	knnScorer *KNNScorer
}

// NewHybridScorer creates a new hybrid scorer
func NewHybridScorer(knnScorer *KNNScorer) *HybridScorer {
	return &HybridScorer{
		knnScorer: knnScorer,
	}
}

// GetCurrentPhase determines the current scoring phase based on feedback count
func (hs *HybridScorer) GetCurrentPhase() (ScoringPhase, int, error) {
	count, err := hs.knnScorer.GetFeedbackCount()
	if err != nil {
		return PhaseBootstrap, 0, err
	}

	if count < BootstrapThreshold {
		return PhaseBootstrap, count, nil
	} else if count < HybridThreshold {
		return PhaseHybrid, count, nil
	}
	return PhaseKNN, count, nil
}

// CalculateScore calculates a task's priority score using the appropriate strategy
// Returns: finalScore, usedKNN (bool), currentPhase, error
func (hs *HybridScorer) CalculateScore(ctx context.Context, task *db.Task, llmBaseScore float64) (float64, bool, ScoringPhase, error) {
	phase, feedbackCount, err := hs.GetCurrentPhase()
	if err != nil {
		log.Printf("Failed to get scoring phase: %v", err)
		return llmBaseScore, false, PhaseBootstrap, nil // Fallback to LLM
	}

	log.Printf("Scoring phase: %v (%d feedback items)", phase, feedbackCount)

	switch phase {
	case PhaseBootstrap:
		// Pure LLM scoring - we're collecting training data
		return llmBaseScore, false, phase, nil

	case PhaseHybrid:
		// Blend LLM + K-NN
		// Weight K-NN more as we approach HybridThreshold
		// weight = (count - BootstrapThreshold) / (HybridThreshold - BootstrapThreshold)
		weight := float64(feedbackCount-BootstrapThreshold) / float64(HybridThreshold-BootstrapThreshold)

		knnScore, usedKNN, err := hs.knnScorer.ScoreWithKNN(ctx, task, llmBaseScore)
		if err != nil || !usedKNN {
			// Fallback to LLM if K-NN fails
			return llmBaseScore, false, phase, nil
		}

		// Blend: finalScore = (1-w)*LLM + w*KNN
		blendedScore := (1-weight)*llmBaseScore + weight*knnScore

		// Clamp to [1, 10]
		if blendedScore < 1.0 {
			blendedScore = 1.0
		} else if blendedScore > 10.0 {
			blendedScore = 10.0
		}

		log.Printf("Hybrid scoring: LLM=%.2f, KNN=%.2f, weight=%.2f, blended=%.2f",
			llmBaseScore, knnScore, weight, blendedScore)

		return blendedScore, true, phase, nil

	case PhaseKNN:
		// Primary K-NN with LLM fallback
		knnScore, usedKNN, err := hs.knnScorer.ScoreWithKNN(ctx, task, llmBaseScore)
		if err != nil || !usedKNN {
			// Fallback to LLM if K-NN unavailable (e.g., new task pattern)
			log.Printf("K-NN unavailable, falling back to LLM for task %s", task.ID)
			return llmBaseScore, false, phase, nil
		}

		log.Printf("K-NN scoring: %.2f (LLM base was %.2f)", knnScore, llmBaseScore)
		return knnScore, true, phase, nil

	default:
		return llmBaseScore, false, phase, nil
	}
}

// GetPhaseDescription returns a human-readable description of the scoring phase
func GetPhaseDescription(phase ScoringPhase, count int) string {
	switch phase {
	case PhaseBootstrap:
		return "Bootstrap: Collecting feedback data (LLM only)"
	case PhaseHybrid:
		progress := float64(count-BootstrapThreshold) / float64(HybridThreshold-BootstrapThreshold) * 100
		return "Hybrid: Blending LLM + K-NN (" + string(rune(int(progress))) + "% K-NN weight)"
	case PhaseKNN:
		return "K-NN: Learning from feedback (LLM fallback)"
	default:
		return "Unknown phase"
	}
}
