package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexrabarts/focus-agent/internal/api"
	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/front"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
	"github.com/alexrabarts/focus-agent/internal/scheduler"
	"github.com/alexrabarts/focus-agent/internal/tui"
)

var (
	configFile      = flag.String("config", os.ExpandEnv("$HOME/.focus-agent/config.yaml"), "Path to configuration file")
	runOnce         = flag.Bool("once", false, "Run once and exit (for testing)")
	processOnly     = flag.Bool("process", false, "Process threads with AI and exit")
	authOnly        = flag.Bool("auth", false, "Run OAuth flow only")
	briefOnly       = flag.Bool("brief", false, "Generate and send brief immediately")
	apiMode         = flag.Bool("api", false, "Run API server with scheduler (for remote TUI access)")
	tuiMode         = flag.Bool("tui", false, "Run interactive TUI (Terminal User Interface)")
	reprocessTasks      = flag.Bool("reprocess-tasks", false, "Re-extract tasks from existing thread summaries with updated parser")
	enrichTasks         = flag.Bool("enrich-tasks", false, "Enrich descriptions for existing email-extracted tasks with AI context")
	cleanupOthers       = flag.Bool("cleanup-other-tasks", false, "Delete tasks assigned to other people (one-time cleanup)")
	recalculatePriorities = flag.Bool("recalculate-priorities", false, "Recalculate priority scores and populate matched priorities for all pending tasks")
	migratePriorities   = flag.Bool("migrate-priorities", false, "Migrate strategic priorities from config.yaml to database (one-time migration)")
	migrateToDuckDB     = flag.String("migrate-to-duckdb", "", "Migrate SQLite database to DuckDB (provide new DuckDB path)")
	version             = flag.Bool("version", false, "Show version")
)

const VERSION = "0.1.0"

func main() {
	flag.Parse()

	if *version {
		fmt.Printf("focus-agent v%s\n", VERSION)
		os.Exit(0)
	}

	// Setup logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Load configuration
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Handle migrate-to-duckdb mode
	if *migrateToDuckDB != "" {
		log.Println("Starting SQLite to DuckDB migration...")
		if err := db.MigrateSQLiteToDuckDB(cfg.Database.Path, *migrateToDuckDB); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle TUI remote mode early - doesn't need database or other services
	if *tuiMode && cfg.Remote.URL != "" {
		log.Println("Starting TUI in remote mode...")
		if err := tui.Start(nil, nil, nil, nil, nil, cfg); err != nil {
			log.Fatalf("TUI error: %v", err)
		}
		os.Exit(0)
	}

	// Initialize database
	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := db.RunMigrations(database); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Initialize Google clients
	googleClients, err := google.NewClients(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize Google clients: %v", err)
	}

	// Handle auth-only mode
	if *authOnly {
		log.Println("Authentication successful!")
		os.Exit(0)
	}

	// Initialize Hybrid LLM client (Claude primary, Gemini fallback)
	llmClient, err := llm.NewHybridClient(cfg.Gemini.APIKey, database, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize LLM client: %v", err)
	}
	defer llmClient.Close()

	// Initialize planner
	plannerService := planner.New(database, googleClients, llmClient, cfg)

	// Initialize Front client (conditional)
	var frontClient *front.Client
	log.Printf("Front config: Enabled=%v, APIToken length=%d, InboxID=%s",
		cfg.Front.Enabled, len(cfg.Front.APIToken), cfg.Front.InboxID)
	if cfg.Front.Enabled {
		frontClient = front.NewClient(cfg.Front.APIToken)
		log.Println("Front client initialized")
	} else {
		log.Println("Front integration disabled in config")
	}

	// Handle brief-only mode
	if *briefOnly {
		if err := plannerService.GenerateDailyBrief(ctx); err != nil {
			log.Fatalf("Failed to generate brief: %v", err)
		}
		log.Println("Brief sent successfully!")
		os.Exit(0)
	}

	// Handle process-only mode
	if *processOnly {
		log.Println("Processing threads with AI...")
		sched := scheduler.New(database, googleClients, llmClient, plannerService, frontClient, cfg)
		sched.ProcessNewMessages()
		log.Println("Processing complete!")
		os.Exit(0)
	}

	// Handle reprocess-tasks mode
	if *reprocessTasks {
		log.Println("Re-extracting tasks from existing thread summaries...")
		sched := scheduler.New(database, googleClients, llmClient, plannerService, frontClient, cfg)
		if err := sched.ReprocessAITasks(); err != nil {
			log.Fatalf("Failed to reprocess tasks: %v", err)
		}
		os.Exit(0)
	}

	// Handle enrich-tasks mode
	if *enrichTasks {
		log.Println("Enriching descriptions for existing email-extracted tasks...")
		sched := scheduler.New(database, googleClients, llmClient, plannerService, frontClient, cfg)
		if err := sched.EnrichExistingTasks(); err != nil {
			log.Fatalf("Failed to enrich tasks: %v", err)
		}
		os.Exit(0)
	}

	// Handle cleanup-other-tasks mode
	if *cleanupOthers {
		log.Println("Cleaning up tasks assigned to other people...")
		sched := scheduler.New(database, googleClients, llmClient, plannerService, frontClient, cfg)
		if err := sched.CleanupOtherPeoplesTasks(); err != nil {
			log.Fatalf("Failed to cleanup tasks: %v", err)
		}
		os.Exit(0)
	}

	// Handle recalculate-priorities mode
	if *recalculatePriorities {
		log.Println("Recalculating priority scores and populating matched priorities...")
		if err := plannerService.PrioritizeTasks(ctx); err != nil {
			log.Fatalf("Failed to recalculate priorities: %v", err)
		}
		log.Println("Priority recalculation complete!")
		os.Exit(0)
	}

	// Handle migrate-priorities mode
	if *migratePriorities {
		log.Println("Migrating strategic priorities from config.yaml to database...")

		// Check if priorities already exist in database
		existingPriorities, err := database.GetAllPriorities()
		if err != nil {
			log.Fatalf("Failed to check existing priorities: %v", err)
		}

		if len(existingPriorities) > 0 {
			log.Printf("Database already contains %d priorities. Migration skipped.", len(existingPriorities))
			log.Println("To re-import, manually delete existing priorities from the database first.")
			os.Exit(0)
		}

		// Migrate OKRs
		for _, okr := range cfg.Priorities.OKRs {
			if _, err := database.AddPriority("okr", okr, "Migrated from config.yaml"); err != nil {
				log.Printf("Failed to add OKR '%s': %v", okr, err)
			} else {
				log.Printf("Added OKR: %s", okr)
			}
		}

		// Migrate focus areas
		for _, area := range cfg.Priorities.FocusAreas {
			if _, err := database.AddPriority("focus_area", area, "Migrated from config.yaml"); err != nil {
				log.Printf("Failed to add focus area '%s': %v", area, err)
			} else {
				log.Printf("Added focus area: %s", area)
			}
		}

		// Migrate key stakeholders
		for _, stakeholder := range cfg.Priorities.KeyStakeholders {
			if _, err := database.AddPriority("stakeholder", stakeholder, "Migrated from config.yaml"); err != nil {
				log.Printf("Failed to add stakeholder '%s': %v", stakeholder, err)
			} else {
				log.Printf("Added stakeholder: %s", stakeholder)
			}
		}

		// Migrate key projects
		for _, project := range cfg.Priorities.KeyProjects {
			if _, err := database.AddPriority("project", project, "Migrated from config.yaml"); err != nil {
				log.Printf("Failed to add project '%s': %v", project, err)
			} else {
				log.Printf("Added project: %s", project)
			}
		}

		log.Println("Priority migration complete!")
		log.Println("Priorities are now loaded from database. You can update config.yaml comments to reflect this change.")
		os.Exit(0)
	}

	// Handle TUI mode (local mode only - remote mode handled earlier)
	if *tuiMode {
		log.Println("Starting TUI in local mode...")
		if err := tui.Start(database, googleClients, llmClient, plannerService, frontClient, cfg); err != nil {
			log.Fatalf("TUI error: %v", err)
		}
		os.Exit(0)
	}

	// Initialize scheduler first
	sched := scheduler.New(database, googleClients, llmClient, plannerService, frontClient, cfg)

	// Handle API mode or if API is enabled in config
	if *apiMode || cfg.API.Enabled {
		// Start API server in background
		apiServer := api.NewServer(database, googleClients, llmClient, plannerService, cfg)
		apiServer.SetScheduler(sched)

		go func() {
			log.Printf("API server starting on port %d", cfg.API.Port)
			if err := apiServer.Start(cfg.API.Port); err != nil && err != http.ErrServerClosed {
				log.Fatalf("API server error: %v", err)
			}
		}()

		// If -api flag is set, run scheduler + API server together
		// If just config enabled, continue to scheduler below
	}

	// Handle run-once mode
	if *runOnce {
		log.Println("Running sync once...")
		if err := runSync(ctx, googleClients, database, llmClient); err != nil {
			log.Fatalf("Sync failed: %v", err)
		}
		log.Println("Sync completed successfully!")
		os.Exit(0)
	}

	// Start scheduler
	log.Println("Starting focus-agent...")
	if err := sched.Start(); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	sched.Stop()
	time.Sleep(1 * time.Second)
}

func runSync(ctx context.Context, clients *google.Clients, database *db.DB, llm llm.Client) error {
	log.Println("╔═══════════════════════════════════════════════════════╗")
	log.Println("║           STARTING FULL SYNC                          ║")
	log.Println("╚═══════════════════════════════════════════════════════╝")

	// Gmail sync
	log.Println("\n📧 Syncing Gmail...")
	if err := clients.Gmail.SyncThreads(ctx, database); err != nil {
		return fmt.Errorf("gmail sync failed: %w", err)
	}

	// Drive sync
	log.Println("\n📁 Syncing Drive...")
	if err := clients.Drive.SyncDocuments(ctx, database); err != nil {
		return fmt.Errorf("drive sync failed: %w", err)
	}

	// Calendar sync
	log.Println("\n📅 Syncing Calendar...")
	if err := clients.Calendar.SyncEvents(ctx, database); err != nil {
		return fmt.Errorf("calendar sync failed: %w", err)
	}

	// Tasks sync
	log.Println("\n✅ Syncing Tasks...")
	if err := clients.Tasks.SyncTasks(ctx, database); err != nil {
		return fmt.Errorf("tasks sync failed: %w", err)
	}

	// Show summary
	log.Println("\n╔═══════════════════════════════════════════════════════╗")
	log.Println("║           SYNC SUMMARY                                ║")
	log.Println("╚═══════════════════════════════════════════════════════╝")

	var msgCount, threadCount, docCount, eventCount, taskCount int
	database.QueryRow("SELECT COUNT(*) FROM messages").Scan(&msgCount)
	database.QueryRow("SELECT COUNT(*) FROM threads").Scan(&threadCount)
	database.QueryRow("SELECT COUNT(*) FROM docs").Scan(&docCount)
	database.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventCount)
	database.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&taskCount)

	log.Printf("📧 Email threads: %d", threadCount)
	log.Printf("💬 Messages: %d", msgCount)
	log.Printf("📁 Documents: %d", docCount)
	log.Printf("📅 Events: %d", eventCount)
	log.Printf("✅ Tasks: %d", taskCount)
	log.Println("═══════════════════════════════════════════════════════")

	return nil
}