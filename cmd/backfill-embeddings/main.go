package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/embeddings"
)

func main() {
	configPath := flag.String("config", "", "Path to config file")
	limit := flag.Int("limit", 0, "Limit number of tasks to process (0 = all)")
	dryRun := flag.Bool("dry-run", false, "Dry run - don't actually generate embeddings")
	flag.Parse()

	log.Println("Starting embeddings backfill...")

	// Load config
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = "~/.focus-agent/config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Open database
	database, err := db.Init(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Run migrations to ensure tables exist
	log.Println("Running migrations...")
	if err := db.RunStructuredMigrations(database); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Create embeddings client - use first Ollama host if available
	ollamaURL := "http://localhost:11434"
	if len(cfg.Ollama.Hosts) > 0 {
		ollamaURL = cfg.Ollama.Hosts[0].URL
	}

	embClient := embeddings.NewClient(ollamaURL, "nomic-embed-text")

	// Get tasks without embeddings
	query := `
		SELECT t.id, t.source, t.source_id, t.title, t.description,
		       t.project, t.status, t.matched_priorities
		FROM tasks t
		LEFT JOIN task_embeddings te ON t.id = te.task_id
		WHERE te.task_id IS NULL
		ORDER BY t.created_at DESC
	`

	if *limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", *limit)
	}

	rows, err := database.Query(query)
	if err != nil {
		log.Fatalf("Failed to query tasks: %v", err)
	}
	defer rows.Close()

	var tasks []*db.Task
	for rows.Next() {
		task := &db.Task{}
		err := rows.Scan(
			&task.ID, &task.Source, &task.SourceID, &task.Title,
			&task.Description, &task.Project, &task.Status, &task.MatchedPriorities,
		)
		if err != nil {
			log.Printf("Failed to scan task: %v", err)
			continue
		}
		tasks = append(tasks, task)
	}

	log.Printf("Found %d tasks without embeddings", len(tasks))

	if *dryRun {
		log.Println("Dry run mode - showing what would be processed:")
		for i, task := range tasks {
			content := embeddings.BuildEmbeddingContent(task)
			log.Printf("[%d/%d] %s: %s (content: %d chars)",
				i+1, len(tasks), task.ID, task.Title, len(content))
		}
		return
	}

	// Generate embeddings
	ctx := context.Background()
	success := 0
	failed := 0

	for i, task := range tasks {
		log.Printf("[%d/%d] Generating embedding for task: %s", i+1, len(tasks), task.Title)

		// Generate embedding
		taskEmb, err := embeddings.GenerateTaskEmbedding(ctx, embClient, task)
		if err != nil {
			log.Printf("  ✗ Failed: %v", err)
			failed++
			continue
		}

		// Save to database
		if err := embeddings.SaveTaskEmbedding(database, taskEmb); err != nil {
			log.Printf("  ✗ Failed to save: %v", err)
			failed++
			continue
		}

		log.Printf("  ✓ Embedded %d dimensions", len(taskEmb.Embedding))
		success++

		// Rate limit to avoid overwhelming Ollama
		if i < len(tasks)-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	log.Printf("\nBackfill complete:")
	log.Printf("  Success: %d", success)
	log.Printf("  Failed: %d", failed)
	log.Printf("  Total: %d", len(tasks))
}
