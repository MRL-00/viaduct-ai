package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migration dir: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionPart := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.Atoi(versionPart)
		if err != nil {
			return fmt.Errorf("parse migration version for %s: %w", entry.Name(), err)
		}
		b, err := migrationFS.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: entry.Name(), sql: string(b)})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	for _, m := range migrations {
		var exists int
		err := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
			m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}

	return nil
}
