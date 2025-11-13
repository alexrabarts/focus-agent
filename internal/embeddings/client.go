package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Client wraps the Ollama API for generating embeddings
type Client struct {
	baseURL    string
	httpClient *http.Client
	model      string
}

// NewClient creates a new embeddings client
func NewClient(baseURL string, model string) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text" // Default model (768 dimensions)
	}

	return &Client{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

// EmbeddingsRequest represents a request to generate embeddings
type EmbeddingsRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// EmbeddingsResponse represents the response from the embeddings API
type EmbeddingsResponse struct {
	Embedding []float64 `json:"embedding"`
}

// Generate generates an embedding vector for the given text
func (c *Client) Generate(ctx context.Context, text string) ([]float64, error) {
	reqBody := EmbeddingsRequest{
		Model:  c.model,
		Prompt: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(body))
	}

	var embResp EmbeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(embResp.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding received")
	}

	return embResp.Embedding, nil
}

// GenerateWithRetry generates an embedding with automatic retry on failure
func (c *Client) GenerateWithRetry(ctx context.Context, text string, maxRetries int) ([]float64, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		embedding, err := c.Generate(ctx, text)
		if err == nil {
			return embedding, nil
		}
		lastErr = err
		if i < maxRetries-1 {
			// Exponential backoff
			time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second)
		}
	}
	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// CosineSimilarity calculates the cosine similarity between two embedding vectors
// Returns a value between -1 and 1, where 1 means identical direction
func CosineSimilarity(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vectors must have same length: got %d and %d", len(a), len(b))
	}

	if len(a) == 0 {
		return 0, fmt.Errorf("vectors cannot be empty")
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)

	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("cannot compute similarity with zero vector")
	}

	return dotProduct / (normA * normB), nil
}
