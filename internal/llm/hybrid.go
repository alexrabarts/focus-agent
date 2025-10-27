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

// OllamaInterface defines methods that both OllamaClient and DistributedOllamaClient implement
type OllamaInterface interface {
	SummarizeThread(ctx context.Context, messages []*db.Message) (string, error)
	ExtractTasks(ctx context.Context, content, userEmail string) ([]*db.Task, error)
	EnrichTaskDescription(ctx context.Context, prompt string) (string, error)
	EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error)
}

// HybridClient uses Ollama as primary, Claude CLI as secondary, and Gemini as final fallback
type HybridClient struct {
	ollama         OllamaInterface // Can be *OllamaClient or *DistributedOllamaClient
	distributedOllama *DistributedOllamaClient // Keep reference for shutdown
	claudePath     string
	gemini         *GeminiClient
	db             *db.DB
	config         *config.Config
}

// NewHybridClient creates a hybrid LLM client with fallback chain: Ollama -> Claude CLI -> Gemini
func NewHybridClient(geminiAPIKey string, database *db.DB, cfg *config.Config) (*HybridClient, error) {
	// Initialize Gemini client as final fallback
	geminiClient, err := NewGeminiClient(geminiAPIKey, database, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini fallback client: %w", err)
	}

	// Initialize Ollama client(s) if enabled
	var ollamaInterface OllamaInterface
	var distributedOllama *DistributedOllamaClient

	if cfg.Ollama.Enabled {
		// Use distributed client if multiple hosts configured
		if len(cfg.Ollama.Hosts) > 1 {
			distributedOllama = NewDistributedOllamaClient(cfg)
			if distributedOllama != nil {
				ollamaInterface = distributedOllama
			}
		} else if len(cfg.Ollama.Hosts) == 1 {
			// Single host - use simple client
			host := cfg.Ollama.Hosts[0]
			simpleClient := NewOllamaClient(host.URL, cfg.Ollama.Model)

			// Test connectivity
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := simpleClient.Ping(ctx); err != nil {
				log.Printf("Warning: Ollama server unreachable at %s: %v (will use fallbacks)", host.URL, err)
			} else {
				log.Printf("Ollama client initialized: %s (model: %s)", host.URL, cfg.Ollama.Model)
				ollamaInterface = simpleClient
			}
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
		ollama:            ollamaInterface,
		distributedOllama: distributedOllama,
		claudePath:        claudePath,
		gemini:            geminiClient,
		db:                database,
		config:            cfg,
	}, nil
}

// Close closes all underlying clients
func (h *HybridClient) Close() error {
	// Shutdown distributed Ollama worker pool if running
	if h.distributedOllama != nil {
		h.distributedOllama.Shutdown()
	}

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

// SummarizeThreadWithModelSelection summarizes with 3-tier fallback (Ollama → Claude → Gemini)
func (h *HybridClient) SummarizeThreadWithModelSelection(ctx context.Context, messages []*db.Message, metadata ThreadMetadata) (string, error) {
	// Try Ollama first (free, unlimited local processing)
	if h.ollama != nil {
		summary, err := h.ollama.SummarizeThread(ctx, messages)
		if err == nil && summary != "" {
			log.Printf("Thread summary generated using Ollama (qwen2.5)")
			return summary, nil
		}
		log.Printf("Ollama summarization failed, falling back to Claude: %v", err)
	}

	// Try Claude CLI second (free, remote API)
	if h.claudePath != "" {
		prompt := h.buildThreadSummaryPrompt(messages)
		summary, err := h.callClaude(ctx, prompt)
		if err == nil && summary != "" {
			log.Printf("Thread summary generated using Claude CLI")
			return summary, nil
		}
		log.Printf("Claude summarization failed, falling back to Gemini: %v", err)
	}

	// Final fallback to Gemini (with Pro/Flash model selection)
	log.Printf("Using Gemini for thread summarization (fallback)")
	return h.gemini.SummarizeThreadWithModelSelection(ctx, messages, metadata)
}

// buildThreadSummaryPrompt creates a prompt for thread summarization (reused by Claude fallback)
func (h *HybridClient) buildThreadSummaryPrompt(messages []*db.Message) string {
	var prompt strings.Builder

	prompt.WriteString("Summarize this email thread concisely. Focus on:\n")
	prompt.WriteString("1. Main topic/issue\n")
	prompt.WriteString("2. Key decisions or action items\n")
	prompt.WriteString("3. Who needs to do what\n")
	prompt.WriteString("4. Deadlines mentioned\n")
	prompt.WriteString("5. Any risks or blockers\n\n")

	prompt.WriteString("Thread:\n")
	for _, msg := range messages {
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("Date: %s\n", msg.Timestamp.Format("Jan 2, 3:04 PM")))
		prompt.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
		prompt.WriteString(fmt.Sprintf("Content: %s\n\n", msg.Snippet))
	}

	prompt.WriteString("Summary (be concise, max 200 words):")

	return prompt.String()
}

// ExtractTasks extracts action items with 3-tier fallback
func (h *HybridClient) ExtractTasks(ctx context.Context, content string) ([]*db.Task, error) {
	return h.ExtractTasksFromMessages(ctx, content, nil)
}

// ExtractTasksFromMessages extracts tasks with 3-tier fallback (Ollama → Claude → Gemini)
func (h *HybridClient) ExtractTasksFromMessages(ctx context.Context, content string, messages []*db.Message) ([]*db.Task, error) {
	userEmail := ""
	if h.gemini != nil && h.gemini.config != nil {
		userEmail = h.gemini.config.Google.UserEmail
	}

	// Try Ollama first (free, unlimited local processing)
	if h.ollama != nil {
		tasks, err := h.ollama.ExtractTasks(ctx, content, userEmail)
		if err == nil {
			// Ollama succeeded - return results even if empty (no tasks found)
			if len(tasks) > 0 {
				log.Printf("Extracted %d tasks using Ollama (qwen2.5)", len(tasks))
			} else {
				log.Printf("Ollama processed thread successfully - no tasks found")
			}
			return tasks, nil
		}
		log.Printf("Ollama task extraction failed, falling back to Claude: %v", err)
	}

	// Try Claude CLI second (would need implementation for task parsing)
	// Skipping Claude for now as it requires custom parser implementation
	// TODO: Add Claude task extraction with custom parser

	// Final fallback to Gemini (handles sent email detection and filtering)
	log.Printf("Using Gemini for task extraction (fallback)")
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

	// Try Ollama first (distributed or single client)
	if h.ollama != nil {
		startTime := time.Now()
		enrichedDesc, err := h.ollama.EnrichTaskDescription(ctx, prompt)
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
