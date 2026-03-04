package llm

import "context"

type Provider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	CompleteStream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error)
}

type CompletionRequest struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
	MaxTokens    int
	Temperature  float64
	Model        string
	TaskType     string
}

type CompletionResponse struct {
	Content    string
	ToolCalls  []ToolCall
	Usage      Usage
	StopReason string
	Model      string
	Provider   string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type StreamChunk struct {
	Content  string
	ToolCall *ToolCall
	Usage    *Usage
	Done     bool
	Err      error
}
