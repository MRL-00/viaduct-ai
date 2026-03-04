package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/agent/llm"
	"github.com/MRL-00/viaduct-ai/internal/config"
	"github.com/MRL-00/viaduct-ai/internal/connector"
	"github.com/MRL-00/viaduct-ai/internal/security"
	"github.com/MRL-00/viaduct-ai/internal/storage"
)

type TaskRequest struct {
	Goal          string
	Context       map[string]any
	TaskType      string
	TriggerSource string
	TriggerRef    string
}

type TaskResult struct {
	Response        string
	Iterations      int
	TotalCostUSD    float64
	Usage           llm.Usage
	ToolInvocations int
}

type Agent struct {
	logger            *slog.Logger
	router            *llm.Router
	registry          *connector.Registry
	permissionChecker *security.PermissionChecker
	auditRepo         *storage.AuditLogRepository
	llmUsageRepo      *storage.LLMUsageRepository
	pricing           map[string]config.PricingConfig
	maxIterations     int
}

func New(logger *slog.Logger, router *llm.Router, registry *connector.Registry,
	checker *security.PermissionChecker, auditRepo *storage.AuditLogRepository,
	llmUsageRepo *storage.LLMUsageRepository, pricing map[string]config.PricingConfig, maxIterations int,
) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	if maxIterations <= 0 {
		maxIterations = 10
	}
	return &Agent{
		logger:            logger,
		router:            router,
		registry:          registry,
		permissionChecker: checker,
		auditRepo:         auditRepo,
		llmUsageRepo:      llmUsageRepo,
		pricing:           pricing,
		maxIterations:     maxIterations,
	}
}

func (a *Agent) Execute(ctx context.Context, req TaskRequest) (TaskResult, error) {
	started := time.Now()
	systemPrompt := a.buildSystemPrompt()
	tools := a.buildTools()

	contextJSON, _ := json.Marshal(req.Context)
	messages := []llm.Message{{Role: "user", Content: fmt.Sprintf("Goal: %s\nContext: %s", req.Goal, string(contextJSON))}}

	totalUsage := llm.Usage{}
	finalText := ""
	iterations := 0
	toolInvocations := 0

	for i := 0; i < a.maxIterations; i++ {
		iterations = i + 1
		callStarted := time.Now()
		resp, err := a.router.Complete(ctx, llm.CompletionRequest{
			SystemPrompt: systemPrompt,
			Messages:     messages,
			Tools:        tools,
			MaxTokens:    1024,
			Temperature:  0.2,
			TaskType:     req.TaskType,
		})
		if err != nil {
			a.insertAudit(ctx, storage.AuditEntry{
				Timestamp:     time.Now().UTC(),
				TriggerSource: req.TriggerSource,
				TriggerRef:    req.TriggerRef,
				Connector:     "agent",
				Operation:     "execute",
				Result:        "error",
				DurationMS:    time.Since(started).Milliseconds(),
				Error:         err.Error(),
				Context: map[string]any{
					"goal": req.Goal,
				},
			})
			return TaskResult{}, err
		}

		cost := a.calculateCost(resp.Provider, resp.Model, resp.Usage)
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens
		totalUsage.CostUSD += cost

		a.insertUsage(ctx, storage.LLMUsage{
			Timestamp:    callStarted,
			RequestID:    fmt.Sprintf("%s-%d", req.TriggerRef, i),
			Provider:     resp.Provider,
			Model:        resp.Model,
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			CostUSD:      cost,
			DurationMS:   time.Since(callStarted).Milliseconds(),
		})

		if resp.Content != "" {
			messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
			finalText = resp.Content
		}

		if len(resp.ToolCalls) == 0 {
			a.insertAudit(ctx, storage.AuditEntry{
				Timestamp:     time.Now().UTC(),
				TriggerSource: req.TriggerSource,
				TriggerRef:    req.TriggerRef,
				Connector:     "agent",
				Operation:     "execute",
				Result:        "success",
				DurationMS:    time.Since(started).Milliseconds(),
				Context: map[string]any{
					"goal":           req.Goal,
					"iterations":     iterations,
					"tool_calls":     toolInvocations,
					"input_tokens":   totalUsage.InputTokens,
					"output_tokens":  totalUsage.OutputTokens,
					"total_cost_usd": totalUsage.CostUSD,
				},
			})
			return TaskResult{
				Response:        finalText,
				Iterations:      iterations,
				TotalCostUSD:    totalUsage.CostUSD,
				Usage:           totalUsage,
				ToolInvocations: toolInvocations,
			}, nil
		}

		for _, toolCall := range resp.ToolCalls {
			toolInvocations++
			result, err := a.executeToolCall(ctx, toolCall)
			if err != nil {
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: fmt.Sprintf("Tool %s failed: %v", toolCall.Name, err),
				})
				continue
			}
			resultJSON, _ := json.Marshal(result)
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: fmt.Sprintf("Tool %s result: %s", toolCall.Name, string(resultJSON)),
			})
		}
	}

	err := fmt.Errorf("max iteration limit (%d) reached", a.maxIterations)
	a.insertAudit(ctx, storage.AuditEntry{
		Timestamp:     time.Now().UTC(),
		TriggerSource: req.TriggerSource,
		TriggerRef:    req.TriggerRef,
		Connector:     "agent",
		Operation:     "execute",
		Result:        "error",
		DurationMS:    time.Since(started).Milliseconds(),
		Error:         err.Error(),
		Context: map[string]any{
			"goal": req.Goal,
		},
	})
	return TaskResult{}, err
}

func (a *Agent) buildSystemPrompt() string {
	descriptors := a.registry.Descriptors()
	if len(descriptors) == 0 {
		return "You are Viaduct, an enterprise operations agent. No connectors are currently available."
	}
	lines := []string{
		"You are Viaduct, an enterprise operations agent.",
		"Available connectors and capabilities:",
	}
	for _, d := range descriptors {
		caps := make([]string, 0, len(d.Capabilities))
		for _, c := range d.Capabilities {
			caps = append(caps, string(c))
		}
		sort.Strings(caps)
		lines = append(lines, fmt.Sprintf("- %s: %s (capabilities: %s)", d.Name,
			d.Description, strings.Join(caps, ", ")))
	}
	lines = append(lines,
		"Use tools only when necessary.",
		"Respect read-only permissions.",
		"When tool output is insufficient, explain what additional data is required.",
	)
	return strings.Join(lines, "\n")
}

func (a *Agent) buildTools() []llm.Tool {
	descriptors := a.registry.Descriptors()
	tools := make([]llm.Tool, 0, len(descriptors)*5)
	for _, d := range descriptors {
		capSet := map[connector.Capability]bool{}
		for _, cap := range d.Capabilities {
			capSet[cap] = true
		}
		if capSet[connector.CapabilityRead] {
			tools = append(tools,
				llm.Tool{
					Name:        d.Name + "_list",
					Description: "List resources via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"filter": map[string]any{"type": "object"},
							"limit":  map[string]any{"type": "integer"},
							"offset": map[string]any{"type": "integer"},
						},
					},
				},
				llm.Tool{
					Name:        d.Name + "_read",
					Description: "Read a single resource via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string"},
						},
						"required": []string{"id"},
					},
				},
				llm.Tool{
					Name:        d.Name + "_search",
					Description: "Search resources via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
						"required": []string{"query"},
					},
				},
			)
		}
		if capSet[connector.CapabilityWrite] {
			tools = append(tools,
				llm.Tool{
					Name:        d.Name + "_create",
					Description: "Create a resource via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"resource": map[string]any{"type": "object"},
						},
						"required": []string{"resource"},
					},
				},
				llm.Tool{
					Name:        d.Name + "_update",
					Description: "Update a resource via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string"},
							"resource": map[string]any{"type": "object"},
						},
						"required": []string{"id", "resource"},
					},
				},
				llm.Tool{
					Name:        d.Name + "_delete",
					Description: "Delete a resource via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string"},
						},
						"required": []string{"id"},
					},
				},
			)
		}
		if capSet[connector.CapabilityMessenger] {
			tools = append(tools,
				llm.Tool{
					Name:        d.Name + "_send",
					Description: "Send a message via " + d.Name,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"channel":   map[string]any{"type": "string"},
							"content":   map[string]any{"type": "string"},
							"thread_id": map[string]any{"type": "string"},
						},
						"required": []string{"channel", "content"},
					},
				},
			)
		}
	}
	return tools
}

func (a *Agent) executeToolCall(ctx context.Context, toolCall llm.ToolCall) (any, error) {
	parts := strings.SplitN(toolCall.Name, "_", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid tool call %q", toolCall.Name)
	}
	connectorName := parts[0]
	action := parts[1]

	c, ok := a.registry.Get(connectorName)
	if !ok {
		return nil, fmt.Errorf("connector %q not found", connectorName)
	}

	switch action {
	case "list", "read", "search":
		if err := a.permissionChecker.Allowed(connectorName, security.OperationRead); err != nil {
			return nil, err
		}
	case "create", "update", "delete", "send":
		if err := a.permissionChecker.Allowed(connectorName, security.OperationWrite); err != nil {
			return nil, err
		}
	}

	switch action {
	case "list":
		reader, ok := c.(connector.Reader)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support list", connectorName)
		}
		query := connector.Query{}
		if v, ok := toolCall.Arguments["filter"].(map[string]any); ok {
			query.Filter = make(map[string]string, len(v))
			for k, raw := range v {
				query.Filter[k] = fmt.Sprint(raw)
			}
		}
		query.Limit = asInt(toolCall.Arguments["limit"])
		query.Offset = asInt(toolCall.Arguments["offset"])
		return reader.List(ctx, query)
	case "read":
		reader, ok := c.(connector.Reader)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support read", connectorName)
		}
		id := fmt.Sprint(toolCall.Arguments["id"])
		return reader.Read(ctx, id)
	case "search":
		reader, ok := c.(connector.Reader)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support search", connectorName)
		}
		q := fmt.Sprint(toolCall.Arguments["query"])
		return reader.Search(ctx, q)
	case "create":
		writer, ok := c.(connector.Writer)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support create", connectorName)
		}
		resource, err := asResource(toolCall.Arguments["resource"])
		if err != nil {
			return nil, err
		}
		return writer.Create(ctx, resource)
	case "update":
		writer, ok := c.(connector.Writer)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support update", connectorName)
		}
		id := fmt.Sprint(toolCall.Arguments["id"])
		resource, err := asResource(toolCall.Arguments["resource"])
		if err != nil {
			return nil, err
		}
		return "ok", writer.Update(ctx, id, resource)
	case "delete":
		writer, ok := c.(connector.Writer)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support delete", connectorName)
		}
		id := fmt.Sprint(toolCall.Arguments["id"])
		return "ok", writer.Delete(ctx, id)
	case "send":
		messenger, ok := c.(connector.Messenger)
		if !ok {
			return nil, fmt.Errorf("connector %q does not support send", connectorName)
		}
		channel := fmt.Sprint(toolCall.Arguments["channel"])
		content := fmt.Sprint(toolCall.Arguments["content"])
		threadID := fmt.Sprint(toolCall.Arguments["thread_id"])
		msg := connector.Message{Channel: channel, ThreadID: threadID, Content: content}
		return "ok", messenger.Send(ctx, channel, msg)
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

func (a *Agent) calculateCost(provider, model string, usage llm.Usage) float64 {
	if usage.CostUSD > 0 {
		return usage.CostUSD
	}
	key := provider + "/" + model
	pricing, ok := a.pricing[key]
	if !ok {
		return 0
	}
	inputCost := (float64(usage.InputTokens) / 1_000_000.0) * pricing.InputPerMillion
	outputCost := (float64(usage.OutputTokens) / 1_000_000.0) * pricing.OutputPerMillion
	return inputCost + outputCost
}

func (a *Agent) insertAudit(ctx context.Context, entry storage.AuditEntry) {
	if a.auditRepo == nil {
		return
	}
	if _, err := a.auditRepo.Insert(ctx, entry); err != nil {
		a.logger.Warn("failed to persist audit entry", "error", err)
	}
}

func (a *Agent) insertUsage(ctx context.Context, usage storage.LLMUsage) {
	if a.llmUsageRepo == nil {
		return
	}
	if _, err := a.llmUsageRepo.Insert(ctx, usage); err != nil {
		a.logger.Warn("failed to persist llm usage", "error", err)
	}
}

func asInt(v any) int {
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		var i int
		fmt.Sscanf(value, "%d", &i)
		return i
	default:
		return 0
	}
}

func asResource(v any) (connector.Resource, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return connector.Resource{}, fmt.Errorf("resource argument must be object")
	}
	resource := connector.Resource{}
	if id, ok := m["id"].(string); ok {
		resource.ID = id
	}
	if typ, ok := m["type"].(string); ok {
		resource.Type = typ
	}
	if name, ok := m["name"].(string); ok {
		resource.Name = name
	}
	if content, ok := m["content"].(string); ok {
		resource.Content = content
	}
	if metadata, ok := m["metadata"].(map[string]any); ok {
		resource.Metadata = metadata
	} else {
		resource.Metadata = map[string]any{}
	}
	return resource, nil
}
