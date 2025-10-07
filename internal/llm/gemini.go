package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"

	"github.com/alexrabarts/focus-agent/internal/db"
)

// GeminiClient handles Gemini API operations
type GeminiClient struct {
	client   *genai.Client
	model    *genai.GenerativeModel
	db       *db.DB
	cacheTTL time.Duration
}

// NewGeminiClient creates a new Gemini client
func NewGeminiClient(apiKey string, database *db.DB) (*GeminiClient, error) {
	ctx := context.Background()

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Use Gemini 2.5 Flash for efficiency (1.5 models retired)
	model := client.GenerativeModel("gemini-2.5-flash")

	// Configure model parameters
	model.SetTemperature(0.3)
	model.SetTopK(40)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(2000)

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

	return &GeminiClient{
		client:   client,
		model:    model,
		db:       database,
		cacheTTL: 24 * time.Hour, // Default 24 hour cache
	}, nil
}

// Close closes the Gemini client
func (g *GeminiClient) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
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

	// Generate summary
	startTime := time.Now()
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
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

	// Generate response
	startTime := time.Now()
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
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

	// Generate reply
	startTime := time.Now()
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
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

	// Generate prep
	startTime := time.Now()
	resp, err := g.model.GenerateContent(ctx, genai.Text(prompt))
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