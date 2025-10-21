package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

// Scheduler manages all scheduled jobs
type Scheduler struct {
	cron     *cron.Cron
	db       *db.DB
	google   *google.Clients
	llm      llm.Client
	planner  *planner.Planner
	config   *config.Config
	jobs     map[string]cron.EntryID
	ctx      context.Context
	cancel   context.CancelFunc
}

// New creates a new scheduler
func New(database *db.DB, googleClients *google.Clients, llmClient llm.Client, plannerService *planner.Planner, cfg *config.Config) *Scheduler {
	// Create cron with timezone
	location, err := time.LoadLocation(cfg.Schedule.Timezone)
	if err != nil {
		log.Printf("Invalid timezone %s, using local: %v", cfg.Schedule.Timezone, err)
		location = time.Local
	}

	c := cron.New(
		cron.WithLocation(location),
		cron.WithSeconds(), // Allow second-level precision
		cron.WithChain(
			cron.SkipIfStillRunning(cron.DefaultLogger),
			cron.Recover(cron.DefaultLogger),
		),
	)

	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		cron:    c,
		db:      database,
		google:  googleClients,
		llm:     llmClient,
		planner: plannerService,
		config:  cfg,
		jobs:    make(map[string]cron.EntryID),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start begins the scheduler
func (s *Scheduler) Start() error {
	log.Println("Starting scheduler...")

	// Schedule Gmail sync
	gmailSpec := fmt.Sprintf("@every %dm", s.config.Google.PollingMinutes.Gmail)
	gmailID, err := s.cron.AddFunc(gmailSpec, s.syncGmail)
	if err != nil {
		return fmt.Errorf("failed to schedule Gmail sync: %w", err)
	}
	s.jobs["gmail"] = gmailID
	log.Printf("Scheduled Gmail sync every %d minutes", s.config.Google.PollingMinutes.Gmail)

	// Schedule Drive sync
	driveSpec := fmt.Sprintf("@every %dm", s.config.Google.PollingMinutes.Drive)
	driveID, err := s.cron.AddFunc(driveSpec, s.syncDrive)
	if err != nil {
		return fmt.Errorf("failed to schedule Drive sync: %w", err)
	}
	s.jobs["drive"] = driveID
	log.Printf("Scheduled Drive sync every %d minutes", s.config.Google.PollingMinutes.Drive)

	// Schedule Calendar sync
	calendarSpec := fmt.Sprintf("@every %dm", s.config.Google.PollingMinutes.Calendar)
	calendarID, err := s.cron.AddFunc(calendarSpec, s.syncCalendar)
	if err != nil {
		return fmt.Errorf("failed to schedule Calendar sync: %w", err)
	}
	s.jobs["calendar"] = calendarID
	log.Printf("Scheduled Calendar sync every %d minutes", s.config.Google.PollingMinutes.Calendar)

	// Schedule Tasks sync
	tasksSpec := fmt.Sprintf("@every %dm", s.config.Google.PollingMinutes.Tasks)
	tasksID, err := s.cron.AddFunc(tasksSpec, s.syncTasks)
	if err != nil {
		return fmt.Errorf("failed to schedule Tasks sync: %w", err)
	}
	s.jobs["tasks"] = tasksID
	log.Printf("Scheduled Tasks sync every %d minutes", s.config.Google.PollingMinutes.Tasks)

	// Schedule daily brief
	dailyTime := s.config.Schedule.DailyBriefTime
	dailySpec := fmt.Sprintf("0 %s %s * * *",
		dailyTime[3:], // minutes
		dailyTime[:2], // hours
	)
	dailyID, err := s.cron.AddFunc(dailySpec, s.sendDailyBrief)
	if err != nil {
		return fmt.Errorf("failed to schedule daily brief: %w", err)
	}
	s.jobs["daily_brief"] = dailyID
	log.Printf("Scheduled daily brief at %s", dailyTime)

	// Schedule midday replan
	replanTime := s.config.Schedule.ReplanTime
	replanSpec := fmt.Sprintf("0 %s %s * * *",
		replanTime[3:], // minutes
		replanTime[:2], // hours
	)
	replanID, err := s.cron.AddFunc(replanSpec, s.sendReplanBrief)
	if err != nil {
		return fmt.Errorf("failed to schedule replan brief: %w", err)
	}
	s.jobs["replan_brief"] = replanID
	log.Printf("Scheduled replan brief at %s", replanTime)

	// Schedule follow-up checker
	followupSpec := fmt.Sprintf("@every %dm", s.config.Schedule.FollowUpMinutes)
	followupID, err := s.cron.AddFunc(followupSpec, s.checkFollowUps)
	if err != nil {
		return fmt.Errorf("failed to schedule follow-up checker: %w", err)
	}
	s.jobs["followup"] = followupID
	log.Printf("Scheduled follow-up checker every %d minutes", s.config.Schedule.FollowUpMinutes)

	// Schedule task prioritization every 10 minutes
	prioritizeSpec := "@every 10m"
	prioritizeID, err := s.cron.AddFunc(prioritizeSpec, s.prioritizeTasks)
	if err != nil {
		return fmt.Errorf("failed to schedule task prioritization: %w", err)
	}
	s.jobs["prioritize"] = prioritizeID
	log.Printf("Scheduled task prioritization every 10 minutes")

	// Schedule cache cleanup daily at 3 AM
	cleanupSpec := "0 0 3 * * *"
	cleanupID, err := s.cron.AddFunc(cleanupSpec, s.cleanupCache)
	if err != nil {
		return fmt.Errorf("failed to schedule cache cleanup: %w", err)
	}
	s.jobs["cleanup"] = cleanupID
	log.Printf("Scheduled cache cleanup at 3:00 AM daily")

	// Run initial sync after a short delay
	go func() {
		time.Sleep(5 * time.Second)
		log.Println("Running initial sync...")
		s.syncAll()
	}()

	// Start the cron scheduler
	s.cron.Start()
	log.Println("Scheduler started successfully")

	return nil
}

// Stop gracefully stops the scheduler
func (s *Scheduler) Stop() {
	log.Println("Stopping scheduler...")

	// Stop accepting new jobs
	ctx := s.cron.Stop()

	// Cancel context
	s.cancel()

	// Wait for running jobs to complete
	<-ctx.Done()

	log.Println("Scheduler stopped")
}

// syncGmail syncs Gmail messages
func (s *Scheduler) syncGmail() {
	log.Println("Starting Gmail sync...")

	if err := s.google.Gmail.SyncThreads(s.ctx, s.db); err != nil {
		log.Printf("Gmail sync failed: %v", err)
		s.db.LogUsage("gmail", "sync", 0, 0, 0, err)
	} else {
		log.Println("Gmail sync completed")

		// After sync, process new messages for task extraction
		go s.ProcessNewMessages()
	}
}

// syncDrive syncs Drive documents
func (s *Scheduler) syncDrive() {
	log.Println("Starting Drive sync...")

	if err := s.google.Drive.SyncDocuments(s.ctx, s.db); err != nil {
		log.Printf("Drive sync failed: %v", err)
		s.db.LogUsage("drive", "sync", 0, 0, 0, err)
	} else {
		log.Println("Drive sync completed")
	}
}

// syncCalendar syncs Calendar events
func (s *Scheduler) syncCalendar() {
	log.Println("Starting Calendar sync...")

	if err := s.google.Calendar.SyncEvents(s.ctx, s.db); err != nil {
		log.Printf("Calendar sync failed: %v", err)
		s.db.LogUsage("calendar", "sync", 0, 0, 0, err)
	} else {
		log.Println("Calendar sync completed")
	}
}

// syncTasks syncs Google Tasks
func (s *Scheduler) syncTasks() {
	log.Println("Starting Tasks sync...")

	if err := s.google.Tasks.SyncTasks(s.ctx, s.db); err != nil {
		log.Printf("Tasks sync failed: %v", err)
		s.db.LogUsage("tasks", "sync", 0, 0, 0, err)
	} else {
		log.Println("Tasks sync completed")
	}
}

// syncAll runs all sync operations
func (s *Scheduler) syncAll() {
	s.syncGmail()
	s.syncDrive()
	s.syncCalendar()
	s.syncTasks()
}

// sendDailyBrief sends the morning daily brief
func (s *Scheduler) sendDailyBrief() {
	log.Println("Generating daily brief...")

	if err := s.planner.GenerateDailyBrief(s.ctx); err != nil {
		log.Printf("Failed to generate daily brief: %v", err)
		s.db.LogUsage("planner", "daily_brief", 0, 0, 0, err)
	} else {
		log.Println("Daily brief sent successfully")
	}
}

// sendReplanBrief sends the midday replan brief
func (s *Scheduler) sendReplanBrief() {
	log.Println("Generating replan brief...")

	if err := s.planner.GenerateReplanBrief(s.ctx); err != nil {
		log.Printf("Failed to generate replan brief: %v", err)
		s.db.LogUsage("planner", "replan_brief", 0, 0, 0, err)
	} else {
		log.Println("Replan brief sent successfully")
	}
}

// checkFollowUps checks for threads needing follow-up
func (s *Scheduler) checkFollowUps() {
	log.Println("Checking for follow-ups...")

	if err := s.planner.CheckFollowUps(s.ctx); err != nil {
		log.Printf("Failed to check follow-ups: %v", err)
		s.db.LogUsage("planner", "followup_check", 0, 0, 0, err)
	}
}

// prioritizeTasks recalculates task priorities
func (s *Scheduler) prioritizeTasks() {
	log.Println("Prioritizing tasks...")

	if err := s.planner.PrioritizeTasks(s.ctx); err != nil {
		log.Printf("Failed to prioritize tasks: %v", err)
		s.db.LogUsage("planner", "prioritize", 0, 0, 0, err)
	} else {
		log.Println("Task prioritization completed")
	}
}

// ProcessSingleThread processes a single thread with AI
func (s *Scheduler) ProcessSingleThread(threadID string) error {
	log.Printf("Processing thread %s with AI...", threadID)

	// Get messages for thread (including labels to determine if in INBOX)
	messagesQuery := `
		SELECT id, thread_id, from_addr, to_addr, subject, snippet, body, labels, ts
		FROM messages
		WHERE thread_id = ?
		ORDER BY ts DESC
		LIMIT 20
	`

	msgRows, err := s.db.Query(messagesQuery, threadID)
	if err != nil {
		return fmt.Errorf("failed to get messages for thread %s: %w", threadID, err)
	}
	defer msgRows.Close()

	var messages []*db.Message
	hasInboxLabel := false
	for msgRows.Next() {
		msg := &db.Message{}
		var ts int64
		var labels string
		err := msgRows.Scan(&msg.ID, &msg.ThreadID, &msg.From, &msg.To,
			&msg.Subject, &msg.Snippet, &msg.Body, &labels, &ts)
		if err != nil {
			continue
		}
		msg.Timestamp = time.Unix(ts, 0)
		messages = append(messages, msg)

		// Check if any message in thread has INBOX label
		if strings.Contains(strings.ToLower(labels), "inbox") {
			hasInboxLabel = true
		}
	}

	if len(messages) == 0 {
		return fmt.Errorf("no messages found for thread %s", threadID)
	}

	// Prepare metadata for smart model selection
	metadata := llm.ThreadMetadata{
		QueueSize:    1, // Processing single thread
		SenderEmail:  messages[0].From,
		Timestamp:    messages[0].Timestamp,
		MessageCount: len(messages),
	}

	// Generate summary with smart model selection
	summary, err := s.llm.SummarizeThreadWithModelSelection(s.ctx, messages, metadata)
	if err != nil {
		return fmt.Errorf("failed to summarize thread %s: %w", threadID, err)
	}

	// Extract tasks
	tasks, err := s.llm.ExtractTasks(s.ctx, summary)
	if err != nil {
		log.Printf("Failed to extract tasks from thread %s: %v", threadID, err)
	}

	// Save summary and tasks
	thread := &db.Thread{
		ID:        threadID,
		Summary:   summary,
		TaskCount: len(tasks),
	}

	if err := s.db.SaveThread(thread); err != nil {
		return fmt.Errorf("failed to save thread summary: %w", err)
	}

	// Enrich and save extracted tasks
	for _, task := range tasks {
		// Set source to gmail for email-extracted tasks
		task.Source = "gmail"
		// Set source_id to thread ID so we can link tasks to threads
		task.SourceID = threadID

		// Enrich task description with full context from email thread
		if enrichedDesc, err := s.llm.EnrichTaskDescription(s.ctx, task, messages); err == nil {
			task.Description = enrichedDesc
			log.Printf("Enriched task description: %s -> %s", task.Title, enrichedDesc[:min(100, len(enrichedDesc))])
		} else {
			log.Printf("Failed to enrich task description: %v", err)
			// Continue with original task if enrichment fails
		}

		if err := s.db.SaveTask(task); err != nil {
			log.Printf("Failed to save extracted task: %v", err)
		}
	}

	// Prioritize tasks (instant, no tokens - pure algorithm)
	if err := s.planner.PrioritizeTasks(s.ctx); err != nil {
		log.Printf("Failed to prioritize tasks: %v", err)
	}

	// Calculate thread priority score from task scores
	var priorityScore float64
	relevantToUser := false

	if len(tasks) > 0 {
		// Query saved tasks to get their scores
		taskQuery := `SELECT score FROM tasks WHERE source = 'gmail' AND source_id = ? ORDER BY score DESC LIMIT 1`
		var maxScore sql.NullFloat64
		if err := s.db.QueryRow(taskQuery, threadID).Scan(&maxScore); err == nil && maxScore.Valid {
			priorityScore = maxScore.Float64
		}

		// Determine if relevant to user:
		// 1. If in INBOX (primary heuristic)
		// 2. AND any task owner is unspecified, "me", "you", or matches user email
		if hasInboxLabel {
			relevantToUser = true // In inbox means it's for the user

			// Could add more specific owner checking here if needed
			userEmail := strings.ToLower(s.config.Google.UserEmail)
			for _, task := range tasks {
				owner := strings.ToLower(task.Stakeholder)
				if owner == "" || owner == "me" || owner == "you" || owner == userEmail || strings.Contains(owner, userEmail) {
					relevantToUser = true
					break
				}
			}
		}
	}

	// Update thread with priority information
	thread.PriorityScore = priorityScore
	thread.RelevantToUser = relevantToUser
	if err := s.db.SaveThread(thread); err != nil {
		return fmt.Errorf("failed to update thread priority: %w", err)
	}

	log.Printf("Processed thread %s: summary generated, %d tasks extracted, priority=%.2f, relevant=%v",
		threadID, len(tasks), priorityScore, relevantToUser)
	return nil
}

// ProcessNewMessages processes new messages for summaries and task extraction
func (s *Scheduler) ProcessNewMessages() {
	log.Println("Processing new messages with AI...")

	// Respect AI processing limits
	maxProcessing := s.config.Limits.MaxAIProcessingPerRun
	log.Printf("Will process up to %d threads with AI (to limit token usage)", maxProcessing)

	// Get threads that need summarization
	query := `
		SELECT DISTINCT t.id
		FROM threads t
		WHERE t.summary IS NULL OR t.summary = ''
		LIMIT ?
	`

	rows, err := s.db.Query(query, maxProcessing)
	if err != nil {
		log.Printf("Failed to query threads: %v", err)
		return
	}
	defer rows.Close()

	var threadIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		threadIDs = append(threadIDs, id)
	}

	if len(threadIDs) == 0 {
		log.Println("No new threads to process")
		return
	}

	log.Printf("Found %d threads needing AI processing", len(threadIDs))

	// Estimate token usage and cost
	estimatedTokensPerThread := 500 // Conservative estimate
	totalEstimatedTokens := len(threadIDs) * estimatedTokensPerThread
	estimatedCost := float64(totalEstimatedTokens) * 0.0000002 // $0.20 per 1M tokens

	log.Println("═══════════════════════════════════════════════════════")
	log.Printf("🤖 AI PROCESSING ESTIMATE:")
	log.Printf("   Threads to process: %d", len(threadIDs))
	log.Printf("   Estimated tokens: ~%d tokens", totalEstimatedTokens)
	log.Printf("   Estimated cost: ~$%.4f", estimatedCost)
	log.Println("═══════════════════════════════════════════════════════")

	// Track actual usage
	successCount := 0
	startTime := time.Now()

	// Process each thread
	for i, threadID := range threadIDs {
		log.Printf("Processing thread %d/%d with AI...", i+1, len(threadIDs))
		// Get messages for thread
		messagesQuery := `
			SELECT id, thread_id, from_addr, to_addr, subject, snippet, body, ts
			FROM messages
			WHERE thread_id = ?
			ORDER BY ts DESC
			LIMIT 20
		`

		msgRows, err := s.db.Query(messagesQuery, threadID)
		if err != nil {
			log.Printf("Failed to get messages for thread %s: %v", threadID, err)
			continue
		}

		var messages []*db.Message
		for msgRows.Next() {
			msg := &db.Message{}
			var ts int64
			err := msgRows.Scan(&msg.ID, &msg.ThreadID, &msg.From, &msg.To,
				&msg.Subject, &msg.Snippet, &msg.Body, &ts)
			if err != nil {
				continue
			}
			msg.Timestamp = time.Unix(ts, 0)
			messages = append(messages, msg)
		}
		msgRows.Close()

		if len(messages) == 0 {
			continue
		}

		// Prepare metadata for smart model selection
		metadata := llm.ThreadMetadata{
			QueueSize:    len(threadIDs),
			SenderEmail:  messages[0].From, // Most recent message's sender
			Timestamp:    messages[0].Timestamp,
			MessageCount: len(messages),
		}

		// Generate summary with smart model selection
		summary, err := s.llm.SummarizeThreadWithModelSelection(s.ctx, messages, metadata)
		if err != nil {
			// Check if daily quota is exhausted
			var quotaErr *llm.DailyQuotaExceededError
			if errors.As(err, &quotaErr) {
				log.Printf("Daily quota exhausted. Stopping AI processing. %d/%d threads processed.", successCount, len(threadIDs))
				break // Stop processing immediately
			}
			log.Printf("Failed to summarize thread %s: %v", threadID, err)
			continue
		}

		// Extract tasks
		tasks, err := s.llm.ExtractTasks(s.ctx, summary)
		if err != nil {
			log.Printf("Failed to extract tasks from thread %s: %v", threadID, err)
		}

		// Save summary and tasks
		thread := &db.Thread{
			ID:        threadID,
			Summary:   summary,
			TaskCount: len(tasks),
		}

		if err := s.db.SaveThread(thread); err != nil {
			log.Printf("Failed to save thread summary: %v", err)
		}

		// Enrich and save extracted tasks
		for _, task := range tasks {
			// Set source to gmail for email-extracted tasks
			task.Source = "gmail"
			// Set source_id to thread ID so we can link tasks to threads
			task.SourceID = threadID

			// Enrich task description with full context from email thread
			if enrichedDesc, err := s.llm.EnrichTaskDescription(s.ctx, task, messages); err == nil {
				task.Description = enrichedDesc
			} else {
				log.Printf("Failed to enrich task description: %v", err)
				// Continue with original task if enrichment fails
			}

			if err := s.db.SaveTask(task); err != nil {
				log.Printf("Failed to save extracted task: %v", err)
			}
		}

		log.Printf("Processed thread %s: summary generated, %d tasks extracted and enriched", threadID, len(tasks))
		successCount++

		// Show progress every 10 threads
		if (i+1)%10 == 0 {
			elapsed := time.Since(startTime)
			remaining := len(threadIDs) - (i + 1)
			estimatedTimeLeft := time.Duration(float64(elapsed) / float64(i+1) * float64(remaining))
			avgTimePerThread := elapsed / time.Duration(i+1)
			log.Printf("Progress: %d/%d threads | Elapsed: %v | Avg: %v/thread | Est. remaining: %v",
				i+1, len(threadIDs), elapsed.Round(time.Second), avgTimePerThread.Round(time.Second), estimatedTimeLeft.Round(time.Second))
		}
	}

	// Final summary
	elapsed := time.Since(startTime)

	// Get actual token usage from database
	var totalTokens int
	var totalCost float64
	usageQuery := `SELECT SUM(tokens), SUM(cost) FROM usage WHERE ts >= ?`
	s.db.QueryRow(usageQuery, startTime.Unix()).Scan(&totalTokens, &totalCost)

	log.Println("═══════════════════════════════════════════════════════")
	log.Printf("✅ AI PROCESSING COMPLETE:")
	log.Printf("   Successfully processed: %d/%d threads", successCount, len(threadIDs))
	log.Printf("   Total time: %v", elapsed.Round(time.Second))
	log.Printf("   Actual tokens used: %d", totalTokens)
	log.Printf("   Actual cost: $%.4f", totalCost)
	log.Println("═══════════════════════════════════════════════════════")
}

// cleanupCache cleans expired cache entries
func (s *Scheduler) cleanupCache() {
	log.Println("Cleaning up expired cache...")

	if err := s.db.CleanExpiredCache(); err != nil {
		log.Printf("Failed to clean cache: %v", err)
	} else {
		log.Println("Cache cleanup completed")
	}
}

// GetNextRuns returns the next scheduled run times for all jobs
func (s *Scheduler) GetNextRuns() map[string]time.Time {
	nextRuns := make(map[string]time.Time)

	entries := s.cron.Entries()
	for name, id := range s.jobs {
		for _, entry := range entries {
			if entry.ID == id {
				nextRuns[name] = entry.Next
				break
			}
		}
	}

	return nextRuns
}

// ReprocessAITasks re-extracts tasks from existing thread summaries with the updated parser
func (s *Scheduler) ReprocessAITasks() error {
	log.Println("═══════════════════════════════════════════════════════")
	log.Println("🔄 REPROCESSING AI TASKS")
	log.Println("═══════════════════════════════════════════════════════")

	// Step 1: Get all threads with summaries
	threadsQuery := `SELECT id, summary FROM threads WHERE summary IS NOT NULL AND summary <> '' ORDER BY id`
	rows, err := s.db.Query(threadsQuery)
	if err != nil {
		return fmt.Errorf("failed to query threads: %w", err)
	}
	defer rows.Close()

	type threadSummary struct {
		ID      string
		Summary string
	}

	var threads []threadSummary
	for rows.Next() {
		var t threadSummary
		if err := rows.Scan(&t.ID, &t.Summary); err != nil {
			log.Printf("Failed to scan thread: %v", err)
			continue
		}
		threads = append(threads, t)
	}

	log.Printf("Found %d threads with summaries", len(threads))

	// Step 2: Delete all existing AI tasks
	deleteQuery := `DELETE FROM tasks WHERE source = 'ai'`
	result, err := s.db.Exec(deleteQuery)
	if err != nil {
		return fmt.Errorf("failed to delete AI tasks: %w", err)
	}

	rowsDeleted, _ := result.RowsAffected()
	log.Printf("Deleted %d old AI tasks", rowsDeleted)

	// Step 3: Re-extract tasks from summaries using new parser
	totalTasks := 0
	for i, thread := range threads {
		log.Printf("Processing thread %d/%d: %s", i+1, len(threads), thread.ID)

		// Extract tasks from summary
		tasks, err := s.llm.ExtractTasks(s.ctx, thread.Summary)
		if err != nil {
			log.Printf("Failed to extract tasks from thread %s: %v", thread.ID, err)
			continue
		}

		// Save extracted tasks
		for _, task := range tasks {
			// Set source to gmail for email-extracted tasks
			task.Source = "gmail"
			task.SourceID = thread.ID
			if err := s.db.SaveTask(task); err != nil {
				log.Printf("Failed to save task: %v", err)
				continue
			}
			totalTasks++
		}

		log.Printf("  Extracted %d tasks from thread %s", len(tasks), thread.ID)
	}

	// Step 4: Recalculate priorities
	log.Println("Recalculating task priorities...")
	if err := s.planner.PrioritizeTasks(s.ctx); err != nil {
		log.Printf("Warning: Failed to prioritize tasks: %v", err)
	}

	// Step 5: Recalculate thread priorities
	log.Println("Recalculating thread priorities...")
	if err := s.planner.RecalculateThreadPriorities(s.ctx); err != nil {
		log.Printf("Warning: Failed to recalculate thread priorities: %v", err)
	}

	log.Println("═══════════════════════════════════════════════════════")
	log.Printf("✅ REPROCESSING COMPLETE:")
	log.Printf("   Processed threads: %d", len(threads))
	log.Printf("   Old tasks deleted: %d", rowsDeleted)
	log.Printf("   New tasks extracted: %d", totalTasks)
	log.Println("═══════════════════════════════════════════════════════")

	return nil
}

// CleanupOtherPeoplesTasks deletes tasks assigned to other specific people
func (s *Scheduler) CleanupOtherPeoplesTasks() error {
	log.Println("═══════════════════════════════════════════════════════")
	log.Println("🧹 CLEANING UP TASKS ASSIGNED TO OTHERS")
	log.Println("═══════════════════════════════════════════════════════")

	userEmail := strings.ToLower(s.config.Google.UserEmail)
	if userEmail == "" {
		return fmt.Errorf("user email not set in config")
	}

	log.Printf("User email: %s", userEmail)

	// Query tasks that are NOT for the user
	// Keep tasks where stakeholder is empty, "me", "you", or contains user email
	// Delete everything else
	query := `
		SELECT id, title, stakeholder
		FROM tasks
		WHERE stakeholder IS NOT NULL
		  AND stakeholder != ''
		  AND LOWER(stakeholder) != 'me'
		  AND LOWER(stakeholder) != 'you'
		  AND LOWER(stakeholder) NOT LIKE ?
	`

	rows, err := s.db.Query(query, "%"+userEmail+"%")
	if err != nil {
		return fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasksToDelete []struct {
		ID          string
		Title       string
		Stakeholder string
	}

	for rows.Next() {
		var t struct {
			ID          string
			Title       string
			Stakeholder string
		}
		if err := rows.Scan(&t.ID, &t.Title, &t.Stakeholder); err != nil {
			log.Printf("Failed to scan task: %v", err)
			continue
		}
		tasksToDelete = append(tasksToDelete, t)
	}

	log.Printf("Found %d tasks assigned to other people:", len(tasksToDelete))
	for _, t := range tasksToDelete {
		log.Printf("  - [%s] %s → %s", t.ID[:8], t.Title, t.Stakeholder)
	}

	if len(tasksToDelete) == 0 {
		log.Println("No tasks to delete.")
		log.Println("═══════════════════════════════════════════════════════")
		return nil
	}

	// Delete the tasks
	deleteQuery := `
		DELETE FROM tasks
		WHERE stakeholder IS NOT NULL
		  AND stakeholder != ''
		  AND LOWER(stakeholder) != 'me'
		  AND LOWER(stakeholder) != 'you'
		  AND LOWER(stakeholder) NOT LIKE ?
	`

	result, err := s.db.Exec(deleteQuery, "%"+userEmail+"%")
	if err != nil {
		return fmt.Errorf("failed to delete tasks: %w", err)
	}

	rowsDeleted, _ := result.RowsAffected()

	log.Println("═══════════════════════════════════════════════════════")
	log.Printf("✅ CLEANUP COMPLETE:")
	log.Printf("   Tasks deleted: %d", rowsDeleted)
	log.Println("═══════════════════════════════════════════════════════")

	return nil
}