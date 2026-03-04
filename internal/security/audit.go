package security

import (
	"context"
	"fmt"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/connector"
	"github.com/MRL-00/viaduct-ai/internal/storage"
)

type AuditLogger struct {
	repo *storage.AuditLogRepository
}

func NewAuditLogger(repo *storage.AuditLogRepository) *AuditLogger {
	return &AuditLogger{repo: repo}
}

func (a *AuditLogger) Wrap(name string, c connector.Connector) connector.Connector {
	return &auditedConnector{
		name:    name,
		inner:   c,
		auditor: a,
	}
}

func (a *AuditLogger) Log(ctx context.Context, entry storage.AuditEntry) {
	if a == nil || a.repo == nil {
		return
	}
	_, _ = a.repo.Insert(ctx, entry)
}

type auditedConnector struct {
	name    string
	inner   connector.Connector
	auditor *AuditLogger
}

func (a *auditedConnector) Name() string { return a.inner.Name() }

func (a *auditedConnector) Description() string { return a.inner.Description() }

func (a *auditedConnector) Configure(cfg connector.ConnectorConfig) error {
	return a.inner.Configure(cfg)
}

func (a *auditedConnector) HealthCheck(ctx context.Context) error {
	start := time.Now()
	err := a.inner.HealthCheck(ctx)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "system",
		Connector:     a.name,
		Operation:     "healthcheck",
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return err
}

func (a *auditedConnector) List(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	reader, ok := a.inner.(connector.Reader)
	if !ok {
		return nil, fmt.Errorf("connector %s does not support read/list", a.name)
	}
	start := time.Now()
	resources, err := reader.List(ctx, query)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "list",
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return resources, err
}

func (a *auditedConnector) Read(ctx context.Context, id string) (connector.Resource, error) {
	reader, ok := a.inner.(connector.Reader)
	if !ok {
		return connector.Resource{}, fmt.Errorf("connector %s does not support read", a.name)
	}
	start := time.Now()
	resource, err := reader.Read(ctx, id)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "read",
		Resource:      id,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return resource, err
}

func (a *auditedConnector) Search(ctx context.Context, query string) ([]connector.Resource, error) {
	reader, ok := a.inner.(connector.Reader)
	if !ok {
		return nil, fmt.Errorf("connector %s does not support search", a.name)
	}
	start := time.Now()
	resources, err := reader.Search(ctx, query)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "search",
		Query:         query,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return resources, err
}

func (a *auditedConnector) Create(ctx context.Context, resource connector.Resource) (string, error) {
	writer, ok := a.inner.(connector.Writer)
	if !ok {
		return "", fmt.Errorf("connector %s does not support create", a.name)
	}
	start := time.Now()
	id, err := writer.Create(ctx, resource)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "create",
		Resource:      resource.Type,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return id, err
}

func (a *auditedConnector) Update(ctx context.Context, id string, resource connector.Resource) error {
	writer, ok := a.inner.(connector.Writer)
	if !ok {
		return fmt.Errorf("connector %s does not support update", a.name)
	}
	start := time.Now()
	err := writer.Update(ctx, id, resource)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "update",
		Resource:      id,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return err
}

func (a *auditedConnector) Delete(ctx context.Context, id string) error {
	writer, ok := a.inner.(connector.Writer)
	if !ok {
		return fmt.Errorf("connector %s does not support delete", a.name)
	}
	start := time.Now()
	err := writer.Delete(ctx, id)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "delete",
		Resource:      id,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return err
}

func (a *auditedConnector) Send(ctx context.Context, channel string, message connector.Message) error {
	messenger, ok := a.inner.(connector.Messenger)
	if !ok {
		return fmt.Errorf("connector %s does not support send", a.name)
	}
	start := time.Now()
	err := messenger.Send(ctx, channel, message)
	a.auditor.Log(ctx, storage.AuditEntry{
		Timestamp:     start,
		TriggerSource: "agent",
		Connector:     a.name,
		Operation:     "send",
		Resource:      channel,
		Result:        resultFromErr(err),
		DurationMS:    time.Since(start).Milliseconds(),
		Error:         errorString(err),
	})
	return err
}

func (a *auditedConnector) Listen(ctx context.Context, handler connector.MessageHandler) error {
	messenger, ok := a.inner.(connector.Messenger)
	if !ok {
		return fmt.Errorf("connector %s does not support listen", a.name)
	}
	return messenger.Listen(ctx, handler)
}

var (
	_ connector.Connector = (*auditedConnector)(nil)
	_ connector.Reader    = (*auditedConnector)(nil)
	_ connector.Writer    = (*auditedConnector)(nil)
	_ connector.Messenger = (*auditedConnector)(nil)
)

func resultFromErr(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
