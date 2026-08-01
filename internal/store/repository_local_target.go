package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func encodeRepositoryLocalTarget(repository domain.Repository) (string, error) {
	if repository.EffectiveKind() != domain.LocalRepository {
		if repository.LocalTarget != (execution.Target{}) {
			return "", errors.New("remote repository cannot declare a local filesystem owner")
		}
		return "", nil
	}
	target := repository.EffectiveLocalTarget()
	if err := target.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return "", fmt.Errorf("encode repository local target: %w", err)
	}
	return string(encoded), nil
}

func decodeRepositoryLocalTarget(repository *domain.Repository, encoded string) error {
	if repository.EffectiveKind() != domain.LocalRepository {
		if encoded != "" {
			return errors.New("remote repository has an invalid local filesystem owner")
		}
		return nil
	}
	if encoded == "" {
		repository.LocalTarget = execution.Target{Kind: execution.Local}
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &repository.LocalTarget); err != nil {
		return fmt.Errorf("decode repository local target: %w", err)
	}
	if err := repository.LocalTarget.Validate(); err != nil {
		return fmt.Errorf("validate repository local target: %w", err)
	}
	return nil
}

func (s *Store) ensureRepositoryLocalTargets(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(repositories)`)
	if err != nil {
		return fmt.Errorf("inspect repository local target column: %w", err)
	}
	present := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present = present || name == "local_target_json"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !present {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE repositories ADD COLUMN local_target_json TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add repository local target column: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE repositories SET local_target_json='{"kind":"local"}' WHERE kind='local' AND local_target_json=''`); err != nil {
		return fmt.Errorf("default legacy local repository ownership: %w", err)
	}
	// A repository is owned by at most one task. Existing Agent tasks therefore
	// provide unambiguous migration evidence for the filesystem that owns an old
	// local path.
	if _, err := tx.ExecContext(ctx, `
		UPDATE repositories
		SET local_target_json=(
			SELECT t.execution_target_json FROM tasks t
			WHERE t.repository_id=repositories.id
			  AND json_extract(t.execution_target_json,'$.kind')='agent'
			LIMIT 1
		)
		WHERE kind='local' AND EXISTS(
			SELECT 1 FROM tasks t
			WHERE t.repository_id=repositories.id
			  AND json_extract(t.execution_target_json,'$.kind')='agent'
		)`); err != nil {
		return fmt.Errorf("backfill Agent-local repository ownership: %w", err)
	}
	return tx.Commit()
}

func requireRepositoryLocalTargetReference(ctx context.Context, tx *sql.Tx, repository domain.Repository) error {
	target := repository.EffectiveLocalTarget()
	if repository.EffectiveKind() == domain.LocalRepository && target.Kind == execution.Agent {
		return requireLogicalReference(ctx, tx, `SELECT 1 FROM agents WHERE id=?`, target.AgentID)
	}
	return nil
}

func validateTaskRepositoryTarget(ctx context.Context, tx *sql.Tx, repositoryID string, target execution.Target) error {
	if repositoryID == "" {
		return nil
	}
	var repository domain.Repository
	var localTargetJSON string
	if err := tx.QueryRowContext(ctx, `SELECT kind,local_target_json FROM repositories WHERE id=?`, repositoryID).Scan(&repository.Kind, &localTargetJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if err := decodeRepositoryLocalTarget(&repository, localTargetJSON); err != nil {
		return err
	}
	if err := repository.ValidateExecutionTarget(target); err != nil {
		return ErrConflict
	}
	return nil
}
