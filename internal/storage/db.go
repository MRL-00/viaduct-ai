package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB          *sql.DB
	Credentials *CredentialsRepository
	Jobs        *JobsRepository
	JobRuns     *JobRunsRepository
	Audit       *AuditLogRepository
	LLMUsage    *LLMUsageRepository
	Memory      *MemoryRepository
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{
		DB:          db,
		Credentials: &CredentialsRepository{db: db},
		Jobs:        &JobsRepository{db: db},
		JobRuns:     &JobRunsRepository{db: db},
		Audit:       &AuditLogRepository{db: db},
		LLMUsage:    &LLMUsageRepository{db: db},
		Memory:      &MemoryRepository{db: db},
	}, nil
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
