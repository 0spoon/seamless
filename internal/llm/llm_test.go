package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

func TestOllamaEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/embed", r.URL.Path)
		var req ollamaEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "qwen3-embedding:8b", req.Model)
		require.Equal(t, "hello world", req.Input)
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "qwen3-embedding:8b")
	require.Equal(t, "qwen3-embedding:8b", e.Model())

	vec, err := e.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	require.Equal(t, []float32{0.1, 0.2, 0.3}, vec)
}

func TestOllamaEmbedUnavailable(t *testing.T) {
	// Point at a server that immediately closes, so the request errors.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	e := NewOllamaEmbedder(url, "m")
	_, err := e.Embed(context.Background(), "x")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestOllamaEmbedEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[]}`))
	}))
	defer srv.Close()
	_, err := NewOllamaEmbedder(srv.URL, "m").Embed(context.Background(), "x")
	require.Error(t, err)
}

func TestOpenAIEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/embeddings", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		var req openaiEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "text-embedding-3-large", req.Model)
		require.Equal(t, 3072, req.Dimensions)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5,-0.25]}]}`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("sk-test", srv.URL, "text-embedding-3-large", 3072)
	vec, err := e.Embed(context.Background(), "hi")
	require.NoError(t, err)
	require.Equal(t, []float32{0.5, -0.25}, vec)
}

// dims of 0 must omit the dimensions field entirely.
func TestOpenAIEmbedOmitsZeroDims(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		_, present := raw["dimensions"]
		require.False(t, present, "dimensions must be omitted when 0")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1.0]}]}`))
	}))
	defer srv.Close()
	_, err := NewOpenAIEmbedder("k", srv.URL, "m", 0).Embed(context.Background(), "x")
	require.NoError(t, err)
}

func TestOpenAIStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuth},
		{http.StatusForbidden, ErrAuth},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusInternalServerError, ErrUnavailable},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		}))
		e := NewOpenAIEmbedder("k", srv.URL, "m", 0)
		_, err := e.Embed(context.Background(), "x")
		require.Error(t, err)
		require.ErrorIs(t, err, tc.want, "status %d", tc.status)
		srv.Close()
	}
}

func TestNewEmbedderSelectsProvider(t *testing.T) {
	openai := config.LLM{
		Provider: config.ProviderOpenAI,
		OpenAI:   config.OpenAI{APIKey: "sk-x", EmbeddingModel: "text-embedding-3-large", EmbeddingDims: 3072},
	}
	e, err := NewEmbedder(openai)
	require.NoError(t, err)
	require.IsType(t, &OpenAIEmbedder{}, e)
	require.Equal(t, "text-embedding-3-large", e.Model())

	ollama := config.LLM{
		Provider: config.ProviderOllama,
		Ollama:   config.Ollama{EmbeddingModel: "qwen3-embedding:8b"},
	}
	e, err = NewEmbedder(ollama)
	require.NoError(t, err)
	require.IsType(t, &OllamaEmbedder{}, e)
}

func TestNewEmbedderRejects(t *testing.T) {
	// OpenAI selected but no key.
	_, err := NewEmbedder(config.LLM{Provider: config.ProviderOpenAI})
	require.Error(t, err)

	// Anthropic has no embeddings API.
	_, err = NewEmbedder(config.LLM{Provider: config.ProviderAnthropic})
	require.Error(t, err)

	// Unknown provider.
	_, err = NewEmbedder(config.LLM{Provider: "cohere"})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUnavailable)) // config error, not a runtime one
}

// A base_url that cannot address a provider must fail at construction. Caught
// here, the owner gets one loud startup warning naming the offending key and a
// doctor check that says so; missed, every request fails as an opaque transport
// error that recall cannot tell from a provider outage, silently disabling
// semantic search. The bare-host and misspelled-scheme cases are the realistic
// typos: both build a perfectly valid *http.Request and only fail inside Do.
func TestValidateBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"empty uses the default", "", true},
		{"https", "https://api.openai.com/v1", true},
		{"http loopback", "http://127.0.0.1:11434", true},
		{"bare host, no scheme", "api.openai.com/v1", false},
		{"no scheme at all", "notaurl", false},
		{"misspelled scheme", "htp://x", false},
		{"non-http scheme", "ftp://files.example.com", false},
		{"scheme but no host", "://nope", false},
		{"unparseable", "http://[::1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBaseURL("llm.openai.base_url", tc.raw)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorIs(t, err, ErrConfig)
			require.Contains(t, err.Error(), "llm.openai.base_url", "the error must name the offending config key")
		})
	}
}

// Both factories are the single construction point for every provider, so the
// base_url check belongs there next to the existing api_key check.
func TestFactoriesRejectBadBaseURL(t *testing.T) {
	openai := config.LLM{Provider: config.ProviderOpenAI, OpenAI: config.OpenAI{APIKey: "sk-x", BaseURL: "notaurl"}}
	_, err := NewEmbedder(openai)
	require.ErrorIs(t, err, ErrConfig)
	_, err = NewChatClient(openai)
	require.ErrorIs(t, err, ErrConfig)

	ollama := config.LLM{Provider: config.ProviderOllama, Ollama: config.Ollama{BaseURL: "htp://x"}}
	_, err = NewEmbedder(ollama)
	require.ErrorIs(t, err, ErrConfig)
	_, err = NewChatClient(ollama)
	require.ErrorIs(t, err, ErrConfig)

	anthropic := config.LLM{Provider: config.ProviderAnthropic, Anthropic: config.Anthropic{APIKey: "sk-a", BaseURL: "://nope"}}
	_, err = NewChatClient(anthropic)
	require.ErrorIs(t, err, ErrConfig)
}

// A request that cannot be built contacted no provider and no retry can help, so
// it must not inherit ErrUnavailable -- recall degrades on that sentinel and
// would bury the defect. Regression for F15; the factories reject this base_url
// now, so the client is built directly to reach the guard underneath.
func TestEmbedRequestBuildFailureIsConfigNotUnavailable(t *testing.T) {
	_, err := NewOpenAIEmbedder("k", "http://[::1", "m", 0).Embed(context.Background(), "x")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrConfig)
	require.False(t, errors.Is(err, ErrUnavailable), "a request that was never sent is not an outage")

	_, err = NewOllamaEmbedder("http://[::1", "m").Embed(context.Background(), "x")
	require.ErrorIs(t, err, ErrConfig)
	require.False(t, errors.Is(err, ErrUnavailable))
}
