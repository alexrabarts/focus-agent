package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// GmailClient handles Gmail API operations
type GmailClient struct {
	Service *gmail.Service
	Config  *config.Config
}

// GmailSyncState stores Gmail-specific sync state
type GmailSyncState struct {
	LastHistoryID string `json:"last_history_id"`
}

// SyncThreads performs incremental sync of Gmail threads
func (g *GmailClient) SyncThreads(ctx context.Context, database *db.DB) error {
	// Get sync state
	syncState, err := database.GetSyncState("gmail")
	if err != nil {
		return fmt.Errorf("failed to get sync state: %w", err)
	}

	var state GmailSyncState
	if syncState.State != "" && syncState.State != "{}" {
		if err := json.Unmarshal([]byte(syncState.State), &state); err != nil {
			log.Printf("Warning: invalid sync state, doing full sync: %v", err)
			state = GmailSyncState{}
		}
	}

	// If we have a history ID, try incremental sync
	if state.LastHistoryID != "" {
		if err := g.incrementalSync(ctx, database, &state); err != nil {
			log.Printf("Incremental sync failed, falling back to full sync: %v", err)
			state.LastHistoryID = ""
		}
	}

	// Full sync if needed
	if state.LastHistoryID == "" {
		if err := g.fullSync(ctx, database, &state); err != nil {
			return fmt.Errorf("full sync failed: %w", err)
		}
	}

	// Save sync state
	stateJSON, _ := json.Marshal(state)
	syncState.State = string(stateJSON)
	syncState.LastSync = time.Now()
	syncState.NextSync = time.Now().Add(time.Duration(g.Config.Google.PollingMinutes.Gmail) * time.Minute)

	if err := database.SaveSyncState(syncState); err != nil {
		return fmt.Errorf("failed to save sync state: %w", err)
	}

	return nil
}

// fullSync performs a full synchronization of threads
func (g *GmailClient) fullSync(ctx context.Context, database *db.DB, state *GmailSyncState) error {
	log.Println("Starting Gmail full sync...")

	// Get profile to get history ID and user email
	profile, err := g.Service.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get profile: %w", err)
	}

	// Save user email to config if not already set
	if g.Config.Google.UserEmail == "" && profile.EmailAddress != "" {
		g.Config.Google.UserEmail = profile.EmailAddress
		log.Printf("Captured user email: %s", profile.EmailAddress)
	}

	// Build comprehensive query to catch important items
	// Base: exclude spam and trash
	queryParts := []string{"-in:spam", "-in:trash"}

	// Priority 1: Starred or important emails (regardless of age/read status)
	priorityQuery := "(is:starred OR is:important)"

	// Priority 2: Recent unread in inbox
	var recentQuery string
	if g.Config.Limits.DaysOfHistory > 0 {
		if g.Config.Limits.UnreadOnly {
			recentQuery = fmt.Sprintf("(in:inbox is:unread newer_than:%dd)", g.Config.Limits.DaysOfHistory)
		} else {
			recentQuery = fmt.Sprintf("(in:inbox newer_than:%dd)", g.Config.Limits.DaysOfHistory)
		}
	} else {
		recentQuery = "(in:inbox is:unread)"
	}

	// Priority 3: Recent sent emails (you might need to follow up)
	sentQuery := fmt.Sprintf("(in:sent newer_than:%dd)", g.Config.Limits.DaysOfHistory)

	// Combine: starred/important OR recent inbox OR recent sent
	query := strings.Join(queryParts, " ") + " AND (" +
		strings.Join([]string{priorityQuery, recentQuery, sentQuery}, " OR ") + ")"

	log.Printf("Gmail query: %s", query)
	log.Println("Syncing: starred/important + recent inbox + recent sent emails")

	// List threads with pagination
	pageToken := ""
	threadCount := 0
	maxThreads := g.Config.Limits.MaxThreadsPerSync

	log.Printf("Max threads to sync: %d", maxThreads)

	for {
		call := g.Service.Users.Threads.List("me").
			Context(ctx).
			MaxResults(100).
			Q(query)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to list threads: %w", err)
		}

		// Process each thread
		for _, thread := range resp.Threads {
			// Check if we've reached the limit
			if threadCount >= maxThreads {
				log.Printf("Reached max thread limit (%d), stopping sync", maxThreads)
				goto done
			}

			if err := g.syncThread(ctx, database, thread.Id); err != nil {
				log.Printf("Failed to sync thread %s: %v", thread.Id, err)
				continue
			}
			threadCount++

			// Progress indicator
			if threadCount%10 == 0 {
				log.Printf("Progress: %d threads synced...", threadCount)
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

done:

	// Update history ID
	state.LastHistoryID = fmt.Sprintf("%d", profile.HistoryId)
	log.Printf("Gmail full sync completed: %d threads synced", threadCount)

	return nil
}

// incrementalSync performs incremental sync using history API
func (g *GmailClient) incrementalSync(ctx context.Context, database *db.DB, state *GmailSyncState) error {
	log.Printf("Starting Gmail incremental sync from history ID %s...", state.LastHistoryID)

	startHistoryId, err := parseUint64(state.LastHistoryID)
	if err != nil {
		return fmt.Errorf("invalid history ID: %w", err)
	}

	// Get history list
	pageToken := ""
	messageCount := 0
	latestHistoryId := startHistoryId

	for {
		call := g.Service.Users.History.List("me").
			Context(ctx).
			StartHistoryId(startHistoryId).
			MaxResults(100)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to list history: %w", err)
		}

		// Process history records
		for _, history := range resp.History {
			// Process added messages
			for _, msg := range history.MessagesAdded {
				if err := g.syncMessage(ctx, database, msg.Message.Id, msg.Message.ThreadId); err != nil {
					log.Printf("Failed to sync message %s: %v", msg.Message.Id, err)
					continue
				}
				messageCount++
			}

			// Process modified messages (label changes, etc.)
			for _, msg := range history.MessagesDeleted {
				// Mark as deleted or remove from DB
				log.Printf("Message deleted: %s", msg.Message.Id)
			}

			if history.Id > latestHistoryId {
				latestHistoryId = history.Id
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	// Update history ID
	state.LastHistoryID = fmt.Sprintf("%d", latestHistoryId)
	log.Printf("Gmail incremental sync completed: %d messages processed", messageCount)

	return nil
}

// syncThread fetches and stores a complete thread
func (g *GmailClient) syncThread(ctx context.Context, database *db.DB, threadID string) error {
	thread, err := g.Service.Users.Threads.Get("me", threadID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get thread: %w", err)
	}

	// Process each message in the thread
	for _, msg := range thread.Messages {
		if err := g.processMessage(ctx, database, msg, threadID); err != nil {
			log.Printf("Failed to process message %s: %v", msg.Id, err)
		}
	}

	// Save thread metadata
	threadData := &db.Thread{
		ID:         threadID,
		LastSynced: time.Now(),
	}

	if err := database.SaveThread(threadData); err != nil {
		return fmt.Errorf("failed to save thread: %w", err)
	}

	return nil
}

// syncMessage fetches and stores a single message
func (g *GmailClient) syncMessage(ctx context.Context, database *db.DB, messageID, threadID string) error {
	msg, err := g.Service.Users.Messages.Get("me", messageID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}

	return g.processMessage(ctx, database, msg, threadID)
}

// processMessage extracts and stores message data
func (g *GmailClient) processMessage(ctx context.Context, database *db.DB, msg *gmail.Message, threadID string) error {
	// Extract headers
	headers := make(map[string]string)
	for _, header := range msg.Payload.Headers {
		headers[header.Name] = header.Value
	}

	// Extract body
	body := extractBody(msg.Payload)

	// Determine sensitivity based on labels and content
	sensitivity := "low"
	for _, label := range msg.LabelIds {
		if strings.Contains(strings.ToLower(label), "confidential") ||
			strings.Contains(strings.ToLower(label), "sensitive") {
			sensitivity = "high"
			break
		}
	}

	// Create message record
	message := &db.Message{
		ID:          msg.Id,
		ThreadID:    threadID,
		From:        headers["From"],
		To:          headers["To"],
		Subject:     headers["Subject"],
		Snippet:     msg.Snippet,
		Body:        body,
		Timestamp:   time.Unix(msg.InternalDate/1000, 0),
		Labels:      msg.LabelIds,
		Sensitivity: sensitivity,
	}

	// Save to database
	if err := database.SaveMessage(message); err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}

	return nil
}

// extractBody recursively extracts the body from message parts
func extractBody(payload *gmail.MessagePart) string {
	var body strings.Builder

	// Single part message
	if payload.Body != nil && payload.Body.Data != "" {
		decoded, err := base64.URLEncoding.DecodeString(payload.Body.Data)
		if err == nil {
			body.WriteString(string(decoded))
		}
	}

	// Multipart message
	for _, part := range payload.Parts {
		if part.MimeType == "text/plain" || part.MimeType == "text/html" {
			if part.Body != nil && part.Body.Data != "" {
				decoded, err := base64.URLEncoding.DecodeString(part.Body.Data)
				if err == nil {
					body.WriteString(string(decoded))
					body.WriteString("\n")
				}
			}
		}

		// Recursively process nested parts
		if len(part.Parts) > 0 {
			body.WriteString(extractBody(part))
		}
	}

	return body.String()
}

// SearchThreads searches for threads matching a query
func (g *GmailClient) SearchThreads(ctx context.Context, query string, maxResults int64) ([]*gmail.Thread, error) {
	resp, err := g.Service.Users.Threads.List("me").
		Context(ctx).
		Q(query).
		MaxResults(maxResults).
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to search threads: %w", err)
	}

	return resp.Threads, nil
}

// GetThread retrieves a single thread with all messages
func (g *GmailClient) GetThread(ctx context.Context, threadID string) (*gmail.Thread, error) {
	thread, err := g.Service.Users.Threads.Get("me", threadID).
		Context(ctx).
		Format("full").
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to get thread: %w", err)
	}

	return thread, nil
}

// SendMessage sends an email message
func (g *GmailClient) SendMessage(ctx context.Context, to, subject, body string, threadID string) error {
	var message gmail.Message

	// Create RFC 2822 formatted message
	var msgStr strings.Builder
	msgStr.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msgStr.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	if threadID != "" {
		msgStr.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", threadID))
		msgStr.WriteString(fmt.Sprintf("References: %s\r\n", threadID))
	}
	msgStr.WriteString("MIME-Version: 1.0\r\n")
	msgStr.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msgStr.WriteString("\r\n")
	msgStr.WriteString(body)

	// Encode message
	message.Raw = base64.URLEncoding.EncodeToString([]byte(msgStr.String()))

	if threadID != "" {
		message.ThreadId = threadID
	}

	// Send message
	_, err := g.Service.Users.Messages.Send("me", &message).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// CreateDraft creates a draft email
func (g *GmailClient) CreateDraft(ctx context.Context, to, subject, body string, threadID string) (*gmail.Draft, error) {
	var message gmail.Message

	// Create RFC 2822 formatted message
	var msgStr strings.Builder
	msgStr.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msgStr.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	if threadID != "" {
		msgStr.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", threadID))
		msgStr.WriteString(fmt.Sprintf("References: %s\r\n", threadID))
	}
	msgStr.WriteString("MIME-Version: 1.0\r\n")
	msgStr.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msgStr.WriteString("\r\n")
	msgStr.WriteString(body)

	// Encode message
	message.Raw = base64.URLEncoding.EncodeToString([]byte(msgStr.String()))

	if threadID != "" {
		message.ThreadId = threadID
	}

	// Create draft
	draft := &gmail.Draft{
		Message: &message,
	}

	createdDraft, err := g.Service.Users.Drafts.Create("me", draft).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create draft: %w", err)
	}

	return createdDraft, nil
}

// Helper function to parse uint64
func parseUint64(s string) (uint64, error) {
	var result uint64
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}