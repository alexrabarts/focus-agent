package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"golang.org/x/time/rate"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// Client is the interface for LLM operations
type Client interface {
	Close() error
	SummarizeThread(ctx context.Context, messages []*db.Message) (string, error)
	SummarizeThreadWithModelSelection(ctx context.Context, messages []*db.Message, metadata ThreadMetadata) (string, error)
	ExtractTasks(ctx context.Context, content string) ([]*db.Task, error)
	ExtractTasksFromMessages(ctx context.Context, content string, messages []*db.Message, frontComments []*db.FrontComment, frontMetadata *db.FrontMetadata) ([]*db.Task, error)
	EnrichTaskDescription(ctx context.Context, task *db.Task, messages []*db.Message) (string, error)
	EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error)
	DraftReply(ctx context.Context, thread []*db.Message, goal string) (string, error)
	GenerateMeetingPrep(ctx context.Context, event *db.Event, relatedDocs []*db.Document) (string, error)
}

// GeminiClient handles Gemini API operations
type GeminiClient struct {
	client          *genai.Client
	model           *genai.GenerativeModel
	proModel        *genai.GenerativeModel
	db              *db.DB
	config          *config.Config
	prompts         *PromptBuilder
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
func NewGeminiClient(apiKey string, database *db.DB, cfg *config.Config, prompts *PromptBuilder) (*GeminiClient, error) {
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
		prompts:        prompts,
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
	prompt := g.prompts.BuildThreadSummary(messages)

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
	prompt := g.prompts.BuildThreadSummary(messages)

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
	return g.ExtractTasksFromMessages(ctx, content, nil, nil, nil)
}

// ExtractTasksFromMessages extracts action items from content with message context + Front data
func (g *GeminiClient) ExtractTasksFromMessages(ctx context.Context, content string, messages []*db.Message, frontComments []*db.FrontComment, frontMetadata *db.FrontMetadata) ([]*db.Task, error) {
	// Determine if this is a sent email thread
	isSentEmail := false
	var recipients []string
	userEmail := g.config.Google.UserEmail

	if len(messages) > 0 && userEmail != "" {
		// Check the most recent message to determine direction
		lastMsg := messages[len(messages)-1]
		if strings.Contains(lastMsg.From, userEmail) {
			isSentEmail = true
			// Parse recipients from To field
			if lastMsg.To != "" {
				// Split by comma and clean up
				for _, r := range strings.Split(lastMsg.To, ",") {
					recipients = append(recipients, strings.TrimSpace(r))
				}
			}
		}
	}

	// Choose appropriate prompt based on email direction
	var prompt string
	if isSentEmail {
		prompt = g.prompts.BuildSentEmailTaskExtraction(content, recipients)
	} else {
		prompt = g.prompts.BuildTaskExtraction(content)
	}

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
	prompt := g.prompts.BuildStrategicAlignment(task, priorities)

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
// NOTE: Filtering disabled - prompts are now more aggressive about extracting relevant tasks
func (g *GeminiClient) filterTasksForUser(tasks []*db.Task) []*db.Task {
	// No filtering - trust the LLM prompt to extract only relevant tasks
	if len(tasks) > 0 {
		log.Printf("Extracted %d tasks (no filtering applied)", len(tasks))
	}
	return tasks
}

// DraftReply drafts a reply to an email
func (g *GeminiClient) DraftReply(ctx context.Context, thread []*db.Message, goal string) (string, error) {
	prompt := g.prompts.BuildReply(thread, goal)

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
	prompt := g.prompts.BuildMeetingPrep(event, relatedDocs)

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

// EnrichTaskDescription generates a rich, contextual description for a task based on email thread
func (g *GeminiClient) EnrichTaskDescription(ctx context.Context, task *db.Task, messages []*db.Message) (string, error) {
	prompt := g.prompts.BuildTaskEnrichment(task, messages)

	// Check cache
	hash := g.hashPrompt(prompt)
	cached, err := g.db.GetCachedResponse(hash)
	if err == nil && cached != nil {
		log.Printf("Using cached task enrichment")
		return cached.Response, nil
	}

	// Wait for rate limit
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limiter error: %w", err)
	}

	// Generate enriched description with retry
	startTime := time.Now()
	resp, err := g.generateWithRetry(ctx, genai.Text(prompt))
	if err != nil {
		g.db.LogUsage("gemini", "enrich_task", 0, 0, time.Since(startTime), err)
		return "", fmt.Errorf("failed to enrich task description: %w", err)
	}

	// Extract text
	enrichedDesc := g.extractText(resp)

	// Calculate usage
	tokens := g.estimateTokens(prompt + enrichedDesc)
	cost := g.calculateCost(tokens)
	g.db.LogUsage("gemini", "enrich_task", tokens, cost, time.Since(startTime), nil)

	// Cache response
	cache := &db.LLMCache{
		Hash:      hash,
		Prompt:    prompt,
		Response:  enrichedDesc,
		Model:     "gemini-1.5-flash",
		Tokens:    tokens,
		ExpiresAt: time.Now().Add(g.cacheTTL),
	}
	g.db.SaveCachedResponse(cache)

	return enrichedDesc, nil
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
// isMeetingInvitationGemini checks if a task title is a meeting invitation (should be filtered)
func isMeetingInvitationGemini(title string) bool {
	titleLower := strings.ToLower(title)
	meetingPatterns := []string{
		"respond to meeting invitation",
		"accept meeting",
		"decline meeting",
		"confirm availability for meeting",
		"rsvp to",
		"reply to invitation",
		"accept invitation",
		"respond to invitation",
		// Calendar event patterns
		"join ",
		"attend ",
		"lead ",
		"host ",
		"participate in",
	}

	for _, pattern := range meetingPatterns {
		if strings.Contains(titleLower, pattern) {
			return true
		}
	}
	return false
}

// isValidStakeholder checks if a stakeholder field contains a person's name
// Returns false for: team names, departments, companies, generic roles
func isValidStakeholder(stakeholder string) bool {
	if stakeholder == "" || stakeholder == "N/A" {
		return true // Empty/N/A is valid
	}

	stakeholderLower := strings.ToLower(stakeholder)

	// Invalid patterns: teams, departments, generic roles, companies
	invalidPatterns := []string{
		"team", "department", "support", "marketing", "finance",
		"operations", "group", "division", "sales", "customer",
		"engineering", "product", "design", "hr", "legal",
		"accounting", "admin", "relevant", "member", "staff",
	}

	for _, pattern := range invalidPatterns {
		if strings.Contains(stakeholderLower, pattern) {
			return false
		}
	}

	return true
}

func (g *GeminiClient) parseTasksFromResponse(response string) []*db.Task {
	var tasks []*db.Task
	seenTitles := make(map[string]bool) // Track duplicate titles

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
			// Save previous task if exists (skip meeting invitations and duplicates)
			if currentTask != nil && currentTask.Title != "" && !isMeetingInvitationGemini(currentTask.Title) {
				titleLower := strings.ToLower(currentTask.Title)
				if !seenTitles[titleLower] {
					seenTitles[titleLower] = true
					tasks = append(tasks, currentTask)
				}
			}

			// Start new task
			currentTask = &db.Task{
				ID:      fmt.Sprintf("ai_%d", time.Now().UnixNano()),
				Source:  "ai",
				Status:  "pending",
				Impact:  3, // Default medium impact
				Urgency: 3, // Default medium urgency
				Effort:  "M", // Default medium effort
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

	// Add last task (skip meeting invitations and duplicates)
	if currentTask != nil && currentTask.Title != "" && !isMeetingInvitationGemini(currentTask.Title) {
		titleLower := strings.ToLower(currentTask.Title)
		if !seenTitles[titleLower] {
			seenTitles[titleLower] = true
			tasks = append(tasks, currentTask)
		}
	}

	return tasks
}

// parseTaskField parses a single field from a task line
func (g *GeminiClient) parseTaskField(task *db.Task, field string) {
	if task == nil {
		return
	}

	field = strings.TrimSpace(field)

	fieldLower := strings.ToLower(field)

	if strings.HasPrefix(field, "Title:") || strings.HasPrefix(fieldLower, "title:") {
		task.Title = strings.TrimSpace(strings.TrimPrefix(field, "Title:"))
		task.Title = strings.TrimSpace(strings.TrimPrefix(task.Title, "title:"))
	} else if strings.HasPrefix(field, "Impact:") || strings.HasPrefix(fieldLower, "impact:") {
		impactStr := strings.TrimSpace(strings.TrimPrefix(field, "Impact:"))
		impactStr = strings.TrimSpace(strings.TrimPrefix(impactStr, "impact:"))
		if val, err := strconv.Atoi(impactStr); err == nil && val >= 1 && val <= 5 {
			task.Impact = val
		}
	} else if strings.HasPrefix(field, "Urgency:") || strings.HasPrefix(fieldLower, "urgency:") {
		urgencyStr := strings.TrimSpace(strings.TrimPrefix(field, "Urgency:"))
		urgencyStr = strings.TrimSpace(strings.TrimPrefix(urgencyStr, "urgency:"))
		if val, err := strconv.Atoi(urgencyStr); err == nil && val >= 1 && val <= 5 {
			task.Urgency = val
		}
	} else if strings.HasPrefix(field, "Effort:") || strings.HasPrefix(fieldLower, "effort:") {
		effortStr := strings.TrimSpace(strings.TrimPrefix(field, "Effort:"))
		effortStr = strings.TrimSpace(strings.TrimPrefix(effortStr, "effort:"))
		effortStr = strings.ToUpper(effortStr)
		if effortStr == "S" || effortStr == "M" || effortStr == "L" {
			task.Effort = effortStr
		}
	} else if strings.HasPrefix(field, "Stakeholder:") || strings.HasPrefix(fieldLower, "stakeholder:") {
		stakeholderStr := strings.TrimSpace(strings.TrimPrefix(field, "Stakeholder:"))
		stakeholderStr = strings.TrimSpace(strings.TrimPrefix(stakeholderStr, "stakeholder:"))
		// Validate stakeholder is a person's name, not a team/department/company
		if isValidStakeholder(stakeholderStr) {
			task.Stakeholder = stakeholderStr
		} else {
			task.Stakeholder = "" // Clear invalid stakeholders
		}
	} else if strings.HasPrefix(field, "Project:") || strings.HasPrefix(fieldLower, "project:") {
		projectStr := strings.TrimSpace(strings.TrimPrefix(field, "Project:"))
		projectStr = strings.TrimSpace(strings.TrimPrefix(projectStr, "project:"))
		task.Project = projectStr
	} else if strings.HasPrefix(field, "Owner:") {
		task.Stakeholder = strings.TrimSpace(strings.TrimPrefix(field, "Owner:"))
	} else if strings.HasPrefix(field, "Due:") || strings.HasPrefix(fieldLower, "due:") {
		dueStr := strings.TrimSpace(strings.TrimPrefix(field, "Due:"))
		dueStr = strings.TrimSpace(strings.TrimPrefix(dueStr, "due:"))
		task.DueTS = parseDueDate(dueStr)
		if task.DueTS != nil {
			task.Urgency = maxInt(task.Urgency, calculateUrgencyFromDue(*task.DueTS))
		}
	} else if strings.HasPrefix(field, "Priority:") {
		// Legacy support for old format
		priorityStr := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(field, "Priority:")))
		// Only override if impact not explicitly set
		if task.Impact == 2 || task.Impact == 3 {
			if strings.Contains(priorityStr, "high") {
				task.Impact = 4
				task.Urgency = maxInt(task.Urgency, 4)
			} else if strings.Contains(priorityStr, "medium") {
				task.Impact = 3
				task.Urgency = maxInt(task.Urgency, 3)
			} else {
				task.Impact = 2
			}
		}
	} else if task.Title == "" && len(field) > 10 {
		// If no title yet and this is a substantial line, use it as the title
		task.Title = field
	}
}

// parseDueDate parses due date strings with enhanced pattern matching (15+ patterns)
func parseDueDate(dueStr string) *time.Time {
	dueStr = strings.ToLower(strings.TrimSpace(dueStr))

	if dueStr == "" || dueStr == "n/a" || dueStr == "none" {
		return nil
	}

	now := time.Now()

	// Exact keyword matches
	switch dueStr {
	case "today":
		due := now.Add(8 * time.Hour)
		return &due
	case "tomorrow":
		due := now.Add(24 * time.Hour)
		return &due
	case "this week", "week":
		due := now.Add(72 * time.Hour)
		return &due
	case "next week":
		due := now.Add(7 * 24 * time.Hour)
		return &due
	case "end of week", "eow":
		// Find next Friday
		daysUntilFriday := (5 - int(now.Weekday()) + 7) % 7
		if daysUntilFriday == 0 {
			daysUntilFriday = 7
		}
		due := now.Add(time.Duration(daysUntilFriday) * 24 * time.Hour)
		return &due
	case "end of month", "eom":
		// Last day of current month
		year, month, _ := now.Date()
		due := time.Date(year, month+1, 0, 23, 59, 59, 0, now.Location())
		return &due
	}

	// Remove common prefix words to normalize
	dueStr = strings.TrimPrefix(dueStr, "by ")
	dueStr = strings.TrimPrefix(dueStr, "before ")
	dueStr = strings.TrimPrefix(dueStr, "no later than ")
	dueStr = strings.TrimPrefix(dueStr, "due ")
	dueStr = strings.TrimSpace(dueStr)

	// Handle "eod" or "end of day" → treat as today 5pm
	if dueStr == "eod" || dueStr == "end of day" {
		due := time.Date(now.Year(), now.Month(), now.Day(), 17, 0, 0, 0, now.Location())
		return &due
	}

	// Handle "in X days" patterns
	if strings.HasPrefix(dueStr, "in ") {
		parts := strings.Fields(dueStr)
		if len(parts) >= 3 && parts[2] == "days" {
			if days, err := strconv.Atoi(parts[1]); err == nil {
				due := now.Add(time.Duration(days) * 24 * time.Hour)
				return &due
			}
		}
		if len(parts) >= 3 && (parts[2] == "weeks" || parts[2] == "week") {
			if weeks, err := strconv.Atoi(parts[1]); err == nil {
				due := now.Add(time.Duration(weeks*7) * 24 * time.Hour)
				return &due
			}
		}
	}

	// Handle "next Monday", "next Friday" patterns
	if strings.HasPrefix(dueStr, "next ") {
		weekdayStr := strings.TrimPrefix(dueStr, "next ")
		weekdays := map[string]int{
			"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3,
			"thursday": 4, "friday": 5, "saturday": 6,
		}
		if weekdayNum, ok := weekdays[weekdayStr]; ok {
			currentWeekday := int(now.Weekday())
			daysUntil := (weekdayNum - currentWeekday + 7) % 7
			if daysUntil == 0 {
				daysUntil = 7 // Force next week
			}
			due := now.Add(time.Duration(daysUntil) * 24 * time.Hour)
			return &due
		}
	}

	// Try weekday names (this Friday = this week's Friday)
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

	// Try parsing specific date formats
	// Format: "Oct 30", "October 30", "Jan 15"
	monthNames := map[string]int{
		"jan": 1, "january": 1, "feb": 2, "february": 2,
		"mar": 3, "march": 3, "apr": 4, "april": 4,
		"may": 5, "jun": 6, "june": 6,
		"jul": 7, "july": 7, "aug": 8, "august": 8,
		"sep": 9, "september": 9, "oct": 10, "october": 10,
		"nov": 11, "november": 11, "dec": 12, "december": 12,
	}

	parts := strings.Fields(dueStr)
	if len(parts) == 2 {
		monthStr := strings.ToLower(parts[0])
		if month, ok := monthNames[monthStr]; ok {
			dayStr := strings.TrimSuffix(parts[1], "th")
			dayStr = strings.TrimSuffix(dayStr, "st")
			dayStr = strings.TrimSuffix(dayStr, "nd")
			dayStr = strings.TrimSuffix(dayStr, "rd")
			if day, err := strconv.Atoi(dayStr); err == nil && day >= 1 && day <= 31 {
				year := now.Year()
				// If month has passed this year, use next year
				if month < int(now.Month()) || (month == int(now.Month()) && day < now.Day()) {
					year++
				}
				due := time.Date(year, time.Month(month), day, 23, 59, 59, 0, now.Location())
				return &due
			}
		}
	}

	// Try standard date formats: "2025-01-15", "01/15/2025", "15/01/2025"
	formats := []string{
		"2006-01-02",
		"01/02/2006",
		"1/2/2006",
		"01/02",
		"1/2",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, dueStr); err == nil {
			// For formats without year, use current year or next year
			if !strings.Contains(format, "2006") {
				year := now.Year()
				if parsed.Month() < now.Month() || (parsed.Month() == now.Month() && parsed.Day() < now.Day()) {
					year++
				}
				parsed = time.Date(year, parsed.Month(), parsed.Day(), 23, 59, 59, 0, now.Location())
			}
			return &parsed
		}
	}

	// If nothing matched, return nil
	return nil
}

// calculateUrgencyFromDue calculates urgency score based on due date
func calculateUrgencyFromDue(dueDate time.Time) int {
	hoursUntil := time.Until(dueDate).Hours()

	switch {
	case hoursUntil <= 0:
		return 5 // Overdue
	case hoursUntil <= 48:
		return 5 // Today or tomorrow (within 48 hours)
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