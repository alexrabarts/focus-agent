package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
	"github.com/alexrabarts/focus-agent/internal/scheduler"
	"github.com/alexrabarts/focus-agent/internal/tui"
)

var (
	configFile  = flag.String("config", os.ExpandEnv("$HOME/.focus-agent/config.yaml"), "Path to configuration file")
	runOnce     = flag.Bool("once", false, "Run once and exit (for testing)")
	processOnly = flag.Bool("process", false, "Process threads with AI and exit")
	authOnly    = flag.Bool("auth", false, "Run OAuth flow only")
	briefOnly   = flag.Bool("brief", false, "Generate and send brief immediately")
	tuiMode     = flag.Bool("tui", false, "Run interactive TUI (Terminal User Interface)")
	version     = flag.Bool("version", false, "Show version")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Initialize LLM client
	llmClient, err := llm.NewGeminiClient(cfg.Gemini.APIKey, database)
	if err != nil {
		log.Fatalf("Failed to initialize LLM client: %v", err)
	}
	defer llmClient.Close()

	// Initialize planner
	plannerService := planner.New(database, googleClients, llmClient, cfg)

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
		sched := scheduler.New(database, googleClients, llmClient, plannerService, cfg)
		sched.ProcessNewMessages()
		log.Println("Processing complete!")
		os.Exit(0)
	}

	// Handle TUI mode
	if *tuiMode {
		log.Println("Starting TUI...")
		if err := tui.Start(database, googleClients, llmClient, plannerService, cfg); err != nil {
			log.Fatalf("TUI error: %v", err)
		}
		os.Exit(0)
	}

	// Initialize scheduler
	sched := scheduler.New(database, googleClients, llmClient, plannerService, cfg)

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

func runSync(ctx context.Context, clients *google.Clients, database *db.DB, llm *llm.GeminiClient) error {
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