package store

import (
	"context"
	"fmt"
	"time"
)

type EncryptedSecret struct {
	Purpose    string
	Ciphertext []byte
}

func (s *Store) SaveSecret(ctx context.Context, id, purpose string, ciphertext []byte, now time.Time) error {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO secrets(id, purpose, ciphertext, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
	`, id, purpose, ciphertext, timestamp, timestamp)
	if err != nil {
		return fmt.Errorf("save secret: %w", err)
	}
	return nil
}

func (s *Store) LoadSecret(ctx context.Context, id string) (EncryptedSecret, error) {
	var secret EncryptedSecret
	err := s.db.QueryRowContext(ctx, `
		SELECT purpose, ciphertext FROM secrets WHERE id = ?
	`, id).Scan(&secret.Purpose, &secret.Ciphertext)
	if err != nil {
		return EncryptedSecret{}, err
	}
	return secret, nil
}

func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}
