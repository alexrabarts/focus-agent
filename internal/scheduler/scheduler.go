package scheduler

import (
	"context"
	"fmt"
	"log"
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
	llm      *llm.GeminiClient
	planner  *planner.Planner
	config   *config.Config
	jobs     map[string]cron.EntryID
	ctx      context.Context
	cancel   context.CancelFunc
}

// New creates a new scheduler
func New(database *db.DB, googleClients *google.Clients, llmClient *llm.GeminiClient, plannerService *planner.Planner, cfg *config.Config) *Scheduler {
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

		// Generate summary
		summary, err := s.llm.SummarizeThread(s.ctx, messages)
		if err != nil {
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

		// Save extracted tasks
		for _, task := range tasks {
			if err := s.db.SaveTask(task); err != nil {
				log.Printf("Failed to save extracted task: %v", err)
			}
		}

		log.Printf("Processed thread %s: summary generated, %d tasks extracted", threadID, len(tasks))
		successCount++

		// Show progress every 50 threads
		if (i+1)%50 == 0 {
			elapsed := time.Since(startTime)
			remaining := len(threadIDs) - (i + 1)
			estimatedTimeLeft := time.Duration(float64(elapsed) / float64(i+1) * float64(remaining))
			log.Printf("Progress: %d/%d threads | Elapsed: %v | Est. remaining: %v",
				i+1, len(threadIDs), elapsed.Round(time.Second), estimatedTimeLeft.Round(time.Second))
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