package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"golang.org/x/time/rate"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// GeminiClient handles Gemini API operations
type GeminiClient struct {
	client          *genai.Client
	model           *genai.GenerativeModel
	proModel        *genai.GenerativeModel
	db              *db.DB
	config          *config.Config
	rateLimiter     *rate.Limiter
	proRateLimiter  *rate.Limiter
	cacheTTL        time.Duration
}

// ThreadMetadata contains metadata for smart model selection
type ThreadMetadata struct {
	QueueSize      int       // Number of threads waiting to be processed
	SenderEmail    string    // Email address of sender
	Timestamp      time.Time // When the thread was received
	MessageCount   int       // Number of messages in thread
}

// NewGeminiClient creates a new Gemini client
func NewGeminiClient(apiKey string, database *db.DB, cfg *config.Config) (*GeminiClient, error) {
	ctx := context.Background()

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Use configured model (defaults to gemini-2.5-flash)
	modelName := cfg.Gemini.Model
	if modelName == "" {
		modelName = "gemini-2.5-flash"
	}
	model := client.GenerativeModel(modelName)

	// Configure model parameters from config
	if cfg.Gemini.Temperature > 0 {
		model.SetTemperature(cfg.Gemini.Temperature)
	}
	model.SetTopK(40)
	model.SetTopP(0.95)
	if cfg.Gemini.MaxTokens > 0 {
		model.SetMaxOutputTokens(int32(cfg.Gemini.MaxTokens))
	}

	// Safety settings - allow most content for productivity use
	model.SafetySettings = []*genai.SafetySetting{
		{
			Category:  genai.HarmCategoryHarassment,
			Threshold: genai.HarmBlockOnlyHigh,
		},
		{
			Category:  genai.HarmCategoryHateSpeech,
			Threshold: genai.HarmBlockOnlyHigh,
		},
		{
			Category:  genai.HarmCategoryDangerousContent,
			Threshold: genai.HarmBlockOnlyHigh,
		},
	}

	// Create rate limiter based on model
	rateLimit := getRateLimit(cfg, modelName)
	limiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(rateLimit)), 1)

	// Create Pro model and rate limiter for strategic use
	proModel := client.GenerativeModel("gemini-2.5-pro")
	if cfg.Gemini.Temperature > 0 {
		proModel.SetTemperature(cfg.Gemini.Temperature)
	}
	proModel.SetTopK(40)
	proModel.SetTopP(0.95)
	if cfg.Gemini.MaxTokens > 0 {
		proModel.SetMaxOutputTokens(int32(cfg.Gemini.MaxTokens))
	}
	proModel.SafetySettings = model.SafetySettings

	proRateLimit := getRateLimit(cfg, "gemini-2.5-pro")
	proLimiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(proRateLimit)), 1)

	log.Printf("Initialized Gemini client with model %s (rate limit: %d RPM)", modelName, rateLimit)
	log.Printf("Pro model available: gemini-2.5-pro (rate limit: %d RPM)", proRateLimit)

	cacheTTL := time.Duration(cfg.Gemini.CacheHours) * time.Hour
	if cacheTTL == 0 {
		cacheTTL = 24 * time.Hour
	}

	return &GeminiClient{
		client:         client,
		model:          model,
		proModel:       proModel,
		db:             database,
		config:         cfg,
		rateLimiter:    limiter,
		proRateLimiter: proLimiter,
		cacheTTL:       cacheTTL,
	}, nil
}

// getRateLimit returns the rate limit for a given model
func getRateLimit(cfg *config.Config, model string) int {
	if limit, ok := cfg.Gemini.RateLimits[model]; ok {
		return limit
	}
	return cfg.Gemini.DefaultRateLimit
}

// Close closes the Gemini client
func (g *GeminiClient) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}

// generateWithRetry wraps GenerateContent with exponential backoff retry logic
func (g *GeminiClient) generateWithRetry(ctx context.Context, prompt genai.Text) (*genai.GenerateContentResponse, error) {
	return g.generateWithRetryForModel(ctx, prompt, g.model)
}

// DailyQuotaExceededError is returned when the daily API quota is exhausted
type DailyQuotaExceededError struct {
	Message string
}

func (e *DailyQuotaExceededError) Error() string {
	return e.Message
}

// generateWithRetryForModel wraps GenerateContent with exponential backoff retry logic for a specific model
func (g *GeminiClient) generateWithRetryForModel(ctx context.Context, prompt genai.Text, model *genai.GenerativeModel) (*genai.GenerateContentResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= g.config.Gemini.MaxRetries; attempt++ {
		resp, err := model.GenerateContent(ctx, prompt)
		if err == nil {
			return resp, nil
		}

		// Check if it's a rate limit error (429)
		if apiErr, ok := err.(*googleapi.Error); ok && apiErr.Code == 429 {
			// Check if it's a daily quota error (not just per-minute rate limit)
			errMsg := apiErr.Error()
			if strings.Contains(errMsg, "generate_content_free_tier_requests") ||
			   strings.Contains(errMsg, "current quota") {
				// Daily quota exhausted - don't retry
				log.Printf("Daily quota exhausted (250 requests/day limit). Processing will resume when quota resets.")
				return nil, &DailyQuotaExceededError{
					Message: "Daily API quota exhausted (250 requests/day free tier limit). Quota resets in ~24 hours.",
				}
			}

			// Per-minute rate limit - retry with backoff
			if !g.config.Gemini.RetryOnRateLimit || attempt >= g.config.Gemini.MaxRetries {
				return nil, err
			}

			// Calculate exponential backoff delay
			backoffDelay := time.Duration(g.config.Gemini.BaseRetryDelay) * time.Second * (1 << uint(attempt))

			log.Printf("Rate limit hit (429), retrying in %v (attempt %d/%d)", backoffDelay, attempt+1, g.config.Gemini.MaxRetries)

			// Wait with backoff
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoffDelay):
				// Continue to next attempt
			}

			lastErr = err
			continue
		}

		// For non-rate-limit errors, fail immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// SummarizeThread summarizes an email thread
func (g *GeminiClient) SummarizeThread(ctx context.Context, messages []*db.Message) (string, error) {
	// Build prompt
	prompt := g.buildThreadSummaryPrompt(messages)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached summary for thread")
		return cached.Response, nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate summary with retry
	startTime := time.Now()
	resp, err := g.generateWithRetry(ctx, genai.Text(prompt))
	if err != nil {
		g.db.LogUsage("gemini", "summarize_thread", 0, 0, time.Since(startTime), err)
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	// Extract text from response
	summary := g.extractText(resp)

	// Calculate token usage (approximate)
	tokens := g.estimateTokens(prompt + summary)
	cost := g.calculateCost(tokens)

	// Log usage
	g.db.LogUsage("gemini", "summarize_thread", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  summary,
		Model:     "gemini-1.5-flash",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return summary, nil
}

// SummarizeThreadWithModelSelection summarizes a thread using smart model selection
func (g *GeminiClient) SummarizeThreadWithModelSelection(ctx context.Context, messages []*db.Message, metadata ThreadMetadata) (string, error) {
	// Calculate priority score for model selection
	score := 0
	reasoning := []string{}

	// Small queue (≤10 threads): +3 points
	if metadata.QueueSize <= 10 {
		score += 3
		reasoning = append(reasoning, fmt.Sprintf("small queue (%d threads)", metadata.QueueSize))
	}

	// Key stakeholder sender: +2 points
	isKeyStakeholder := false
	for _, stakeholder := range g.config.Priorities.KeyStakeholders {
		if strings.Contains(strings.ToLower(metadata.SenderEmail), strings.ToLower(stakeholder)) {
			isKeyStakeholder = true
			break
		}
	}
	if isKeyStakeholder {
		score += 2
		reasoning = append(reasoning, "key stakeholder")
	}

	// Recent (< 24h): +2 points
	if time.Since(metadata.Timestamp) < 24*time.Hour {
		score += 2
		reasoning = append(reasoning, "recent message")
	}

	// Complex (5+ messages): +1 point
	if metadata.MessageCount >= 5 {
		score += 1
		reasoning = append(reasoning, fmt.Sprintf("complex thread (%d messages)", metadata.MessageCount))
	}

	// Decide which model to use (score ≥ 3 = Pro, else Flash)
	usePro := score >= 3
	selectedModel := "gemini-2.5-flash"
	selectedRateLimiter := g.rateLimiter
	actualModel := g.model

	if usePro {
		// Check if Pro rate limiter has capacity
		if g.proRateLimiter.Allow() {
			selectedModel = "gemini-2.5-pro"
			selectedRateLimiter = g.proRateLimiter
			actualModel = g.proModel
			log.Printf("Using Pro model (score: %d, reasons: %s)", score, strings.Join(reasoning, ", "))
		} else {
			// Fallback to Flash if Pro exhausted
			log.Printf("Pro model exhausted, falling back to Flash (score: %d, reasons: %s)", score, strings.Join(reasoning, ", "))
		}
	} else {
		log.Printf("Using Flash model (score: %d)", score)
	}

	// Build prompt
	prompt := g.buildThreadSummaryPrompt(messages)

	// Check cache
	hash := g.hashPrompt(prompt + selectedModel)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached summary for thread")
		return cached.Response, nil
	}

	// Wait for rate limit
	if err := selectedRateLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate summary with retry using selected model
	startTime := time.Now()
	resp, err := g.generateWithRetryForModel(ctx, genai.Text(prompt), actualModel)
	if err != nil {
		g.db.LogUsage("gemini", "summarize_thread", 0, 0, time.Since(startTime), err)
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	// Extract text from response
	summary := g.extractText(resp)

	// Calculate token usage (approximate)
	tokens := g.estimateTokens(prompt + summary)
	cost := g.calculateCost(tokens)

	// Log usage
	g.db.LogUsage("gemini", "summarize_thread", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  summary,
		Model:     selectedModel,
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return summary, nil
}

// ExtractTasks extracts action items from content
func (g *GeminiClient) ExtractTasks(ctx context.Context, content string) ([]*db.Task, error) {
	prompt := g.buildTaskExtractionPrompt(content)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached task extraction")
		return g.parseTasksFromResponse(cached.Response), nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate response with retry
	startTime := time.Now()
	resp, err := g.generateWithRetry(ctx, genai.Text(prompt))
	if err != nil {
		g.db.LogUsage("gemini", "extract_tasks", 0, 0, time.Since(startTime), err)
		return nil, fmt.Errorf("failed to extract tasks: %w", err)
	}

	// Extract text
	text := g.extractText(resp)

	// Calculate usage
	tokens := g.estimateTokens(prompt + text)
	cost := g.calculateCost(tokens)
	g.db.LogUsage("gemini", "extract_tasks", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  text,
		Model:     "gemini-1.5-flash",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return g.parseTasksFromResponse(text), nil
}

// DraftReply drafts a reply to an email
func (g *GeminiClient) DraftReply(ctx context.Context, thread []*db.Message, goal string) (string, error) {
	prompt := g.buildReplyPrompt(thread, goal)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached draft reply")
		return cached.Response, nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate reply with retry
	startTime := time.Now()
	resp, err := g.generateWithRetry(ctx, genai.Text(prompt))
	if err != nil {
		g.db.LogUsage("gemini", "draft_reply", 0, 0, time.Since(startTime), err)
		return "", fmt.Errorf("failed to draft reply: %w", err)
	}

	// Extract text
	reply := g.extractText(resp)

	// Calculate usage
	tokens := g.estimateTokens(prompt + reply)
	cost := g.calculateCost(tokens)
	g.db.LogUsage("gemini", "draft_reply", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  reply,
		Model:     "gemini-1.5-flash",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return reply, nil
}

// GenerateMeetingPrep generates meeting preparation notes
func (g *GeminiClient) GenerateMeetingPrep(ctx context.Context, event *db.Event, relatedDocs []*db.Document) (string, error) {
	prompt := g.buildMeetingPrepPrompt(event, relatedDocs)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached meeting prep")
		return cached.Response, nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate prep with retry
	startTime := time.Now()
	resp, err := g.generateWithRetry(ctx, genai.Text(prompt))
	if err != nil {
		g.db.LogUsage("gemini", "meeting_prep", 0, 0, time.Since(startTime), err)
		return "", fmt.Errorf("failed to generate meeting prep: %w", err)
	}

	// Extract text
	prep := g.extractText(resp)

	// Calculate usage
	tokens := g.estimateTokens(prompt + prep)
	cost := g.calculateCost(tokens)
	g.db.LogUsage("gemini", "meeting_prep", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  prep,
		Model:     "gemini-1.5-flash",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return prep, nil
}

// buildThreadSummaryPrompt creates a prompt for thread summarization
func (g *GeminiClient) buildThreadSummaryPrompt(messages []*db.Message) string {
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

// buildTaskExtractionPrompt creates a prompt for task extraction
func (g *GeminiClient) buildTaskExtractionPrompt(content string) string {
	return fmt.Sprintf(`Extract action items from this content. For each task, provide:
- Title (brief description)
- Owner (if mentioned)
- Due date/urgency (if mentioned)
- Priority (High/Medium/Low based on context)

Content:
%s

Format as a numbered list. Example:
1. Title: Review Q3 budget | Owner: Alex | Due: Friday | Priority: High
2. Title: Send meeting notes | Owner: Me | Due: Today | Priority: Medium

Tasks:`, content)
}

// buildReplyPrompt creates a prompt for drafting replies
func (g *GeminiClient) buildReplyPrompt(thread []*db.Message, goal string) string {
	var prompt strings.Builder

	prompt.WriteString("Draft a concise, professional email reply.\n\n")
	prompt.WriteString(fmt.Sprintf("Goal: %s\n\n", goal))

	prompt.WriteString("Thread context (most recent first):\n")
	for i := len(thread) - 1; i >= 0 && i >= len(thread)-3; i-- {
		msg := thread[i]
		prompt.WriteString(fmt.Sprintf("From: %s\n", msg.From))
		prompt.WriteString(fmt.Sprintf("Content: %s\n\n", msg.Snippet))
	}

	prompt.WriteString("Draft a reply that:\n")
	prompt.WriteString("- Is concise and to the point\n")
	prompt.WriteString("- Maintains professional tone\n")
	prompt.WriteString("- Addresses the goal clearly\n")
	prompt.WriteString("- Uses my typical writing style (direct, friendly)\n\n")

	prompt.WriteString("Reply (max 150 words):")

	return prompt.String()
}

// buildMeetingPrepPrompt creates a prompt for meeting preparation
func (g *GeminiClient) buildMeetingPrepPrompt(event *db.Event, docs []*db.Document) string {
	var prompt strings.Builder

	prompt.WriteString("Generate a one-page meeting preparation brief.\n\n")

	prompt.WriteString(fmt.Sprintf("Meeting: %s\n", event.Title))
	prompt.WriteString(fmt.Sprintf("Time: %s\n", event.StartTS.Format("Monday, Jan 2, 3:04 PM")))

	if len(event.Attendees) > 0 {
		prompt.WriteString(fmt.Sprintf("Attendees: %s\n", strings.Join(event.Attendees, ", ")))
	}

	if event.Description != "" {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", event.Description))
	}

	if len(docs) > 0 {
		prompt.WriteString("\nRelated documents:\n")
		for _, doc := range docs {
			prompt.WriteString(fmt.Sprintf("- %s\n", doc.Title))
		}
	}

	prompt.WriteString("\nGenerate a brief with:\n")
	prompt.WriteString("1. Meeting context and objectives\n")
	prompt.WriteString("2. Key talking points\n")
	prompt.WriteString("3. Questions to ask\n")
	prompt.WriteString("4. Potential decisions needed\n")
	prompt.WriteString("5. Follow-up actions\n\n")

	prompt.WriteString("Meeting Brief:")

	return prompt.String()
}

// parseTasksFromResponse parses tasks from LLM response
func (g *GeminiClient) parseTasksFromResponse(response string) []*db.Task {
	var tasks []*db.Task

	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse task from line (basic parsing, could be improved)
		task := &db.Task{
			ID:     fmt.Sprintf("ai_%d", time.Now().UnixNano()),
			Source: "ai",
			Title:  line,
			Status: "pending",
			Effort: "M", // Default to medium effort
		}

		// Extract priority if mentioned
		if strings.Contains(strings.ToLower(line), "high") {
			task.Impact = 4
			task.Urgency = 4
		} else if strings.Contains(strings.ToLower(line), "medium") {
			task.Impact = 3
			task.Urgency = 3
		} else {
			task.Impact = 2
			task.Urgency = 2
		}

		// Extract due date if mentioned
		if strings.Contains(strings.ToLower(line), "today") {
			due := time.Now().Add(8 * time.Hour)
			task.DueTS = &due
			task.Urgency = 5
		} else if strings.Contains(strings.ToLower(line), "tomorrow") {
			due := time.Now().Add(24 * time.Hour)
			task.DueTS = &due
			task.Urgency = 4
		}

		tasks = append(tasks, task)
	}

	return tasks
}

// extractText extracts text from Gemini response
func (g *GeminiClient) extractText(resp *genai.GenerateContentResponse) string {
	var text strings.Builder

	if resp != nil && len(resp.Candidates) > 0 {
		for _, part := range resp.Candidates[0].Content.Parts {
			text.WriteString(fmt.Sprintf("%v", part))
		}
	}

	return text.String()
}

// hashPrompt generates a hash for caching
func (g *GeminiClient) hashPrompt(prompt string) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// estimateTokens estimates token count (rough approximation)
func (g *GeminiClient) estimateTokens(text string) int {
	// Rough estimate: 1 token ≈ 4 characters
	return len(text) / 4
}

// calculateCost calculates API cost (Gemini 1.5 Flash pricing)
func (g *GeminiClient) calculateCost(tokens int) float64 {
	// Gemini 1.5 Flash: $0.075 per 1M input tokens, $0.30 per 1M output tokens
	// Using average for simplicity
	costPerToken := 0.0000002 // $0.20 per 1M tokens average
	return float64(tokens) * costPerToken
}