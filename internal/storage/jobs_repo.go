package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type JobsRepository struct {
	db *sql.DB
}

func (r *JobsRepository) Upsert(ctx context.Context, job Job) error {
	connectorsJSON, err := json.Marshal(job.Connectors)
	if err != nil {
		return fmt.Errorf("marshal connectors: %w", err)
	}
	permissionsJSON, err := json.Marshal(job.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO jobs(
			name, cron_expr, timezone, task, connectors_json, permissions_json,
			cost_limit_usd, on_failure, enabled, timeout_seconds, allow_overlap,
			run_on_startup_if_missed, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name)
		DO UPDATE SET
			cron_expr = excluded.cron_expr,
			timezone = excluded.timezone,
			task = excluded.task,
			connectors_json = excluded.connectors_json,
			permissions_json = excluded.permissions_json,
			cost_limit_usd = excluded.cost_limit_usd,
			on_failure = excluded.on_failure,
			enabled = excluded.enabled,
			timeout_seconds = excluded.timeout_seconds,
			allow_overlap = excluded.allow_overlap,
			run_on_startup_if_missed = excluded.run_on_startup_if_missed,
			updated_at = excluded.updated_at
	`,
		job.Name,
		job.CronExpr,
		job.Timezone,
		job.Task,
		string(connectorsJSON),
		string(permissionsJSON),
		job.CostLimitUSD,
		job.OnFailure,
		boolToInt(job.Enabled),
		job.TimeoutSeconds,
		boolToInt(job.AllowOverlap),
		boolToInt(job.RunOnStartupIfMissed),
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert job %s: %w", job.Name, err)
	}
	return nil
}

func (r *JobsRepository) UpdateRunTimes(ctx context.Context, name string, lastRunAt, nextRunAt *time.Time) error {
	var lastRunValue any
	if lastRunAt != nil {
		lastRunValue = lastRunAt.UTC().Format(time.RFC3339Nano)
	}
	var nextRunValue any
	if nextRunAt != nil {
		nextRunValue = nextRunAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs
		SET last_run_at = ?, next_run_at = ?, updated_at = ?
		WHERE name = ?
	`, lastRunValue, nextRunValue, time.Now().UTC().Format(time.RFC3339Nano), name)
	if err != nil {
		return fmt.Errorf("update run times for job %s: %w", name, err)
	}
	return nil
}

func (r *JobsRepository) List(ctx context.Context) ([]Job, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, cron_expr, timezone, task, connectors_json, permissions_json,
		       cost_limit_usd, on_failure, enabled, timeout_seconds, allow_overlap,
		       run_on_startup_if_missed, last_run_at, next_run_at, created_at, updated_at
		FROM jobs
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func (r *JobsRepository) GetByName(ctx context.Context, name string) (Job, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, cron_expr, timezone, task, connectors_json, permissions_json,
		       cost_limit_usd, on_failure, enabled, timeout_seconds, allow_overlap,
		       run_on_startup_if_missed, last_run_at, next_run_at, created_at, updated_at
		FROM jobs
		WHERE name = ?
	`, name)

	job, err := scanJob(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Job{}, fmt.Errorf("job %s not found", name)
		}
		return Job{}, err
	}
	return job, nil
}

func scanJob(scanner interface{ Scan(dest ...any) error }) (Job, error) {
	var (
		job                  Job
		connectorsJSON       string
		permissionsJSON      string
		enabled              int
		allowOverlap         int
		runOnStartupIfMissed int
		lastRunAtRaw         sql.NullString
		nextRunAtRaw         sql.NullString
		createdAtRaw         string
		updatedAtRaw         string
	)

	if err := scanner.Scan(
		&job.ID,
		&job.Name,
		&job.CronExpr,
		&job.Timezone,
		&job.Task,
		&connectorsJSON,
		&permissionsJSON,
		&job.CostLimitUSD,
		&job.OnFailure,
		&enabled,
		&job.TimeoutSeconds,
		&allowOverlap,
		&runOnStartupIfMissed,
		&lastRunAtRaw,
		&nextRunAtRaw,
		&createdAtRaw,
		&updatedAtRaw,
	); err != nil {
		return Job{}, err
	}

	job.Enabled = enabled == 1
	job.AllowOverlap = allowOverlap == 1
	job.RunOnStartupIfMissed = runOnStartupIfMissed == 1

	if err := json.Unmarshal([]byte(connectorsJSON), &job.Connectors); err != nil {
		return Job{}, fmt.Errorf("decode connectors for job %s: %w", job.Name, err)
	}
	if err := json.Unmarshal([]byte(permissionsJSON), &job.Permissions); err != nil {
		return Job{}, fmt.Errorf("decode permissions for job %s: %w", job.Name, err)
	}

	var err error
	if job.CreatedAt, err = parseTime(createdAtRaw); err != nil {
		return Job{}, err
	}
	if job.UpdatedAt, err = parseTime(updatedAtRaw); err != nil {
		return Job{}, err
	}
	if lastRunAtRaw.Valid {
		t, err := parseTime(lastRunAtRaw.String)
		if err != nil {
			return Job{}, err
		}
		job.LastRunAt = &t
	}
	if nextRunAtRaw.Valid {
		t, err := parseTime(nextRunAtRaw.String)
		if err != nil {
			return Job{}, err
		}
		job.NextRunAt = &t
	}

	return job, nil
}
