package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/agent"
	"github.com/MRL-00/viaduct-ai/internal/connector"
)

const (
	chatTaskTimeout     = 2 * time.Minute
	chatReplyMaxRunes   = 3000
	chatDefaultReplyMsg = "Request completed."
)

var slackMentionPattern = regexp.MustCompile(`<@[A-Z0-9]+>`)

func (a *app) startMessageBridges(ctx context.Context) {
	for _, c := range a.registry.List() {
		messenger, ok := c.(connector.Messenger)
		if !ok {
			continue
		}
		name := c.Name()
		if !a.chatBridgeEnabled(name) {
			continue
		}
		go a.runMessageBridge(ctx, name, messenger)
	}
}

func (a *app) chatBridgeEnabled(connectorName string) bool {
	cfg, ok := a.cfg.Connectors[connectorName]
	if !ok {
		return false
	}
	if !hasAnyPermission(cfg.Permissions, "write", "admin") {
		a.logger.Info("chat bridge skipped because connector permission is not write/admin",
			"connector", connectorName)
		return false
	}
	if connectorName == "slack" {
		if strings.TrimSpace(fmt.Sprint(cfg.Config["app_token"])) == "" {
			a.logger.Info("chat bridge skipped because slack app_token is not configured",
				"connector", connectorName)
			return false
		}
	}
	return true
}

func hasAnyPermission(permissions []string, needed ...string) bool {
	for _, permission := range permissions {
		for _, candidate := range needed {
			if strings.EqualFold(permission, candidate) {
				return true
			}
		}
	}
	return false
}

func (a *app) runMessageBridge(ctx context.Context, connectorName string, messenger connector.Messenger) {
	a.logger.Info("message bridge started", "connector", connectorName)
	err := messenger.Listen(ctx, func(handlerCtx context.Context, message connector.Message) error {
		go a.handleInboundMessage(handlerCtx, connectorName, messenger, message)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		a.logger.Error("message bridge stopped with error", "connector", connectorName, "error", err)
		return
	}
	a.logger.Info("message bridge stopped", "connector", connectorName)
}

func (a *app) handleInboundMessage(
	ctx context.Context,
	connectorName string,
	messenger connector.Messenger,
	message connector.Message,
) {
	goal := normalizeInboundGoal(connectorName, message.Content)
	if goal == "" {
		a.replyToMessage(ctx, connectorName, messenger, message, "Tell me what you need me to do.")
		return
	}

	taskCtx, cancel := context.WithTimeout(ctx, chatTaskTimeout)
	defer cancel()

	result, err := a.agent.Execute(taskCtx, agent.TaskRequest{
		Goal: goal,
		Context: map[string]any{
			"source_connector": connectorName,
			"channel":          message.Channel,
			"thread_id":        message.ThreadID,
			"user":             message.User,
			"metadata":         message.Metadata,
		},
		TaskType:      "analysis",
		TriggerSource: connectorName + "_chat",
		TriggerRef:    message.ID,
	})
	if err != nil {
		a.logger.Error("chat request failed",
			"connector", connectorName,
			"channel", message.Channel,
			"thread_id", message.ThreadID,
			"error", err)
		a.replyToMessage(ctx, connectorName, messenger, message,
			fmt.Sprintf("I could not complete that request: %v", err))
		return
	}

	reply := strings.TrimSpace(result.Response)
	if reply == "" {
		reply = chatDefaultReplyMsg
	}
	a.replyToMessage(ctx, connectorName, messenger, message, reply)
}

func (a *app) replyToMessage(
	ctx context.Context,
	connectorName string,
	messenger connector.Messenger,
	incoming connector.Message,
	reply string,
) {
	reply = truncateRunes(strings.TrimSpace(reply), chatReplyMaxRunes)
	if reply == "" {
		reply = chatDefaultReplyMsg
	}

	err := messenger.Send(ctx, incoming.Channel, connector.Message{
		Channel:  incoming.Channel,
		ThreadID: incoming.ThreadID,
		Content:  reply,
		Metadata: map[string]any{
			"source":      "chat_bridge",
			"connector":   connectorName,
			"in_reply_to": incoming.ID,
		},
	})
	if err != nil {
		a.logger.Error("failed to send chat reply",
			"connector", connectorName,
			"channel", incoming.Channel,
			"thread_id", incoming.ThreadID,
			"error", err)
	}
}

func normalizeInboundGoal(connectorName, content string) string {
	goal := strings.TrimSpace(content)
	if connectorName == "slack" {
		goal = slackMentionPattern.ReplaceAllString(goal, " ")
	}
	goal = strings.Join(strings.Fields(goal), " ")
	return goal
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
