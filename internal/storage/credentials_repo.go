package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type CredentialsRepository struct {
	db *sql.DB
}

func (r *CredentialsRepository) Upsert(ctx context.Context, connector, key string, encrypted []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO credentials(connector, key_name, encrypted_value, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(connector, key_name)
		DO UPDATE SET encrypted_value = excluded.encrypted_value, updated_at = excluded.updated_at
	`, connector, key, encrypted, now, now)
	if err != nil {
		return fmt.Errorf("upsert credential: %w", err)
	}
	return nil
}

func (r *CredentialsRepository) Get(ctx context.Context, connector, key string) (Credential, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, connector, key_name, encrypted_value, created_at, updated_at
		FROM credentials
		WHERE connector = ? AND key_name = ?
	`, connector, key)

	var rec Credential
	var createdAt, updatedAt string
	if err := row.Scan(&rec.ID, &rec.Connector, &rec.KeyName, &rec.EncryptedValue, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Credential{}, fmt.Errorf("credential not found")
		}
		return Credential{}, fmt.Errorf("read credential: %w", err)
	}

	var err error
	if rec.CreatedAt, err = parseTime(createdAt); err != nil {
		return Credential{}, err
	}
	if rec.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Credential{}, err
	}

	return rec, nil
}

func (r *CredentialsRepository) Delete(ctx context.Context, connector, key string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM credentials WHERE connector = ? AND key_name = ?`, connector, key)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}
