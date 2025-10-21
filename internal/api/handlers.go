package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Task response structure
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

// Priorities response structure
type PrioritiesResponse struct {
	OKRs            []string `json:"okrs"`
	FocusAreas      []string `json:"focus_areas"`
	KeyProjects     []string `json:"key_projects"`
	KeyStakeholders []string `json:"key_stakeholders"`
	UndoAvailable   bool     `json:"undo_available"`
}

// Stats response structure
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

// Thread response structure
type ThreadResponse struct {
	ID             string  `json:"id"`
	LastHistoryID  string  `json:"last_history_id"`
	Summary        string  `json:"summary"`
	SummaryHash    string  `json:"summary_hash"`
	TaskCount      int     `json:"task_count"`
	NextFollowupTS *string `json:"next_followup_ts,omitempty"`
	LastSynced     string  `json:"last_synced"`
}

// Message response structure
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

// Queue item response structure
type QueueItemResponse struct {
	ThreadID  string `json:"thread_id"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
}

// GET /api/tasks - List pending tasks
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	tasks, err := s.database.GetPendingTasks(50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to response format
	response := make([]TaskResponse, 0, len(tasks))
	for _, task := range tasks {
		var dueTS *string
		if task.DueTS != nil {
			formatted := task.DueTS.Format(time.RFC3339)
			dueTS = &formatted
		}

		response = append(response, TaskResponse{
			ID:          task.ID,
			Source:      task.Source,
			SourceID:    task.SourceID,
			Title:       task.Title,
			Description: task.Description,
			DueTS:       dueTS,
			Project:     task.Project,
			Impact:      task.Impact,
			Urgency:     task.Urgency,
			Effort:      task.Effort,
			Stakeholder: task.Stakeholder,
			Score:       task.Score,
			Status:      task.Status,
			CreatedAt:   task.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   task.UpdatedAt.Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, response)
}

// POST /api/tasks/:id/complete - Complete a task
// POST /api/tasks/:id/uncomplete - Uncomplete a task (undo)
func (s *Server) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract task ID and action from path
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "Invalid path")
		return
	}

	taskID := parts[0]
	action := parts[1]

	if taskID == "" {
		writeError(w, http.StatusBadRequest, "Invalid task ID")
		return
	}

	ctx := context.Background()

	switch action {
	case "complete":
		if err := s.planner.CompleteTask(ctx, taskID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})

	case "uncomplete":
		if err := s.planner.UncompleteTask(ctx, taskID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})

	default:
		writeError(w, http.StatusBadRequest, "Invalid action")
	}
}

// GET /api/priorities - Get all priorities
// PUT /api/priorities - Update priorities
func (s *Server) handlePriorities(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getPriorities(w, r)
	case http.MethodPut:
		s.updatePriorities(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) getPriorities(w http.ResponseWriter, r *http.Request) {
	// Note: undoAvailable would need to be tracked in state
	// For now, we'll return false since undo state is per-TUI session
	response := PrioritiesResponse{
		OKRs:            s.config.Priorities.OKRs,
		FocusAreas:      s.config.Priorities.FocusAreas,
		KeyProjects:     s.config.Priorities.KeyProjects,
		KeyStakeholders: s.config.Priorities.KeyStakeholders,
		UndoAvailable:   false,
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) updatePriorities(w http.ResponseWriter, r *http.Request) {
	var req PrioritiesResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Update config in memory
	s.config.Priorities.OKRs = req.OKRs
	s.config.Priorities.FocusAreas = req.FocusAreas
	s.config.Priorities.KeyProjects = req.KeyProjects
	s.config.Priorities.KeyStakeholders = req.KeyStakeholders

	// Save to file - reuse the same logic from TUI
	// We'll need to pass the config path through
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// POST /api/priorities/undo - Undo last priority change
func (s *Server) handlePrioritiesUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Undo functionality would need server-side state management
	// For now, return not implemented
	writeError(w, http.StatusNotImplemented, "Undo not available in remote mode")
}

// GET /api/stats - Get database statistics
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var stats StatsResponse

	// Count records
	s.database.QueryRow("SELECT COUNT(*) FROM threads").Scan(&stats.ThreadCount)
	s.database.QueryRow("SELECT COUNT(*) FROM messages").Scan(&stats.MessageCount)
	s.database.QueryRow("SELECT COUNT(*) FROM docs").Scan(&stats.DocCount)
	s.database.QueryRow("SELECT COUNT(*) FROM events").Scan(&stats.EventCount)
	s.database.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&stats.TaskCount)
	s.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'").Scan(&stats.PendingTasks)
	s.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending' AND score >= 4.0").Scan(&stats.HighPriorityTasks)
	s.database.QueryRow("SELECT COUNT(*) FROM threads WHERE summary IS NULL OR summary = ''").Scan(&stats.ThreadsNeedingAI)

	// Completed today
	today := time.Now().Format("2006-01-02")
	s.database.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'completed' AND DATE(completed_at) = ?", today).Scan(&stats.CompletedToday)

	// Last sync times
	var gmailSyncStr, driveSyncStr, calendarSyncStr, tasksSyncStr string
	err := s.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'gmail' ORDER BY last_sync DESC LIMIT 1").Scan(&gmailSyncStr)
	if err == nil {
		stats.LastGmailSync = &gmailSyncStr
	}

	err = s.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'drive' ORDER BY last_sync DESC LIMIT 1").Scan(&driveSyncStr)
	if err == nil {
		stats.LastDriveSync = &driveSyncStr
	}

	err = s.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'calendar' ORDER BY last_sync DESC LIMIT 1").Scan(&calendarSyncStr)
	if err == nil {
		stats.LastCalendarSync = &calendarSyncStr
	}

	err = s.database.QueryRow("SELECT last_sync FROM sync_status WHERE source = 'tasks' ORDER BY last_sync DESC LIMIT 1").Scan(&tasksSyncStr)
	if err == nil {
		stats.LastTasksSync = &tasksSyncStr
	}

	writeJSON(w, http.StatusOK, stats)
}

// GET /api/threads - List threads with summaries
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	threads, err := s.database.GetThreadsWithSummaries(50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to response format
	response := make([]ThreadResponse, 0, len(threads))
	for _, thread := range threads {
		var nextFollowupTS *string
		if thread.NextFollowupTS != nil {
			formatted := thread.NextFollowupTS.Format(time.RFC3339)
			nextFollowupTS = &formatted
		}

		response = append(response, ThreadResponse{
			ID:             thread.ID,
			LastHistoryID:  thread.LastHistoryID,
			Summary:        thread.Summary,
			SummaryHash:    thread.SummaryHash,
			TaskCount:      thread.TaskCount,
			NextFollowupTS: nextFollowupTS,
			LastSynced:     thread.LastSynced.Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, response)
}

// GET /api/threads/:id/messages - Get messages for a thread
func (s *Server) handleThreadMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract thread ID from path: /api/threads/:id/messages
	path := strings.TrimPrefix(r.URL.Path, "/api/threads/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "messages" {
		writeError(w, http.StatusBadRequest, "Invalid path - expected /api/threads/:id/messages")
		return
	}

	threadID := parts[0]
	if threadID == "" {
		writeError(w, http.StatusBadRequest, "Invalid thread ID")
		return
	}

	messages, err := s.database.GetThreadMessages(threadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to response format
	response := make([]MessageResponse, 0, len(messages))
	for _, msg := range messages {
		response = append(response, MessageResponse{
			ID:          msg.ID,
			ThreadID:    msg.ThreadID,
			From:        msg.From,
			To:          msg.To,
			Subject:     msg.Subject,
			Snippet:     msg.Snippet,
			Body:        msg.Body,
			Timestamp:   msg.Timestamp.Format(time.RFC3339),
			Labels:      msg.Labels,
			Sensitivity: msg.Sensitivity,
		})
	}

	writeJSON(w, http.StatusOK, response)
}

// GET /api/queue - List threads waiting for AI processing
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Query threads without summaries
	query := `
		SELECT DISTINCT t.id, ANY_VALUE(m.subject) as subject, ANY_VALUE(m.from_addr) as from_addr, MAX(m.ts) as ts
		FROM threads t
		JOIN messages m ON t.id = m.thread_id
		WHERE t.summary IS NULL OR t.summary = ''
		GROUP BY t.id
		ORDER BY MAX(m.ts) DESC
		LIMIT 100
	`

	rows, err := s.database.Query(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	response := make([]QueueItemResponse, 0)
	for rows.Next() {
		var item QueueItemResponse
		var tsUnix int64
		if scanErr := rows.Scan(&item.ThreadID, &item.Subject, &item.From, &tsUnix); scanErr != nil {
			continue
		}
		item.Timestamp = time.Unix(tsUnix, 0).Format(time.RFC3339)
		response = append(response, item)
	}

	writeJSON(w, http.StatusOK, response)
}

// POST /api/queue/process - Trigger AI processing of queue
func (s *Server) handleQueueProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Trigger processing via scheduler
	// This requires access to the scheduler, which we need to add to the Server
	if s.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "Processing not available")
		return
	}

	// Trigger processing in background
	go s.scheduler.ProcessNewMessages()

	writeJSON(w, http.StatusOK, map[string]string{"status": "processing started"})
}

// POST /api/brief - Send daily brief via Google Chat
func (s *Server) handleBrief(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	ctx := context.Background()

	// Get pending tasks (top 10 for the brief)
	tasks, err := s.database.GetPendingTasks(10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get tasks: "+err.Error())
		return
	}

	// Get upcoming events for the next 24 hours
	events, err := s.database.GetUpcomingEvents(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get events: "+err.Error())
		return
	}

	// Send the daily brief via Chat API
	if err := s.clients.Chat.SendDailyBrief(ctx, tasks, events); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to send brief: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       "sent",
		"tasks_count":  len(tasks),
		"events_count": len(events),
	})
}
