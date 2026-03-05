package llm

import (
	"strings"
	"testing"
)

func TestBuildOpenAICodexRequestBody(t *testing.T) {
	body, err := buildOpenAICodexRequestBody("gpt-5.3-codex", CompletionRequest{
		SystemPrompt: "system",
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
		},
		Tools: []Tool{{
			Name:        "slack_list",
			Description: "List Slack channels",
			Parameters:  map[string]any{"type": "object"},
		}},
		MaxTokens:   321,
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("buildOpenAICodexRequestBody() error = %v", err)
	}

	if got := body["model"]; got != "gpt-5.3-codex" {
		t.Fatalf("expected model to be set, got %v", got)
	}
	if got := body["instructions"]; got != "system" {
		t.Fatalf("expected instructions to be set, got %v", got)
	}
	input, ok := body["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected 2 input messages, got %#v", body["input"])
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("expected max_output_tokens to be omitted for codex backend, got %#v", body["max_output_tokens"])
	}
	if _, ok := body["temperature"]; ok {
		t.Fatalf("expected temperature to be omitted for codex backend, got %#v", body["temperature"])
	}
	tools, ok := body["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %#v", body["tools"])
	}
}

func TestParseOpenAICodexStream(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"fc_1","name":"slack_list","arguments":"{\"limit\":5}"}}`,
		"",
		`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"done"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":3}}}`,
		"",
	}, "\n"))

	resp, err := parseOpenAICodexStream(stream, "gpt-5.3-codex", "openai")
	if err != nil {
		t.Fatalf("parseOpenAICodexStream() error = %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("expected content to be parsed, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %#v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "slack_list" {
		t.Fatalf("expected tool call name, got %#v", resp.ToolCalls[0])
	}
	if got := resp.ToolCalls[0].Arguments["limit"]; got != float64(5) {
		t.Fatalf("expected parsed tool args, got %#v", resp.ToolCalls[0].Arguments)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("expected usage to be parsed, got %#v", resp.Usage)
	}
	if resp.StopReason != "toolUse" {
		t.Fatalf("expected toolUse stop reason, got %q", resp.StopReason)
	}
}
