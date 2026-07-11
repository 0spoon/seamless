package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

// chatTimeout bounds a single completion. Digests are short; a minute is ample.
const chatTimeout = 60 * time.Second

// chatMaxTokens caps a completion's length. Gardener digests are only a few
// paragraphs, but on OpenAI reasoning models (gpt-5 / o-series) this budget is
// shared with the model's internal reasoning tokens, so too low a ceiling can
// be spent entirely on reasoning and return empty visible text. The cap only
// bounds the worst case -- billing is for tokens actually generated -- so we
// keep enough headroom for reasoning plus a short digest.
const chatMaxTokens = 4096

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicVersion is the required API version header for the Messages API.
const anthropicVersion = "2023-06-01"

// Chat turns a system + user prompt into a single text completion. It backs the
// gardener's session digests; it is intentionally minimal (no streaming, no tool
// use). Implementations are safe for concurrent use.
type Chat interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Model() string
}

// NewChatClient builds the chat client selected by cfg.Provider. Unlike embeddings,
// every provider offers chat, so all three are supported.
func NewChatClient(cfg config.LLM) (Chat, error) {
	switch cfg.Provider {
	case config.ProviderOpenAI:
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("llm.NewChatClient: openai selected but api_key is empty")
		}
		return newOpenAIChat(cfg.OpenAI.APIKey, cfg.OpenAI.BaseURL, cfg.OpenAI.ChatModel), nil
	case config.ProviderOllama:
		return newOllamaChat(cfg.Ollama.BaseURL, cfg.Ollama.ChatModel), nil
	case config.ProviderAnthropic:
		if cfg.Anthropic.APIKey == "" {
			return nil, fmt.Errorf("llm.NewChatClient: anthropic selected but api_key is empty")
		}
		return newAnthropicChat(cfg.Anthropic.APIKey, cfg.Anthropic.ChatModel), nil
	default:
		return nil, fmt.Errorf("llm.NewChatClient: unknown provider %q", cfg.Provider)
	}
}

// ---------------------------------------------------------------------------
// OpenAI (POST {baseURL}/chat/completions)
// ---------------------------------------------------------------------------

type openAIChat struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newOpenAIChat(apiKey, baseURL, model string) *openAIChat {
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return &openAIChat{baseURL: baseURL, apiKey: apiKey, model: model, client: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *openAIChat) Model() string { return c.model }

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	// max_completion_tokens is the modern field; max_tokens is deprecated on the
	// Chat Completions API and is rejected outright by reasoning models.
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
}

func (c *openAIChat) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	var msgs []openAIChatMessage
	if system != "" {
		msgs = append(msgs, openAIChatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, openAIChatMessage{Role: "user", Content: user})

	body, err := json.Marshal(openAIChatRequest{Model: c.model, Messages: msgs, MaxCompletionTokens: chatMaxTokens})
	if err != nil {
		return "", fmt.Errorf("llm.OpenAI.Complete: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm.OpenAI.Complete: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm.OpenAI.Complete: %w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkOpenAIStatus(resp); err != nil {
		return "", fmt.Errorf("llm.OpenAI.Complete: %w", err)
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm.OpenAI.Complete: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm.OpenAI.Complete: empty completion")
	}
	return out.Choices[0].Message.Content, nil
}

var _ Chat = (*openAIChat)(nil)

// ---------------------------------------------------------------------------
// Ollama (POST {baseURL}/api/chat)
// ---------------------------------------------------------------------------

type ollamaChat struct {
	baseURL string
	model   string
	client  *http.Client
}

func newOllamaChat(baseURL, model string) *ollamaChat {
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	return &ollamaChat{baseURL: baseURL, model: model, client: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *ollamaChat) Model() string { return c.model }

type ollamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
}

type ollamaChatResponse struct {
	Message openAIChatMessage `json:"message"`
}

func (c *ollamaChat) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	var msgs []openAIChatMessage
	if system != "" {
		msgs = append(msgs, openAIChatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, openAIChatMessage{Role: "user", Content: user})

	body, err := json.Marshal(ollamaChatRequest{Model: c.model, Messages: msgs, Stream: false})
	if err != nil {
		return "", fmt.Errorf("llm.Ollama.Complete: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm.Ollama.Complete: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm.Ollama.Complete: %w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm.Ollama.Complete: %w: status %d", ErrUnavailable, resp.StatusCode)
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm.Ollama.Complete: decode: %w", err)
	}
	return out.Message.Content, nil
}

var _ Chat = (*ollamaChat)(nil)

// ---------------------------------------------------------------------------
// Anthropic (POST {baseURL}/v1/messages)
// ---------------------------------------------------------------------------

type anthropicChat struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newAnthropicChat(apiKey, model string) *anthropicChat {
	return &anthropicChat{baseURL: defaultAnthropicBaseURL, apiKey: apiKey, model: model, client: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *anthropicChat) Model() string { return c.model }

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []openAIChatMessage `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (c *anthropicChat) Complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	body, err := json.Marshal(anthropicRequest{
		Model: c.model, MaxTokens: chatMaxTokens, System: system,
		Messages: []openAIChatMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", fmt.Errorf("llm.Anthropic.Complete: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm.Anthropic.Complete: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm.Anthropic.Complete: %w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkOpenAIStatus(resp); err != nil { // status-code mapping is provider-agnostic
		return "", fmt.Errorf("llm.Anthropic.Complete: %w", err)
	}

	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm.Anthropic.Complete: decode: %w", err)
	}
	for _, block := range out.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("llm.Anthropic.Complete: empty completion")
}

var _ Chat = (*anthropicChat)(nil)
