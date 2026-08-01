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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	referenced, err := secretReferenced(ctx, tx, id)
	if err != nil {
		return err
	}
	if referenced {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func secretReferenced(ctx context.Context, queryer logicalReferenceQueryer, id string) (bool, error) {
	var referenced int
	if err := queryer.QueryRowContext(ctx, `
		SELECT CASE WHEN
			EXISTS(SELECT 1 FROM remote_hosts WHERE private_key_secret_id=?) OR
			EXISTS(SELECT 1 FROM repositories WHERE password_secret_id=? OR backend_secret_id=?) OR
			EXISTS(SELECT 1 FROM repository_key_revocations WHERE secret_id=?) OR
			EXISTS(SELECT 1 FROM database_connections WHERE password_secret_id=?) OR
			EXISTS(SELECT 1 FROM protection_draft_items WHERE repository_password_secret_id=?) OR
			EXISTS(SELECT 1 FROM metadata WHERE key='ntfy.config' AND CASE WHEN json_valid(value) THEN json_extract(value,'$.tokenSecretId') ELSE NULL END=?) OR
			EXISTS(SELECT 1 FROM metadata WHERE key='webhook.config' AND CASE WHEN json_valid(value) THEN json_extract(value,'$.secretId') ELSE NULL END=?) OR
			EXISTS(SELECT 1 FROM metadata WHERE key='email.config' AND CASE WHEN json_valid(value) THEN json_extract(value,'$.passwordSecretId') ELSE NULL END=?)
		THEN 1 ELSE 0 END`, id, id, id, id, id, id, id, id, id).Scan(&referenced); err != nil {
		return false, err
	}
	return referenced != 0, nil
}
