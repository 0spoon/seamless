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
	"net/url"

	"github.com/0spoon/seamless/internal/config"
)

// Provider-agnostic sentinel errors. Callers (e.g. recall) use errors.Is to
// decide whether to degrade to lexical-only search when embedding is unavailable.
//
// The split that matters is remote vs local. ErrUnavailable, ErrAuth, and
// ErrRateLimited all describe a provider that answered badly or not at all:
// nothing in this process is wrong, so degrading to lexical-only search is
// honest and the condition may clear on its own. ErrConfig describes a client
// that could not even form the request -- degrading on that would hide a local
// defect behind quietly worse results for as long as the daemon runs.
var (
	// ErrUnavailable indicates the embedding backend could not be reached.
	ErrUnavailable = errors.New("embedding provider unavailable")
	// ErrAuth indicates authentication with the provider failed.
	ErrAuth = errors.New("embedding provider authentication failed")
	// ErrRateLimited indicates the provider throttled the request.
	ErrRateLimited = errors.New("embedding provider rate limited")
	// ErrConfig indicates the client itself is misconfigured: the request could
	// not be built at all, so no provider was contacted and no retry can help.
	// The factories below reject a bad base_url up front, so a client that
	// exists should never produce this at request time.
	ErrConfig = errors.New("llm client misconfigured")
)

// validateBaseURL rejects a configured base URL that cannot address a provider,
// so a typo fails loudly at construction instead of at every request.
//
// This is the only check that catches the realistic mistakes. A bare host or a
// misspelled scheme ("api.openai.com/v1", "notaurl", "htp://x") parses happily
// and builds a valid *http.Request; it only fails later inside Do as an opaque
// transport error, indistinguishable from a provider genuinely being down --
// which is exactly how a config typo used to disable semantic search silently.
// An empty value is left alone: each constructor substitutes its own default.
func validateBaseURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s is not a valid URL: %w", ErrConfig, field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must be an http(s) URL, got %q", ErrConfig, field, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %s has no host, got %q", ErrConfig, field, raw)
	}
	return nil
}

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
		if err := validateBaseURL("llm.openai.base_url", cfg.OpenAI.BaseURL); err != nil {
			return nil, fmt.Errorf("llm.NewEmbedder: %w", err)
		}
		return NewOpenAIEmbedder(cfg.OpenAI.APIKey, cfg.OpenAI.BaseURL, cfg.OpenAI.EmbeddingModel, cfg.OpenAI.EmbeddingDims), nil
	case config.ProviderOllama:
		if err := validateBaseURL("llm.ollama.base_url", cfg.Ollama.BaseURL); err != nil {
			return nil, fmt.Errorf("llm.NewEmbedder: %w", err)
		}
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
