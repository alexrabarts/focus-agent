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
	Assignee             *Assignee `json:"assignee"`
	Tags                 []Tag     `json:"tags"`
	LastMessageTimestamp int64     `json:"last_message"`
}

// Assignee represents a conversation assignee
type Assignee struct {
	ID   string `json:"id"`
	Name string `json:"first_name"`
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
	CreatedAt int64     `json:"posted_at"`
}

// Author represents a comment author
type Author struct {
	ID        string `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
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
// by searching for conversations with matching subject around the same timestamp
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
	// Skip snoozed and archived conversations
	tolerance := 5 * time.Minute
	var bestMatch *Conversation
	var minDiff time.Duration = 24 * time.Hour

	for i := range conversations {
		// Skip snoozed and archived conversations
		if conversations[i].Status == "snoozed" || conversations[i].Status == "archived" {
			continue
		}

		convTS := time.Unix(conversations[i].LastMessageTimestamp, 0)
		diff := absTimeDiff(timestamp, convTS)
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

// GetAuthorName returns the full name of a comment author
func (a *Author) GetFullName() string {
	if a.LastName != "" {
		return fmt.Sprintf("%s %s", a.FirstName, a.LastName)
	}
	return a.FirstName
}

func absTimeDiff(a, b time.Time) time.Duration {
	if a.After(b) {
		return a.Sub(b)
	}
	return b.Sub(a)
}
