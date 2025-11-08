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

// Snooze snoozes a conversation until a specific time
func (c *Client) Snooze(ctx context.Context, convID string, until time.Time) error {
	url := fmt.Sprintf("%s/conversations/%s", baseURL, convID)

	payload := map[string]interface{}{
		"status":     "snoozed",
		"snooze_until": until.Unix(),
	}

	return c.updateConversation(ctx, url, payload)
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
