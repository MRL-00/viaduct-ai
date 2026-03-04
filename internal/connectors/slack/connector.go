package slack

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/connector"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type Connector struct {
	botToken string
	appToken string

	client       *slackapi.Client
	socketClient *socketmode.Client

	threadMu      sync.Mutex
	threadContext map[string][]string
}

func New() *Connector {
	return &Connector{
		threadContext: make(map[string][]string),
	}
}

func (c *Connector) Name() string {
	return "slack"
}

func (c *Connector) Description() string {
	return "Reads/writes Slack messages and listens with Socket Mode"
}

func (c *Connector) Configure(cfg connector.ConnectorConfig) error {
	botToken, ok := cfg["bot_token"].(string)
	if !ok || botToken == "" {
		return fmt.Errorf("slack.bot_token is required")
	}
	appToken, _ := cfg["app_token"].(string)

	c.botToken = botToken
	c.appToken = appToken
	c.client = slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	if appToken != "" {
		c.socketClient = socketmode.New(c.client)
	}
	return nil
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.client == nil {
		return fmt.Errorf("slack connector is not configured")
	}
	_, err := c.client.AuthTestContext(ctx)
	return err
}

func (c *Connector) List(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	resourceType := query.Filter["resource"]
	if resourceType == "" {
		resourceType = "channels"
	}

	switch resourceType {
	case "channels":
		return c.listChannels(ctx, query)
	case "messages":
		return c.listMessages(ctx, query)
	default:
		return nil, fmt.Errorf("unsupported slack list resource %q", resourceType)
	}
}

func (c *Connector) Read(ctx context.Context, id string) (connector.Resource, error) {
	parts := strings.Split(id, ":")
	if len(parts) == 0 {
		return connector.Resource{}, fmt.Errorf("invalid resource id")
	}

	switch parts[0] {
	case "channel":
		if len(parts) != 2 {
			return connector.Resource{}, fmt.Errorf(
				"channel resource id should be channel:<channelID>")
		}
		channels, err := c.getChannels(ctx, 1000)
		if err != nil {
			return connector.Resource{}, err
		}
		for _, channel := range channels {
			if channel.ID == parts[1] {
				return connector.Resource{
					ID:      id,
					Type:    "slack_channel",
					Name:    channel.Name,
					Content: channel.Purpose.Value,
					Metadata: map[string]any{
						"is_private": channel.IsPrivate,
					},
				}, nil
			}
		}
		return connector.Resource{}, fmt.Errorf("channel %s not found", parts[1])
	case "message":
		if len(parts) != 3 {
			return connector.Resource{}, fmt.Errorf(
				"message resource id should be message:<channelID>:<timestamp>")
		}
		history, err := c.client.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
			ChannelID: parts[1],
			Latest:    parts[2],
			Oldest:    parts[2],
			Inclusive: true,
			Limit:     1,
		})
		if err != nil {
			return connector.Resource{}, err
		}
		if len(history.Messages) == 0 {
			return connector.Resource{}, fmt.Errorf("message %s not found", id)
		}
		msg := history.Messages[0]
		return connector.Resource{
			ID:      id,
			Type:    "slack_message",
			Name:    msg.User,
			Content: msg.Text,
			Metadata: map[string]any{
				"channel":   parts[1],
				"thread_ts": msg.ThreadTimestamp,
			},
		}, nil
	default:
		return connector.Resource{}, fmt.Errorf("unsupported slack read prefix %q", parts[0])
	}
}

func (c *Connector) Search(ctx context.Context, query string) ([]connector.Resource, error) {
	res, err := c.client.SearchMessagesContext(ctx, query, slackapi.SearchParameters{Count: 25})
	if err != nil {
		return nil, err
	}
	resources := make([]connector.Resource, 0, len(res.Matches))
	for _, match := range res.Matches {
		resources = append(resources, connector.Resource{
			ID:      fmt.Sprintf("message:%s:%s", match.Channel.ID, match.Timestamp),
			Type:    "slack_message",
			Name:    match.Username,
			Content: match.Text,
			Metadata: map[string]any{
				"channel": match.Channel.Name,
			},
		})
	}
	return resources, nil
}

func (c *Connector) Create(ctx context.Context, resource connector.Resource) (string, error) {
	channel, _ := resource.Metadata["channel"].(string)
	if channel == "" {
		return "", fmt.Errorf("resource.metadata.channel is required")
	}
	threadTS, _ := resource.Metadata["thread_ts"].(string)
	blocks := parseBlocks(resource.Metadata["blocks"])

	options := []slackapi.MsgOption{}
	if len(blocks) > 0 {
		options = append(options, slackapi.MsgOptionBlocks(blocks...))
	}
	options = append(options, slackapi.MsgOptionText(resource.Content, false))
	if threadTS != "" {
		options = append(options, slackapi.MsgOptionTS(threadTS))
	}

	_, ts, err := c.client.PostMessageContext(ctx, channel, options...)
	if err != nil {
		return "", err
	}
	c.rememberThread(threadTS, ts)
	return ts, nil
}

func (c *Connector) Update(ctx context.Context, id string, resource connector.Resource) error {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "message" {
		return fmt.Errorf("id must be message:<channelID>:<timestamp>")
	}
	channelID := parts[1]
	ts := parts[2]

	options := []slackapi.MsgOption{slackapi.MsgOptionText(resource.Content, false)}
	blocks := parseBlocks(resource.Metadata["blocks"])
	if len(blocks) > 0 {
		options = append(options, slackapi.MsgOptionBlocks(blocks...))
	}
	_, _, _, err := c.client.UpdateMessageContext(ctx, channelID, ts, options...)
	return err
}

func (c *Connector) Delete(ctx context.Context, id string) error {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "message" {
		return fmt.Errorf("id must be message:<channelID>:<timestamp>")
	}
	_, _, err := c.client.DeleteMessageContext(ctx, parts[1], parts[2])
	return err
}

func (c *Connector) Send(ctx context.Context, channel string, message connector.Message) error {
	meta := map[string]any{}
	for k, v := range message.Metadata {
		meta[k] = v
	}
	meta["channel"] = channel
	if message.ThreadID != "" {
		meta["thread_ts"] = message.ThreadID
	}
	_, err := c.Create(ctx, connector.Resource{
		Type:     "slack_message",
		Name:     "message",
		Content:  message.Content,
		Metadata: meta,
	})
	return err
}

func (c *Connector) Listen(ctx context.Context, handler connector.MessageHandler) error {
	if c.socketClient == nil {
		return fmt.Errorf("slack.app_token is required for socket mode listener")
	}

	go c.socketClient.RunContext(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-c.socketClient.Events:
			switch event.Type {
			case socketmode.EventTypeEventsAPI:
				eventData, ok := event.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				c.socketClient.Ack(*event.Request)
				if eventData.Type == slackevents.CallbackEvent {
					switch inner := eventData.InnerEvent.Data.(type) {
					case *slackevents.AppMentionEvent:
						msg := connector.Message{
							ID:       inner.TimeStamp,
							Channel:  inner.Channel,
							ThreadID: firstNonEmpty(inner.ThreadTimeStamp, inner.TimeStamp),
							User:     inner.User,
							Content:  inner.Text,
							Metadata: map[string]any{"source": "app_mention"},
						}
						c.rememberThread(msg.ThreadID, msg.ID)
						if err := handler(ctx, msg); err != nil {
							return err
						}
					}
				}
			case socketmode.EventTypeSlashCommand:
				cmd, ok := event.Data.(slackapi.SlashCommand)
				if !ok {
					continue
				}
				c.socketClient.Ack(*event.Request)
				msg := connector.Message{
					ID:       fmt.Sprintf("%s-%d", cmd.Command, time.Now().UnixNano()),
					Channel:  cmd.ChannelID,
					ThreadID: "",
					User:     cmd.UserID,
					Content:  cmd.Text,
					Metadata: map[string]any{"source": "slash_command", "command": cmd.Command},
				}
				if err := handler(ctx, msg); err != nil {
					return err
				}
			}
		}
	}
}

func (c *Connector) listChannels(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	limit := 200
	if query.Limit > 0 {
		limit = query.Limit
	}
	channels, err := c.getChannels(ctx, limit)
	if err != nil {
		return nil, err
	}

	resources := make([]connector.Resource, 0, len(channels))
	for _, ch := range channels {
		resources = append(resources, connector.Resource{
			ID:      "channel:" + ch.ID,
			Type:    "slack_channel",
			Name:    ch.Name,
			Content: ch.Purpose.Value,
			Metadata: map[string]any{
				"is_private": ch.IsPrivate,
				"is_member":  ch.IsMember,
			},
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}

func (c *Connector) listMessages(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	channel := query.Filter["channel_id"]
	if channel == "" {
		return nil, fmt.Errorf("channel_id is required for messages")
	}
	limit := 25
	if query.Limit > 0 {
		limit = query.Limit
	}
	history, err := c.client.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
		ChannelID: channel,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}

	resources := make([]connector.Resource, 0, len(history.Messages))
	for _, msg := range history.Messages {
		threadTS := firstNonEmpty(msg.ThreadTimestamp, msg.Timestamp)
		c.rememberThread(threadTS, msg.Timestamp)
		resources = append(resources, connector.Resource{
			ID:      fmt.Sprintf("message:%s:%s", channel, msg.Timestamp),
			Type:    "slack_message",
			Name:    msg.User,
			Content: msg.Text,
			Metadata: map[string]any{
				"thread_ts": threadTS,
				"channel":   channel,
			},
		})
	}
	return resources, nil
}

func (c *Connector) rememberThread(threadID, messageID string) {
	if threadID == "" || messageID == "" {
		return
	}
	c.threadMu.Lock()
	defer c.threadMu.Unlock()
	c.threadContext[threadID] = append(c.threadContext[threadID], messageID)
	if len(c.threadContext[threadID]) > 50 {
		c.threadContext[threadID] = c.threadContext[threadID][len(c.threadContext[threadID])-50:]
	}
}

func (c *Connector) getChannels(ctx context.Context, limit int) ([]slackapi.Channel, error) {
	params := &slackapi.GetConversationsParameters{
		Limit: limit,
	}
	channels := make([]slackapi.Channel, 0, limit)
	for {
		page, nextCursor, err := c.client.GetConversationsContext(ctx, params)
		if err != nil {
			return nil, err
		}
		channels = append(channels, page...)
		if nextCursor == "" || len(channels) >= limit {
			break
		}
		params.Cursor = nextCursor
		params.Limit = limit - len(channels)
		if params.Limit <= 0 {
			break
		}
	}
	return channels, nil
}

func parseBlocks(v any) []slackapi.Block {
	// For Phase 1 we accept plain text by default; structured block parsing can be extended.
	_ = v
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Reader    = (*Connector)(nil)
	_ connector.Writer    = (*Connector)(nil)
	_ connector.Messenger = (*Connector)(nil)
)
