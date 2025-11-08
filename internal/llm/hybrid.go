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
	ollama            OllamaInterface          // Can be *OllamaClient or *DistributedOllamaClient
	distributedOllama *DistributedOllamaClient // Keep reference for shutdown
	claudePath        string
	gemini            *GeminiClient
	db                *db.DB
	config            *config.Config
	prompts           *PromptBuilder
}

// NewHybridClient creates a hybrid LLM client with fallback chain: Ollama -> Claude CLI -> Gemini
func NewHybridClient(geminiAPIKey string, database *db.DB, cfg *config.Config) (*HybridClient, error) {
	// Create centralized prompt builder
	prompts := NewPromptBuilder(cfg.Google.UserEmail)

	// Initialize Gemini client as final fallback
	geminiClient, err := NewGeminiClient(geminiAPIKey, database, cfg, prompts)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini fallback client: %w", err)
	}

	// Initialize Ollama client(s) if enabled
	var ollamaInterface OllamaInterface
	var distributedOllama *DistributedOllamaClient

	if cfg.Ollama.Enabled {
		// Use distributed client if multiple hosts configured
		if len(cfg.Ollama.Hosts) > 1 {
			distributedOllama = NewDistributedOllamaClient(cfg, prompts)
			if distributedOllama != nil {
				ollamaInterface = distributedOllama
			}
		} else if len(cfg.Ollama.Hosts) == 1 {
			// Single host - use simple client
			host := cfg.Ollama.Hosts[0]
			simpleClient := NewOllamaClient(host.URL, cfg.Ollama.Model, prompts)

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
		prompts:           prompts,
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

// extractTasksWithClaude uses Claude CLI to extract tasks from full message thread
func (h *HybridClient) extractTasksWithClaude(ctx context.Context, messages []*db.Message, frontComments []*db.FrontComment, frontMetadata *db.FrontMetadata, userEmail string) ([]*db.Task, error) {
	if h.claudePath == "" {
		return nil, fmt.Errorf("claude CLI not available")
	}

	// Build task extraction prompt with full message context + Front data
	prompt := h.prompts.BuildTaskExtractionWithConversationFlow(messages, frontComments, frontMetadata)

	// Check cache
	hash := h.gemini.hashPrompt(prompt)
	cached, err := h.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached Claude task extraction")
		return h.gemini.parseTasksFromResponse(cached.Response), nil
	}

	// Call Claude CLI with JSON output
	cmd := exec.CommandContext(ctx,
		h.claudePath,
		"-p",
		"--model", "haiku",
		"--dangerously-skip-permissions",
		"--output-format", "text",
		prompt,
	)

	startTime := time.Now()
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("claude CLI failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("claude CLI execution failed: %w", err)
	}

	response := strings.TrimSpace(string(output))
	log.Printf("✓ Claude CLI succeeded for task extraction (%.2fs)", time.Since(startTime).Seconds())

	// Cache the response
	tokens := h.gemini.estimateTokens(prompt + response)
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  response,
		Model:     "claude-haiku",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	h.db.SaveCachedResponse(cache)
	h.db.LogUsage("claude", "extract_tasks", tokens, 0, time.Since(startTime), nil)

	// Parse tasks from pipe-delimited response (same format as Gemini/Ollama)
	return h.gemini.parseTasksFromResponse(response), nil
}

// parseTasksFromJSON parses Claude's JSON response into tasks
func (h *HybridClient) parseTasksFromJSON(response string, userEmail string) ([]*db.Task, error) {
	// Extract JSON from response (might have markdown code blocks)
	jsonStr := response
	if strings.Contains(response, "```json") {
		start := strings.Index(response, "```json") + 7
		end := strings.LastIndex(response, "```")
		if start > 7 && end > start {
			jsonStr = strings.TrimSpace(response[start:end])
		}
	} else if strings.Contains(response, "```") {
		start := strings.Index(response, "```") + 3
		end := strings.LastIndex(response, "```")
		if start > 3 && end > start {
			jsonStr = strings.TrimSpace(response[start:end])
		}
	}

	// Parse JSON
	var result struct {
		Tasks []struct {
			Title     string `json:"title"`
			Priority  string `json:"priority"`
			DueDate   string `json:"due_date"`
			Reasoning string `json:"reasoning"`
		} `json:"tasks"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w (response: %s)", err, jsonStr)
	}

	// Convert to db.Task objects
	var tasks []*db.Task
	for _, t := range result.Tasks {
		if t.Title == "" {
			continue
		}

		// Map priority string to urgency/impact scores
		urgency := 3 // Default medium
		impact := 3  // Default medium
		switch strings.ToLower(t.Priority) {
		case "high":
			urgency = 4
			impact = 4
		case "low":
			urgency = 2
			impact = 2
		}

		task := &db.Task{
			Title:   t.Title,
			Urgency: urgency,
			Impact:  impact,
			Effort:  "M", // Default effort
			Status:  "pending",
			ID:      fmt.Sprintf("ai_%d", time.Now().UnixNano()),
			Source:  "ai",
		}

		if userEmail != "" {
			task.Stakeholder = userEmail
		} else {
			task.Stakeholder = "me"
		}

		// Parse due date if provided
		if t.DueDate != "" && t.DueDate != "0000-00-00" {
			if dueTime, err := time.Parse("2006-01-02", t.DueDate); err == nil {
				task.DueTS = &dueTime
			}
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

// SummarizeThread summarizes an email thread (Claude primary, Gemini fallback)
func (h *HybridClient) SummarizeThread(ctx context.Context, messages []*db.Message) (string, error) {
	// Build prompt
	prompt := h.prompts.BuildThreadSummary(messages)

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
		prompt := h.prompts.BuildThreadSummary(messages)
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

// ExtractTasks extracts action items with 3-tier fallback
func (h *HybridClient) ExtractTasks(ctx context.Context, content string) ([]*db.Task, error) {
	return h.ExtractTasksFromMessages(ctx, content, nil, nil, nil)
}

// ExtractTasksFromMessages extracts tasks with 3-tier fallback (Ollama → Claude → Gemini)
// Now accepts Front data for enhanced context
func (h *HybridClient) ExtractTasksFromMessages(ctx context.Context, content string, messages []*db.Message, frontComments []*db.FrontComment, frontMetadata *db.FrontMetadata) ([]*db.Task, error) {
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

	// Try Claude CLI second (with full message context + Front data for better intelligence)
	if h.claudePath != "" && len(messages) > 0 {
		tasks, err := h.extractTasksWithClaude(ctx, messages, frontComments, frontMetadata, userEmail)
		if err == nil {
			if len(tasks) > 0 {
				log.Printf("Extracted %d tasks using Claude CLI", len(tasks))
			} else {
				log.Printf("Claude CLI processed thread successfully - no tasks found")
			}
			return tasks, nil
		}
		log.Printf("Claude task extraction failed, falling back to Gemini: %v", err)
	}

	// Final fallback to Gemini (handles sent email detection and filtering)
	log.Printf("Using Gemini for task extraction (fallback)")
	return h.gemini.ExtractTasksFromMessages(ctx, content, messages, frontComments, frontMetadata)
}

// EnrichTaskDescription generates rich contextual descriptions (Ollama -> Claude CLI -> Gemini fallback)
func (h *HybridClient) EnrichTaskDescription(ctx context.Context, task *db.Task, messages []*db.Message) (string, error) {
	// Build prompt
	prompt := h.prompts.BuildTaskEnrichment(task, messages)

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
	// Build prompt
	prompt := h.prompts.BuildStrategicAlignment(task, priorities)

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
	prompt := h.prompts.BuildReply(thread, goal)

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
	prompt := h.prompts.BuildMeetingPrep(event, relatedDocs)

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
