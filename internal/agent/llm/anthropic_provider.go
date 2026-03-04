package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicProvider struct {
	name         string
	defaultModel string
	httpClient   *http.Client
	apiKey       string
	baseURL      string

	// Keep an SDK client initialized to align with official dependency usage.
	sdkClient anthropic.Client
}

func NewAnthropicProvider(apiKey, defaultModel, baseURL string) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &AnthropicProvider{
		name:         "anthropic",
		defaultModel: defaultModel,
		httpClient:   &http.Client{Timeout: 60 * time.Second},
		apiKey:       apiKey,
		baseURL:      strings.TrimRight(baseURL, "/"),
		sdkClient:    anthropic.NewClient(option.WithAPIKey(apiKey)),
	}
}

func (p *AnthropicProvider) Name() string {
	return p.name
}

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}

	payload := map[string]any{
		"model":       model,
		"system":      req.SystemPrompt,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"messages":    toAnthropicMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		payload["tools"] = toAnthropicTools(req.Tools)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal anthropic request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build anthropic request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, err
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("read anthropic response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return CompletionResponse{}, fmt.Errorf("anthropic request failed (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode anthropic response: %w", err)
	}

	toolCalls := make([]ToolCall, 0)
	contentParts := make([]string, 0)
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			contentParts = append(contentParts, block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}

	return CompletionResponse{
		Content:   strings.Join(contentParts, "\n"),
		ToolCalls: toolCalls,
		Usage: Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
		StopReason: resp.StopReason,
		Model:      resp.Model,
		Provider:   p.Name(),
	}, nil
}

func (p *AnthropicProvider) CompleteStream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}

	payload := map[string]any{
		"model":       model,
		"system":      req.SystemPrompt,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"messages":    toAnthropicMessages(req.Messages),
		"stream":      true,
	}
	if len(req.Tools) > 0 {
		payload["tools"] = toAnthropicTools(req.Tools)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build anthropic stream request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		defer httpResp.Body.Close()
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic stream failed (%d): %s", httpResp.StatusCode, string(b))
	}

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}
			var event struct {
				Type  string `json:"type"`
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}
			if event.Type == "content_block_delta" && event.Delta.Text != "" {
				ch <- StreamChunk{Content: event.Delta.Text}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Err: err, Done: true}
			return
		}
		ch <- StreamChunk{Done: true}
	}()

	return ch, nil
}

func toAnthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, map[string]any{
			"role": msg.Role,
			"content": []map[string]string{
				{"type": "text", "text": msg.Content},
			},
		})
	}
	return out
}

func toAnthropicTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	return out
}

var _ Provider = (*AnthropicProvider)(nil)
