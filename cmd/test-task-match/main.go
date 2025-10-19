package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: test-task-match <task_id>")
	}
	taskID := os.Args[1]

	// Load config
	cfg, err := config.Load(os.ExpandEnv("$HOME/.focus-agent/config.yaml"))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize database
	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Initialize LLM client
	ctx := context.Background()
	googleClients, err := google.NewClients(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize Google clients: %v", err)
	}

	llmClient, err := llm.NewHybridClient(cfg.Gemini.APIKey, database, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize LLM client: %v", err)
	}
	defer llmClient.Close()

	// Initialize planner
	plannerService := planner.New(database, googleClients, llmClient, cfg)

	// Get the specific task
	var task *db.Task
	query := `SELECT id, source, source_id, title, description, due_ts, project,
	              impact, urgency, effort, stakeholder, status
	         FROM tasks WHERE id = ?`

	row := database.QueryRow(query, taskID)
	task = &db.Task{}
	var dueTS *int64

	err = row.Scan(
		&task.ID, &task.Source, &task.SourceID, &task.Title,
		&task.Description, &dueTS, &task.Project,
		&task.Impact, &task.Urgency, &task.Effort, &task.Stakeholder,
		&task.Status,
	)
	if err != nil {
		log.Fatalf("Failed to get task: %v", err)
	}

	fmt.Printf("Testing task:\n")
	fmt.Printf("  ID: %s\n", task.ID)
	fmt.Printf("  Title: %s\n", task.Title)
	fmt.Printf("  Description: %s\n\n", task.Description)

	// Test strategic alignment calculation
	score, matches := plannerService.CalculateStrategicAlignmentWithMatches(task)

	fmt.Printf("RESULTS:\n")
	fmt.Printf("  Score: %.2f\n\n", score)

	fmt.Printf("  Matched OKRs:\n")
	if len(matches.OKRs) == 0 {
		fmt.Printf("    (none)\n")
	}
	for _, okr := range matches.OKRs {
		fmt.Printf("    - %s\n", okr)
	}

	fmt.Printf("\n  Matched Focus Areas:\n")
	if len(matches.FocusAreas) == 0 {
		fmt.Printf("    (none)\n")
	}
	for _, area := range matches.FocusAreas {
		fmt.Printf("    - %s\n", area)
	}

	fmt.Printf("\n  Matched Projects:\n")
	if len(matches.Projects) == 0 {
		fmt.Printf("    (none)\n")
	}
	for _, project := range matches.Projects {
		fmt.Printf("    - %s\n", project)
	}

	fmt.Printf("\n  Key Stakeholder: %v\n", matches.KeyStakeholder)

	// Marshal to JSON
	matchesJSON, err := json.MarshalIndent(matches, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal: %v", err)
	}

	fmt.Printf("\nJSON:\n%s\n", string(matchesJSON))
}
