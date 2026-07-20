package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAIEmbedder calls POST {baseURL}/embeddings. It is compatible with OpenAI,
// Azure OpenAI, and any OpenAI-compatible endpoint. Ported from Seam v1
// internal/ai/openai_embedder.go.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dims    int // 0 = omit dimensions (model's native size)
	client  retryClient
}

// NewOpenAIEmbedder returns an OpenAI embeddings client. An empty baseURL uses
// the default endpoint. dims of 0 omits the dimensions field.
func NewOpenAIEmbedder(apiKey, baseURL, model string, dims int) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return &OpenAIEmbedder{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		dims:    dims,
		client:  newRetryClient(2 * time.Minute),
	}
}

// Model returns the embedding model name.
func (c *OpenAIEmbedder) Model() string { return c.model }

type openaiEmbedRequest struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type openaiEmbedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for text.
func (c *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	body, err := json.Marshal(openaiEmbedRequest{Input: text, Model: c.model, Dimensions: c.dims})
	if err != nil {
		return nil, fmt.Errorf("llm.OpenAI.Embed: marshal: %w", err)
	}
	resp, err := c.client.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		return req, nil
	})
	if err != nil {
		return nil, doErr("llm.OpenAI.Embed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := checkOpenAIStatus(resp); err != nil {
		return nil, fmt.Errorf("llm.OpenAI.Embed: %w", err)
	}

	var out openaiEmbedResponse
	if err := decodeJSONResponse(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("llm.OpenAI.Embed: decode: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("llm.OpenAI.Embed: empty embedding")
	}
	return toFloat32(out.Data[0].Embedding), nil
}

// checkOpenAIStatus maps non-2xx responses to sentinel errors so callers can
// distinguish auth/rate-limit/unavailable conditions.
func checkOpenAIStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Best-effort detail only: the sentinel below comes from the status code,
	// so a failed read costs the message a snippet, not its meaning.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: status %d", ErrAuth, resp.StatusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: status %d", ErrRateLimited, resp.StatusCode)
	default:
		return fmt.Errorf("%w: status %d: %s", ErrUnavailable, resp.StatusCode, string(snippet))
	}
}

var _ Embedder = (*OpenAIEmbedder)(nil)
