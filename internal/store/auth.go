package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrAlreadyInitialized = errors.New("application is already initialized")

type Administrator struct {
	Username     string
	PasswordHash string
}

func (s *Store) CreateAdministrator(ctx context.Context, username, passwordHash string, createdAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin administrator setup: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM administrators`).Scan(&count); err != nil {
		return fmt.Errorf("count administrators: %w", err)
	}
	if count != 0 {
		return ErrAlreadyInitialized
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO administrators(id, username, password_hash, created_at)
		VALUES(1, ?, ?, ?)
	`, username, passwordHash, createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("create administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO metadata(key, value) VALUES('initialized', 'true')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`); err != nil {
		return fmt.Errorf("mark application initialized: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit administrator setup: %w", err)
	}
	return nil
}

func (s *Store) AdministratorByUsername(ctx context.Context, username string) (Administrator, error) {
	var admin Administrator
	err := s.db.QueryRowContext(ctx, `
		SELECT username, password_hash FROM administrators WHERE username = ?
	`, username).Scan(&admin.Username, &admin.PasswordHash)
	if err != nil {
		return Administrator{}, err
	}
	return admin, nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, csrfHash []byte, createdAt, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(token_hash, csrf_hash, created_at, expires_at)
		VALUES(?, ?, ?, ?)
	`, tokenHash, csrfHash, createdAt.UTC().Format(time.RFC3339Nano), expiresAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) SessionUsername(ctx context.Context, tokenHash []byte, now time.Time) (string, error) {
	var username string
	err := s.db.QueryRowContext(ctx, `
		SELECT a.username
		FROM sessions s CROSS JOIN administrators a
		WHERE a.id = 1 AND s.token_hash = ? AND s.expires_at > ?
	`, tokenHash, now.UTC().Format(time.RFC3339Nano)).Scan(&username)
	if err != nil {
		return "", err
	}
	return username, nil
}

func (s *Store) SessionCSRFMatches(ctx context.Context, tokenHash, csrfHash []byte, now time.Time) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM sessions
		WHERE token_hash = ? AND csrf_hash = ? AND expires_at > ?
	`, tokenHash, csrfHash, now.UTC().Format(time.RFC3339Nano)).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("validate session csrf: %w", err)
	}
	return true, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ResetAdministrator(ctx context.Context, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin administrator reset: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE administrators SET password_hash = ? WHERE id = 1`, passwordHash)
	if err != nil {
		return fmt.Errorf("update administrator password: %w", err)
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("revoke administrator sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit administrator reset: %w", err)
	}
	return nil
}
