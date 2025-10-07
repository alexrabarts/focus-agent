package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"

	"github.com/alexrabarts/focus-agent/internal/config"
	"github.com/alexrabarts/focus-agent/internal/db"
)

// DriveClient handles Google Drive API operations
type DriveClient struct {
	Service *drive.Service
	Config  *config.Config
}

// DriveSyncState stores Drive-specific sync state
type DriveSyncState struct {
	StartPageToken string `json:"start_page_token"`
}

// SyncDocuments performs incremental sync of Drive documents
func (d *DriveClient) SyncDocuments(ctx context.Context, database *db.DB) error {
	// Get sync state
	syncState, err := database.GetSyncState("drive")
	if err != nil {
		return fmt.Errorf("failed to get sync state: %w", err)
	}

	var state DriveSyncState
	if syncState.State != "" && syncState.State != "{}" {
		if err := json.Unmarshal([]byte(syncState.State), &state); err != nil {
			log.Printf("Warning: invalid sync state, doing full sync: %v", err)
			state = DriveSyncState{}
		}
	}

	// Perform sync based on state
	if state.StartPageToken != "" {
		// Incremental sync using changes API
		if err := d.incrementalSync(ctx, database, &state); err != nil {
			log.Printf("Incremental sync failed, falling back to full sync: %v", err)
			state.StartPageToken = ""
		}
	}

	if state.StartPageToken == "" {
		// Full sync
		if err := d.fullSync(ctx, database, &state); err != nil {
			return fmt.Errorf("full sync failed: %w", err)
		}
	}

	// Save sync state
	stateJSON, _ := json.Marshal(state)
	syncState.State = string(stateJSON)
	syncState.LastSync = time.Now()
	syncState.NextSync = time.Now().Add(time.Duration(d.Config.Google.PollingMinutes.Drive) * time.Minute)

	if err := database.SaveSyncState(syncState); err != nil {
		return fmt.Errorf("failed to save sync state: %w", err)
	}

	return nil
}

// fullSync performs a full synchronization of documents
func (d *DriveClient) fullSync(ctx context.Context, database *db.DB, state *DriveSyncState) error {
	log.Println("Starting Drive full sync...")

	// Document types to sync
	docTypes := "(mimeType='application/vnd.google-apps.document' " +
		"OR mimeType='application/vnd.google-apps.spreadsheet' " +
		"OR mimeType='application/vnd.google-apps.presentation')"

	// Build comprehensive query
	// Priority 1: Starred documents (regardless of age)
	// Priority 2: Recently modified documents
	// Priority 3: Shared with me (active collaboration)

	queryParts := []string{docTypes, "trashed=false"}

	// Add time filter OR starred
	if d.Config.Limits.DriveDaysOfHistory > 0 {
		threshold := time.Now().AddDate(0, 0, -d.Config.Limits.DriveDaysOfHistory)
		timeFilter := fmt.Sprintf("modifiedTime > '%s'", threshold.Format(time.RFC3339))
		queryParts = append(queryParts, fmt.Sprintf("(starred=true OR %s)", timeFilter))
		log.Printf("Syncing: starred docs + docs modified in last %d days", d.Config.Limits.DriveDaysOfHistory)
	}

	query := strings.Join(queryParts, " AND ")

	pageToken := ""
	fileCount := 0
	maxFiles := d.Config.Limits.MaxDocumentsPerSync

	log.Printf("Max documents to sync: %d", maxFiles)

	for {
		call := d.Service.Files.List().
			Context(ctx).
			Q(query).
			Fields("nextPageToken, files(id, name, mimeType, webViewLink, owners, modifiedTime)").
			PageSize(100)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to list files: %w", err)
		}

		// Process each file
		for _, file := range resp.Files {
			// Check if we've reached the limit
			if fileCount >= maxFiles {
				log.Printf("Reached max document limit (%d), stopping sync", maxFiles)
				goto done
			}

			if err := d.processFile(ctx, database, file); err != nil {
				log.Printf("Failed to process file %s: %v", file.Id, err)
				continue
			}
			fileCount++

			// Progress indicator
			if fileCount%10 == 0 {
				log.Printf("Progress: %d documents synced...", fileCount)
			}
		}

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

done:

	// Get start page token for future incremental syncs
	startPageTokenResp, err := d.Service.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		log.Printf("Warning: failed to get start page token: %v", err)
	} else {
		state.StartPageToken = startPageTokenResp.StartPageToken
	}

	log.Printf("Drive full sync completed: %d files synced", fileCount)
	return nil
}

// incrementalSync performs incremental sync using changes API
func (d *DriveClient) incrementalSync(ctx context.Context, database *db.DB, state *DriveSyncState) error {
	log.Printf("Starting Drive incremental sync from token %s...", state.StartPageToken)

	pageToken := state.StartPageToken
	changeCount := 0

	for {
		call := d.Service.Changes.List(pageToken).
			Context(ctx).
			Fields("nextPageToken, newStartPageToken, changes(file(id, name, mimeType, webViewLink, owners, modifiedTime))").
			PageSize(100)

		resp, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to list changes: %w", err)
		}

		// Process each change
		for _, change := range resp.Changes {
			if change.File != nil && !change.File.Trashed {
				// Check if it's a document type we care about
				if isRelevantDocument(change.File.MimeType) {
					if err := d.processFile(ctx, database, change.File); err != nil {
						log.Printf("Failed to process changed file %s: %v", change.File.Id, err)
						continue
					}
					changeCount++
				}
			}
		}

		// Check if we have more pages
		if resp.NextPageToken != "" {
			pageToken = resp.NextPageToken
		} else {
			// Update the start page token for next sync
			state.StartPageToken = resp.NewStartPageToken
			break
		}
	}

	log.Printf("Drive incremental sync completed: %d changes processed", changeCount)
	return nil
}

// processFile saves or updates a file in the database
func (d *DriveClient) processFile(ctx context.Context, database *db.DB, file *drive.File) error {
	// Extract owner
	owner := ""
	if len(file.Owners) > 0 {
		owner = file.Owners[0].EmailAddress
	}

	// Parse modified time
	modifiedTime, err := time.Parse(time.RFC3339, file.ModifiedTime)
	if err != nil {
		modifiedTime = time.Now()
	}

	// Create document record
	doc := &db.Document{
		ID:         file.Id,
		Title:      file.Name,
		Link:       file.WebViewLink,
		MimeType:   file.MimeType,
		Owner:      owner,
		UpdatedTS:  modifiedTime,
		LastSynced: time.Now(),
	}

	// Save to database
	if err := database.SaveDocument(doc); err != nil {
		return fmt.Errorf("failed to save document: %w", err)
	}

	return nil
}

// GetDocument retrieves detailed document information
func (d *DriveClient) GetDocument(ctx context.Context, fileID string) (*drive.File, error) {
	file, err := d.Service.Files.Get(fileID).
		Context(ctx).
		Fields("id, name, mimeType, webViewLink, owners, modifiedTime, description").
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	return file, nil
}

// SearchDocuments searches for documents by query
func (d *DriveClient) SearchDocuments(ctx context.Context, query string, maxResults int64) ([]*drive.File, error) {
	resp, err := d.Service.Files.List().
		Context(ctx).
		Q(query).
		Fields("files(id, name, mimeType, webViewLink, owners, modifiedTime)").
		PageSize(maxResults).
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to search documents: %w", err)
	}

	return resp.Files, nil
}

// CreateDocument creates a new Google Doc
func (d *DriveClient) CreateDocument(ctx context.Context, title, content string) (*drive.File, error) {
	file := &drive.File{
		Name:     title,
		MimeType: "application/vnd.google-apps.document",
	}

	createdFile, err := d.Service.Files.Create(file).
		Context(ctx).
		Fields("id, webViewLink").
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to create document: %w", err)
	}

	// Note: To add content, you would need to use the Google Docs API
	// This just creates an empty document

	return createdFile, nil
}

// isRelevantDocument checks if a MIME type is one we care about
func isRelevantDocument(mimeType string) bool {
	relevantTypes := []string{
		"application/vnd.google-apps.document",
		"application/vnd.google-apps.spreadsheet",
		"application/vnd.google-apps.presentation",
		"application/pdf",
		"text/plain",
		"text/markdown",
	}

	for _, t := range relevantTypes {
		if mimeType == t {
			return true
		}
	}
	return false
}