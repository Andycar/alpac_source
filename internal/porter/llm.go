package porter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Message represents a chat message for the OpenAI-compatible API.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMClient communicates with an OpenAI-compatible API (local llama.cpp or cloud providers).
type LLMClient struct {
	BaseURL    string
	ApiKey     string
	Model      string
	Temp       float64
	Verbose    bool
	httpClient *http.Client
}

// NewLLMClient creates a new LLM client.
func NewLLMClient(baseURL, model string, temp float64) *LLMClient {
	return &LLMClient{
		BaseURL: baseURL,
		Model:   model,
		Temp:    temp,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

// NewLLMClientWithKey creates a new LLM client with API key for cloud providers.
func NewLLMClientWithKey(baseURL, apiKey, model string, temp float64) *LLMClient {
	c := NewLLMClient(baseURL, model, temp)
	c.ApiKey = apiKey
	return c
}

// apiURL builds the full API URL, handling both styles:
// "http://localhost:8080" + "/v1/chat/completions" → "http://localhost:8080/v1/chat/completions"
// "https://openrouter.ai/api/v1" + "/v1/chat/completions" → "https://openrouter.ai/api/v1/chat/completions"
func (c *LLMClient) apiURL(path string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	// If base already ends with /v1, don't prepend /v1 again.
	if strings.HasSuffix(base, "/v1") {
		return base + strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ChatCompletion sends a chat completion request and returns the generated text.
func (c *LLMClient) ChatCompletion(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	reqBody := chatRequest{
		Model:       c.Model,
		Messages:    messages,
		Temperature: c.Temp,
		MaxTokens:   maxTokens,
		Stream:      false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.apiURL("/v1/chat/completions")

	// Retry loop: up to 3 attempts on server errors.
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*5) * time.Second):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.ApiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.ApiKey)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request: %w", err)
			continue
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, string(respBody))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			return "", fmt.Errorf("unmarshal response: %w", err)
		}

		if chatResp.Error != nil {
			return "", fmt.Errorf("API error: %s", chatResp.Error.Message)
		}

		if len(chatResp.Choices) == 0 {
			return "", fmt.Errorf("no choices in response")
		}

		content := chatResp.Choices[0].Message.Content
		if c.Verbose {
			log.Printf("porter: LLM usage: prompt=%d completion=%d total=%d finish=%s",
				chatResp.Usage.PromptTokens,
				chatResp.Usage.CompletionTokens,
				chatResp.Usage.TotalTokens,
				chatResp.Choices[0].FinishReason,
			)
		}

		return content, nil
	}

	return "", fmt.Errorf("LLM request failed after retries: %w", lastErr)
}

// Ping checks if the LLM server is reachable.
func (c *LLMClient) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL("/v1/models"), nil)
	if err != nil {
		return err
	}
	if c.ApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.ApiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("LLM server not reachable at %s: %w", c.BaseURL, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LLM server returned %d", resp.StatusCode)
	}
	return nil
}
