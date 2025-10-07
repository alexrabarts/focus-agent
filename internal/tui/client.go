package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// APIClient wraps HTTP calls to the remote API server
type APIClient struct {
	baseURL string
	authKey string
	client  *http.Client
}

// NewAPIClient creates a new API client
func NewAPIClient(cfg *config.Config) *APIClient {
	return &APIClient{
		baseURL: cfg.Remote.URL,
		authKey: cfg.Remote.AuthKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// TaskResponse matches the API response structure
type TaskResponse struct {
	ID          string  `json:"id"`
	Source      string  `json:"source"`
	SourceID    string  `json:"source_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	DueTS       *string `json:"due_ts,omitempty"`
	Project     string  `json:"project"`
	Impact      int     `json:"impact"`
	Urgency     int     `json:"urgency"`
	Effort      string  `json:"effort"`
	Stakeholder string  `json:"stakeholder"`
	Score       float64 `json:"score"`
	Status      string  `json:"status"`
}

// PrioritiesResponse matches the API response structure
type PrioritiesResponse struct {
	OKRs            []string `json:"okrs"`
	FocusAreas      []string `json:"focus_areas"`
	KeyProjects     []string `json:"key_projects"`
	KeyStakeholders []string `json:"key_stakeholders"`
	UndoAvailable   bool     `json:"undo_available"`
}

// StatsResponse matches the API response structure
type StatsResponse struct {
	ThreadCount       int     `json:"thread_count"`
	MessageCount      int     `json:"message_count"`
	DocCount          int     `json:"doc_count"`
	EventCount        int     `json:"event_count"`
	TaskCount         int     `json:"task_count"`
	PendingTasks      int     `json:"pending_tasks"`
	CompletedToday    int     `json:"completed_today"`
	HighPriorityTasks int     `json:"high_priority_tasks"`
	LastGmailSync     *string `json:"last_gmail_sync,omitempty"`
	LastDriveSync     *string `json:"last_drive_sync,omitempty"`
	LastCalendarSync  *string `json:"last_calendar_sync,omitempty"`
	LastTasksSync     *string `json:"last_tasks_sync,omitempty"`
}

// Helper to make authenticated requests
func (c *APIClient) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var reqBody *bytes.Buffer
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	var req *http.Request
	var err error
	if reqBody != nil {
		req, err = http.NewRequest(method, c.baseURL+path, reqBody)
	} else {
		req, err = http.NewRequest(method, c.baseURL+path, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.authKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var errResp struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
	}

	return resp, nil
}

// GetTasks fetches pending tasks from the remote API
func (c *APIClient) GetTasks() ([]*db.Task, error) {
	resp, err := c.doRequest("GET", "/api/tasks", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tasks []TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to db.Task
	result := make([]*db.Task, 0, len(tasks))
	for _, t := range tasks {
		var dueTS *time.Time
		if t.DueTS != nil {
			parsed, err := time.Parse(time.RFC3339, *t.DueTS)
			if err == nil {
				dueTS = &parsed
			}
		}

		result = append(result, &db.Task{
			ID:          t.ID,
			Source:      t.Source,
			SourceID:    t.SourceID,
			Title:       t.Title,
			Description: t.Description,
			DueTS:       dueTS,
			Project:     t.Project,
			Impact:      t.Impact,
			Urgency:     t.Urgency,
			Effort:      t.Effort,
			Stakeholder: t.Stakeholder,
			Score:       t.Score,
			Status:      t.Status,
		})
	}

	return result, nil
}

// CompleteTask marks a task as complete via the remote API
func (c *APIClient) CompleteTask(taskID string) error {
	path := fmt.Sprintf("/api/tasks/%s/complete", taskID)
	resp, err := c.doRequest("POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// GetPriorities fetches priorities from the remote API
func (c *APIClient) GetPriorities() (*config.Priorities, error) {
	resp, err := c.doRequest("GET", "/api/priorities", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var priorities PrioritiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&priorities); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &config.Priorities{
		OKRs:            priorities.OKRs,
		FocusAreas:      priorities.FocusAreas,
		KeyProjects:     priorities.KeyProjects,
		KeyStakeholders: priorities.KeyStakeholders,
	}, nil
}

// UpdatePriorities sends updated priorities to the remote API
func (c *APIClient) UpdatePriorities(priorities *config.Priorities) error {
	body := PrioritiesResponse{
		OKRs:            priorities.OKRs,
		FocusAreas:      priorities.FocusAreas,
		KeyProjects:     priorities.KeyProjects,
		KeyStakeholders: priorities.KeyStakeholders,
	}

	resp, err := c.doRequest("PUT", "/api/priorities", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// GetStats fetches database statistics from the remote API
func (c *APIClient) GetStats() (Stats, error) {
	resp, err := c.doRequest("GET", "/api/stats", nil)
	if err != nil {
		return Stats{}, err
	}
	defer resp.Body.Close()

	var statsResp StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&statsResp); err != nil {
		return Stats{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert StatsResponse to Stats (TUI Stats struct)
	stats := Stats{
		ThreadCount:       statsResp.ThreadCount,
		MessageCount:      statsResp.MessageCount,
		DocCount:          statsResp.DocCount,
		EventCount:        statsResp.EventCount,
		TaskCount:         statsResp.TaskCount,
		PendingTasks:      statsResp.PendingTasks,
		CompletedToday:    statsResp.CompletedToday,
		HighPriorityTasks: statsResp.HighPriorityTasks,
	}

	// Parse sync times
	if statsResp.LastGmailSync != nil {
		t, err := time.Parse(time.RFC3339, *statsResp.LastGmailSync)
		if err == nil {
			stats.LastGmailSync = &t
		}
	}
	if statsResp.LastDriveSync != nil {
		t, err := time.Parse(time.RFC3339, *statsResp.LastDriveSync)
		if err == nil {
			stats.LastDriveSync = &t
		}
	}
	if statsResp.LastCalendarSync != nil {
		t, err := time.Parse(time.RFC3339, *statsResp.LastCalendarSync)
		if err == nil {
			stats.LastCalendarSync = &t
		}
	}
	if statsResp.LastTasksSync != nil {
		t, err := time.Parse(time.RFC3339, *statsResp.LastTasksSync)
		if err == nil {
			stats.LastTasksSync = &t
		}
	}

	return stats, nil
}
