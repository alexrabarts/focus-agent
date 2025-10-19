package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
	"github.com/alexrabarts/focus-agent/internal/google"
	"github.com/alexrabarts/focus-agent/internal/llm"
	"github.com/alexrabarts/focus-agent/internal/planner"
)

// Scheduler interface to avoid circular dependency
type Scheduler interface {
	ProcessNewMessages()
}

type Server struct {
	database  *db.DB
	clients   *google.Clients
	llm       *llm.GeminiClient
	planner   *planner.Planner
	scheduler Scheduler
	config    *config.Config
	server    *http.Server
}

func NewServer(database *db.DB, clients *google.Clients, llmClient *llm.GeminiClient, plannerService *planner.Planner, cfg *config.Config) *Server {
	return &Server{
		database: database,
		clients:  clients,
		llm:      llmClient,
		planner:  plannerService,
		config:   cfg,
	}
}

// SetScheduler sets the scheduler for processing queue
func (s *Server) SetScheduler(scheduler Scheduler) {
	s.scheduler = scheduler
}

func (s *Server) Start(port int) error {
	mux := http.NewServeMux()

	// Register routes
	mux.HandleFunc("/api/tasks", s.authMiddleware(s.handleTasks))
	mux.HandleFunc("/api/tasks/", s.authMiddleware(s.handleTaskAction))
	mux.HandleFunc("/api/priorities", s.authMiddleware(s.handlePriorities))
	mux.HandleFunc("/api/priorities/undo", s.authMiddleware(s.handlePrioritiesUndo))
	mux.HandleFunc("/api/stats", s.authMiddleware(s.handleStats))
	mux.HandleFunc("/api/threads", s.authMiddleware(s.handleThreads))
	mux.HandleFunc("/api/threads/", s.authMiddleware(s.handleThreadMessages))
	mux.HandleFunc("/api/queue", s.authMiddleware(s.handleQueue))
	mux.HandleFunc("/api/queue/process", s.authMiddleware(s.handleQueueProcess))
	mux.HandleFunc("/api/brief", s.authMiddleware(s.handleBrief))
	mux.HandleFunc("/health", s.handleHealth)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      s.corsMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("API server starting on port %d", port)
	return s.server.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// Auth middleware checks for Bearer token
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Extract token from "Bearer <token>"
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid authorization header", http.StatusUnauthorized)
			return
		}

		token := parts[1]
		if token != s.config.API.AuthKey {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// CORS middleware for development
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Health check endpoint (no auth required)
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// Helper to write JSON responses
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// Helper to write error responses
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
