package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type LLMUsageRepository struct {
	db *sql.DB
}

func (r *LLMUsageRepository) Insert(ctx context.Context, usage LLMUsage) (int64, error) {
	if usage.Timestamp.IsZero() {
		usage.Timestamp = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO llm_usage(
			timestamp, request_id, job_run_id, provider, model,
			input_tokens, output_tokens, cost_usd, duration_ms, error
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, usage.Timestamp.UTC().Format(time.RFC3339Nano), usage.RequestID, usage.JobRunID,
		usage.Provider, usage.Model, usage.InputTokens, usage.OutputTokens,
		usage.CostUSD, usage.DurationMS, usage.Error)
	if err != nil {
		return 0, fmt.Errorf("insert llm usage: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read llm usage id: %w", err)
	}
	return id, nil
}
