package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
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
	prompts    *PromptBuilder
}

// NewOllamaClient creates a new Ollama client
func NewOllamaClient(baseURL, model string, prompts *PromptBuilder) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://alex-mm:11434"
	}
	if model == "" {
		model = "mistral:latest"
	}

	return &OllamaClient{
		baseURL: baseURL,
		model:   model,
		prompts: prompts,
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
	// Build the strategic alignment prompt
	prompt := c.prompts.BuildStrategicAlignment(task, priorities)

	// Request JSON format
	response, err := c.GenerateWithFormat(ctx, prompt, "json")
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate strategic alignment: %w", err)
	}

	// Parse the response using same parser as Gemini
	return c.parseStrategicAlignmentResponse(response), nil
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
	prompt := c.prompts.BuildThreadSummary(messages)

	response, err := c.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	return strings.TrimSpace(response), nil
}

// ExtractTasks extracts action items from content
func (c *OllamaClient) ExtractTasks(ctx context.Context, content, userEmail string) ([]*db.Task, error) {
	prompt := c.prompts.BuildTaskExtraction(content)

	response, err := c.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to extract tasks: %w", err)
	}

	tasks := c.parseTasksFromResponse(response)
	if len(tasks) > 0 {
		log.Printf("Ollama extracted %d tasks", len(tasks))
	} else {
		log.Printf("Ollama found no tasks in content")
	}

	return tasks, nil
}

// EnrichTaskDescription generates a rich task description
func (c *OllamaClient) EnrichTaskDescription(ctx context.Context, prompt string) (string, error) {
	return c.GenerateWithFormat(ctx, prompt, "json")
}

// isMeetingInvitation checks if a task title is a meeting invitation (should be filtered)
func isMeetingInvitation(title string) bool {
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

// parseTasksFromResponse parses the task extraction response
func (c *OllamaClient) parseTasksFromResponse(response string) []*db.Task {
	var tasks []*db.Task
	seenTitles := make(map[string]bool) // Track duplicate titles

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
			Source:  "gmail",
			Status:  "pending",
			Impact:  3, // Default medium impact
			Urgency: 3, // Default medium urgency
			Effort:  "M", // Default medium effort
		}

		// Parse pipe-delimited fields
		parts := strings.Split(line, "|")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			partLower := strings.ToLower(part)

			if strings.HasPrefix(partLower, "title:") {
				task.Title = strings.TrimSpace(strings.TrimPrefix(part, "Title:"))
				task.Title = strings.TrimSpace(strings.TrimPrefix(task.Title, "title:"))
			} else if strings.HasPrefix(partLower, "impact:") {
				impactStr := strings.TrimSpace(strings.TrimPrefix(part, "Impact:"))
				impactStr = strings.TrimSpace(strings.TrimPrefix(impactStr, "impact:"))
				// Parse as integer 1-5
				if val, err := strconv.Atoi(impactStr); err == nil && val >= 1 && val <= 5 {
					task.Impact = val
				}
			} else if strings.HasPrefix(partLower, "urgency:") {
				urgencyStr := strings.TrimSpace(strings.TrimPrefix(part, "Urgency:"))
				urgencyStr = strings.TrimSpace(strings.TrimPrefix(urgencyStr, "urgency:"))
				// Parse as integer 1-5
				if val, err := strconv.Atoi(urgencyStr); err == nil && val >= 1 && val <= 5 {
					task.Urgency = val
				}
			} else if strings.HasPrefix(partLower, "effort:") {
				effortStr := strings.TrimSpace(strings.TrimPrefix(part, "Effort:"))
				effortStr = strings.TrimSpace(strings.TrimPrefix(effortStr, "effort:"))
				effortStr = strings.ToUpper(effortStr)
				// Validate S/M/L
				if effortStr == "S" || effortStr == "M" || effortStr == "L" {
					task.Effort = effortStr
				}
			} else if strings.HasPrefix(partLower, "stakeholder:") {
				stakeholderStr := strings.TrimSpace(strings.TrimPrefix(part, "Stakeholder:"))
				stakeholderStr = strings.TrimSpace(strings.TrimPrefix(stakeholderStr, "stakeholder:"))
				// Validate stakeholder is a person's name, not a team/department/company
				if isValidStakeholder(stakeholderStr) {
					task.Stakeholder = stakeholderStr
				} else {
					task.Stakeholder = "" // Clear invalid stakeholders
				}
			} else if strings.HasPrefix(partLower, "project:") {
				projectStr := strings.TrimSpace(strings.TrimPrefix(part, "Project:"))
				projectStr = strings.TrimSpace(strings.TrimPrefix(projectStr, "project:"))
				task.Project = projectStr
			} else if strings.HasPrefix(partLower, "due:") {
				dueStr := strings.TrimSpace(strings.TrimPrefix(part, "Due:"))
				dueStr = strings.TrimSpace(strings.TrimPrefix(dueStr, "due:"))
				// Store due date string for later parsing
				// TODO: Parse relative dates like "tomorrow", "Friday", "Oct 30"
				task.Description = "Due: " + dueStr
			} else if strings.HasPrefix(partLower, "priority:") {
				// Legacy support for old format
				priority := strings.TrimSpace(strings.TrimPrefix(part, "Priority:"))
				priority = strings.TrimSpace(strings.TrimPrefix(priority, "priority:"))
				priority = strings.ToLower(priority)

				// Map to impact and urgency scores (fallback if not explicitly set)
				if task.Impact == 3 { // Only override default
					switch priority {
					case "high", "critical", "urgent":
						task.Impact = 5
						task.Urgency = 4
					case "medium", "moderate":
						task.Impact = 3
						task.Urgency = 3
					case "low":
						task.Impact = 2
						task.Urgency = 2
					}
				}
			}
		}

		// Only add if we got a title, it's not a meeting invitation, and not a duplicate
		if task.Title != "" && !isMeetingInvitation(task.Title) {
			// Check for duplicate title
			titleLower := strings.ToLower(task.Title)
			if !seenTitles[titleLower] {
				seenTitles[titleLower] = true
				tasks = append(tasks, task)
			}
		}
	}

	return tasks
}
