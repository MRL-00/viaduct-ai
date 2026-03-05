package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type OpenAIProvider struct {
	name         string
	client       *openai.Client
	defaultModel string
}

func NewOpenAIProvider(apiKey, defaultModel, baseURL string) *OpenAIProvider {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &OpenAIProvider{
		name:         "openai",
		client:       openai.NewClientWithConfig(cfg),
		defaultModel: defaultModel,
	}
}

func (p *OpenAIProvider) Name() string {
	return p.name
}

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = openai.GPT4oMini
	}

	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: req.SystemPrompt})
	}
	for _, msg := range req.Messages {
		messages = append(messages, openai.ChatCompletionMessage{Role: msg.Role, Content: msg.Content})
	}

	tools := make([]openai.Tool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}

	chatReq := buildOpenAIChatRequest(model, messages, tools, req, false)

	resp, err := p.client.CreateChatCompletion(ctx, chatReq)
	if err != nil {
		return CompletionResponse{}, err
	}
	if len(resp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("openai returned no choices")
	}

	choice := resp.Choices[0]
	toolCalls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return CompletionResponse{}, fmt.Errorf("decode openai tool args for %s: %w", tc.Function.Name, err)
			}
		}
		toolCalls = append(toolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}

	return CompletionResponse{
		Content:   choice.Message.Content,
		ToolCalls: toolCalls,
		Usage: Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
		StopReason: string(choice.FinishReason),
		Model:      resp.Model,
		Provider:   p.Name(),
	}, nil
}

func (p *OpenAIProvider) CompleteStream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = openai.GPT4oMini
	}

	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: req.SystemPrompt})
	}
	for _, msg := range req.Messages {
		messages = append(messages, openai.ChatCompletionMessage{Role: msg.Role, Content: msg.Content})
	}

	tools := make([]openai.Tool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}

	chatReq := buildOpenAIChatRequest(model, messages, tools, req, true)

	stream, err := p.client.CreateChatCompletionStream(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer stream.Close()
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					ch <- StreamChunk{Done: true}
					return
				}
				ch <- StreamChunk{Err: err, Done: true}
				return
			}
			for _, choice := range resp.Choices {
				if choice.Delta.Content != "" {
					ch <- StreamChunk{Content: choice.Delta.Content}
				}
			}
		}
	}()
	return ch, nil
}

func buildOpenAIChatRequest(
	model string,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
	req CompletionRequest,
	stream bool,
) openai.ChatCompletionRequest {
	chatReq := openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Stream:   stream,
	}

	if isReasoningModel(model) {
		if req.MaxTokens > 0 {
			chatReq.MaxCompletionTokens = req.MaxTokens
		}
		if req.Temperature == 1 {
			chatReq.Temperature = 1
		}
		return chatReq
	}

	if req.MaxTokens > 0 {
		chatReq.MaxTokens = req.MaxTokens
	}
	chatReq.Temperature = float32(req.Temperature)
	return chatReq
}

func isReasoningModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	return strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4") ||
		strings.HasPrefix(model, "gpt-5")
}

var _ Provider = (*OpenAIProvider)(nil)
