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

	// Get one pending task
	tasks, err := database.GetPendingTasks(1)
	if err != nil || len(tasks) == 0 {
		log.Fatalf("Failed to get task: %v", err)
	}

	task := tasks[0]
	fmt.Printf("Testing task: %s - %s\n", task.ID, task.Title)

	// Test strategic alignment calculation
	score, matches := plannerService.CalculateStrategicAlignmentWithMatches(task)

	fmt.Printf("\nScore: %.2f\n", score)
	fmt.Printf("Matches OKRs: %v\n", matches.OKRs)
	fmt.Printf("Matches Focus Areas: %v\n", matches.FocusAreas)
	fmt.Printf("Matches Projects: %v\n", matches.Projects)
	fmt.Printf("Matches Key Stakeholder: %v\n", matches.KeyStakeholder)

	// Marshal to JSON like the real code does
	matchesJSON, err := json.Marshal(matches)
	if err != nil {
		log.Fatalf("Failed to marshal: %v", err)
	}

	fmt.Printf("\nMarshaled JSON: %s\n", string(matchesJSON))
}
