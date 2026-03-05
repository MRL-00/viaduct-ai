package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultOpenAICodexBaseURL = "https://chatgpt.com/backend-api"
	openAICodexJWTClaimPath   = "https://api.openai.com/auth"
)

type OpenAICodexProvider struct {
	name         string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
	tokenSource  *oauthTokenSource
}

func NewOpenAICodexOAuthProvider(name, defaultModel, baseURL string, oauthCfg OAuthClientCredentialsConfig) *OpenAICodexProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAICodexBaseURL
	}
	return &OpenAICodexProvider{
		name:         name,
		baseURL:      strings.TrimRight(baseURL, "/"),
		defaultModel: defaultModel,
		httpClient:   &http.Client{Timeout: 0},
		tokenSource: &oauthTokenSource{
			httpClient: &http.Client{Timeout: 30 * time.Second},
			cfg:        oauthCfg,
			token:      strings.TrimSpace(oauthCfg.AccessToken),
			refreshToken: strings.TrimSpace(
				oauthCfg.RefreshToken,
			),
			expiresAt: time.Unix(oauthCfg.ExpiresAt, 0),
		},
	}
}

func (p *OpenAICodexProvider) Name() string {
	return p.name
}

func (p *OpenAICodexProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	token, err := p.tokenSource.Token(ctx)
	if err != nil {
		return CompletionResponse{}, err
	}
	accountID, err := extractOpenAICodexAccountID(token)
	if err != nil {
		return CompletionResponse{}, err
	}

	model := req.Model
	if strings.TrimSpace(model) == "" {
		model = p.defaultModel
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-5.3-codex"
	}

	body, err := buildOpenAICodexRequestBody(model, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal openai codex request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOpenAICodexURL(p.baseURL), bytes.NewReader(payload))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build openai codex request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("chatgpt-account-id", accountID)
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
	httpReq.Header.Set("originator", "pi")
	httpReq.Header.Set("User-Agent", "viaduct")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("openai codex request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var bodyText bytes.Buffer
		_, _ = bodyText.ReadFrom(resp.Body)
		return CompletionResponse{}, fmt.Errorf("openai codex request failed (%d): %s", resp.StatusCode, strings.TrimSpace(bodyText.String()))
	}

	return parseOpenAICodexStream(resp.Body, model, p.name)
}

func (p *OpenAICodexProvider) CompleteStream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 2)
	go func() {
		defer close(ch)
		resp, err := p.Complete(ctx, req)
		if err != nil {
			ch <- StreamChunk{Err: err, Done: true}
			return
		}
		if resp.Content != "" {
			ch <- StreamChunk{Content: resp.Content}
		}
		ch <- StreamChunk{Done: true, Usage: &resp.Usage}
	}()
	return ch, nil
}

func resolveOpenAICodexURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" || strings.Contains(base, "api.openai.com/v1") {
		base = defaultOpenAICodexBaseURL
	}
	if strings.HasSuffix(base, "/codex/responses") {
		return base
	}
	if strings.HasSuffix(base, "/codex") {
		return base + "/responses"
	}
	return base + "/codex/responses"
}

func extractOpenAICodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid openai codex access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode openai codex access token: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse openai codex access token: %w", err)
	}
	authClaim, ok := claims[openAICodexJWTClaimPath].(map[string]any)
	if !ok {
		return "", fmt.Errorf("openai codex access token missing auth claim")
	}
	accountID, _ := authClaim["chatgpt_account_id"].(string)
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", fmt.Errorf("openai codex access token missing chatgpt account id")
	}
	return accountID, nil
}

func buildOpenAICodexRequestBody(model string, req CompletionRequest) (map[string]any, error) {
	input := make([]map[string]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		switch msg.Role {
		case "assistant":
			input = append(input, map[string]any{
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        msg.Content,
					"annotations": []any{},
				}},
			})
		default:
			input = append(input, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type": "input_text",
					"text": msg.Content,
				}},
			})
		}
	}

	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}

	body := map[string]any{
		"model":               model,
		"store":               false,
		"stream":              true,
		"instructions":        req.SystemPrompt,
		"input":               input,
		"text":                map[string]any{"verbosity": "medium"},
		"include":             []string{"reasoning.encrypted_content"},
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return body, nil
}

func parseOpenAICodexStream(readerBody io.Reader, model, provider string) (CompletionResponse, error) {
	scanner := bufio.NewScanner(readerBody)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var dataLines []string
	resp := CompletionResponse{Model: model, Provider: provider}
	content := strings.Builder{}
	toolCalls := []ToolCall{}
	usage := Usage{}

	handleEvent := func(event map[string]any) error {
		typ, _ := event["type"].(string)
		switch typ {
		case "error":
			message, _ := event["message"].(string)
			code, _ := event["code"].(string)
			if strings.TrimSpace(message) == "" {
				message = code
			}
			return fmt.Errorf("codex error: %s", strings.TrimSpace(message))
		case "response.failed":
			if responseMap, ok := event["response"].(map[string]any); ok {
				if errMap, ok := responseMap["error"].(map[string]any); ok {
					if message, ok := errMap["message"].(string); ok && strings.TrimSpace(message) != "" {
						return fmt.Errorf("codex response failed: %s", strings.TrimSpace(message))
					}
				}
			}
			return fmt.Errorf("codex response failed")
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			switch itemType {
			case "message":
				if parts, ok := item["content"].([]any); ok {
					for _, raw := range parts {
						part, _ := raw.(map[string]any)
						partType, _ := part["type"].(string)
						switch partType {
						case "output_text":
							if text, ok := part["text"].(string); ok {
								content.WriteString(text)
							}
						case "refusal":
							if text, ok := part["refusal"].(string); ok {
								content.WriteString(text)
							}
						}
					}
				}
			case "function_call":
				args := map[string]any{}
				if argText, ok := item["arguments"].(string); ok && strings.TrimSpace(argText) != "" {
					if err := json.Unmarshal([]byte(argText), &args); err != nil {
						return fmt.Errorf("decode codex tool args for %v: %w", item["name"], err)
					}
				}
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				toolCalls = append(toolCalls, ToolCall{
					ID:        callID,
					Name:      name,
					Arguments: args,
				})
			}
		case "response.completed", "response.done":
			responseMap, _ := event["response"].(map[string]any)
			if usageMap, ok := responseMap["usage"].(map[string]any); ok {
				usage.InputTokens = int(numberValue(usageMap["input_tokens"]))
				usage.OutputTokens = int(numberValue(usageMap["output_tokens"]))
			}
			if status, ok := responseMap["status"].(string); ok {
				resp.StopReason = mapOpenAICodexStopReason(status, len(toolCalls) > 0)
			}
		}
		return nil
	}

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil
		}
		return handleEvent(event)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return CompletionResponse{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return CompletionResponse{}, fmt.Errorf("read codex stream: %w", err)
	}
	if err := flush(); err != nil {
		return CompletionResponse{}, err
	}

	resp.Content = strings.TrimSpace(content.String())
	resp.ToolCalls = toolCalls
	resp.Usage = usage
	if resp.StopReason == "" {
		resp.StopReason = mapOpenAICodexStopReason("completed", len(toolCalls) > 0)
	}
	return resp, nil
}

func mapOpenAICodexStopReason(status string, hasToolCalls bool) string {
	switch status {
	case "incomplete":
		return "length"
	case "failed", "cancelled":
		return "error"
	default:
		if hasToolCalls {
			return "toolUse"
		}
		return "stop"
	}
}

func numberValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

var _ Provider = (*OpenAICodexProvider)(nil)
