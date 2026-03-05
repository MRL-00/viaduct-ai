package llm

import (
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestBuildOpenAIChatRequestReasoningModel(t *testing.T) {
	req := buildOpenAIChatRequest(
		"gpt-5.3-codex",
		[]openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "hello"}},
		nil,
		CompletionRequest{MaxTokens: 1024, Temperature: 0.2},
		false,
	)

	if req.MaxTokens != 0 {
		t.Fatalf("expected MaxTokens to be omitted for reasoning models, got %d", req.MaxTokens)
	}
	if req.MaxCompletionTokens != 1024 {
		t.Fatalf("expected MaxCompletionTokens to be set, got %d", req.MaxCompletionTokens)
	}
	if req.Temperature != 0 {
		t.Fatalf("expected Temperature to be omitted for reasoning models, got %v", req.Temperature)
	}
}

func TestBuildOpenAIChatRequestStandardModel(t *testing.T) {
	req := buildOpenAIChatRequest(
		"gpt-4o-mini",
		[]openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "hello"}},
		nil,
		CompletionRequest{MaxTokens: 1024, Temperature: 0.2},
		true,
	)

	if !req.Stream {
		t.Fatal("expected Stream to be preserved")
	}
	if req.MaxTokens != 1024 {
		t.Fatalf("expected MaxTokens to be set, got %d", req.MaxTokens)
	}
	if req.MaxCompletionTokens != 0 {
		t.Fatalf("expected MaxCompletionTokens to be omitted, got %d", req.MaxCompletionTokens)
	}
	if req.Temperature != 0.2 {
		t.Fatalf("expected Temperature to be set, got %v", req.Temperature)
	}
}
