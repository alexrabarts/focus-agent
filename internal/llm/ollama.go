package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// OllamaClient wraps the Ollama API for text generation
type OllamaClient struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOllamaClient creates a new Ollama client
func NewOllamaClient(baseURL, model string) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://alex-mm:11434"
	}
	if model == "" {
		model = "mistral:latest"
	}

	return &OllamaClient{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Generous timeout for model operations
		},
	}
}

// GenerateRequest represents a request to generate text
type GenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// GenerateResponse represents the response from the generate API
type GenerateResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
}

// Generate generates text using the configured model
func (c *OllamaClient) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody := GenerateRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/generate", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(body))
	}

	var genResp GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return genResp.Response, nil
}

// GenerateWithFormat generates text with optional JSON format
func (c *OllamaClient) GenerateWithFormat(ctx context.Context, prompt string, format string) (string, error) {
	// If JSON format requested, add JSON instruction to prompt
	if format == "json" {
		prompt = prompt + "\n\nIMPORTANT: Respond with ONLY a valid JSON object, no markdown formatting or explanation."
	}

	reqBody := GenerateRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/generate", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(body))
	}

	var genResp GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return genResp.Response, nil
}

// Ping checks if the Ollama server is reachable
func (c *OllamaClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	return nil
}

// EvaluateStrategicAlignment evaluates how well a task aligns with strategic priorities
func (c *OllamaClient) EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error) {
	// Build the strategic alignment prompt (same as Gemini uses)
	prompt := c.buildStrategicAlignmentPrompt(task, priorities)

	// Request JSON format
	response, err := c.GenerateWithFormat(ctx, prompt, "json")
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate strategic alignment: %w", err)
	}

	// Parse the response using same parser as Gemini
	return c.parseStrategicAlignmentResponse(response), nil
}

// buildStrategicAlignmentPrompt creates the prompt for strategic alignment evaluation
func (c *OllamaClient) buildStrategicAlignmentPrompt(task *db.Task, priorities *config.Priorities) string {
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
	prompt.WriteString("- 'Review document' does NOT align unless reviewing is part of implementing the priority\n\n")

	prompt.WriteString("Respond with a JSON object with these exact fields:\n")
	prompt.WriteString("{\n")
	prompt.WriteString("  \"score\": <number from 0.0 to 5.0>,\n")
	prompt.WriteString("  \"okrs\": [<array of OKR names that genuinely align>],\n")
	prompt.WriteString("  \"focus_areas\": [<array of focus area names that align>],\n")
	prompt.WriteString("  \"projects\": [<array of project names that align>],\n")
	prompt.WriteString("  \"reasoning\": \"<brief explanation of evaluation>\"\n")
	prompt.WriteString("}\n")

	return prompt.String()
}

// parseStrategicAlignmentResponse parses the JSON response from strategic alignment
func (c *OllamaClient) parseStrategicAlignmentResponse(response string) *StrategicAlignmentResult {
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
		log.Printf("Empty strategic alignment response from Ollama")
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
		log.Printf("Failed to parse strategic alignment response from Ollama: %v", err)
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

	return result
}

// SummarizeThread generates a concise summary of an email thread
func (c *OllamaClient) SummarizeThread(ctx context.Context, messages []*db.Message) (string, error) {
	prompt := c.buildThreadSummaryPrompt(messages)

	response, err := c.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	return strings.TrimSpace(response), nil
}

// buildThreadSummaryPrompt creates a prompt for thread summarization
func (c *OllamaClient) buildThreadSummaryPrompt(messages []*db.Message) string {
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

// ExtractTasks extracts action items from content
func (c *OllamaClient) ExtractTasks(ctx context.Context, content, userEmail string) ([]*db.Task, error) {
	prompt := c.buildTaskExtractionPrompt(content, userEmail)

	response, err := c.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to extract tasks: %w", err)
	}

	return c.parseTasksFromResponse(response), nil
}

// buildTaskExtractionPrompt creates a prompt for task extraction
func (c *OllamaClient) buildTaskExtractionPrompt(content, userEmail string) string {
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

If there are NO action items for me, respond with: "No tasks found."
`, userEmail, userEmail, content)
}

// parseTasksFromResponse parses the task extraction response
func (c *OllamaClient) parseTasksFromResponse(response string) []*db.Task {
	var tasks []*db.Task

	// Check for "no tasks" response
	if strings.Contains(strings.ToLower(response), "no tasks found") ||
		strings.Contains(strings.ToLower(response), "no action items") {
		return tasks
	}

	// Parse numbered list format
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Match numbered list items: "1. Title: X | Owner: Y | Due: Z | Priority: W"
		// More flexible regex to catch various formats
		if !strings.HasPrefix(line, "1") && !strings.HasPrefix(line, "2") &&
			!strings.HasPrefix(line, "3") && !strings.HasPrefix(line, "4") &&
			!strings.HasPrefix(line, "5") && !strings.HasPrefix(line, "6") &&
			!strings.HasPrefix(line, "7") && !strings.HasPrefix(line, "8") &&
			!strings.HasPrefix(line, "9") {
			continue
		}

		// Remove number prefix
		line = strings.TrimLeft(line, "0123456789.")
		line = strings.TrimSpace(line)

		task := &db.Task{
			Source: "gmail",
			Status: "pending",
		}

		// Parse pipe-delimited fields
		parts := strings.Split(line, "|")
		for _, part := range parts {
			part = strings.TrimSpace(part)

			if strings.HasPrefix(strings.ToLower(part), "title:") {
				task.Title = strings.TrimSpace(strings.TrimPrefix(part, "Title:"))
				task.Title = strings.TrimSpace(strings.TrimPrefix(task.Title, "title:"))
			} else if strings.HasPrefix(strings.ToLower(part), "priority:") {
				priority := strings.TrimSpace(strings.TrimPrefix(part, "Priority:"))
				priority = strings.TrimSpace(strings.TrimPrefix(priority, "priority:"))
				priority = strings.ToLower(priority)

				// Map to impact score
				switch priority {
				case "high", "critical", "urgent":
					task.Impact = 5
				case "medium", "moderate":
					task.Impact = 3
				case "low":
					task.Impact = 1
				}
			}
			// Note: Owner and Due parsing could be added here if needed
		}

		// Only add if we got a title
		if task.Title != "" {
			tasks = append(tasks, task)
		}
	}

	return tasks
}
