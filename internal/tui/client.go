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
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
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
	ThreadsNeedingAI  int     `json:"threads_needing_ai"`
	LastGmailSync     *string `json:"last_gmail_sync,omitempty"`
	LastDriveSync     *string `json:"last_drive_sync,omitempty"`
	LastCalendarSync  *string `json:"last_calendar_sync,omitempty"`
	LastTasksSync     *string `json:"last_tasks_sync,omitempty"`
}

// ThreadResponse matches the API response structure
type ThreadResponse struct {
	ID             string  `json:"id"`
	LastHistoryID  string  `json:"last_history_id"`
	Summary        string  `json:"summary"`
	SummaryHash    *string `json:"summary_hash,omitempty"`
	TaskCount      int     `json:"task_count"`
	NextFollowupTS *string `json:"next_followup_ts,omitempty"`
	LastSynced     string  `json:"last_synced"`
}

// MessageResponse matches the API response structure
type MessageResponse struct {
	ID          string   `json:"id"`
	ThreadID    string   `json:"thread_id"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Subject     string   `json:"subject"`
	Snippet     string   `json:"snippet"`
	Body        string   `json:"body"`
	Timestamp   string   `json:"timestamp"`
	Labels      []string `json:"labels"`
	Sensitivity string   `json:"sensitivity"`
}

// QueueItemResponse matches the API response structure
type QueueItemResponse struct {
	ThreadID  string `json:"thread_id"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
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

		// Parse timestamps
		createdAt, _ := time.Parse(time.RFC3339, t.CreatedAt)
		updatedAt, _ := time.Parse(time.RFC3339, t.UpdatedAt)

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
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
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

// UncompleteTask marks a task as pending via the remote API (undo completion)
func (c *APIClient) UncompleteTask(taskID string) error {
	path := fmt.Sprintf("/api/tasks/%s/uncomplete", taskID)
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
		ThreadsNeedingAI:  statsResp.ThreadsNeedingAI,
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

// GetThreads fetches threads with summaries from the remote API
func (c *APIClient) GetThreads() ([]*db.Thread, error) {
	resp, err := c.doRequest("GET", "/api/threads", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var threads []ThreadResponse
	if err := json.NewDecoder(resp.Body).Decode(&threads); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to db.Thread
	result := make([]*db.Thread, 0, len(threads))
	for _, t := range threads {
		var nextFollowupTS *time.Time
		if t.NextFollowupTS != nil {
			parsed, err := time.Parse(time.RFC3339, *t.NextFollowupTS)
			if err == nil {
				nextFollowupTS = &parsed
			}
		}

		lastSynced, _ := time.Parse(time.RFC3339, t.LastSynced)

		result = append(result, &db.Thread{
			ID:             t.ID,
			LastHistoryID:  t.LastHistoryID,
			Summary:        t.Summary,
			SummaryHash:    t.SummaryHash,
			TaskCount:      t.TaskCount,
			NextFollowupTS: nextFollowupTS,
			LastSynced:     lastSynced,
		})
	}

	return result, nil
}

// GetThreadMessages fetches messages for a thread from the remote API
func (c *APIClient) GetThreadMessages(threadID string) ([]*db.Message, error) {
	path := fmt.Sprintf("/api/threads/%s/messages", threadID)
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var messages []MessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to db.Message
	result := make([]*db.Message, 0, len(messages))
	for _, m := range messages {
		timestamp, _ := time.Parse(time.RFC3339, m.Timestamp)

		result = append(result, &db.Message{
			ID:          m.ID,
			ThreadID:    m.ThreadID,
			From:        m.From,
			To:          m.To,
			Subject:     m.Subject,
			Snippet:     m.Snippet,
			Body:        m.Body,
			Timestamp:   timestamp,
			Labels:      m.Labels,
			Sensitivity: m.Sensitivity,
		})
	}

	return result, nil
}

// GetQueue fetches threads waiting for AI processing from the remote API
func (c *APIClient) GetQueue() ([]QueueItem, error) {
	resp, err := c.doRequest("GET", "/api/queue", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var queueResp []QueueItemResponse
	if err := json.NewDecoder(resp.Body).Decode(&queueResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert to QueueItem
	result := make([]QueueItem, 0, len(queueResp))
	for _, item := range queueResp {
		timestamp, _ := time.Parse(time.RFC3339, item.Timestamp)

		result = append(result, QueueItem{
			ThreadID:  item.ThreadID,
			Subject:   item.Subject,
			From:      item.From,
			Timestamp: timestamp,
		})
	}

	return result, nil
}

// TriggerProcessing triggers AI processing of the queue via the remote API
func (c *APIClient) TriggerProcessing() error {
	resp, err := c.doRequest("POST", "/api/queue/process", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
