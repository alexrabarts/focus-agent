package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// HybridClient uses Ollama as primary, Claude CLI as secondary, and Gemini as final fallback
type HybridClient struct {
	ollama     *OllamaClient
	claudePath string
	gemini     *GeminiClient
	db         *db.DB
	config     *config.Config
}

// NewHybridClient creates a hybrid LLM client with fallback chain: Ollama -> Claude CLI -> Gemini
func NewHybridClient(geminiAPIKey string, database *db.DB, cfg *config.Config) (*HybridClient, error) {
	// Initialize Gemini client as final fallback
	geminiClient, err := NewGeminiClient(geminiAPIKey, database, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini fallback client: %w", err)
	}

	// Initialize Ollama client if enabled
	var ollamaClient *OllamaClient
	if cfg.Ollama.Enabled {
		ollamaClient = NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)

		// Test connectivity
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := ollamaClient.Ping(ctx); err != nil {
			log.Printf("Warning: Ollama server unreachable at %s: %v (will use fallbacks)", cfg.Ollama.URL, err)
			ollamaClient = nil // Disable if unreachable
		} else {
			log.Printf("Ollama client initialized: %s (model: %s)", cfg.Ollama.URL, cfg.Ollama.Model)
		}
	} else {
		log.Printf("Ollama disabled in config")
	}

	// Find claude CLI - try full path first, then PATH
	claudePath := "/home/alex/.claude/local/claude"
	if _, err := exec.LookPath(claudePath); err != nil {
		// Try finding in PATH as fallback
		claudePath, err = exec.LookPath("claude")
		if err != nil {
			log.Printf("Warning: claude CLI not found: %v", err)
			claudePath = ""
		} else {
			log.Printf("Claude CLI found at: %s", claudePath)
		}
	} else {
		log.Printf("Claude CLI found at: %s", claudePath)
	}

	return &HybridClient{
		ollama:     ollamaClient,
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
	return h.ExtractTasksFromMessages(ctx, content, nil)
}

func (h *HybridClient) ExtractTasksFromMessages(ctx context.Context, content string, messages []*db.Message) ([]*db.Task, error) {
	// Delegate to Gemini client which handles sent email detection
	// For now, we'll use Gemini directly for task extraction with message context
	// TODO: Consider adding Claude/Ollama fallback for this method
	return h.gemini.ExtractTasksFromMessages(ctx, content, messages)
}

// EnrichTaskDescription generates rich contextual descriptions (Ollama -> Claude CLI -> Gemini fallback)
func (h *HybridClient) EnrichTaskDescription(ctx context.Context, task *db.Task, messages []*db.Message) (string, error) {
	// Build prompt
	prompt := h.gemini.buildTaskEnrichmentPrompt(task, messages)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached task enrichment")
		return cached.Response, nil
	}

	// Try Ollama first
	if h.ollama != nil {
		startTime := time.Now()
		enrichedDesc, err := h.ollama.Generate(ctx, prompt)
		if err == nil {
			log.Printf("✓ Ollama succeeded for EnrichTaskDescription (%.2fs)", time.Since(startTime).Seconds())

			// Cache the response
			tokens := h.gemini.estimateTokens(prompt + enrichedDesc)
			cache := &db.LLMCache{
				Hash:      hash,
				Prompt:    prompt,
				Response:  enrichedDesc,
				Model:     "ollama-" + h.config.Ollama.Model,
				Tokens:    tokens,
				ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
			}
			h.db.SaveCachedResponse(cache)

			// Log usage (free, so cost = 0)
			h.db.LogUsage("ollama", "enrich_task", tokens, 0, time.Since(startTime), nil)

			return enrichedDesc, nil
		}
		log.Printf("⚠ Ollama failed for EnrichTaskDescription: %v", err)
	}

	// Try Claude CLI second
	startTime := time.Now()
	enrichedDesc, err := h.callClaude(ctx, prompt)
	if err == nil {
		log.Printf("✓ Claude CLI succeeded for EnrichTaskDescription (%.2fs)", time.Since(startTime).Seconds())

		// Cache the response
		tokens := h.gemini.estimateTokens(prompt + enrichedDesc)
		cache := &db.LLMCache{
			Hash:      hash,
			Prompt:    prompt,
			Response:  enrichedDesc,
			Model:     "claude-haiku",
			Tokens:    tokens,
			ExpiresAt: time.Now().Add(h.gemini.cacheTTL),
		}
		h.db.SaveCachedResponse(cache)

		// Log usage
		h.db.LogUsage("claude", "enrich_task", tokens, 0, time.Since(startTime), nil)

		return enrichedDesc, nil
	}

	// Final fallback to Gemini
	log.Printf("⚠ Claude CLI failed, falling back to Gemini: %v", err)
	return h.gemini.EnrichTaskDescription(ctx, task, messages)
}

// EvaluateStrategicAlignment evaluates strategic alignment (Ollama -> Claude -> Gemini fallback)
func (h *HybridClient) EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error) {
	// Build prompt (using gemini's prompt builder for consistency)
	prompt := h.gemini.buildStrategicAlignmentPrompt(task, priorities)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached strategic alignment")
		return h.gemini.parseStrategicAlignmentResponse(cached.Response), nil
	}

	// Try Ollama first (qwen2.5:7b with JSON format)
	if h.ollama != nil {
		startTime := time.Now()
		result, err := h.ollama.EvaluateStrategicAlignment(ctx, task, priorities)
		if err == nil {
			log.Printf("✓ Ollama succeeded for EvaluateStrategicAlignment (%.2fs)", time.Since(startTime).Seconds())

			// Convert result back to JSON for caching
			resultJSON, _ := json.Marshal(result)
			response := string(resultJSON)

			// Cache the response
			tokens := h.gemini.estimateTokens(prompt + response)
			cache := &db.LLMCache{
				Hash:      hash,
				Prompt:    prompt,
				Response:  response,
				Model:     "ollama-" + h.config.Ollama.Model,
				Tokens:    tokens,
				ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7 days like Gemini
			}
			h.db.SaveCachedResponse(cache)

			// Log usage (free, so cost = 0)
			h.db.LogUsage("ollama", "strategic_alignment", tokens, 0, time.Since(startTime), nil)

			return result, nil
		}
		log.Printf("⚠ Ollama failed for EvaluateStrategicAlignment: %v", err)
	}

	// Add JSON formatting instruction for Claude
	claudePrompt := prompt + "\n\nIMPORTANT: Respond with ONLY a valid JSON object, no markdown formatting or explanation. The JSON must have these exact fields: score (number), okrs (array of strings), focus_areas (array of strings), projects (array of strings), reasoning (string)."

	// Try Claude CLI second
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

	// Final fallback to Gemini
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
