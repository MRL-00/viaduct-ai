package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type JobRunsRepository struct {
	db *sql.DB
}

func (r *JobRunsRepository) Create(ctx context.Context, run JobRun) (int64, error) {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO job_runs(
			job_id, job_name, status, duration_ms, cost_usd, error, result,
			trigger_source, started_at, finished_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.JobID, run.JobName, run.Status, run.DurationMS, run.CostUSD, run.Error, run.Result,
		run.TriggerSource, run.StartedAt.UTC().Format(time.RFC3339Nano), nullableTime(run.FinishedAt))
	if err != nil {
		return 0, fmt.Errorf("create job run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read job run id: %w", err)
	}
	return id, nil
}

func (r *JobRunsRepository) Complete(ctx context.Context, id int64, status, result string, duration time.Duration, costUSD float64, runErr error) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE job_runs
		SET status = ?, result = ?, duration_ms = ?, cost_usd = ?, error = ?, finished_at = ?
		WHERE id = ?
	`, status, result, duration.Milliseconds(), costUSD, errMsg, now, id)
	if err != nil {
		return fmt.Errorf("complete job run %d: %w", id, err)
	}
	return nil
}

func (r *JobRunsRepository) ListByJobName(ctx context.Context, jobName string, limit int) ([]JobRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, job_id, job_name, status, duration_ms, cost_usd, error, result,
		       trigger_source, started_at, finished_at
		FROM job_runs
		WHERE job_name = ?
		ORDER BY started_at DESC
		LIMIT ?
	`, jobName, limit)
	if err != nil {
		return nil, fmt.Errorf("list job runs: %w", err)
	}
	defer rows.Close()

	var runs []JobRun
	for rows.Next() {
		var (
			run        JobRun
			jobID      sql.NullInt64
			startedAt  string
			finishedAt sql.NullString
		)
		if err := rows.Scan(
			&run.ID,
			&jobID,
			&run.JobName,
			&run.Status,
			&run.DurationMS,
			&run.CostUSD,
			&run.Error,
			&run.Result,
			&run.TriggerSource,
			&startedAt,
			&finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job run: %w", err)
		}
		if jobID.Valid {
			run.JobID = &jobID.Int64
		}
		parsedStartedAt, err := parseTime(startedAt)
		if err != nil {
			return nil, err
		}
		run.StartedAt = parsedStartedAt
		if finishedAt.Valid {
			parsedFinishedAt, err := parseTime(finishedAt.String)
			if err != nil {
				return nil, err
			}
			run.FinishedAt = &parsedFinishedAt
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job runs: %w", err)
	}
	return runs, nil
}
