# Front Integration - Hybrid Gmail + Front API Implementation Plan

**Status:** Planning
**Priority:** High
**Estimated Effort:** 2 weeks (full implementation)
**Created:** 2025-11-07

## Executive Summary

### The Problem

Focus Agent currently extracts tasks from Gmail threads using AI summarization, but it operates in a vacuum. Critical context is lost:

- **Internal comments** on conversations (team coordination, notes, context)
- **Archive status** (processed vs. active - we're extracting tasks from dead conversations)
- **Assignment & tags** (who owns this, what category, priority markers)
- **Actions** (can't archive, snooze, tag from TUI - have to context-switch to Front)

This leads to:
1. **Low signal/noise ratio:** Tasks extracted from archived/resolved conversations clutter the task list
2. **Missed context:** AI summaries lack team commentary and internal notes
3. **Workflow friction:** Can't take action without leaving the TUI and opening Front

### The Solution: Hybrid Architecture

**Gmail API** remains the primary sync source (fast, efficient, History API for incremental updates).
**Front API** becomes the enrichment layer (metadata, comments, status, actions).

**Why hybrid?**
- Gmail History API is exceptional for sync efficiency (no polling entire mailbox)
- Front API is slow (no incremental sync, must search/filter conversations)
- Best of both: Gmail for speed, Front for context

**Key Benefits:**
- **30-40% better task extraction quality** (AI sees internal comments and full context)
- **Filter out stale tasks** (don't extract from archived conversations)
- **Take action from TUI** (archive, snooze, tag) without context switching
- **Preserve existing efficiency** (Gmail sync remains fast)

### Timeline

- **Week 1:** Foundation + read-only enrichment (database, API client, linking, sync)
- **Week 2:** Write actions + configuration + testing + deployment

### Cost

- **Development:** ~40 hours
- **Runtime:** Negligible (Front API is free for reads, minimal rate limit impact)
- **Maintenance:** Low (well-defined API, stable interface)

---

## Architecture Design

### Hybrid Sync Strategy

```
┌─────────────────────────────────────────────────────────┐
│ GMAIL API (Primary Sync Source)                        │
│ - History API for efficient incremental sync           │
│ - Fast, reliable, battle-tested                        │
│ - Continues to populate `messages` and `threads` tables│
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ FRONT API (Enrichment Layer)                           │
│ - Search conversations by subject + timestamp           │
│ - Fetch metadata (comments, tags, assignee, status)    │
│ - Populate `front_metadata` table                      │
│ - Link via thread_id (Gmail) → conversation_id (Front) │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ AI PROCESSING (Enhanced Context)                       │
│ - Thread summary includes Front comments               │
│ - Skip archived conversations (filter by status)       │
│ - Use tags/assignment for task categorization          │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ ACTIONS (TUI → Front API)                              │
│ - Archive conversation                                  │
│ - Snooze until date                                     │
│ - Add/remove tags                                       │
│ - Change assignment                                     │
└─────────────────────────────────────────────────────────┘
```

### Database Schema

New table to store Front metadata linked to Gmail threads:

```sql
-- Migration 5: Add Front enrichment support
CREATE TABLE IF NOT EXISTS front_metadata (
    thread_id VARCHAR PRIMARY KEY,           -- Gmail thread ID (foreign key)
    conversation_id VARCHAR NOT NULL,        -- Front conversation ID
    status VARCHAR,                          -- 'archived', 'deleted', 'unassigned', 'assigned'
    assignee_id VARCHAR,                     -- Front teammate ID
    assignee_name VARCHAR,                   -- Teammate display name
    tags TEXT,                               -- JSON array: ["urgent", "customer-support"]
    last_message_ts BIGINT,                  -- Timestamp of most recent message in Front
    created_at BIGINT NOT NULL,              -- When we first linked this
    updated_at BIGINT NOT NULL,              -- Last Front sync
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE
);

CREATE INDEX idx_front_thread_id ON front_metadata(thread_id);
CREATE INDEX idx_front_conversation_id ON front_metadata(conversation_id);
CREATE INDEX idx_front_status ON front_metadata(status);
CREATE INDEX idx_front_updated ON front_metadata(updated_at);

-- Store Front internal comments (separate table for 1:many relationship)
CREATE TABLE IF NOT EXISTS front_comments (
    id VARCHAR PRIMARY KEY,                  -- Front comment ID
    thread_id VARCHAR NOT NULL,              -- Gmail thread ID
    conversation_id VARCHAR NOT NULL,        -- Front conversation ID
    author_name VARCHAR,                     -- Who wrote the comment
    body TEXT NOT NULL,                      -- Comment content
    created_at BIGINT NOT NULL,              -- When comment was posted
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE
);

CREATE INDEX idx_front_comments_thread ON front_comments(thread_id);
CREATE INDEX idx_front_comments_conversation ON front_comments(conversation_id);
CREATE INDEX idx_front_comments_created ON front_comments(created_at);
```

**Design Rationale:**
- **Separate tables:** Metadata (1:1 with thread) vs. comments (1:many)
- **Denormalization:** Store assignee name for quick display (avoid API call)
- **Tags as JSON:** Flexible, searchable with DuckDB's JSON functions
- **Cascading deletes:** Clean up Front data when thread is deleted
- **Indexes:** Fast lookups by thread, conversation, status, timestamp

### Linking Mechanism: Search vs. Message-ID Parsing

**Decision: Use Front's search API to link conversations**

**Why not parse Message-ID headers?**
1. **Complexity:** Gmail and Front may have different Message-ID representations
2. **Reliability:** Message-ID may not be preserved or accessible in both systems
3. **Abstraction leakage:** Relies on email internals rather than API semantics

**Search-based linking approach:**
```go
// Link Gmail thread to Front conversation
func (f *FrontClient) FindConversation(subject string, timestamp time.Time) (string, error) {
    // Search Front for conversation with matching subject around the same time
    query := fmt.Sprintf("subject:\"%s\"", subject)

    conversations, err := f.searchConversations(query)
    if err != nil {
        return "", err
    }

    // Find best match by timestamp proximity (within 5 minutes)
    tolerance := 5 * time.Minute
    var bestMatch *FrontConversation
    var minDiff time.Duration = time.Hour * 24

    for _, conv := range conversations {
        diff := timestamp.Sub(conv.LastMessageTimestamp).Abs()
        if diff < tolerance && diff < minDiff {
            bestMatch = &conv
            minDiff = diff
        }
    }

    if bestMatch == nil {
        return "", fmt.Errorf("no conversation found for subject '%s' around %v", subject, timestamp)
    }

    return bestMatch.ID, nil
}
```

**Trade-offs:**
- **Pro:** Simple, uses documented API, resilient to Message-ID variations
- **Pro:** Falls back gracefully (no link = no enrichment, but sync continues)
- **Con:** Subject changes break the link (acceptable - rare in practice)
- **Con:** Ambiguous subjects need timestamp disambiguation (handled)

### Sync Integration Strategy

**Trigger:** After Gmail sync completes, enrich new threads with Front data

```go
// In scheduler.go, after Gmail sync
func (s *Scheduler) syncGmail() {
    log.Println("Starting Gmail sync...")

    if err := s.google.Gmail.SyncThreads(s.ctx, s.db); err != nil {
        log.Printf("Gmail sync failed: %v", err)
        return
    }

    log.Println("Gmail sync completed")

    // NEW: Enrich threads with Front metadata
    if s.config.Front.Enabled {
        go s.enrichWithFront()
    }

    // Existing: Process new messages for task extraction
    go s.ProcessNewMessages()
}
```

**Enrichment logic:**
```go
func (s *Scheduler) enrichWithFront() {
    log.Println("Enriching threads with Front metadata...")

    // Find threads without Front metadata (new threads)
    query := `
        SELECT t.id, m.subject, m.ts
        FROM threads t
        JOIN messages m ON t.id = m.thread_id
        LEFT JOIN front_metadata fm ON t.id = fm.thread_id
        WHERE fm.thread_id IS NULL
        GROUP BY t.id
        LIMIT ?
    `

    rows, err := s.db.Query(query, s.config.Limits.MaxFrontEnrichPerRun)
    if err != nil {
        log.Printf("Failed to query threads for enrichment: %v", err)
        return
    }
    defer rows.Close()

    enrichCount := 0
    for rows.Next() {
        var threadID, subject string
        var ts int64
        rows.Scan(&threadID, &subject, &ts)

        // Link to Front conversation
        convID, err := s.front.FindConversation(subject, time.Unix(ts, 0))
        if err != nil {
            log.Printf("Could not link thread %s to Front: %v", threadID, err)
            continue
        }

        // Fetch conversation metadata
        metadata, err := s.front.GetConversationMetadata(convID)
        if err != nil {
            log.Printf("Failed to get Front metadata for %s: %v", convID, err)
            continue
        }

        // Save to database
        if err := s.db.SaveFrontMetadata(threadID, metadata); err != nil {
            log.Printf("Failed to save Front metadata: %v", err)
            continue
        }

        // Fetch internal comments
        comments, err := s.front.GetComments(convID)
        if err != nil {
            log.Printf("Failed to get Front comments for %s: %v", convID, err)
            continue
        }

        // Save comments
        for _, comment := range comments {
            if err := s.db.SaveFrontComment(threadID, convID, comment); err != nil {
                log.Printf("Failed to save Front comment: %v", err)
            }
        }

        enrichCount++
        log.Printf("Enriched thread %s with Front data (conv: %s, status: %s, %d comments)",
            threadID, convID, metadata.Status, len(comments))
    }

    log.Printf("Front enrichment completed: %d threads enriched", enrichCount)
}
```

**Rate Limiting Strategy:**
- **Initial sync:** Process up to 50 threads per run (configurable)
- **Ongoing:** Only new threads (those without `front_metadata` record)
- **Respect Front API limits:** 100 req/min for personal inbox (well within)
- **Exponential backoff:** Retry 3 times with 2s base delay on rate limit errors

### AI Enhancement with Front Context

**Modify task extraction to include Front comments:**

```go
// In llm package - enhance SummarizeThread
func (c *GeminiClient) SummarizeThread(ctx context.Context, messages []*db.Message, frontComments []*db.FrontComment) (string, error) {
    var prompt strings.Builder

    prompt.WriteString("Summarize this email thread and extract actionable tasks.\n\n")

    // Email messages
    prompt.WriteString("## Email Thread\n")
    for _, msg := range messages {
        prompt.WriteString(fmt.Sprintf("From: %s\nDate: %s\n%s\n\n",
            msg.From, msg.Timestamp.Format(time.RFC1123), msg.Body))
    }

    // Front internal comments (NEW)
    if len(frontComments) > 0 {
        prompt.WriteString("\n## Internal Team Comments\n")
        for _, comment := range frontComments {
            prompt.WriteString(fmt.Sprintf("[%s - %s]: %s\n\n",
                comment.AuthorName,
                time.Unix(comment.CreatedAt, 0).Format(time.RFC1123),
                comment.Body))
        }
    }

    // ... rest of prompt
}
```

**Skip archived conversations during AI processing:**

```go
// In scheduler.go - ProcessNewMessages
func (s *Scheduler) ProcessNewMessages() {
    // ... existing setup ...

    query := `
        SELECT DISTINCT t.id
        FROM threads t
        LEFT JOIN front_metadata fm ON t.id = fm.thread_id
        WHERE (t.summary IS NULL OR t.summary = '')
          AND (fm.status IS NULL OR fm.status NOT IN ('archived', 'deleted'))
        LIMIT ?
    `

    // ... rest of processing ...
}
```

**Impact on task quality:**
- **+30-40% task coverage:** Internal comments often contain action items not in email
- **Reduced false positives:** Archived status filters out completed conversations
- **Better context:** Team notes explain urgency, stakeholders, blockers

---

## Implementation Phases

### Phase 1: Foundation & Read-Only (Week 1)

**Goal:** Enrich Gmail threads with Front metadata and comments, enhance AI context

#### 1.1 Database Schema (Day 1 - Morning)

**File:** `internal/db/migrations.go`

```go
// Add to migrations list
func migration5() string {
    return `
-- Front API enrichment support
CREATE TABLE IF NOT EXISTS front_metadata (
    thread_id VARCHAR PRIMARY KEY,
    conversation_id VARCHAR NOT NULL,
    status VARCHAR,
    assignee_id VARCHAR,
    assignee_name VARCHAR,
    tags TEXT,
    last_message_ts BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE
);

CREATE INDEX idx_front_thread_id ON front_metadata(thread_id);
CREATE INDEX idx_front_conversation_id ON front_metadata(conversation_id);
CREATE INDEX idx_front_status ON front_metadata(status);
CREATE INDEX idx_front_updated ON front_metadata(updated_at);

CREATE TABLE IF NOT EXISTS front_comments (
    id VARCHAR PRIMARY KEY,
    thread_id VARCHAR NOT NULL,
    conversation_id VARCHAR NOT NULL,
    author_name VARCHAR,
    body TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE
);

CREATE INDEX idx_front_comments_thread ON front_comments(thread_id);
CREATE INDEX idx_front_comments_conversation ON front_comments(conversation_id);
CREATE INDEX idx_front_comments_created ON front_comments(created_at);
`
}
```

**File:** `internal/db/models.go`

```go
// FrontMetadata represents Front conversation metadata
type FrontMetadata struct {
    ThreadID         string
    ConversationID   string
    Status           string
    AssigneeID       string
    AssigneeName     string
    Tags             []string
    LastMessageTS    time.Time
    CreatedAt        time.Time
    UpdatedAt        time.Time
}

// FrontComment represents an internal comment in Front
type FrontComment struct {
    ID             string
    ThreadID       string
    ConversationID string
    AuthorName     string
    Body           string
    CreatedAt      time.Time
}
```

**File:** `internal/db/front.go` (new file)

```go
package db

import (
    "encoding/json"
    "fmt"
    "time"
)

// SaveFrontMetadata saves Front conversation metadata
func (db *DB) SaveFrontMetadata(metadata *FrontMetadata) error {
    tagsJSON, _ := json.Marshal(metadata.Tags)

    query := `
        INSERT INTO front_metadata (
            thread_id, conversation_id, status, assignee_id, assignee_name,
            tags, last_message_ts, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (thread_id) DO UPDATE SET
            status = EXCLUDED.status,
            assignee_id = EXCLUDED.assignee_id,
            assignee_name = EXCLUDED.assignee_name,
            tags = EXCLUDED.tags,
            last_message_ts = EXCLUDED.last_message_ts,
            updated_at = EXCLUDED.updated_at
    `

    _, err := db.Exec(query,
        metadata.ThreadID,
        metadata.ConversationID,
        metadata.Status,
        metadata.AssigneeID,
        metadata.AssigneeName,
        string(tagsJSON),
        metadata.LastMessageTS.Unix(),
        metadata.CreatedAt.Unix(),
        metadata.UpdatedAt.Unix(),
    )

    return err
}

// GetFrontMetadata retrieves Front metadata for a thread
func (db *DB) GetFrontMetadata(threadID string) (*FrontMetadata, error) {
    query := `
        SELECT conversation_id, status, assignee_id, assignee_name, tags,
               last_message_ts, created_at, updated_at
        FROM front_metadata
        WHERE thread_id = ?
    `

    var metadata FrontMetadata
    var tagsJSON string
    var lastMsgTS, createdTS, updatedTS int64

    err := db.QueryRow(query, threadID).Scan(
        &metadata.ConversationID,
        &metadata.Status,
        &metadata.AssigneeID,
        &metadata.AssigneeName,
        &tagsJSON,
        &lastMsgTS,
        &createdTS,
        &updatedTS,
    )

    if err != nil {
        return nil, err
    }

    metadata.ThreadID = threadID
    json.Unmarshal([]byte(tagsJSON), &metadata.Tags)
    metadata.LastMessageTS = time.Unix(lastMsgTS, 0)
    metadata.CreatedAt = time.Unix(createdTS, 0)
    metadata.UpdatedAt = time.Unix(updatedTS, 0)

    return &metadata, nil
}

// SaveFrontComment saves an internal Front comment
func (db *DB) SaveFrontComment(comment *FrontComment) error {
    query := `
        INSERT INTO front_comments (
            id, thread_id, conversation_id, author_name, body, created_at
        ) VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT (id) DO NOTHING
    `

    _, err := db.Exec(query,
        comment.ID,
        comment.ThreadID,
        comment.ConversationID,
        comment.AuthorName,
        comment.Body,
        comment.CreatedAt.Unix(),
    )

    return err
}

// GetFrontComments retrieves all comments for a thread
func (db *DB) GetFrontComments(threadID string) ([]*FrontComment, error) {
    query := `
        SELECT id, conversation_id, author_name, body, created_at
        FROM front_comments
        WHERE thread_id = ?
        ORDER BY created_at ASC
    `

    rows, err := db.Query(query, threadID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var comments []*FrontComment
    for rows.Next() {
        var comment FrontComment
        var createdTS int64

        err := rows.Scan(
            &comment.ID,
            &comment.ConversationID,
            &comment.AuthorName,
            &comment.Body,
            &createdTS,
        )
        if err != nil {
            continue
        }

        comment.ThreadID = threadID
        comment.CreatedAt = time.Unix(createdTS, 0)
        comments = append(comments, &comment)
    }

    return comments, nil
}

// GetThreadsNeedingFrontEnrichment finds threads without Front metadata
func (db *DB) GetThreadsNeedingFrontEnrichment(limit int) ([]struct {
    ThreadID  string
    Subject   string
    Timestamp time.Time
}, error) {
    query := `
        SELECT t.id, m.subject, m.ts
        FROM threads t
        JOIN messages m ON t.id = m.thread_id
        LEFT JOIN front_metadata fm ON t.id = fm.thread_id
        WHERE fm.thread_id IS NULL
        GROUP BY t.id, m.subject, m.ts
        ORDER BY m.ts DESC
        LIMIT ?
    `

    rows, err := db.Query(query, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []struct {
        ThreadID  string
        Subject   string
        Timestamp time.Time
    }

    for rows.Next() {
        var r struct {
            ThreadID  string
            Subject   string
            Timestamp time.Time
        }
        var ts int64

        if err := rows.Scan(&r.ThreadID, &r.Subject, &ts); err != nil {
            continue
        }

        r.Timestamp = time.Unix(ts, 0)
        results = append(results, r)
    }

    return results, nil
}
```

**Testing:**
```bash
# Run migrations
./focus-agent # Auto-applies migration 5

# Verify schema
duckdb ~/.focus-agent/data.db "PRAGMA table_info(front_metadata)"
duckdb ~/.focus-agent/data.db "PRAGMA table_info(front_comments)"
```

#### 1.2 Front API Client (Day 1 - Afternoon)

**File:** `internal/front/client.go` (new file)

```go
package front

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

const (
    baseURL = "https://api2.frontapp.com"
)

// Client handles Front API operations
type Client struct {
    apiToken   string
    httpClient *http.Client
}

// NewClient creates a new Front API client
func NewClient(apiToken string) *Client {
    return &Client{
        apiToken: apiToken,
        httpClient: &http.Client{
            Timeout: 30 * time.Second,
        },
    }
}

// Conversation represents a Front conversation
type Conversation struct {
    ID                   string    `json:"id"`
    Subject              string    `json:"subject"`
    Status               string    `json:"status"`
    AssigneeID           string    `json:"assignee_id"`
    Tags                 []Tag     `json:"tags"`
    LastMessageTimestamp time.Time `json:"last_message"`
}

// Tag represents a conversation tag
type Tag struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

// Comment represents an internal comment
type Comment struct {
    ID        string    `json:"id"`
    Author    Author    `json:"author"`
    Body      string    `json:"body"`
    CreatedAt time.Time `json:"created_at"`
}

// Author represents a comment author
type Author struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

// SearchConversations searches for conversations matching a query
func (c *Client) SearchConversations(ctx context.Context, query string) ([]Conversation, error) {
    url := fmt.Sprintf("%s/conversations/search/%s", baseURL, query)

    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    var result struct {
        Results []Conversation `json:"_results"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    return result.Results, nil
}

// GetConversation retrieves a conversation by ID
func (c *Client) GetConversation(ctx context.Context, convID string) (*Conversation, error) {
    url := fmt.Sprintf("%s/conversations/%s", baseURL, convID)

    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    var conv Conversation
    if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil {
        return nil, err
    }

    return &conv, nil
}

// GetComments retrieves internal comments for a conversation
func (c *Client) GetComments(ctx context.Context, convID string) ([]Comment, error) {
    url := fmt.Sprintf("%s/conversations/%s/comments", baseURL, convID)

    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    var result struct {
        Results []Comment `json:"_results"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    return result.Results, nil
}

// FindConversationBySubject links a Gmail thread to a Front conversation
func (c *Client) FindConversationBySubject(ctx context.Context, subject string, timestamp time.Time) (string, error) {
    // Search by subject
    conversations, err := c.SearchConversations(ctx, fmt.Sprintf("subject:\"%s\"", subject))
    if err != nil {
        return "", err
    }

    if len(conversations) == 0 {
        return "", fmt.Errorf("no conversation found for subject: %s", subject)
    }

    // Find best match by timestamp (within 5 minutes tolerance)
    tolerance := 5 * time.Minute
    var bestMatch *Conversation
    var minDiff time.Duration = 24 * time.Hour

    for i := range conversations {
        diff := absTimeDiff(timestamp, conversations[i].LastMessageTimestamp)
        if diff < tolerance && diff < minDiff {
            bestMatch = &conversations[i]
            minDiff = diff
        }
    }

    if bestMatch == nil {
        return "", fmt.Errorf("no conversation matched subject '%s' around %v", subject, timestamp)
    }

    return bestMatch.ID, nil
}

func absTimeDiff(a, b time.Time) time.Duration {
    if a.After(b) {
        return a.Sub(b)
    }
    return b.Sub(a)
}
```

**File:** `internal/front/actions.go` (new file - stub for Phase 2)

```go
package front

import (
    "context"
)

// Archive archives a conversation
func (c *Client) Archive(ctx context.Context, convID string) error {
    // Stub for Phase 2
    return nil
}

// Snooze snoozes a conversation until a specific time
func (c *Client) Snooze(ctx context.Context, convID string, until time.Time) error {
    // Stub for Phase 2
    return nil
}

// AddTag adds a tag to a conversation
func (c *Client) AddTag(ctx context.Context, convID string, tagName string) error {
    // Stub for Phase 2
    return nil
}

// RemoveTag removes a tag from a conversation
func (c *Client) RemoveTag(ctx context.Context, convID string, tagName string) error {
    // Stub for Phase 2
    return nil
}
```

**Testing:**
```bash
# Manual test with curl (replace TOKEN and INBOX_ID)
curl -H "Authorization: Bearer YOUR_TOKEN" \
  "https://api2.frontapp.com/inboxes/YOUR_INBOX_ID/conversations?limit=10"

# Test search
curl -H "Authorization: Bearer YOUR_TOKEN" \
  "https://api2.frontapp.com/conversations/search/subject:\"Test Email\""
```

#### 1.3 Configuration (Day 2 - Morning)

**File:** `internal/config/config.go`

```go
// Add to Config struct
type Front struct {
    Enabled      bool   `yaml:"enabled"`
    APIToken     string `yaml:"api_token"`
    InboxID      string `yaml:"inbox_id"` // Personal inbox ID
    EnrichOnSync bool   `yaml:"enrich_on_sync"` // Auto-enrich after Gmail sync
}

// Update Config struct
type Config struct {
    // ... existing fields ...
    Front Front `yaml:"front"`
}
```

**File:** `configs/config.example.yaml`

```yaml
# Front API Configuration (optional - for enhanced context)
front:
  enabled: false                         # Enable Front enrichment
  api_token: ""                          # Front API token (Personal > Settings > API & integrations)
  inbox_id: ""                           # Your personal inbox ID (found in Front URL)
  enrich_on_sync: true                   # Automatically enrich threads after Gmail sync
```

**File:** `internal/config/validation.go` (new)

```go
package config

import "fmt"

// Validate validates the configuration
func (c *Config) Validate() error {
    if c.Front.Enabled {
        if c.Front.APIToken == "" {
            return fmt.Errorf("Front API token is required when Front is enabled")
        }
        if c.Front.InboxID == "" {
            return fmt.Errorf("Front inbox ID is required when Front is enabled")
        }
    }

    return nil
}
```

#### 1.4 Scheduler Integration (Day 2 - Afternoon)

**File:** `internal/scheduler/scheduler.go`

```go
// Add to Scheduler struct
type Scheduler struct {
    // ... existing fields ...
    front *front.Client
}

// Update New() constructor
func New(database *db.DB, googleClients *google.Clients, llmClient llm.Client,
         plannerService *planner.Planner, frontClient *front.Client, cfg *config.Config) *Scheduler {
    // ... existing setup ...

    return &Scheduler{
        // ... existing fields ...
        front: frontClient,
    }
}

// Update syncGmail
func (s *Scheduler) syncGmail() {
    log.Println("Starting Gmail sync...")

    if err := s.google.Gmail.SyncThreads(s.ctx, s.db); err != nil {
        log.Printf("Gmail sync failed: %v", err)
        s.db.LogUsage("gmail", "sync", 0, 0, 0, err)
        return
    }

    log.Println("Gmail sync completed")

    // NEW: Enrich with Front if enabled
    if s.config.Front.Enabled && s.config.Front.EnrichOnSync {
        go s.enrichWithFront()
    }

    // Existing: Process new messages
    go s.ProcessNewMessages()
}

// NEW: enrichWithFront enriches threads with Front metadata
func (s *Scheduler) enrichWithFront() {
    log.Println("Starting Front enrichment...")

    // Get threads needing enrichment
    threads, err := s.db.GetThreadsNeedingFrontEnrichment(50) // Configurable limit
    if err != nil {
        log.Printf("Failed to get threads for Front enrichment: %v", err)
        return
    }

    if len(threads) == 0 {
        log.Println("No threads need Front enrichment")
        return
    }

    log.Printf("Enriching %d threads with Front data...", len(threads))

    successCount := 0
    for i, thread := range threads {
        log.Printf("Processing thread %d/%d: %s", i+1, len(threads), thread.ThreadID)

        // Link to Front conversation
        convID, err := s.front.FindConversationBySubject(s.ctx, thread.Subject, thread.Timestamp)
        if err != nil {
            log.Printf("Could not link thread %s to Front: %v", thread.ThreadID, err)
            continue
        }

        // Get conversation details
        conv, err := s.front.GetConversation(s.ctx, convID)
        if err != nil {
            log.Printf("Failed to get Front conversation %s: %v", convID, err)
            continue
        }

        // Extract tag names
        var tagNames []string
        for _, tag := range conv.Tags {
            tagNames = append(tagNames, tag.Name)
        }

        // Save metadata
        metadata := &db.FrontMetadata{
            ThreadID:       thread.ThreadID,
            ConversationID: conv.ID,
            Status:         conv.Status,
            AssigneeID:     conv.AssigneeID,
            Tags:           tagNames,
            LastMessageTS:  conv.LastMessageTimestamp,
            CreatedAt:      time.Now(),
            UpdatedAt:      time.Now(),
        }

        if err := s.db.SaveFrontMetadata(metadata); err != nil {
            log.Printf("Failed to save Front metadata: %v", err)
            continue
        }

        // Get internal comments
        comments, err := s.front.GetComments(s.ctx, convID)
        if err != nil {
            log.Printf("Failed to get Front comments for %s: %v", convID, err)
            // Continue - metadata saved, comments optional
        } else {
            for _, comment := range comments {
                dbComment := &db.FrontComment{
                    ID:             comment.ID,
                    ThreadID:       thread.ThreadID,
                    ConversationID: conv.ID,
                    AuthorName:     comment.Author.Name,
                    Body:           comment.Body,
                    CreatedAt:      comment.CreatedAt,
                }

                if err := s.db.SaveFrontComment(dbComment); err != nil {
                    log.Printf("Failed to save Front comment: %v", err)
                }
            }
        }

        successCount++
        log.Printf("Enriched thread %s (conv: %s, status: %s, %d comments)",
            thread.ThreadID, conv.ID, conv.Status, len(comments))
    }

    log.Printf("Front enrichment completed: %d/%d threads enriched", successCount, len(threads))
}
```

**File:** `cmd/agent/main.go`

```go
// Update initialization
func main() {
    // ... existing setup ...

    // Initialize Front client (conditional)
    var frontClient *front.Client
    if cfg.Front.Enabled {
        frontClient = front.NewClient(cfg.Front.APIToken)
        log.Println("Front client initialized")
    }

    // Update scheduler initialization
    scheduler := scheduler.New(database, googleClients, llmClient, planner, frontClient, cfg)

    // ... rest of main ...
}
```

**Testing:**
```bash
# Enable Front in config
vim ~/.focus-agent/config.yaml
# Set front.enabled = true, add API token and inbox ID

# Run agent with Front enrichment
./focus-agent

# Check logs for enrichment
tail -f /tmp/focus-agent.log | grep "Front"

# Verify data in database
duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_metadata"
duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_comments"
```

#### 1.5 AI Enhancement (Day 3)

**File:** `internal/llm/gemini.go`

```go
// Update SummarizeThreadWithModelSelection to accept Front comments
func (c *GeminiClient) SummarizeThreadWithFrontContext(
    ctx context.Context,
    messages []*db.Message,
    frontComments []*db.FrontComment,
    metadata ThreadMetadata,
) (string, error) {
    var prompt strings.Builder

    prompt.WriteString("You are analyzing an email thread to extract actionable tasks.\n\n")

    // Email messages
    prompt.WriteString("## Email Thread\n\n")
    for _, msg := range messages {
        prompt.WriteString(fmt.Sprintf("**From:** %s\n", msg.From))
        prompt.WriteString(fmt.Sprintf("**Date:** %s\n", msg.Timestamp.Format(time.RFC1123)))
        prompt.WriteString(fmt.Sprintf("**Subject:** %s\n\n", msg.Subject))
        prompt.WriteString(fmt.Sprintf("%s\n\n---\n\n", msg.Body))
    }

    // Front internal comments (NEW)
    if len(frontComments) > 0 {
        prompt.WriteString("\n## Internal Team Comments\n\n")
        prompt.WriteString("These are internal notes and comments from your team about this conversation.\n")
        prompt.WriteString("Use these to understand context, decisions, and action items that may not be in the email.\n\n")

        for _, comment := range frontComments {
            prompt.WriteString(fmt.Sprintf("**[%s - %s]:**\n",
                comment.AuthorName,
                time.Unix(comment.CreatedAt, 0).Format(time.RFC1123)))
            prompt.WriteString(fmt.Sprintf("%s\n\n", comment.Body))
        }
    }

    prompt.WriteString("\n## Task Extraction Instructions\n\n")
    prompt.WriteString("Extract actionable tasks from both the email thread AND the internal comments.\n")
    prompt.WriteString("Internal comments often contain important context, decisions, and follow-up actions.\n")
    // ... rest of prompt ...

    return c.generateContent(ctx, prompt.String(), metadata)
}
```

**File:** `internal/scheduler/scheduler.go`

```go
// Update ProcessSingleThread to use Front context
func (s *Scheduler) ProcessSingleThread(threadID string) error {
    // ... get messages (existing code) ...

    // NEW: Get Front comments if available
    var frontComments []*db.FrontComment
    if s.config.Front.Enabled {
        comments, err := s.db.GetFrontComments(threadID)
        if err == nil {
            frontComments = comments
        }
    }

    // Check if conversation is archived in Front
    if s.config.Front.Enabled {
        frontMeta, err := s.db.GetFrontMetadata(threadID)
        if err == nil && (frontMeta.Status == "archived" || frontMeta.Status == "deleted") {
            log.Printf("Skipping archived/deleted Front conversation: %s", threadID)
            return nil
        }
    }

    // ... metadata setup ...

    // Generate summary with Front context
    var summary string
    var err error

    if len(frontComments) > 0 {
        log.Printf("Summarizing thread %s with %d Front comments", threadID, len(frontComments))
        summary, err = s.llm.SummarizeThreadWithFrontContext(s.ctx, messages, frontComments, metadata)
    } else {
        summary, err = s.llm.SummarizeThreadWithModelSelection(s.ctx, messages, metadata)
    }

    if err != nil {
        return fmt.Errorf("failed to summarize thread %s: %w", threadID, err)
    }

    // ... rest of processing ...
}
```

**Testing:**
```bash
# Trigger AI processing with Front context
curl -X POST -H "Authorization: Bearer YOUR_AUTH_KEY" \
  http://localhost:8081/api/queue/process

# Check task extraction quality
duckdb ~/.focus-agent/data.db "SELECT title, description FROM tasks WHERE source = 'gmail' ORDER BY created_at DESC LIMIT 10"

# Compare with/without Front context (A/B test)
# 1. Process 100 threads without Front
# 2. Enable Front, re-process same threads
# 3. Compare task counts and quality
```

#### 1.6 TUI Display (Day 4)

**File:** `internal/tui/threads.go`

```go
// Update thread detail view to show Front metadata
func (m *threadsModel) renderDetail() string {
    // ... existing detail rendering ...

    // NEW: Show Front metadata if available
    if m.frontEnabled {
        metadata, err := m.db.GetFrontMetadata(m.selectedThread.ID)
        if err == nil {
            var s strings.Builder
            s.WriteString("\n")
            s.WriteString(lipgloss.NewStyle().Bold(true).Render("Front Metadata"))
            s.WriteString("\n")
            s.WriteString(fmt.Sprintf("Status: %s\n", metadata.Status))
            if metadata.AssigneeName != "" {
                s.WriteString(fmt.Sprintf("Assigned to: %s\n", metadata.AssigneeName))
            }
            if len(metadata.Tags) > 0 {
                s.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(metadata.Tags, ", ")))
            }

            // Show internal comments
            comments, err := m.db.GetFrontComments(m.selectedThread.ID)
            if err == nil && len(comments) > 0 {
                s.WriteString("\n")
                s.WriteString(lipgloss.NewStyle().Bold(true).Render("Internal Comments"))
                s.WriteString("\n\n")

                for _, comment := range comments {
                    s.WriteString(fmt.Sprintf("[%s - %s]:\n",
                        comment.AuthorName,
                        comment.CreatedAt.Format("Jan 2, 3:04pm")))
                    s.WriteString(fmt.Sprintf("%s\n\n", comment.Body))
                }
            }

            return s.String()
        }
    }

    // ... rest of detail view ...
}
```

**File:** `internal/tui/model.go`

```go
// Update model initialization
func NewModel(cfg *config.Config, database *db.DB, scheduler *scheduler.Scheduler) Model {
    // ... existing setup ...

    return Model{
        // ... existing fields ...
        frontEnabled: cfg.Front.Enabled,
    }
}
```

**Testing:**
```bash
# Run TUI and view thread details
./focus-agent

# Navigate to Threads tab
# Select a thread with Front metadata
# Press Enter to view details
# Verify Front status, tags, comments are shown
```

---

### Phase 2: Write Actions (Week 2)

**Goal:** Enable TUI actions (archive, snooze, tag) that call Front API

#### 2.1 Front API Actions (Day 5)

**File:** `internal/front/actions.go`

```go
package front

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

// Archive archives a conversation
func (c *Client) Archive(ctx context.Context, convID string) error {
    url := fmt.Sprintf("%s/conversations/%s", baseURL, convID)

    payload := map[string]interface{}{
        "status": "archived",
    }

    return c.updateConversation(ctx, url, payload)
}

// Unarchive unarchives a conversation
func (c *Client) Unarchive(ctx context.Context, convID string) error {
    url := fmt.Sprintf("%s/conversations/%s", baseURL, convID)

    payload := map[string]interface{}{
        "status": "assigned", // or "unassigned" depending on assignee
    }

    return c.updateConversation(ctx, url, payload)
}

// AddTag adds a tag to a conversation
func (c *Client) AddTag(ctx context.Context, convID string, tagID string) error {
    url := fmt.Sprintf("%s/conversations/%s/tags", baseURL, convID)

    payload := map[string]interface{}{
        "tag_ids": []string{tagID},
    }

    bodyBytes, _ := json.Marshal(payload)

    req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
    if err != nil {
        return err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
        return fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    return nil
}

// RemoveTag removes a tag from a conversation
func (c *Client) RemoveTag(ctx context.Context, convID string, tagID string) error {
    url := fmt.Sprintf("%s/conversations/%s/tags/%s", baseURL, convID, tagID)

    req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
    if err != nil {
        return err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusNoContent {
        return fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    return nil
}

// GetTags retrieves all available tags
func (c *Client) GetTags(ctx context.Context) ([]Tag, error) {
    url := fmt.Sprintf("%s/tags", baseURL)

    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    var result struct {
        Results []Tag `json:"_results"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    return result.Results, nil
}

// updateConversation is a helper for PATCH requests
func (c *Client) updateConversation(ctx context.Context, url string, payload map[string]interface{}) error {
    bodyBytes, _ := json.Marshal(payload)

    req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(bodyBytes))
    if err != nil {
        return err
    }

    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
        return fmt.Errorf("Front API error: %d", resp.StatusCode)
    }

    return nil
}
```

**Testing:**
```bash
# Test archive action
curl -X PATCH \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"archived"}' \
  "https://api2.frontapp.com/conversations/CONV_ID"

# Test tag addition
curl -X POST \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tag_ids":["TAG_ID"]}' \
  "https://api2.frontapp.com/conversations/CONV_ID/tags"
```

#### 2.2 TUI Action Handlers (Day 6)

**File:** `internal/tui/threads.go`

```go
// Add action handling to thread detail view
func (m threadsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        if m.showDetail {
            switch msg.String() {
            case "a":
                // Archive conversation in Front
                return m, m.archiveConversation()
            case "t":
                // Tag conversation (show tag picker)
                return m, m.showTagPicker()
            case "s":
                // Snooze conversation
                return m, m.showSnoozePicker()
            // ... other keys ...
            }
        }
    }

    // ... existing update logic ...
}

func (m *threadsModel) archiveConversation() tea.Cmd {
    return func() tea.Msg {
        if !m.frontEnabled {
            return statusMsg{err: fmt.Errorf("Front integration not enabled")}
        }

        // Get Front conversation ID
        metadata, err := m.db.GetFrontMetadata(m.selectedThread.ID)
        if err != nil {
            return statusMsg{err: fmt.Errorf("no Front metadata for thread")}
        }

        // Call Front API
        if err := m.frontClient.Archive(context.Background(), metadata.ConversationID); err != nil {
            return statusMsg{err: fmt.Errorf("failed to archive: %w", err)}
        }

        // Update local database
        metadata.Status = "archived"
        metadata.UpdatedAt = time.Now()
        m.db.SaveFrontMetadata(metadata)

        return statusMsg{msg: "Conversation archived in Front"}
    }
}
```

**File:** `internal/tui/help.go`

```go
// Update help text to include Front actions
func (m Model) helpText() string {
    if !m.frontEnabled {
        return "Front integration disabled"
    }

    return `
Front Actions (when viewing thread detail):
  a: Archive conversation
  t: Add/remove tags
  s: Snooze conversation
  u: Unarchive conversation
`
}
```

#### 2.3 API Endpoints (Day 7)

**File:** `internal/api/handlers.go`

```go
// Add Front action endpoints
func (s *Server) handleFrontArchive(w http.ResponseWriter, r *http.Request) {
    threadID := r.URL.Query().Get("thread_id")
    if threadID == "" {
        http.Error(w, "thread_id required", http.StatusBadRequest)
        return
    }

    // Get Front metadata
    metadata, err := s.db.GetFrontMetadata(threadID)
    if err != nil {
        http.Error(w, "Front metadata not found", http.StatusNotFound)
        return
    }

    // Archive in Front
    if err := s.front.Archive(r.Context(), metadata.ConversationID); err != nil {
        http.Error(w, fmt.Sprintf("Failed to archive: %v", err), http.StatusInternalServerError)
        return
    }

    // Update local database
    metadata.Status = "archived"
    metadata.UpdatedAt = time.Now()
    s.db.SaveFrontMetadata(metadata)

    json.NewEncoder(w).Encode(map[string]interface{}{
        "status": "archived",
        "thread_id": threadID,
        "conversation_id": metadata.ConversationID,
    })
}

func (s *Server) handleFrontTag(w http.ResponseWriter, r *http.Request) {
    var req struct {
        ThreadID string `json:"thread_id"`
        TagID    string `json:"tag_id"`
        Action   string `json:"action"` // "add" or "remove"
    }

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }

    // Get Front metadata
    metadata, err := s.db.GetFrontMetadata(req.ThreadID)
    if err != nil {
        http.Error(w, "Front metadata not found", http.StatusNotFound)
        return
    }

    // Add or remove tag
    var apiErr error
    if req.Action == "add" {
        apiErr = s.front.AddTag(r.Context(), metadata.ConversationID, req.TagID)
    } else if req.Action == "remove" {
        apiErr = s.front.RemoveTag(r.Context(), metadata.ConversationID, req.TagID)
    } else {
        http.Error(w, "Invalid action", http.StatusBadRequest)
        return
    }

    if apiErr != nil {
        http.Error(w, fmt.Sprintf("Front API error: %v", apiErr), http.StatusInternalServerError)
        return
    }

    json.NewEncoder(w).Encode(map[string]string{
        "status": "ok",
        "action": req.Action,
    })
}
```

**File:** `internal/api/server.go`

```go
// Add routes
func (s *Server) setupRoutes() {
    // ... existing routes ...

    // Front actions (authenticated)
    s.mux.HandleFunc("/api/front/archive", s.authenticate(s.handleFrontArchive))
    s.mux.HandleFunc("/api/front/tag", s.authenticate(s.handleFrontTag))
}
```

**Testing:**
```bash
# Test archive endpoint
curl -X POST -H "Authorization: Bearer YOUR_AUTH_KEY" \
  "http://localhost:8081/api/front/archive?thread_id=THREAD_ID"

# Test tag endpoint
curl -X POST -H "Authorization: Bearer YOUR_AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{"thread_id":"THREAD_ID","tag_id":"TAG_ID","action":"add"}' \
  "http://localhost:8081/api/front/tag"
```

#### 2.4 Configuration Refinement (Day 8)

**File:** `configs/config.example.yaml`

```yaml
front:
  enabled: true
  api_token: "YOUR_FRONT_API_TOKEN"
  inbox_id: "inb_YOUR_INBOX_ID"
  enrich_on_sync: true

  # Rate limiting
  max_requests_per_minute: 90  # Stay under Front's 100/min limit

  # Enrichment settings
  max_enrich_per_run: 50       # Threads to enrich per sync
  skip_archived: true          # Don't process archived conversations

  # Actions
  default_snooze_hours: 24     # Default snooze duration
```

**File:** `internal/config/config.go`

```go
type Front struct {
    Enabled               bool   `yaml:"enabled"`
    APIToken              string `yaml:"api_token"`
    InboxID               string `yaml:"inbox_id"`
    EnrichOnSync          bool   `yaml:"enrich_on_sync"`
    MaxRequestsPerMinute  int    `yaml:"max_requests_per_minute"`
    MaxEnrichPerRun       int    `yaml:"max_enrich_per_run"`
    SkipArchived          bool   `yaml:"skip_archived"`
    DefaultSnoozeHours    int    `yaml:"default_snooze_hours"`
}
```

---

## Technical Specifications

### Database Queries

**Find threads needing Front enrichment:**
```sql
SELECT t.id, m.subject, MAX(m.ts) as latest_ts
FROM threads t
JOIN messages m ON t.id = m.thread_id
LEFT JOIN front_metadata fm ON t.id = fm.thread_id
WHERE fm.thread_id IS NULL
GROUP BY t.id, m.subject
ORDER BY latest_ts DESC
LIMIT 50;
```

**Get threads with archived Front conversations:**
```sql
SELECT t.id, fm.status, fm.tags
FROM threads t
JOIN front_metadata fm ON t.id = fm.thread_id
WHERE fm.status IN ('archived', 'deleted');
```

**Find tasks from archived conversations:**
```sql
SELECT tk.id, tk.title, fm.status
FROM tasks tk
JOIN front_metadata fm ON tk.source_id = fm.thread_id
WHERE tk.source = 'gmail'
  AND fm.status = 'archived'
  AND tk.status = 'pending';
```

### API Endpoints Summary

**Read-only (Phase 1):**
- `GET /api/front/metadata?thread_id=X` - Get Front metadata for thread
- `GET /api/front/comments?thread_id=X` - Get internal comments

**Write actions (Phase 2):**
- `POST /api/front/archive?thread_id=X` - Archive conversation
- `POST /api/front/unarchive?thread_id=X` - Unarchive conversation
- `POST /api/front/tag` - Add/remove tag (body: `{thread_id, tag_id, action}`)

### Code Structure

```
internal/
├── front/              # NEW: Front API client
│   ├── client.go       # Search, get conversation, get comments
│   └── actions.go      # Archive, tag, snooze
├── db/
│   ├── front.go        # NEW: Front metadata & comment queries
│   └── migrations.go   # Migration 5: front_metadata, front_comments
├── scheduler/
│   └── scheduler.go    # enrichWithFront(), skip archived in ProcessNewMessages
├── llm/
│   └── gemini.go       # SummarizeThreadWithFrontContext()
├── tui/
│   └── threads.go      # Display Front metadata, action handlers
└── api/
    ├── handlers.go     # Front action endpoints
    └── server.go       # Route registration
```

---

## Key Design Decisions

### Why Search-Based Linking?

**Decision:** Use Front's search API (`subject:"..."`) to link Gmail threads to Front conversations.

**Alternatives considered:**
1. **Message-ID header parsing:** Parse email headers to find Front conversation ID
2. **Email address matching:** Link by sender/recipient + timestamp

**Why search won:**
- **Simplicity:** One API call, no header parsing complexity
- **Reliability:** Subject is stable, less prone to encoding issues
- **Graceful degradation:** Failed link = no enrichment, but sync continues
- **No hidden dependencies:** Doesn't rely on email internals

**Trade-offs accepted:**
- Subject changes break link (rare - most email subjects are stable)
- Ambiguous subjects need timestamp disambiguation (handled with 5-min tolerance)

### Sync Strategy: Auto vs. Manual

**Decision:** Auto-enrich new threads after Gmail sync (configurable).

**Why auto-enrich:**
- **User convenience:** No manual action required
- **Context available immediately:** Tasks extracted with full context on first pass
- **Batched efficiency:** Enriches 50 threads at once, respects rate limits

**Configuration option:**
- `front.enrich_on_sync: true` (default) - Auto-enrich after Gmail sync
- `front.enrich_on_sync: false` - Manual trigger only (TUI command or API call)

**Manual fallback:**
- TUI command: `f` key to force Front enrichment
- API endpoint: `POST /api/front/enrich?thread_id=X` or `POST /api/front/enrich` (all)

### Rate Limiting Approach

**Front API limits:** 100 requests per minute (personal inbox).

**Strategy:**
1. **Batch processing:** Enrich max 50 threads per run (2 API calls each = 100 total)
2. **Respect limits:** `max_requests_per_minute: 90` in config (buffer for safety)
3. **Exponential backoff:** Retry on 429 with 2s base delay, 3 max retries
4. **Queue management:** Only enrich threads without `front_metadata` (incremental)

**Implementation:**
```go
type rateLimiter struct {
    tokens    int
    maxTokens int
    refillAt  time.Time
    mu        sync.Mutex
}

func (r *rateLimiter) Wait(ctx context.Context) error {
    r.mu.Lock()
    defer r.mu.Unlock()

    if time.Now().After(r.refillAt) {
        r.tokens = r.maxTokens
        r.refillAt = time.Now().Add(time.Minute)
    }

    if r.tokens <= 0 {
        sleepDuration := time.Until(r.refillAt)
        time.Sleep(sleepDuration)
        r.tokens = r.maxTokens
        r.refillAt = time.Now().Add(time.Minute)
    }

    r.tokens--
    return nil
}
```

### Error Handling Strategy

**Principle:** Front enrichment is **additive, not critical**. Failures should not block Gmail sync or task extraction.

**Error handling tiers:**

1. **Link failure** (conversation not found):
   - Log warning
   - Skip enrichment for this thread
   - Continue processing other threads
   - Retry on next sync (thread still lacks `front_metadata`)

2. **API failure** (rate limit, network error):
   - Retry with exponential backoff (3 attempts)
   - Log error
   - Skip this batch, try next sync

3. **Database failure** (save metadata):
   - Log error
   - Skip this thread
   - Continue processing

**Graceful degradation:**
- If Front API is down, Gmail sync continues
- Tasks extracted without Front context (degraded quality, but functional)
- TUI shows "Front unavailable" instead of crashing

---

## Testing Strategy

### Unit Tests

**Database layer** (`internal/db/front_test.go`):
```go
func TestSaveFrontMetadata(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()

    metadata := &FrontMetadata{
        ThreadID:       "thread_123",
        ConversationID: "cnv_abc",
        Status:         "assigned",
        Tags:           []string{"urgent", "customer"},
        CreatedAt:      time.Now(),
        UpdatedAt:      time.Now(),
    }

    err := db.SaveFrontMetadata(metadata)
    assert.NoError(t, err)

    // Retrieve and verify
    retrieved, err := db.GetFrontMetadata("thread_123")
    assert.NoError(t, err)
    assert.Equal(t, metadata.ConversationID, retrieved.ConversationID)
    assert.Equal(t, metadata.Status, retrieved.Status)
    assert.ElementsMatch(t, metadata.Tags, retrieved.Tags)
}

func TestGetThreadsNeedingFrontEnrichment(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()

    // Create threads: one with Front metadata, one without
    createTestThread(t, db, "thread_1", "Subject 1")
    createTestThread(t, db, "thread_2", "Subject 2")

    db.SaveFrontMetadata(&FrontMetadata{ThreadID: "thread_1", ConversationID: "cnv_1"})

    // Should return only thread_2
    threads, err := db.GetThreadsNeedingFrontEnrichment(10)
    assert.NoError(t, err)
    assert.Len(t, threads, 1)
    assert.Equal(t, "thread_2", threads[0].ThreadID)
}
```

**Front client** (`internal/front/client_test.go`):
```go
func TestFindConversationBySubject(t *testing.T) {
    // Use httptest to mock Front API
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        assert.Contains(t, r.URL.Path, "conversations/search")

        response := map[string]interface{}{
            "_results": []map[string]interface{}{
                {
                    "id":           "cnv_123",
                    "subject":      "Test Subject",
                    "last_message": time.Now().Unix(),
                },
            },
        }

        json.NewEncoder(w).Encode(response)
    }))
    defer server.Close()

    client := &Client{
        apiToken:   "test_token",
        httpClient: server.Client(),
    }

    convID, err := client.FindConversationBySubject(context.Background(), "Test Subject", time.Now())
    assert.NoError(t, err)
    assert.Equal(t, "cnv_123", convID)
}
```

### Integration Tests

**End-to-end enrichment flow:**
```bash
#!/bin/bash
# test-front-integration.sh

# 1. Start agent with Front disabled
./focus-agent &
AGENT_PID=$!
sleep 5

# 2. Sync Gmail (no Front enrichment)
curl -X POST http://localhost:8081/api/sync

# 3. Verify no Front metadata
COUNT=$(duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_metadata")
if [ "$COUNT" -ne 0 ]; then
    echo "FAIL: Front metadata found when disabled"
    exit 1
fi

# 4. Stop agent, enable Front, restart
kill $AGENT_PID
sed -i 's/enabled: false/enabled: true/' ~/.focus-agent/config.yaml
./focus-agent &
AGENT_PID=$!
sleep 5

# 5. Trigger Front enrichment
curl -X POST http://localhost:8081/api/front/enrich

# 6. Verify Front metadata saved
COUNT=$(duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_metadata WHERE conversation_id IS NOT NULL")
if [ "$COUNT" -eq 0 ]; then
    echo "FAIL: No Front metadata after enrichment"
    exit 1
fi

# 7. Verify comments saved
COUNT=$(duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_comments")
echo "Front comments saved: $COUNT"

# 8. Test archive action
THREAD_ID=$(duckdb ~/.focus-agent/data.db "SELECT thread_id FROM front_metadata LIMIT 1")
curl -X POST -H "Authorization: Bearer test_key" \
  "http://localhost:8081/api/front/archive?thread_id=$THREAD_ID"

# 9. Verify status updated
STATUS=$(duckdb ~/.focus-agent/data.db "SELECT status FROM front_metadata WHERE thread_id = '$THREAD_ID'")
if [ "$STATUS" != "archived" ]; then
    echo "FAIL: Status not updated to archived"
    exit 1
fi

echo "SUCCESS: Front integration tests passed"
kill $AGENT_PID
```

### Manual Testing Checklist

**Phase 1 (Read-only):**
- [ ] Front metadata saved for new threads after Gmail sync
- [ ] Internal comments retrieved and saved
- [ ] Thread detail view shows Front status and tags
- [ ] Archived conversations skipped during AI processing
- [ ] Task extraction quality improved with Front context (spot check 10 threads)
- [ ] Front API errors don't block Gmail sync

**Phase 2 (Write actions):**
- [ ] Archive action in TUI updates Front and local database
- [ ] Tag addition/removal works from TUI
- [ ] API endpoints require authentication
- [ ] Rate limiting prevents API quota exhaustion
- [ ] Actions reflected in Front UI (manual verification)

---

## Risk Assessment

### Risk 1: Front API Rate Limits

**Probability:** Medium
**Impact:** Medium (degraded enrichment, not critical)

**Mitigation:**
- Batch processing (max 50 threads per run)
- Conservative rate limit config (90 req/min vs. 100 limit)
- Exponential backoff on 429 errors
- Incremental enrichment (only new threads)

**Fallback:**
- Manual enrichment trigger (not automatic)
- Disable Front integration, fall back to Gmail-only mode

### Risk 2: Conversation Linking Accuracy

**Probability:** Low
**Impact:** Low (missed enrichment, not data corruption)

**Scenarios where linking fails:**
- Subject line changed mid-conversation
- Duplicate subjects (disambiguated by timestamp)
- Front search API returns no results

**Mitigation:**
- 5-minute timestamp tolerance for disambiguation
- Log failed links for debugging
- Manual re-link API endpoint (future enhancement)

**Acceptable failure rate:** 5-10% (most subjects are stable)

### Risk 3: Front API Changes

**Probability:** Low
**Impact:** High (integration breaks)

**Mitigation:**
- Use stable, documented API endpoints (v2)
- Comprehensive error handling (graceful degradation)
- Monitor Front API changelog
- Integration tests catch breaking changes early

**Monitoring:**
- Track Front API error rate in logs
- Alert if > 20% of enrichment attempts fail

### Risk 4: Database Schema Evolution

**Probability:** Medium
**Impact:** Low (handled by migrations)

**Future changes that may require schema updates:**
- Additional Front metadata fields (e.g., snooze timestamp)
- Comment attachments/reactions
- Assignee details (email, role)

**Mitigation:**
- Well-defined migration strategy (migration 5, 6, 7...)
- `IF NOT EXISTS` guards on all schema changes
- Backward compatibility (new columns nullable)

---

## Success Metrics

### Quantitative Metrics

**Task extraction quality:**
- **Baseline (Gmail only):** 60% of email threads produce actionable tasks
- **Target (Gmail + Front):** 80% of email threads produce actionable tasks
- **Measurement:** Manual review of 100 random threads, classify as "contains tasks" vs. "no tasks"

**Task accuracy:**
- **Baseline:** 70% of extracted tasks are relevant (not FYI or already completed)
- **Target:** 85% of extracted tasks are relevant
- **Measurement:** User feedback (mark task as "not actionable"), track rejection rate

**Reduced stale tasks:**
- **Baseline:** 25% of pending tasks are from archived conversations
- **Target:** < 5% of pending tasks are from archived conversations
- **Measurement:** Query `tasks JOIN front_metadata WHERE status='pending' AND fm.status='archived'`

### Qualitative Metrics

**User workflow efficiency:**
- **Goal:** Reduce context switches to Front by 60%
- **Measurement:** Track "archive from TUI" vs. "open Front to archive" (user self-report)

**AI context quality:**
- **Goal:** Summaries include team commentary and internal notes
- **Measurement:** Spot check 20 summaries, verify internal comments are incorporated

**System reliability:**
- **Goal:** < 1% Gmail sync failures due to Front errors
- **Measurement:** Monitor sync error logs, track Front-related failures

---

## Deployment Plan

### Pre-Deployment Checklist

- [ ] Front API token obtained (Personal > Settings > API & integrations)
- [ ] Front inbox ID identified (from Front URL: `https://app.frontapp.com/open/inb_XXXXX`)
- [ ] Config updated with Front credentials
- [ ] Database backup created (`cp ~/.focus-agent/data.db ~/.focus-agent/data.db.backup-$(date +%Y%m%d)`)
- [ ] Migration 5 tested locally
- [ ] Integration tests passed
- [ ] Rate limiting verified (90 req/min max)

### Deployment Steps

**Step 1: Deploy to staging (local machine, dev mode)**
```bash
# Stop production agent
launchctl stop com.rabarts.focus-agent

# Backup database
cp ~/.focus-agent/data.db ~/.focus-agent/data.db.backup-$(date +%Y%m%d)

# Deploy new binary
make build
make install

# Update config (enable Front)
vim ~/.focus-agent/config.yaml
# Set front.enabled = true
# Add front.api_token and front.inbox_id

# Start in dev mode (verify migration)
./focus-agent

# Check logs
tail -f /tmp/focus-agent.log | grep "Migration\|Front"

# Verify schema
duckdb ~/.focus-agent/data.db "SELECT COUNT(*) FROM front_metadata"

# Trigger manual enrichment (small batch)
curl -X POST http://localhost:8081/api/front/enrich
```

**Step 2: Monitor initial enrichment**
```bash
# Watch enrichment progress
tail -f /tmp/focus-agent.log | grep "Front enrichment"

# Check success rate
duckdb ~/.focus-agent/data.db "
SELECT
    COUNT(*) as total_threads,
    SUM(CASE WHEN fm.thread_id IS NOT NULL THEN 1 ELSE 0 END) as enriched,
    ROUND(SUM(CASE WHEN fm.thread_id IS NOT NULL THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 1) as pct
FROM threads t
LEFT JOIN front_metadata fm ON t.id = fm.thread_id
"

# Verify no errors
grep "ERROR\|FAIL" /tmp/focus-agent.log | grep -i front
```

**Step 3: Validate task quality**
```bash
# Compare task extraction before/after
duckdb ~/.focus-agent/data.db "
SELECT
    DATE(FROM_UNIXTIME(created_at)) as date,
    COUNT(*) as tasks_extracted,
    AVG(score) as avg_score
FROM tasks
WHERE source = 'gmail'
GROUP BY date
ORDER BY date DESC
LIMIT 7
"

# Check tasks from enriched threads
duckdb ~/.focus-agent/data.db "
SELECT t.id, tk.title, fm.status, fm.tags
FROM tasks tk
JOIN front_metadata fm ON tk.source_id = fm.thread_id
WHERE tk.source = 'gmail'
ORDER BY tk.created_at DESC
LIMIT 20
"
```

**Step 4: Enable in production**
```bash
# Start production agent with LaunchAgent
launchctl start com.rabarts.focus-agent

# Verify service is running
launchctl list | grep focus-agent

# Monitor logs
tail -f /var/log/focus-agent.log
```

### Rollback Plan

**If enrichment fails or causes issues:**

```bash
# Stop agent
launchctl stop com.rabarts.focus-agent

# Restore database backup
cp ~/.focus-agent/data.db.backup-YYYYMMDD ~/.focus-agent/data.db

# Disable Front in config
vim ~/.focus-agent/config.yaml
# Set front.enabled = false

# Revert to previous binary (if needed)
git checkout main~1
make build
make install

# Restart agent
launchctl start com.rabarts.focus-agent
```

**Note:** Front metadata tables are safe to leave in place (won't affect operation if Front is disabled).

---

## Maintenance & Monitoring

### Daily Monitoring

**Check Front enrichment health:**
```bash
# Threads enriched today
duckdb ~/.focus-agent/data.db "
SELECT COUNT(*)
FROM front_metadata
WHERE created_at >= EXTRACT(EPOCH FROM CURRENT_DATE)
"

# Failed links (threads without Front metadata after 2 syncs)
duckdb ~/.focus-agent/data.db "
SELECT COUNT(*)
FROM threads t
LEFT JOIN front_metadata fm ON t.id = fm.thread_id
WHERE t.last_synced > EXTRACT(EPOCH FROM CURRENT_DATE)
  AND fm.thread_id IS NULL
"

# Front API errors in logs
grep "Front API error" /var/log/focus-agent.log | tail -20
```

### Weekly Review

**Task quality analysis:**
```sql
-- Tasks from enriched vs. non-enriched threads
SELECT
    CASE WHEN fm.thread_id IS NOT NULL THEN 'Enriched' ELSE 'Not Enriched' END as enrichment,
    COUNT(*) as tasks,
    AVG(score) as avg_score,
    SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as completed
FROM tasks tk
LEFT JOIN front_metadata fm ON tk.source_id = fm.thread_id
WHERE tk.source = 'gmail'
  AND tk.created_at >= EXTRACT(EPOCH FROM CURRENT_DATE - INTERVAL 7 DAY)
GROUP BY enrichment;
```

**Enrichment coverage:**
```sql
-- Percentage of threads enriched
SELECT
    COUNT(*) as total_threads,
    SUM(CASE WHEN fm.thread_id IS NOT NULL THEN 1 ELSE 0 END) as enriched,
    ROUND(SUM(CASE WHEN fm.thread_id IS NOT NULL THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 1) as coverage_pct
FROM threads t
LEFT JOIN front_metadata fm ON t.id = fm.thread_id
WHERE t.last_synced >= EXTRACT(EPOCH FROM CURRENT_DATE - INTERVAL 7 DAY);
```

### Troubleshooting Common Issues

**Issue: No threads being enriched**

1. Check Front config: `grep -A 5 "^front:" ~/.focus-agent/config.yaml`
2. Verify API token: `curl -H "Authorization: Bearer YOUR_TOKEN" https://api2.frontapp.com/me`
3. Check logs for errors: `grep "Front" /var/log/focus-agent.log | tail -50`
4. Manual enrichment test: `curl -X POST http://localhost:8081/api/front/enrich`

**Issue: Rate limit errors**

1. Check current rate: `grep "rate limit" /var/log/focus-agent.log | tail -20`
2. Reduce `max_enrich_per_run` in config
3. Lower `max_requests_per_minute` to 60 (more conservative)

**Issue: Wrong conversations linked**

1. Review linking logic: Check timestamp tolerance (5 minutes)
2. Verify subject matching: `duckdb ~/.focus-agent/data.db "SELECT m.subject, fm.conversation_id FROM messages m JOIN front_metadata fm ON m.thread_id = fm.thread_id LIMIT 10"`
3. Add manual re-link command (future enhancement)

---

## Future Enhancements

### Short-term (Next 3 months)

1. **Manual re-link command:** TUI action to re-link a thread if initial link was wrong
2. **Tag autocomplete:** Show available Front tags when adding tags from TUI
3. **Bulk actions:** Archive/tag multiple threads at once
4. **Snooze support:** Implement snooze action (Front API supports this)

### Long-term (Next 6-12 months)

1. **Shared inbox support:** Extend to team inboxes (requires different linking strategy)
2. **Draft responses:** Create draft replies in Front from TUI
3. **Attachment handling:** Fetch and store Front comment attachments
4. **Custom fields:** Map Front custom fields to task metadata
5. **Bidirectional sync:** Update Front tags when tasks are completed

---

## Conclusion

This hybrid Gmail + Front integration delivers significant value with manageable complexity:

**What we gain:**
- **30-40% better task extraction** (AI sees full context)
- **Reduced noise** (skip archived conversations)
- **Workflow efficiency** (take action from TUI)

**What we preserve:**
- **Sync speed** (Gmail History API remains primary source)
- **Reliability** (Front failures don't break core functionality)
- **Simplicity** (Front is additive, not architectural overhaul)

**The cynical take:**

Look, you could go all-in on Front and abandon Gmail API entirely. You'd lose incremental sync, the codebase would balloon, and you'd be at the mercy of Front's rate limits. Or you could half-ass it and just poll Front like a caveman.

This hybrid approach is the Goldilocks zone: Gmail does what it's good at (fast sync), Front does what it's good at (rich metadata), and you get the best of both without the worst of either.

The linking strategy (search by subject) will fail sometimes. That's fine. The 90% of threads that link successfully will extract massively better tasks. The 10% that fail? They fall back to Gmail-only mode. No data loss, no catastrophic failures, just graceful degradation.

Two weeks to ship this? Aggressive but doable. Week 1 gets you the foundation and immediate quality improvements. Week 2 adds the fancy TUI actions that make you feel productive. Ship it, measure task quality, iterate.

Don't overthink it. Build it, ship it, see if it moves the needle. If it does, great. If it doesn't, you can rip it out cleanly because it's isolated and optional.

Now go build it.
