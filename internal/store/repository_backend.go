package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/maboo-run/shadoc/internal/domain"
)

func encodeRepositoryBackend(repository domain.Repository) (string, error) {
	if repository.EffectiveKind() != domain.S3Repository {
		if repository.S3 != nil || repository.BackendSecretID != "" {
			return "", errors.New("non-S3 repository cannot contain S3 backend material")
		}
		return "", nil
	}
	if repository.S3 == nil || repository.BackendSecretID == "" {
		return "", errors.New("S3 repository requires structured settings and a credential secret")
	}
	config := *repository.S3
	config.Endpoint = strings.TrimSuffix(config.Endpoint, "/")
	config.CredentialsConfigured = false
	encoded, err := json.Marshal(config)
	return string(encoded), err
}

func decodeRepositoryBackend(repository *domain.Repository, encoded, secretID string) error {
	if repository.EffectiveKind() != domain.S3Repository {
		if encoded != "" || secretID != "" {
			return errors.New("non-S3 repository contains S3 backend material")
		}
		return nil
	}
	if encoded == "" || secretID == "" {
		return errors.New("S3 repository backend material is incomplete")
	}
	var config domain.S3RepositoryConfig
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return fmt.Errorf("decode S3 repository backend: %w", err)
	}
	config.CredentialsConfigured = true
	repository.S3 = &config
	repository.BackendSecretID = secretID
	return nil
}

func (s *Store) ensureRepositoryBackendColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(repositories)`)
	if err != nil {
		return fmt.Errorf("inspect repository backend columns: %w", err)
	}
	present := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !present["backend_json"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE repositories ADD COLUMN backend_json TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add repository backend configuration: %w", err)
		}
	}
	if !present["backend_secret_id"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE repositories ADD COLUMN backend_secret_id TEXT REFERENCES secrets(id)`); err != nil {
			return fmt.Errorf("add repository backend secret: %w", err)
		}
	}
	return nil
}
