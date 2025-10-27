package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"google.golang.org/api/tasks/v1"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// TasksClient handles Google Tasks API operations
type TasksClient struct {
	Service *tasks.Service
	Config  *config.Config
}

// TasksSyncState stores Tasks-specific sync state
type TasksSyncState struct {
	UpdatedMin map[string]string `json:"updated_min"` // Per task list
}

// SyncTasks performs sync of Google Tasks
func (t *TasksClient) SyncTasks(ctx context.Context, database *db.DB) error {
	// Get sync state
	syncState, err := database.GetSyncState("tasks")
	if err != nil {
		return fmt.Errorf("failed to get sync state: %w", err)
	}

	var state TasksSyncState
	if syncState.State != "" && syncState.State != "{}" {
		if err := json.Unmarshal([]byte(syncState.State), &state); err != nil {
			log.Printf("Warning: invalid sync state, doing full sync: %v", err)
			state = TasksSyncState{UpdatedMin: make(map[string]string)}
		}
	} else {
		state.UpdatedMin = make(map[string]string)
	}

	// Get all task lists
	taskLists, err := t.Service.Tasklists.List().Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to list task lists: %w", err)
	}

	totalTasks := 0
	maxLists := t.Config.Limits.MaxTaskLists
	listCount := 0

	log.Printf("Found %d task lists, will sync up to %d", len(taskLists.Items), maxLists)

	// Sync each task list
	for _, taskList := range taskLists.Items {
		// Check limit
		if listCount >= maxLists {
			log.Printf("Reached max task list limit (%d), stopping", maxLists)
			break
		}
		listCount++
		count, err := t.syncTaskList(ctx, database, taskList, &state)
		if err != nil {
			log.Printf("Failed to sync task list %s: %v", taskList.Title, err)
			continue
		}
		totalTasks += count
	}

	// Save sync state
	stateJSON, _ := json.Marshal(state)
	syncState.State = string(stateJSON)
	syncState.LastSync = time.Now()
	syncState.NextSync = time.Now().Add(time.Duration(t.Config.Google.PollingMinutes.Tasks) * time.Minute)

	if err := database.SaveSyncState(syncState); err != nil {
		return fmt.Errorf("failed to save sync state: %w", err)
	}

	log.Printf("Tasks sync completed: %d tasks synced", totalTasks)
	return nil
}

// syncTaskList syncs a single task list
func (t *TasksClient) syncTaskList(ctx context.Context, database *db.DB, taskList *tasks.TaskList, state *TasksSyncState) (int, error) {
	log.Printf("Syncing task list: %s", taskList.Title)

	// Build query
	call := t.Service.Tasks.List(taskList.Id).
		Context(ctx).
		MaxResults(100).
		ShowCompleted(false).
		ShowDeleted(false)

	// Use updated min if available for incremental sync
	if updatedMin, ok := state.UpdatedMin[taskList.Id]; ok && updatedMin != "" {
		call = call.UpdatedMin(updatedMin)
	}

	taskItems, err := call.Do()
	if err != nil {
		return 0, fmt.Errorf("failed to list tasks: %w", err)
	}

	count := 0
	latestUpdate := ""

	// Process each task
	for _, task := range taskItems.Items {
		if err := t.processTask(ctx, database, task, taskList.Title); err != nil {
			log.Printf("Failed to process task %s: %v", task.Title, err)
			continue
		}
		count++

		// Track latest update time
		if task.Updated > latestUpdate {
			latestUpdate = task.Updated
		}
	}

	// Update state with latest update time
	if latestUpdate != "" {
		state.UpdatedMin[taskList.Id] = latestUpdate
	}

	return count, nil
}

// processTask saves or updates a task in the database
func (t *TasksClient) processTask(ctx context.Context, database *db.DB, task *tasks.Task, listName string) error {
	// Parse due date
	var dueTS *time.Time
	if task.Due != "" {
		due, err := time.Parse(time.RFC3339, task.Due)
		if err == nil {
			dueTS = &due
		}
	}

	// Determine status
	status := "pending"
	if task.Status == "completed" {
		status = "completed"
	}

	// Calculate urgency based on due date
	urgency := 2
	if dueTS != nil {
		hoursUntil := time.Until(*dueTS).Hours()
		if hoursUntil <= 24 {
			urgency = 5
		} else if hoursUntil <= 72 {
			urgency = 4
		} else if hoursUntil <= 168 {
			urgency = 3
		}
	}

	// Parse updated timestamp from Google Tasks
	var updatedTS *time.Time
	if task.Updated != "" {
		updated, err := time.Parse(time.RFC3339, task.Updated)
		if err == nil {
			updatedTS = &updated
		}
	}

	// Create task record
	taskRecord := &db.Task{
		ID:          fmt.Sprintf("gtask_%s", task.Id),
		Source:      "gtasks",
		SourceID:    task.Id,
		Title:       task.Title,
		Description:task.Notes,
		DueTS:       dueTS,
		Project:     listName,
		Impact:      3, // Default medium impact
		Urgency:     urgency,
		Effort:      "M", // Default medium effort
		Status:      status,
		Metadata:    fmt.Sprintf(`{"list":"%s","position":"%s","updated":"%s"}`, listName, task.Position, task.Updated),
		UpdatedAt:   time.Now(), // Will be set by SaveTask if not already set
	}

	// Use Google Tasks updated timestamp as created_at for new tasks
	if updatedTS != nil {
		taskRecord.CreatedAt = *updatedTS
	}

	// Save to database
	if err := database.SaveTask(taskRecord); err != nil {
		return fmt.Errorf("failed to save task: %w", err)
	}

	return nil
}

// CreateTask creates a new task in Google Tasks
func (t *TasksClient) CreateTask(ctx context.Context, listID, title, notes string, due *time.Time) (*tasks.Task, error) {
	task := &tasks.Task{
		Title: title,
		Notes: notes,
	}

	if due != nil {
		task.Due = due.Format(time.RFC3339)
	}

	createdTask, err := t.Service.Tasks.Insert(listID, task).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	return createdTask, nil
}

// UpdateTask updates an existing task
func (t *TasksClient) UpdateTask(ctx context.Context, listID, taskID string, updates *tasks.Task) (*tasks.Task, error) {
	updatedTask, err := t.Service.Tasks.Update(listID, taskID, updates).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to update task: %w", err)
	}

	return updatedTask, nil
}

// CompleteTask marks a task as completed
func (t *TasksClient) CompleteTask(ctx context.Context, listID, taskID string) error {
	task := &tasks.Task{
		Status: "completed",
	}

	_, err := t.Service.Tasks.Update(listID, taskID, task).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}

	return nil
}

// UncompleteTask marks a task as needing action (uncompletes it)
func (t *TasksClient) UncompleteTask(ctx context.Context, listID, taskID string) error {
	task := &tasks.Task{
		Status: "needsAction",
	}

	_, err := t.Service.Tasks.Update(listID, taskID, task).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to uncomplete task: %w", err)
	}

	return nil
}

// GetTaskLists retrieves all task lists
func (t *TasksClient) GetTaskLists(ctx context.Context) ([]*tasks.TaskList, error) {
	lists, err := t.Service.Tasklists.List().Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get task lists: %w", err)
	}

	return lists.Items, nil
}

// GetTasks retrieves tasks from a specific list
func (t *TasksClient) GetTasks(ctx context.Context, listID string, showCompleted bool) ([]*tasks.Task, error) {
	call := t.Service.Tasks.List(listID).
		Context(ctx).
		MaxResults(100).
		ShowCompleted(showCompleted)

	taskItems, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get tasks: %w", err)
	}

	return taskItems.Items, nil
}