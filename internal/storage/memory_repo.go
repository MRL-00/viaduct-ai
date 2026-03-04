package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type MemoryRepository struct {
	db *sql.DB
}

func (r *MemoryRepository) Upsert(ctx context.Context, entry MemoryEntry) error {
	if entry.SessionID == "" || entry.Key == "" {
		return fmt.Errorf("session_id and key are required")
	}
	metadataJSON, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("marshal memory metadata: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO memory(session_id, key, value, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, key)
		DO UPDATE SET value = excluded.value, metadata_json = excluded.metadata_json, updated_at = excluded.updated_at
	`, entry.SessionID, entry.Key, entry.Value, string(metadataJSON), now, now)
	if err != nil {
		return fmt.Errorf("upsert memory: %w", err)
	}
	return nil
}

func (r *MemoryRepository) ListBySession(ctx context.Context, sessionID string, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, key, value, metadata_json, created_at, updated_at
		FROM memory
		WHERE session_id = ?
		ORDER BY updated_at DESC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list memory: %w", err)
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var (
			entry        MemoryEntry
			metadataJSON string
			createdAt    string
			updatedAt    string
		)
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.Key, &entry.Value, &metadataJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		if err := json.Unmarshal([]byte(metadataJSON), &entry.Metadata); err != nil {
			return nil, fmt.Errorf("decode memory metadata: %w", err)
		}
		var err error
		if entry.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if entry.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory: %w", err)
	}
	return entries, nil
}
