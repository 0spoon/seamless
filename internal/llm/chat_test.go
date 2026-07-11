package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

func TestOpenAIChatComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		var req openAIChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "gpt-4o", req.Model)
		require.Len(t, req.Messages, 2)
		require.Equal(t, "system", req.Messages[0].Role)
		require.Equal(t, "user", req.Messages[1].Role)
		require.Equal(t, chatMaxTokens, req.MaxCompletionTokens)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a digest"}}]}`))
	}))
	defer srv.Close()

	c := newOpenAIChat("sk-test", srv.URL, "gpt-4o")
	require.Equal(t, "gpt-4o", c.Model())
	out, err := c.Complete(context.Background(), "be terse", "summarize this")
	require.NoError(t, err)
	require.Equal(t, "a digest", out)
}

func TestOpenAIChatStatusMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()
	_, err := newOpenAIChat("k", srv.URL, "m").Complete(context.Background(), "", "x")
	require.ErrorIs(t, err, ErrAuth)
}

func TestOllamaChatComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/chat", r.URL.Path)
		var req ollamaChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.False(t, req.Stream)
		require.Equal(t, "llama3.3:latest", req.Model)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"local digest"}}`))
	}))
	defer srv.Close()

	out, err := newOllamaChat(srv.URL, "llama3.3:latest").Complete(context.Background(), "sys", "user")
	require.NoError(t, err)
	require.Equal(t, "local digest", out)
}

func TestAnthropicChatComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "sk-ant", r.Header.Get("x-api-key"))
		require.Equal(t, anthropicVersion, r.Header.Get("anthropic-version"))
		var req anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "sys", req.System)
		require.Equal(t, chatMaxTokens, req.MaxTokens)
		require.Len(t, req.Messages, 1)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"claude digest"}]}`))
	}))
	defer srv.Close()

	c := newAnthropicChat("sk-ant", "claude-sonnet-5")
	c.baseURL = srv.URL // override the hardcoded production endpoint
	out, err := c.Complete(context.Background(), "sys", "user")
	require.NoError(t, err)
	require.Equal(t, "claude digest", out)
}

func TestNewChatClientSelectsProvider(t *testing.T) {
	c, err := NewChatClient(config.LLM{
		Provider: config.ProviderOpenAI,
		OpenAI:   config.OpenAI{APIKey: "sk-x", ChatModel: "gpt-4o"},
	})
	require.NoError(t, err)
	require.IsType(t, &openAIChat{}, c)

	c, err = NewChatClient(config.LLM{
		Provider: config.ProviderOllama,
		Ollama:   config.Ollama{ChatModel: "llama3.3:latest"},
	})
	require.NoError(t, err)
	require.IsType(t, &ollamaChat{}, c)

	c, err = NewChatClient(config.LLM{
		Provider:  config.ProviderAnthropic,
		Anthropic: config.Anthropic{APIKey: "sk-ant", ChatModel: "claude-sonnet-5"},
	})
	require.NoError(t, err)
	require.IsType(t, &anthropicChat{}, c)
}

func TestNewChatClientRejects(t *testing.T) {
	_, err := NewChatClient(config.LLM{Provider: config.ProviderOpenAI}) // no key
	require.Error(t, err)
	_, err = NewChatClient(config.LLM{Provider: config.ProviderAnthropic}) // no key
	require.Error(t, err)
	_, err = NewChatClient(config.LLM{Provider: "cohere"})
	require.Error(t, err)
}
