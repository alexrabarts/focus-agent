package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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

// isDailyQuotaError checks if a 429 error is a daily quota exhaustion (not just per-minute rate limit)
func isDailyQuotaError(apiErr *googleapi.Error) bool {
	// Check 1: Message field for quota exhaustion keywords
	if strings.Contains(apiErr.Message, "current quota") ||
		strings.Contains(apiErr.Message, "quota exceeded") {
		return true
	}

	// Check 2: Error() string (includes Message + Details)
	errStr := apiErr.Error()
	if strings.Contains(errStr, "generate_content_free_tier_requests") ||
		strings.Contains(errStr, "Quota exceeded for metric") {
		return true
	}

	// Check 3: Body field (raw response)
	if strings.Contains(apiErr.Body, "generate_content_free_tier_requests") ||
		strings.Contains(apiErr.Body, "generativelanguage.googleapis.com/generate_content") {
		return true
	}

	// Check 4: Details field (structured error info)
	if len(apiErr.Details) > 0 {
		// Marshal Details to JSON string for searching
		detailsJSON, err := json.Marshal(apiErr.Details)
		if err == nil && strings.Contains(string(detailsJSON), "generate_content_free_tier_requests") {
			return true
		}
	}

	return false
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
			if isDailyQuotaError(apiErr) {
				// Daily quota exhausted - don't retry
				log.Printf("🚫 Daily quota exhausted (250 requests/day free tier limit)")
				log.Printf("   Processing stopped. Quota resets in ~24 hours.")
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

			log.Printf("⏱️  Per-minute rate limit (10 RPM), retrying in %v (attempt %d/%d)", backoffDelay, attempt+1, g.config.Gemini.MaxRetries)

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
		return g.filterTasksForUser(g.parseTasksFromResponse(cached.Response)), nil
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

	return g.filterTasksForUser(g.parseTasksFromResponse(text)), nil
}

// StrategicAlignmentResult contains the result of strategic alignment evaluation
type StrategicAlignmentResult struct {
	Score          float64  `json:"score"`
	OKRs           []string `json:"okrs"`
	FocusAreas     []string `json:"focus_areas"`
	Projects       []string `json:"projects"`
	KeyStakeholder bool     `json:"key_stakeholder"`
	Reasoning      string   `json:"reasoning"`
}

// EvaluateStrategicAlignment uses LLM to determine which strategic priorities align with a task
func (g *GeminiClient) EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error) {
	prompt := g.buildStrategicAlignmentPrompt(task, priorities)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		return g.parseStrategicAlignmentResponse(cached.Response), nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	// Create a temporary model with JSON response mode
	jsonModel := g.client.GenerativeModel(g.config.Gemini.Model)
	jsonModel.SetTemperature(g.config.Gemini.Temperature)
	jsonModel.SetTopK(40)
	jsonModel.SetTopP(0.95)
	if g.config.Gemini.MaxTokens > 0 {
		jsonModel.SetMaxOutputTokens(int32(g.config.Gemini.MaxTokens))
	}
	jsonModel.SafetySettings = g.model.SafetySettings

	// Configure JSON response
	jsonModel.ResponseMIMEType = "application/json"
	jsonModel.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"score": {
				Type:        genai.TypeNumber,
				Description: "Strategic alignment score from 0.0 to 5.0",
			},
			"okrs": {
				Type:        genai.TypeArray,
				Items:       &genai.Schema{Type: genai.TypeString},
				Description: "OKR names that genuinely align",
			},
			"focus_areas": {
				Type:        genai.TypeArray,
				Items:       &genai.Schema{Type: genai.TypeString},
				Description: "Focus area names that align",
			},
			"projects": {
				Type:        genai.TypeArray,
				Items:       &genai.Schema{Type: genai.TypeString},
				Description: "Project names that align",
			},
			"reasoning": {
				Type:        genai.TypeString,
				Description: "Brief explanation of evaluation",
			},
		},
		Required: []string{"score", "okrs", "focus_areas", "projects", "reasoning"},
	}

	// Generate response with retry
	startTime := time.Now()
	resp, err := g.generateWithRetryForModel(ctx, genai.Text(prompt), jsonModel)
	if err != nil {
		g.db.LogUsage("gemini", "strategic_alignment", 0, 0, time.Since(startTime), err)
		return nil, fmt.Errorf("failed to evaluate strategic alignment: %w", err)
	}

	// Extract text
	text := g.extractText(resp)

	// Debug logging for empty responses
	if text == "" {
		log.Printf("Warning: Empty response from Gemini for strategic alignment")
		if resp != nil && len(resp.Candidates) > 0 {
			log.Printf("FinishReason: %v", resp.Candidates[0].FinishReason)
			log.Printf("SafetyRatings: %v", resp.Candidates[0].SafetyRatings)
			promptPreview := prompt
			if len(promptPreview) > 200 {
				promptPreview = prompt[:200] + "..."
			}
			log.Printf("Prompt was: %s", promptPreview)
		}
	}

	// Calculate usage
	tokens := g.estimateTokens(prompt + text)
	cost := g.calculateCost(tokens)
	g.db.LogUsage("gemini", "strategic_alignment", tokens, cost, time.Since(startTime), nil)

	// Cache response (longer TTL since priorities don't change often)
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  text,
		Model:     g.config.Gemini.Model,
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7 days
	}
	g.db.SaveCachedResponse(cache)

	return g.parseStrategicAlignmentResponse(text), nil
}

// filterTasksForUser filters out tasks assigned to other specific people
func (g *GeminiClient) filterTasksForUser(tasks []*db.Task) []*db.Task {
	userEmail := strings.ToLower(g.config.Google.UserEmail)
	filtered := make([]*db.Task, 0, len(tasks))

	for _, task := range tasks {
		stakeholder := strings.ToLower(strings.TrimSpace(task.Stakeholder))

		// Keep task if stakeholder is:
		// - empty (unassigned, assume for user)
		// - "me" or "you" (explicitly for user)
		// - matches user's email
		// - contains user's email
		if stakeholder == "" ||
			stakeholder == "me" ||
			stakeholder == "you" ||
			stakeholder == userEmail ||
			strings.Contains(stakeholder, userEmail) {
			filtered = append(filtered, task)
			continue
		}

		// Skip tasks assigned to other specific people
		log.Printf("Filtering out task assigned to '%s': %s", task.Stakeholder, task.Title)
	}

	return filtered
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
	userEmail := g.config.Google.UserEmail
	if userEmail == "" {
		userEmail = "the user"
	}

	return fmt.Sprintf(`Extract action items from this content that are FOR ME (%s) to do.

IMPORTANT RULES:
- ONLY extract tasks where I (%s) am responsible or need to take action
- SKIP tasks assigned to other specific people (e.g., "Andrew: do X", "Maria: review Y")
- INCLUDE tasks with no owner specified (assume they're for me)
- INCLUDE tasks marked as "me", "you", or my email address

For each task, provide:
- Title (brief description)
- Owner (if mentioned, use "me" if it's for me, otherwise the person's name/email)
- Due date/urgency (if mentioned)
- Priority (High/Medium/Low based on context)

Content:
%s

Format as a numbered list. Example:
1. Title: Review Q3 budget | Owner: me | Due: Friday | Priority: High
2. Title: Send meeting notes | Owner: me | Due: Today | Priority: Medium

Only extract tasks for me (%s). Skip tasks for other people.

Tasks:`, userEmail, userEmail, content, userEmail)
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

// buildStrategicAlignmentPrompt creates a prompt for strategic alignment evaluation
func (g *GeminiClient) buildStrategicAlignmentPrompt(task *db.Task, priorities *config.Priorities) string {
	var prompt strings.Builder

	prompt.WriteString("Evaluate how well this task aligns with the following strategic priorities.\n\n")

	prompt.WriteString("TASK:\n")
	prompt.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	if task.Description != "" {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", task.Description))
	}
	if task.Project != "" {
		prompt.WriteString(fmt.Sprintf("Project: %s\n", task.Project))
	}
	prompt.WriteString("\n")

	prompt.WriteString("STRATEGIC PRIORITIES:\n\n")

	if len(priorities.OKRs) > 0 {
		prompt.WriteString("OKRs (Objectives & Key Results):\n")
		for _, okr := range priorities.OKRs {
			prompt.WriteString(fmt.Sprintf("  - %s\n", okr))
		}
		prompt.WriteString("\n")
	}

	if len(priorities.FocusAreas) > 0 {
		prompt.WriteString("Focus Areas:\n")
		for _, area := range priorities.FocusAreas {
			prompt.WriteString(fmt.Sprintf("  - %s\n", area))
		}
		prompt.WriteString("\n")
	}

	if len(priorities.KeyProjects) > 0 {
		prompt.WriteString("Key Projects:\n")
		for _, project := range priorities.KeyProjects {
			prompt.WriteString(fmt.Sprintf("  - %s\n", project))
		}
		prompt.WriteString("\n")
	}

	prompt.WriteString("INSTRUCTIONS:\n")
	prompt.WriteString("Evaluate the DIRECT, MEANINGFUL alignment between this task and the strategic priorities.\n\n")
	prompt.WriteString("STRICT MATCHING RULES:\n")
	prompt.WriteString("1. Only match if the task DIRECTLY advances or relates to the priority\n")
	prompt.WriteString("2. Shared keywords alone are NOT sufficient (e.g., 'data team' ≠ 'Data lake project')\n")
	prompt.WriteString("3. Generic administrative tasks (scheduling, coordinating, reporting) should NOT match strategic priorities unless they're specifically about implementing/advancing that priority\n")
	prompt.WriteString("4. Be conservative - when in doubt, DON'T match\n\n")
	prompt.WriteString("EXAMPLES OF POOR MATCHES TO AVOID:\n")
	prompt.WriteString("- 'Schedule meeting about X' does NOT align with X unless the meeting is to implement/advance X\n")
	prompt.WriteString("- 'Send report to team' does NOT align with 'Improved forecasting' just because both involve data\n")
	prompt.WriteString("- 'Coordinate with data team' does NOT align with 'Data lake' unless specifically about the data lake\n")
	prompt.WriteString("- 'Review budget' does NOT align with 'Profitability' unless it's specifically about improving margins\n\n")
	prompt.WriteString("EXAMPLES OF GOOD MATCHES:\n")
	prompt.WriteString("- 'Implement new CRM dashboard' → 'Scalable systems - CRM implementation'\n")
	prompt.WriteString("- 'Analyze margin trends for cost optimization' → 'Sector Leading Profitability'\n")
	prompt.WriteString("- 'Design brand guidelines for member experience' → 'Known for distinctive service'\n\n")
	prompt.WriteString("Return:\n")
	prompt.WriteString("- score: 0.0 (no alignment) to 5.0 (perfect alignment)\n")
	prompt.WriteString("- okrs: array of OKR names that genuinely align (empty array if none)\n")
	prompt.WriteString("- focus_areas: array of Focus Area names that align (empty array if none)\n")
	prompt.WriteString("- projects: array of Project names that align (empty array if none)\n")
	prompt.WriteString("- reasoning: brief explanation of your evaluation (include why you excluded matches if any)\n")

	return prompt.String()
}

// parseStrategicAlignmentResponse parses the LLM response for strategic alignment
func (g *GeminiClient) parseStrategicAlignmentResponse(response string) *StrategicAlignmentResult {
	// Find JSON object in response
	response = strings.TrimSpace(response)

	// Default result for empty or invalid responses
	result := &StrategicAlignmentResult{
		Score:      0.0,
		OKRs:       []string{},
		FocusAreas: []string{},
		Projects:   []string{},
	}

	// Handle empty response
	if response == "" {
		log.Printf("Empty strategic alignment response from LLM")
		return result
	}

	// Remove markdown code blocks if present
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	// Try to extract JSON object if embedded in text
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		response = response[jsonStart : jsonEnd+1]
	}

	// Try to parse JSON
	if err := json.Unmarshal([]byte(response), result); err != nil {
		log.Printf("Failed to parse strategic alignment response: %v", err)
		log.Printf("Response was: %s", response)
		return result
	}

	// Ensure arrays are not nil
	if result.OKRs == nil {
		result.OKRs = []string{}
	}
	if result.FocusAreas == nil {
		result.FocusAreas = []string{}
	}
	if result.Projects == nil {
		result.Projects = []string{}
	}

	// Ensure score is in valid range
	if result.Score < 0 {
		result.Score = 0
	} else if result.Score > 5 {
		result.Score = 5
	}

	return result
}

// parseTasksFromResponse parses tasks from LLM response
// Handles both single-line format: "1. Title: X | Owner: Y | Due: Z | Priority: W"
// And multi-line format:
//   1. **Title:** X
//      * **Owner:** Y
//      * **Due:** Z
func (g *GeminiClient) parseTasksFromResponse(response string) []*db.Task {
	var tasks []*db.Task

	lines := strings.Split(response, "\n")
	var currentTask *db.Task

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip header lines
		if strings.HasPrefix(line, "Here are") ||
		   strings.HasPrefix(line, "Action items") ||
		   strings.HasPrefix(line, "Tasks:") ||
		   strings.HasPrefix(line, "#") {
			continue
		}

		// Remove markdown formatting
		line = strings.ReplaceAll(line, "**", "")  // Remove bold

		// Check if this is a new task (starts with number)
		isNewTask := regexp.MustCompile(`^\d+\.`).MatchString(line)

		if isNewTask {
			// Save previous task if exists
			if currentTask != nil && currentTask.Title != "" {
				tasks = append(tasks, currentTask)
			}

			// Start new task
			currentTask = &db.Task{
				ID:     fmt.Sprintf("ai_%d", time.Now().UnixNano()),
				Source: "ai",
				Status: "pending",
				Impact: 2,
				Urgency: 2,
				Effort: "M",
			}

			// Remove numbered prefix
			line = regexp.MustCompile(`^\d+\.\s+`).ReplaceAllString(line, "")
		}

		// Remove bullet markers from continuation lines
		line = regexp.MustCompile(`^[\*\-]\s+`).ReplaceAllString(line, "")

		// Parse pipe-delimited format (single-line tasks)
		if strings.Contains(line, "|") {
			parts := strings.Split(line, "|")
			for _, part := range parts {
				g.parseTaskField(currentTask, part)
			}
			continue
		}

		// Parse field: value format (multi-line tasks)
		g.parseTaskField(currentTask, line)
	}

	// Add last task
	if currentTask != nil && currentTask.Title != "" {
		tasks = append(tasks, currentTask)
	}

	return tasks
}

// parseTaskField parses a single field from a task line
func (g *GeminiClient) parseTaskField(task *db.Task, field string) {
	if task == nil {
		return
	}

	field = strings.TrimSpace(field)

	if strings.HasPrefix(field, "Title:") {
		task.Title = strings.TrimSpace(strings.TrimPrefix(field, "Title:"))
	} else if strings.HasPrefix(field, "Owner:") {
		task.Stakeholder = strings.TrimSpace(strings.TrimPrefix(field, "Owner:"))
	} else if strings.HasPrefix(field, "Due:") {
		dueStr := strings.TrimSpace(strings.TrimPrefix(field, "Due:"))
		task.DueTS = parseDueDate(dueStr)
		if task.DueTS != nil {
			task.Urgency = maxInt(task.Urgency, calculateUrgencyFromDue(*task.DueTS))
		}
	} else if strings.HasPrefix(field, "Priority:") {
		priorityStr := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(field, "Priority:")))
		if strings.Contains(priorityStr, "high") {
			task.Impact = 4
			task.Urgency = maxInt(task.Urgency, 4)
		} else if strings.Contains(priorityStr, "medium") {
			task.Impact = 3
			task.Urgency = maxInt(task.Urgency, 3)
		} else {
			task.Impact = 2
		}
	} else if task.Title == "" && len(field) > 10 {
		// If no title yet and this is a substantial line, use it as the title
		task.Title = field
	}
}

// parseDueDate parses due date strings like "Today", "Tomorrow", "Friday", etc.
func parseDueDate(dueStr string) *time.Time {
	dueStr = strings.ToLower(strings.TrimSpace(dueStr))

	if dueStr == "" || dueStr == "n/a" || dueStr == "none" {
		return nil
	}

	now := time.Now()

	if dueStr == "today" {
		due := now.Add(8 * time.Hour)
		return &due
	} else if dueStr == "tomorrow" {
		due := now.Add(24 * time.Hour)
		return &due
	} else if dueStr == "this week" || dueStr == "week" {
		due := now.Add(72 * time.Hour)
		return &due
	}

	// Try to parse weekday names (e.g., "Friday")
	weekdays := map[string]int{
		"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3,
		"thursday": 4, "friday": 5, "saturday": 6,
	}

	if weekdayNum, ok := weekdays[dueStr]; ok {
		currentWeekday := int(now.Weekday())
		daysUntil := (weekdayNum - currentWeekday + 7) % 7
		if daysUntil == 0 {
			daysUntil = 7 // Next occurrence
		}
		due := now.Add(time.Duration(daysUntil) * 24 * time.Hour)
		return &due
	}

	return nil
}

// calculateUrgencyFromDue calculates urgency score based on due date
func calculateUrgencyFromDue(dueDate time.Time) int {
	hoursUntil := time.Until(dueDate).Hours()

	switch {
	case hoursUntil <= 0:
		return 5 // Overdue
	case hoursUntil <= 24:
		return 5 // Today
	case hoursUntil <= 72:
		return 4 // Next 3 days
	case hoursUntil <= 168:
		return 3 // Next week
	default:
		return 2
	}
}

// maxInt returns the maximum of two integers
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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