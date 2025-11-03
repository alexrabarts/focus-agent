package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
)

// Planner handles task prioritization and planning
type Planner struct {
	db     *db.DB
	google *google.Clients
	llm    llm.Client
	config *config.Config
}

// New creates a new planner
func New(database *db.DB, googleClients *google.Clients, llmClient llm.Client, cfg *config.Config) *Planner {
	return &Planner{
		db:     database,
		google: googleClients,
		llm:    llmClient,
		config: cfg,
	}
}

// PrioritizeTasks recalculates scores for all pending tasks
func (p *Planner) PrioritizeTasks(ctx context.Context) error {
	// Get all pending tasks
	query := `
		SELECT id, source, source_id, title, description, due_ts, project,
		       impact, urgency, effort, stakeholder, status
		FROM tasks
		WHERE status = 'pending'
	`

	rows, err := p.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*db.Task
	for rows.Next() {
		task := &db.Task{}
		var dueTS *int64

		err := rows.Scan(
			&task.ID, &task.Source, &task.SourceID, &task.Title,
			&task.Description, &dueTS, &task.Project,
			&task.Impact, &task.Urgency, &task.Effort, &task.Stakeholder,
			&task.Status,
		)
		if err != nil {
			log.Printf("Failed to scan task: %v", err)
			continue
		}

		if dueTS != nil {
			t := time.Unix(*dueTS, 0)
			task.DueTS = &t

			// Update urgency based on due date
			task.Urgency = p.calculateUrgencyFromDue(t)
		}

		// Get strategic alignment and matched priorities (single LLM call)
		strategicScore, matches := p.CalculateStrategicAlignmentWithMatches(task)

		// Calculate score using pre-calculated strategic score (avoids double LLM call)
		task.Score = p.calculateScoreWithStrategic(task, strategicScore)

		// Store matched priorities
		matchesJSON, err := json.Marshal(matches)
		if err != nil {
			log.Printf("Failed to marshal matched priorities: %v", err)
			matchesJSON = []byte("{}")
		}
		task.MatchedPriorities = string(matchesJSON)

		tasks = append(tasks, task)
	}

	// Update scores and matched priorities in database
	for _, task := range tasks {
		updateQuery := `UPDATE tasks SET score = ?, urgency = ?, matched_priorities = ? WHERE id = ?`
		if _, err := p.db.Exec(updateQuery, task.Score, task.Urgency, task.MatchedPriorities, task.ID); err != nil {
			log.Printf("Failed to update task score: %v", err)
		}
	}

	log.Printf("Prioritized %d tasks", len(tasks))
	return nil
}

// PrioritizeTask scores a single task immediately (used during extraction)
func (p *Planner) PrioritizeTask(ctx context.Context, task *db.Task) error {
	// Update urgency based on due date if present
	if task.DueTS != nil {
		task.Urgency = p.calculateUrgencyFromDue(*task.DueTS)
	}

	// Get strategic alignment and matched priorities (single LLM call)
	strategicScore, matches := p.CalculateStrategicAlignmentWithMatches(task)

	// Calculate score using pre-calculated strategic score
	task.Score = p.calculateScoreWithStrategic(task, strategicScore)

	// Store matched priorities
	matchesJSON, err := json.Marshal(matches)
	if err != nil {
		log.Printf("Failed to marshal matched priorities: %v", err)
		matchesJSON = []byte("{}")
	}
	task.MatchedPriorities = string(matchesJSON)

	// Update score, urgency, and matched priorities in database
	updateQuery := `UPDATE tasks SET score = ?, urgency = ?, matched_priorities = ? WHERE id = ?`
	if _, err := p.db.Exec(updateQuery, task.Score, task.Urgency, task.MatchedPriorities, task.ID); err != nil {
		return fmt.Errorf("failed to update task score: %w", err)
	}

	return nil
}

// calculateScore implements the scoring formula with strategic alignment
func (p *Planner) calculateScore(task *db.Task) float64 {
	// Calculate strategic alignment score (0-5)
	strategicScore := p.calculateStrategicAlignment(task)
	return p.calculateScoreWithStrategic(task, strategicScore)
}

// calculateScoreWithStrategic implements the scoring formula with a pre-calculated strategic score
func (p *Planner) calculateScoreWithStrategic(task *db.Task, strategicScore float64) float64 {
	// Default values if not set
	impact := float64(task.Impact)
	if impact == 0 {
		impact = 3
	}

	urgency := float64(task.Urgency)
	if urgency == 0 {
		urgency = 3
	}

	// Effort factor (S=0.5, M=1.0, L=1.5)
	effortFactor := 1.0
	switch task.Effort {
	case "S":
		effortFactor = 0.5
	case "L":
		effortFactor = 1.5
	}

	// Stakeholder weight (internal=1.0, external=1.5, executive=2.0)
	stakeholderWeight := 1.0
	switch task.Stakeholder {
	case "external":
		stakeholderWeight = 1.5
	case "executive":
		stakeholderWeight = 2.0
	}

	// Apply formula: score = 0.3*strategic + 0.25*urgency + 0.2*impact + 0.15*stakeholder - 0.1*effort
	rawScore := 0.3*strategicScore +
		0.25*urgency +
		0.2*impact +
		0.15*stakeholderWeight -
		0.1*effortFactor

	// Clamp to valid range
	if rawScore > 4.0 {
		rawScore = 4.0
	} else if rawScore < 0 {
		rawScore = 0
	}

	// Convert to percentage (0-100) and round to whole number
	percentage := (rawScore / 4.0) * 100.0
	return float64(int(percentage + 0.5)) // Round to nearest integer
}

// calculateStrategicAlignment scores how well a task aligns with strategic priorities
func (p *Planner) calculateStrategicAlignment(task *db.Task) float64 {
	score, _ := p.CalculateStrategicAlignmentWithMatches(task)
	return score
}

// GetPriorities returns priorities from database, falling back to config
func (p *Planner) GetPriorities() *config.Priorities {
	// Try loading from database first
	dbPriorities, err := p.db.GetActivePriorities()
	if err != nil {
		log.Printf("Failed to load priorities from database, falling back to config: %v", err)
		return &p.config.Priorities
	}

	// Check if database has priorities
	if len(dbPriorities) == 0 {
		log.Printf("No priorities in database, using config")
		return &p.config.Priorities
	}

	// Convert database format to config.Priorities format
	priorities := &config.Priorities{
		OKRs:            dbPriorities["okr"],
		FocusAreas:      dbPriorities["focus_area"],
		KeyStakeholders: dbPriorities["stakeholder"],
		KeyProjects:     dbPriorities["project"],
	}

	return priorities
}

// SavePriorities saves priorities to the database
func (p *Planner) SavePriorities(priorities *config.Priorities) error {
	return p.db.UpdatePriorities(priorities)
}

// CalculateStrategicAlignmentWithMatches scores alignment and returns which priorities matched
// Uses LLM for semantic understanding rather than keyword matching
func (p *Planner) CalculateStrategicAlignmentWithMatches(task *db.Task) (float64, *db.PriorityMatches) {
	matches := &db.PriorityMatches{
		OKRs:       []string{},
		FocusAreas: []string{},
		Projects:   []string{},
	}

	// Get priorities (database-first, config fallback)
	priorities := p.GetPriorities()

	// Use LLM to evaluate strategic alignment
	ctx := context.Background()
	result, err := p.llm.EvaluateStrategicAlignment(ctx, task, priorities)
	if err != nil {
		log.Printf("Failed to evaluate strategic alignment for task %s: %v", task.ID, err)
		// Fall back to zero score if LLM fails
		return 0.0, matches
	}

	// Convert LLM result to PriorityMatches
	matches.OKRs = result.OKRs
	matches.FocusAreas = result.FocusAreas
	matches.Projects = result.Projects
	matches.KeyStakeholder = result.KeyStakeholder

	// Check if task source is from key stakeholders (keep this logic as it's not semantic)
	if !matches.KeyStakeholder {
		for _, stakeholder := range priorities.KeyStakeholders {
			if task.SourceID != "" && strings.Contains(strings.ToLower(task.SourceID), strings.ToLower(stakeholder)) {
				matches.KeyStakeholder = true
				break
			}
		}
	}

	return result.Score, matches
}

// calculateUrgencyFromDue calculates urgency based on due date
func (p *Planner) calculateUrgencyFromDue(dueDate time.Time) int {
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
	case hoursUntil <= 720:
		return 2 // Next month
	default:
		return 1 // Later
	}
}

// GenerateDailyBrief generates and sends the daily brief
func (p *Planner) GenerateDailyBrief(ctx context.Context) error {
	// Get top priority tasks
	tasks, err := p.db.GetPendingTasks(p.config.Planner.MaxTasksPerBrief)
	if err != nil {
		return fmt.Errorf("failed to get tasks: %w", err)
	}

	// Get today's events
	events, err := p.db.GetUpcomingEvents(24)
	if err != nil {
		return fmt.Errorf("failed to get events: %w", err)
	}

	// Send to Chat
	if err := p.google.Chat.SendDailyBrief(ctx, tasks, events); err != nil {
		return fmt.Errorf("failed to send brief: %w", err)
	}

	// Log the brief generation
	p.db.LogUsage("planner", "daily_brief", 0, 0, 0, nil)

	return nil
}

// GenerateReplanBrief generates and sends the midday replan brief
func (p *Planner) GenerateReplanBrief(ctx context.Context) error {
	// Get completed tasks count for today
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var completedCount int
	completedQuery := `
		SELECT COUNT(*) FROM tasks
		WHERE status = 'completed'
		AND completed_at >= ?
	`
	err := p.db.QueryRow(completedQuery, startOfDay.Unix()).Scan(&completedCount)
	if err != nil {
		log.Printf("Failed to count completed tasks: %v", err)
		completedCount = 0
	}

	// Get remaining priority tasks
	remainingTasks, err := p.db.GetPendingTasks(5)
	if err != nil {
		return fmt.Errorf("failed to get remaining tasks: %w", err)
	}

	// Get afternoon events
	afternoonEvents, err := p.db.GetUpcomingEvents(8)
	if err != nil {
		return fmt.Errorf("failed to get afternoon events: %w", err)
	}

	// Send replan brief
	if err := p.google.Chat.SendReplanBrief(ctx, completedCount, remainingTasks, afternoonEvents); err != nil {
		return fmt.Errorf("failed to send replan brief: %w", err)
	}

	// Log the brief generation
	p.db.LogUsage("planner", "replan_brief", 0, 0, 0, nil)

	return nil
}

// CheckFollowUps checks for threads needing follow-up
func (p *Planner) CheckFollowUps(ctx context.Context) error {
	// Get threads with follow-ups due
	query := `
		SELECT id, summary FROM threads
		WHERE next_followup_ts IS NOT NULL
		AND next_followup_ts <= ?
		LIMIT 10
	`

	now := time.Now()
	rows, err := p.db.Query(query, now.Unix())
	if err != nil {
		return fmt.Errorf("failed to query follow-ups: %w", err)
	}
	defer rows.Close()

	var threads []*db.Thread
	for rows.Next() {
		thread := &db.Thread{}
		err := rows.Scan(&thread.ID, &thread.Summary)
		if err != nil {
			continue
		}
		threads = append(threads, thread)
	}

	if len(threads) == 0 {
		return nil
	}

	// Send follow-up reminder
	if err := p.google.Chat.SendFollowUpReminder(ctx, threads); err != nil {
		return fmt.Errorf("failed to send follow-up reminder: %w", err)
	}

	// Update follow-up times
	updateQuery := `UPDATE threads SET next_followup_ts = ? WHERE id = ?`
	nextTime := now.Add(time.Duration(p.config.Schedule.FollowUpMinutes) * time.Minute)

	for _, thread := range threads {
		if _, err := p.db.Exec(updateQuery, nextTime.Unix(), thread.ID); err != nil {
			log.Printf("Failed to update follow-up time for thread %s: %v", thread.ID, err)
		}
	}

	log.Printf("Sent follow-up reminders for %d threads", len(threads))
	return nil
}

// GeneratePlan creates a time-blocked plan for the day
func (p *Planner) GeneratePlan(ctx context.Context) (string, error) {
	// Get top tasks
	tasks, err := p.db.GetPendingTasks(15)
	if err != nil {
		return "", fmt.Errorf("failed to get tasks: %w", err)
	}

	// Get today's events
	events, err := p.db.GetUpcomingEvents(24)
	if err != nil {
		return "", fmt.Errorf("failed to get events: %w", err)
	}

	// Generate plan text
	plan := p.formatPlan(tasks, events)

	return plan, nil
}

// formatPlan creates a formatted daily plan
func (p *Planner) formatPlan(tasks []*db.Task, events []*db.Event) string {
	now := time.Now()
	var plan string

	plan += fmt.Sprintf("Daily Plan - %s\n", now.Format("Monday, January 2"))
	plan += "=" + strings.Repeat("=", 40) + "\n\n"

	// Morning focus block
	plan += "Morning Focus Block (9:00 - 11:00 AM)\n"
	plan += "-" + strings.Repeat("-", 40) + "\n"

	// Assign top 2-3 tasks to morning
	morningTasks := 0
	for _, task := range tasks {
		if morningTasks >= 3 {
			break
		}
		if task.Effort != "L" { // Skip large tasks for morning
			plan += fmt.Sprintf("- %s\n", task.Title)
			morningTasks++
		}
	}
	plan += "\n"

	// Check for meetings
	hasMeetings := false
	for _, event := range events {
		if event.StartTS.Day() == now.Day() {
			if !hasMeetings {
				plan += "Meetings\n"
				plan += "-" + strings.Repeat("-", 40) + "\n"
				hasMeetings = true
			}
			plan += fmt.Sprintf("- %s - %s: %s\n",
				event.StartTS.Format("3:04 PM"),
				event.EndTS.Format("3:04 PM"),
				event.Title)
			if event.MeetingLink != "" {
				plan += fmt.Sprintf("  Link: %s\n", event.MeetingLink)
			}
		}
	}
	if hasMeetings {
		plan += "\n"
	}

	// Afternoon focus block
	plan += "Afternoon Focus Block (2:00 - 4:00 PM)\n"
	plan += "-" + strings.Repeat("-", 40) + "\n"

	// Assign next 2-3 tasks
	afternoonTasks := 0
	for i := morningTasks; i < len(tasks) && afternoonTasks < 3; i++ {
		plan += fmt.Sprintf("- %s\n", tasks[i].Title)
		afternoonTasks++
	}
	plan += "\n"

	// Quick wins (small tasks)
	plan += "Quick Wins (4:00 - 5:00 PM)\n"
	plan += "-" + strings.Repeat("-", 40) + "\n"

	quickWins := 0
	for _, task := range tasks {
		if task.Effort == "S" && quickWins < 5 {
			plan += fmt.Sprintf("- %s\n", task.Title)
			quickWins++
		}
	}

	return plan
}

// CompleteTask marks a task as completed and adjusts scores
func (p *Planner) CompleteTask(ctx context.Context, taskID string) error {
	// First, get the task to check if it's from Google Tasks
	var task db.Task
	query := `SELECT source, source_id, metadata FROM tasks WHERE id = ?`
	if err := p.db.QueryRow(query, taskID).Scan(&task.Source, &task.SourceID, &task.Metadata); err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	now := time.Now()
	updateQuery := `UPDATE tasks SET status = 'completed', completed_at = ? WHERE id = ?`

	if _, err := p.db.Exec(updateQuery, now.Unix(), taskID); err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}

	// If task is from Google Tasks, sync completion back to Google
	log.Printf("Task completion: source=%s, source_id=%s, metadata=%s", task.Source, task.SourceID, task.Metadata)
	if task.Source == "gtasks" && task.SourceID != "" {
		// Parse metadata to get list name
		var metadata map[string]string
		if err := json.Unmarshal([]byte(task.Metadata), &metadata); err == nil {
			listName := metadata["list"]
			log.Printf("Looking for Google Tasks list: %s", listName)

			// Get task lists to find the list ID
			lists, err := p.google.Tasks.GetTaskLists(ctx)
			if err != nil {
				log.Printf("Warning: failed to get task lists for Google Tasks sync: %v", err)
			} else {
				log.Printf("Found %d task lists", len(lists))
				// Find list ID by name
				var listID string
				for _, list := range lists {
					log.Printf("Checking list: title=%s, id=%s", list.Title, list.Id)
					if list.Title == listName {
						listID = list.Id
						break
					}
				}

				if listID != "" {
					log.Printf("Found list ID: %s, completing task: %s", listID, task.SourceID)
					// Sync completion to Google Tasks
					if err := p.google.Tasks.CompleteTask(ctx, listID, task.SourceID); err != nil {
						log.Printf("Warning: failed to sync task completion to Google Tasks: %v", err)
					} else {
						log.Printf("Synced task completion to Google Tasks: %s", task.SourceID)
					}
				} else {
					log.Printf("Warning: could not find Google Tasks list: %s", listName)
				}
			}
		}
	}

	// Trigger re-prioritization
	return p.PrioritizeTasks(ctx)
}

// UncompleteTask marks a task as pending (undo completion)
func (p *Planner) UncompleteTask(ctx context.Context, taskID string) error {
	// First, get the task to check if it's from Google Tasks
	var task db.Task
	query := `SELECT source, source_id, metadata FROM tasks WHERE id = ?`
	if err := p.db.QueryRow(query, taskID).Scan(&task.Source, &task.SourceID, &task.Metadata); err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	updateQuery := `UPDATE tasks SET status = 'pending', completed_at = NULL WHERE id = ?`

	if _, err := p.db.Exec(updateQuery, taskID); err != nil {
		return fmt.Errorf("failed to uncomplete task: %w", err)
	}

	// If task is from Google Tasks, sync uncomplete back to Google
	if task.Source == "gtasks" && task.SourceID != "" {
		// Parse metadata to get list name
		var metadata map[string]string
		if err := json.Unmarshal([]byte(task.Metadata), &metadata); err == nil {
			listName := metadata["list"]

			// Get task lists to find the list ID
			lists, err := p.google.Tasks.GetTaskLists(ctx)
			if err != nil {
				log.Printf("Warning: failed to get task lists for Google Tasks sync: %v", err)
			} else {
				// Find list ID by name
				var listID string
				for _, list := range lists {
					if list.Title == listName {
						listID = list.Id
						break
					}
				}

				if listID != "" {
					// Sync uncomplete to Google Tasks
					if err := p.google.Tasks.UncompleteTask(ctx, listID, task.SourceID); err != nil {
						log.Printf("Warning: failed to sync task uncomplete to Google Tasks: %v", err)
					} else {
						log.Printf("Synced task uncomplete to Google Tasks: %s", task.SourceID)
					}
				} else {
					log.Printf("Warning: could not find Google Tasks list: %s", listName)
				}
			}
		}
	}

	// Trigger re-prioritization
	return p.PrioritizeTasks(ctx)
}

// SnoozeTask defers a task to a later time
func (p *Planner) SnoozeTask(ctx context.Context, taskID string, until time.Time) error {
	// Update due date
	query := `UPDATE tasks SET due_ts = ? WHERE id = ?`

	if _, err := p.db.Exec(query, until.Unix(), taskID); err != nil {
		return fmt.Errorf("failed to snooze task: %w", err)
	}

	// Trigger re-prioritization
	return p.PrioritizeTasks(ctx)
}

// GetTaskStats returns statistics about tasks
func (p *Planner) GetTaskStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Total tasks by status
	var pending, completed, inProgress int

	err := p.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = 'pending'`).Scan(&pending)
	if err != nil {
		pending = 0
	}

	err = p.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = 'completed'`).Scan(&completed)
	if err != nil {
		completed = 0
	}

	err = p.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = 'in_progress'`).Scan(&inProgress)
	if err != nil {
		inProgress = 0
	}

	stats["pending"] = pending
	stats["completed"] = completed
	stats["in_progress"] = inProgress

	// Today's completions
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var todayCompleted int
	err = p.db.QueryRow(`
		SELECT COUNT(*) FROM tasks
		WHERE status = 'completed'
		AND completed_at >= ?
	`, startOfDay.Unix()).Scan(&todayCompleted)

	if err != nil {
		todayCompleted = 0
	}

	stats["today_completed"] = todayCompleted

	// High priority tasks
	var highPriority int
	err = p.db.QueryRow(`
		SELECT COUNT(*) FROM tasks
		WHERE status = 'pending'
		AND score >= 4
	`).Scan(&highPriority)

	if err != nil {
		highPriority = 0
	}

	stats["high_priority"] = highPriority

	return stats, nil
}
// RecalculateThreadPriorities recalculates priority scores for all threads
func (p *Planner) RecalculateThreadPriorities(ctx context.Context) error {
	return p.db.RecalculateThreadPriorities()
}
