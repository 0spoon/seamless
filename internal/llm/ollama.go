package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultOllamaBaseURL = "http://127.0.0.1:11434"
	embedTimeout         = 60 * time.Second
)

// OllamaEmbedder calls a local Ollama server's POST /api/embed. Ported from Seam
// v1 internal/ai/ollama.go (embedding path only).
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  retryClient
}

// NewOllamaEmbedder returns an embedder for a local Ollama model. An empty
// baseURL uses the default localhost endpoint.
func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		client:  newRetryClient(2 * time.Minute),
	}
}

// Model returns the embedding model name.
func (c *OllamaEmbedder) Model() string { return c.model }

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// Embed returns the embedding vector for text.
func (c *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	body, err := json.Marshal(ollamaEmbedRequest{Model: c.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("llm.Ollama.Embed: marshal: %w", err)
	}
	resp, err := c.client.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/embed", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, doErr("llm.Ollama.Embed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm.Ollama.Embed: %w: status %d", ErrUnavailable, resp.StatusCode)
	}

	var out ollamaEmbedResponse
	if err := decodeJSONResponse(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("llm.Ollama.Embed: decode: %w", err)
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("llm.Ollama.Embed: empty embedding")
	}
	return toFloat32(out.Embeddings[0]), nil
}

var _ Embedder = (*OllamaEmbedder)(nil)
