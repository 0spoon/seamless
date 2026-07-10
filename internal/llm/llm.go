// Package llm provides embedding clients for the configured provider. It is
// ported and trimmed from Seam v1 internal/ai: the chat completion, ChromaDB,
// and async task-queue machinery are gone; only text -> vector embedding
// remains, since P1 needs brute-force cosine search over locally stored vectors.
//
// OpenAI is the first-class provider; Ollama is the local/dev provider (no API
// cost). Anthropic has no embeddings API and is rejected by the factory.
package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/0spoon/seamless/internal/config"
)

// Provider-agnostic sentinel errors. Callers (e.g. recall) use errors.Is to
// decide whether to degrade to lexical-only search when embedding is unavailable.
var (
	// ErrUnavailable indicates the embedding backend could not be reached.
	ErrUnavailable = errors.New("embedding provider unavailable")
	// ErrAuth indicates authentication with the provider failed.
	ErrAuth = errors.New("embedding provider authentication failed")
	// ErrRateLimited indicates the provider throttled the request.
	ErrRateLimited = errors.New("embedding provider rate limited")
)

// Embedder turns text into a dense vector. Implementations are safe for
// concurrent use. Model identifies which model produced the vectors so the
// store can scope a cosine search to a single model space.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
}

// NewEmbedder builds the embedder selected by cfg.Provider. Chat-only providers
// (Anthropic) are rejected: embeddings must come from OpenAI or Ollama.
func NewEmbedder(cfg config.LLM) (Embedder, error) {
	switch cfg.Provider {
	case config.ProviderOpenAI:
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("llm.NewEmbedder: openai selected but api_key is empty")
		}
		return NewOpenAIEmbedder(cfg.OpenAI.APIKey, cfg.OpenAI.BaseURL, cfg.OpenAI.EmbeddingModel, cfg.OpenAI.EmbeddingDims), nil
	case config.ProviderOllama:
		return NewOllamaEmbedder(cfg.Ollama.BaseURL, cfg.Ollama.EmbeddingModel), nil
	case config.ProviderAnthropic:
		return nil, fmt.Errorf("llm.NewEmbedder: anthropic has no embeddings API; set llm.provider to openai or ollama")
	default:
		return nil, fmt.Errorf("llm.NewEmbedder: unknown provider %q", cfg.Provider)
	}
}

// toFloat32 narrows an API-returned float64 vector to the float32 form stored in
// SQLite. Embedding magnitudes are small; the precision loss is negligible and
// halves storage.
func toFloat32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}
