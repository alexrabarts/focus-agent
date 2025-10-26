package llm

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// OllamaJob represents a job to be processed by the worker pool
type OllamaJob struct {
	ID      string
	Type    string // "summarize", "extract_tasks", "enrich", "strategic"
	Prompt  string
	Format  string // "json" or ""
	Result  chan OllamaResult
	Context context.Context

	// Type-specific data
	Messages  []*db.Message
	Task      *db.Task
	Priorities *config.Priorities
	UserEmail string
}

// OllamaResult represents the result of processing a job
type OllamaResult struct {
	Text  string
	Tasks []*db.Task
	Strategic *StrategicAlignmentResult
	Error error
	HostName string // Which host processed this
	Duration time.Duration
}

// HostStats tracks statistics for a single Ollama host
type HostStats struct {
	Name          string
	URL           string
	Workers       int
	JobsProcessed atomic.Uint64
	JobsFailed    atomic.Uint64
	TotalDuration atomic.Int64 // Nanoseconds
	LastUsed      atomic.Int64 // Unix timestamp
}

// DistributedOllamaClient manages a pool of workers across multiple Ollama hosts
type DistributedOllamaClient struct {
	hosts      []config.OllamaHost
	model      string
	timeout    time.Duration
	jobs       chan *OllamaJob
	stats      map[string]*HostStats
	statsLock  sync.RWMutex
	workerWg   sync.WaitGroup
	shutdownCh chan struct{}
	isRunning  atomic.Bool
}

// NewDistributedOllamaClient creates a distributed Ollama client with worker pool
func NewDistributedOllamaClient(cfg *config.Config) *DistributedOllamaClient {
	if !cfg.Ollama.Enabled || len(cfg.Ollama.Hosts) == 0 {
		return nil
	}

	timeout := time.Duration(cfg.Ollama.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	// Calculate total workers across all hosts
	totalWorkers := 0
	for _, host := range cfg.Ollama.Hosts {
		totalWorkers += host.Workers
	}

	// Buffer job queue to total workers * 2
	jobQueueSize := totalWorkers * 2

	client := &DistributedOllamaClient{
		hosts:      cfg.Ollama.Hosts,
		model:      cfg.Ollama.Model,
		timeout:    timeout,
		jobs:       make(chan *OllamaJob, jobQueueSize),
		stats:      make(map[string]*HostStats),
		shutdownCh: make(chan struct{}),
	}

	// Initialize stats for each host
	for _, host := range cfg.Ollama.Hosts {
		client.stats[host.Name] = &HostStats{
			Name:    host.Name,
			URL:     host.URL,
			Workers: host.Workers,
		}
	}

	// Start worker pool
	client.startWorkers()

	log.Printf("Distributed Ollama client initialized with %d workers across %d hosts (model: %s)",
		totalWorkers, len(cfg.Ollama.Hosts), cfg.Ollama.Model)

	return client
}

// startWorkers launches worker goroutines for all hosts
func (c *DistributedOllamaClient) startWorkers() {
	c.isRunning.Store(true)

	for _, host := range c.hosts {
		// Create a simple OllamaClient for each host
		hostClient := NewOllamaClient(host.URL, c.model)
		hostClient.httpClient.Timeout = c.timeout

		// Spawn N workers for this host
		for i := 0; i < host.Workers; i++ {
			c.workerWg.Add(1)
			go c.worker(hostClient, host.Name, i)
		}

		log.Printf("Started %d workers for Ollama host: %s (%s)", host.Workers, host.Name, host.URL)
	}
}

// worker processes jobs from the queue
func (c *DistributedOllamaClient) worker(client *OllamaClient, hostName string, workerID int) {
	defer c.workerWg.Done()

	for {
		select {
		case <-c.shutdownCh:
			return
		case job, ok := <-c.jobs:
			if !ok {
				return
			}

			// Process the job
			c.processJob(client, hostName, workerID, job)
		}
	}
}

// processJob handles a single job with retry logic
func (c *DistributedOllamaClient) processJob(client *OllamaClient, hostName string, workerID int, job *OllamaJob) {
	start := time.Now()
	var result OllamaResult
	result.HostName = hostName

	// Get stats for this host
	c.statsLock.RLock()
	stats := c.stats[hostName]
	c.statsLock.RUnlock()

	defer func() {
		result.Duration = time.Since(start)

		// Update stats
		if result.Error != nil {
			stats.JobsFailed.Add(1)
		} else {
			stats.JobsProcessed.Add(1)
			stats.TotalDuration.Add(int64(result.Duration))
			stats.LastUsed.Store(time.Now().Unix())
		}

		// Send result back
		select {
		case job.Result <- result:
		case <-job.Context.Done():
			// Context canceled, discard result
		}
	}()

	// Process based on job type
	switch job.Type {
	case "summarize":
		text, err := client.SummarizeThread(job.Context, job.Messages)
		result.Text = text
		result.Error = err

	case "extract_tasks":
		tasks, err := client.ExtractTasks(job.Context, job.Prompt, job.UserEmail)
		result.Tasks = tasks
		result.Error = err

	case "enrich":
		text, err := client.GenerateWithFormat(job.Context, job.Prompt, job.Format)
		result.Text = text
		result.Error = err

	case "strategic":
		strategic, err := client.EvaluateStrategicAlignment(job.Context, job.Task, job.Priorities)
		result.Strategic = strategic
		result.Error = err

	default:
		result.Error = fmt.Errorf("unknown job type: %s", job.Type)
	}
}

// SummarizeThread submits a summarization job to the worker pool
func (c *DistributedOllamaClient) SummarizeThread(ctx context.Context, messages []*db.Message) (string, error) {
	job := &OllamaJob{
		ID:       fmt.Sprintf("summarize-%d", time.Now().UnixNano()),
		Type:     "summarize",
		Messages: messages,
		Result:   make(chan OllamaResult, 1),
		Context:  ctx,
	}

	// Submit to worker pool
	select {
	case c.jobs <- job:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Wait for result
	select {
	case result := <-job.Result:
		if result.Error != nil {
			return "", result.Error
		}
		log.Printf("Thread summarized by %s in %s", result.HostName, result.Duration)
		return result.Text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ExtractTasks submits a task extraction job to the worker pool
func (c *DistributedOllamaClient) ExtractTasks(ctx context.Context, content, userEmail string) ([]*db.Task, error) {
	job := &OllamaJob{
		ID:        fmt.Sprintf("extract-%d", time.Now().UnixNano()),
		Type:      "extract_tasks",
		Prompt:    content,
		UserEmail: userEmail,
		Result:    make(chan OllamaResult, 1),
		Context:   ctx,
	}

	// Submit to worker pool
	select {
	case c.jobs <- job:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Wait for result
	select {
	case result := <-job.Result:
		if result.Error != nil {
			return nil, result.Error
		}
		log.Printf("Tasks extracted by %s in %s", result.HostName, result.Duration)
		return result.Tasks, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// EnrichTaskDescription submits a task enrichment job to the worker pool
func (c *DistributedOllamaClient) EnrichTaskDescription(ctx context.Context, prompt string) (string, error) {
	job := &OllamaJob{
		ID:      fmt.Sprintf("enrich-%d", time.Now().UnixNano()),
		Type:    "enrich",
		Prompt:  prompt,
		Format:  "json",
		Result:  make(chan OllamaResult, 1),
		Context: ctx,
	}

	// Submit to worker pool
	select {
	case c.jobs <- job:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Wait for result
	select {
	case result := <-job.Result:
		if result.Error != nil {
			return "", result.Error
		}
		log.Printf("Task enriched by %s in %s", result.HostName, result.Duration)
		return result.Text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// EvaluateStrategicAlignment submits a strategic alignment job to the worker pool
func (c *DistributedOllamaClient) EvaluateStrategicAlignment(ctx context.Context, task *db.Task, priorities *config.Priorities) (*StrategicAlignmentResult, error) {
	job := &OllamaJob{
		ID:         fmt.Sprintf("strategic-%d", time.Now().UnixNano()),
		Type:       "strategic",
		Task:       task,
		Priorities: priorities,
		Result:     make(chan OllamaResult, 1),
		Context:    ctx,
	}

	// Submit to worker pool
	select {
	case c.jobs <- job:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Wait for result
	select {
	case result := <-job.Result:
		if result.Error != nil {
			return nil, result.Error
		}
		log.Printf("Strategic alignment evaluated by %s in %s", result.HostName, result.Duration)
		return result.Strategic, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetStats returns statistics for all hosts
func (c *DistributedOllamaClient) GetStats() map[string]*HostStats {
	c.statsLock.RLock()
	defer c.statsLock.RUnlock()

	// Return a copy
	stats := make(map[string]*HostStats, len(c.stats))
	for k, v := range c.stats {
		stats[k] = v
	}
	return stats
}

// PrintStats logs current statistics
func (c *DistributedOllamaClient) PrintStats() {
	c.statsLock.RLock()
	defer c.statsLock.RUnlock()

	log.Println("═══════════════════════════════════════════════════════")
	log.Println("Distributed Ollama Worker Pool Statistics:")

	totalProcessed := uint64(0)
	totalFailed := uint64(0)

	for _, stats := range c.stats {
		processed := stats.JobsProcessed.Load()
		failed := stats.JobsFailed.Load()
		totalProcessed += processed
		totalFailed += failed

		avgDuration := time.Duration(0)
		if processed > 0 {
			avgDuration = time.Duration(stats.TotalDuration.Load() / int64(processed))
		}

		log.Printf("  %s (%d workers): %d processed, %d failed, avg: %s",
			stats.Name, stats.Workers, processed, failed, avgDuration)
	}

	log.Printf("  TOTAL: %d processed, %d failed", totalProcessed, totalFailed)
	log.Printf("  Queue depth: %d jobs waiting", len(c.jobs))
	log.Println("═══════════════════════════════════════════════════════")
}

// Shutdown gracefully stops all workers
func (c *DistributedOllamaClient) Shutdown() {
	if !c.isRunning.Load() {
		return
	}

	log.Println("Shutting down distributed Ollama worker pool...")
	c.isRunning.Store(false)

	close(c.shutdownCh)
	close(c.jobs)

	c.workerWg.Wait()

	c.PrintStats()
	log.Println("Distributed Ollama worker pool shutdown complete")
}
