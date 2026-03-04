package connector

import "context"

type ConnectorConfig map[string]any

type Connector interface {
	Name() string
	Description() string
	Configure(cfg ConnectorConfig) error
	HealthCheck(ctx context.Context) error
}

type Reader interface {
	List(ctx context.Context, query Query) ([]Resource, error)
	Read(ctx context.Context, id string) (Resource, error)
	Search(ctx context.Context, query string) ([]Resource, error)
}

type Writer interface {
	Create(ctx context.Context, resource Resource) (string, error)
	Update(ctx context.Context, id string, resource Resource) error
	Delete(ctx context.Context, id string) error
}

type Messenger interface {
	Send(ctx context.Context, channel string, message Message) error
	Listen(ctx context.Context, handler MessageHandler) error
}

type Message struct {
	ID       string         `json:"id"`
	Channel  string         `json:"channel"`
	ThreadID string         `json:"thread_id"`
	User     string         `json:"user"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

type MessageHandler func(ctx context.Context, message Message) error

type Resource struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

type Query struct {
	Filter   map[string]string `json:"filter"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
	SortBy   string            `json:"sort_by"`
	SortDesc bool              `json:"sort_desc"`
}
