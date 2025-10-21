package llm

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// HybridClient uses Claude CLI as primary with Gemini as fallback
type HybridClient struct {
	claudePath string
	gemini     *GeminiClient
	db         *db.DB
	config     *config.Config
}

// NewHybridClient creates a hybrid LLM client
func NewHybridClient(geminiAPIKey string, database *db.DB, cfg *config.Config) (*HybridClient, error) {
	// Initialize Gemini client as fallback
	geminiClient, err := NewGeminiClient(geminiAPIKey, database, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini fallback client: %w", err)
	}

	// Find claude CLI - try full path first, then PATH
	claudePath := "/home/alex/.claude/local/claude"
	if _, err := exec.LookPath(claudePath); err != nil {
		// Try finding in PATH as fallback
		claudePath, err = exec.LookPath("claude")
		if err != nil {
			log.Printf("Warning: claude CLI not found, will use Gemini only: %v", err)
			claudePath = ""
		} else {
			log.Printf("Claude CLI found at: %s", claudePath)
		}
	} else {
		log.Printf("Claude CLI found at: %s", claudePath)
	}

	return &HybridClient{
		claudePath: claudePath,
		gemini:     geminiClient,
		db:         database,
		config:     cfg,
	}, nil
}

// Close closes all underlying clients
func (h *HybridClient) Close() error {
	if h.gemini != nil {
		return h.gemini.Close()
	}
	return nil
}

// callClaude executes the claude CLI with the given prompt
func (h *HybridClient) callClaude(ctx context.Context, prompt string) (string, error) {
	if h.claudePath == "" {
		return "", fmt.Errorf("claude CLI not available")
	}

	cmd := exec.CommandContext(ctx,
		h.claudePath,
		"-p",
		"--model", "haiku",
		"--dangerously-skip-permissions",
		"--output-format", "text",
		prompt,
	)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude CLI failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("claude CLI execution failed: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// SummarizeThread summarizes an email thread (Claude primary, Gemini fallback)
func (h *HybridClient) SummarizeThread(ctx context.Context, messages []*db.Message) (string, error) {
	// Build prompt (reuse Gemini's prompt builder)
	prompt := h.gemini.buildThreadSummaryPrompt(messages)

	// Check cache first
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached summary for thread")
		return cached.Response, nil
	}

	// Try Claude CLI first
	startTime := time.Now()
	summary, err := h.callClaude(ctx, prompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for SummarizeThread (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + summary)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  summary,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "summarize_thread", tokens, 0, time.Since(startTime), nil)

		return summary, nil
	}

	// Fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.SummarizeThread(ctx, messages)
}

// SummarizeThreadWithModelSelection summarizes with smart model selection (Gemini only for now)
func (h *HybridClient) SummarizeThreadWithModelSelection(ctx context.Context, messages []*db.Message, metadata ThreadMetadata) (string, error) {
	// For now, delegate to Gemini which has the Pro model selection logic
	// TODO: Could add Claude Sonnet as "Pro" tier in the future
	return h.gemini.SummarizeThreadWithModelSelection(ctx, messages, metadata)
}

// ExtractTasks extracts action items (Claude primary, Gemini fallback)
func (h *HybridClient) ExtractTasks(ctx context.Context, content string) ([]*db.Task, error) {
	// Build prompt
	prompt := h.gemini.buildTaskExtractionPrompt(content)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached task extraction")
		return h.gemini.filterTasksForUser(h.gemini.parseTasksFromResponse(cached.Response)), nil
	}

	// Try Claude CLI first
	startTime := time.Now()
	response, err := h.callClaude(ctx, prompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for ExtractTasks (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + response)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  response,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "extract_tasks", tokens, 0, time.Since(startTime), nil)

		// Parse and filter tasks
		tasks := h.gemini.parseTasksFromResponse(response)
		return h.gemini.filterTasksForUser(tasks), nil
	}

	// Fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.ExtractTasks(ctx, content)
}

// EvaluateStrategicAlignment evaluates strategic alignment (Claude primary, Gemini fallback)
func (h *HybridClient) EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error) {
	// Build prompt
	prompt := h.gemini.buildStrategicAlignmentPrompt(task, priorities)

	// Add JSON formatting instruction for Claude
	claudePrompt := prompt + "\n\nIMPORTANT: Respond with ONLY a valid JSON object, no markdown formatting or explanation. The JSON must have these exact fields: score (number), okrs (array of strings), focus_areas (array of strings), projects (array of strings), reasoning (string)."

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached strategic alignment")
		return h.gemini.parseStrategicAlignmentResponse(cached.Response), nil
	}

	// Try Claude CLI first
	startTime := time.Now()
	response, err := h.callClaude(ctx, claudePrompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for EvaluateStrategicAlignment (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + response)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  response,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7 days like Gemini
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "strategic_alignment", tokens, 0, time.Since(startTime), nil)

		// Parse and return
		return h.gemini.parseStrategicAlignmentResponse(response), nil
	}

	// Fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.EvaluateStrategicAlignment(ctx, task, priorities)
}

// DraftReply drafts an email reply (Claude primary, Gemini fallback)
func (h *HybridClient) DraftReply(ctx context.Context, thread []*db.Message, goal string) (string, error) {
	// Build prompt
	prompt := h.gemini.buildReplyPrompt(thread, goal)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached draft reply")
		return cached.Response, nil
	}

	// Try Claude CLI first
	startTime := time.Now()
	reply, err := h.callClaude(ctx, prompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for DraftReply (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + reply)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  reply,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "draft_reply", tokens, 0, time.Since(startTime), nil)

		return reply, nil
	}

	// Fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.DraftReply(ctx, thread, goal)
}

// GenerateMeetingPrep generates meeting preparation notes (Claude primary, Gemini fallback)
func (h *HybridClient) GenerateMeetingPrep(ctx context.Context, event *db.Event, relatedDocs []*db.Document) (string, error) {
	// Build prompt
	prompt := h.gemini.buildMeetingPrepPrompt(event, relatedDocs)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached meeting prep")
		return cached.Response, nil
	}

	// Try Claude CLI first
	startTime := time.Now()
	prep, err := h.callClaude(ctx, prompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for GenerateMeetingPrep (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + prep)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  prep,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "meeting_prep", tokens, 0, time.Since(startTime), nil)

		return prep, nil
	}

	// Fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.GenerateMeetingPrep(ctx, event, relatedDocs)
}
