package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type AuditLogRepository struct {
	db *sql.DB
}

func (r *AuditLogRepository) Insert(ctx context.Context, entry AuditEntry) (int64, error) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	contextJSON, err := json.Marshal(entry.Context)
	if err != nil {
		return 0, fmt.Errorf("marshal audit context: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_log(
			timestamp, trigger_source, trigger_ref, connector, operation,
			resource, query, result, duration_ms, error, context_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.Timestamp.UTC().Format(time.RFC3339Nano), entry.TriggerSource, entry.TriggerRef,
		entry.Connector, entry.Operation, entry.Resource, entry.Query, entry.Result,
		entry.DurationMS, entry.Error, string(contextJSON))
	if err != nil {
		return 0, fmt.Errorf("insert audit entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read audit id: %w", err)
	}
	return id, nil
}

func (r *AuditLogRepository) ListRecent(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, timestamp, trigger_source, trigger_ref, connector, operation,
		       resource, query, result, duration_ms, error, context_json
		FROM audit_log
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var (
			entry       AuditEntry
			timestamp   string
			contextJSON string
		)
		if err := rows.Scan(
			&entry.ID,
			&timestamp,
			&entry.TriggerSource,
			&entry.TriggerRef,
			&entry.Connector,
			&entry.Operation,
			&entry.Resource,
			&entry.Query,
			&entry.Result,
			&entry.DurationMS,
			&entry.Error,
			&contextJSON,
		); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		parsedTimestamp, err := parseTime(timestamp)
		if err != nil {
			return nil, err
		}
		entry.Timestamp = parsedTimestamp
		if err := json.Unmarshal([]byte(contextJSON), &entry.Context); err != nil {
			return nil, fmt.Errorf("decode audit context: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit entries: %w", err)
	}
	return entries, nil
}
