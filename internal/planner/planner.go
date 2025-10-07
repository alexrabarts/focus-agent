package planner

import (
	"context"
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
	llm    *llm.GeminiClient
	config *config.Config
}

// New creates a new planner
func New(database *db.DB, googleClients *google.Clients, llmClient *llm.GeminiClient, cfg *config.Config) *Planner {
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

		// Calculate score
		task.Score = p.calculateScore(task)
		tasks = append(tasks, task)
	}

	// Update scores in database
	for _, task := range tasks {
		updateQuery := `UPDATE tasks SET score = ?, urgency = ? WHERE id = ?`
		if _, err := p.db.Exec(updateQuery, task.Score, task.Urgency, task.ID); err != nil {
			log.Printf("Failed to update task score: %v", err)
		}
	}

	log.Printf("Prioritized %d tasks", len(tasks))
	return nil
}

// calculateScore implements the scoring formula with strategic alignment
func (p *Planner) calculateScore(task *db.Task) float64 {
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

	// Calculate strategic alignment score (0-5)
	strategicScore := p.calculateStrategicAlignment(task)

	// Apply formula: score = 0.3*impact + 0.25*urgency + 0.2*strategic + 0.15*stakeholder - 0.1*effort
	score := 0.3*impact +
		0.25*urgency +
		0.2*strategicScore +
		0.15*stakeholderWeight -
		0.1*effortFactor

	// Normalize to 0-5 scale
	if score > 5 {
		score = 5
	} else if score < 0 {
		score = 0
	}

	return score
}

// calculateStrategicAlignment scores how well a task aligns with strategic priorities
func (p *Planner) calculateStrategicAlignment(task *db.Task) float64 {
	score := 0.0
	maxScore := 5.0

	// Combine title and description for matching
	content := strings.ToLower(task.Title + " " + task.Description)

	// Check against OKRs (highest weight)
	okrMatches := 0
	for _, okr := range p.config.Priorities.OKRs {
		keywords := extractKeywords(okr)
		for _, keyword := range keywords {
			if strings.Contains(content, strings.ToLower(keyword)) {
				okrMatches++
				break // Only count once per OKR
			}
		}
	}
	if len(p.config.Priorities.OKRs) > 0 {
		score += (float64(okrMatches) / float64(len(p.config.Priorities.OKRs))) * 2.0 // Max 2.0
	}

	// Check against focus areas
	focusMatches := 0
	for _, area := range p.config.Priorities.FocusAreas {
		keywords := extractKeywords(area)
		for _, keyword := range keywords {
			if strings.Contains(content, strings.ToLower(keyword)) {
				focusMatches++
				break
			}
		}
	}
	if len(p.config.Priorities.FocusAreas) > 0 {
		score += (float64(focusMatches) / float64(len(p.config.Priorities.FocusAreas))) * 1.5 // Max 1.5
	}

	// Check against key projects
	projectMatches := 0
	for _, project := range p.config.Priorities.KeyProjects {
		keywords := extractKeywords(project)
		for _, keyword := range keywords {
			if strings.Contains(content, strings.ToLower(keyword)) || strings.ToLower(task.Project) == strings.ToLower(project) {
				projectMatches++
				break
			}
		}
	}
	if len(p.config.Priorities.KeyProjects) > 0 {
		score += (float64(projectMatches) / float64(len(p.config.Priorities.KeyProjects))) * 1.0 // Max 1.0
	}

	// Check if task source is from key stakeholders
	for _, stakeholder := range p.config.Priorities.KeyStakeholders {
		// This will be matched against email sources later
		if task.SourceID != "" && strings.Contains(strings.ToLower(task.SourceID), strings.ToLower(stakeholder)) {
			score += 0.5 // Bonus for key stakeholder
			break
		}
	}

	// Normalize to 0-5 scale
	if score > maxScore {
		score = maxScore
	}

	return score
}

// extractKeywords extracts meaningful keywords from a phrase
func extractKeywords(phrase string) []string {
	// Remove common stop words and split
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "by": true, "with": true, "is": true,
	}

	words := strings.Fields(strings.ToLower(phrase))
	keywords := []string{}

	for _, word := range words {
		// Remove punctuation
		word = strings.Trim(word, ".,!?;:")
		if len(word) > 2 && !stopWords[word] {
			keywords = append(keywords, word)
		}
	}

	return keywords
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
	now := time.Now()
	query := `UPDATE tasks SET status = 'completed', completed_at = ? WHERE id = ?`

	if _, err := p.db.Exec(query, now.Unix(), taskID); err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}

	// Trigger re-prioritization
	return p.PrioritizeTasks(ctx)
}

// UncompleteTask marks a task as pending (undo completion)
func (p *Planner) UncompleteTask(ctx context.Context, taskID string) error {
	query := `UPDATE tasks SET status = 'pending', completed_at = NULL WHERE id = ?`

	if _, err := p.db.Exec(query, taskID); err != nil {
		return fmt.Errorf("failed to uncomplete task: %w", err)
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