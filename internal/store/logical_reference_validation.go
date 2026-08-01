package store

import (
	"context"
	"database/sql"
	"errors"
)

type logicalReferenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireLogicalReference(ctx context.Context, queryer logicalReferenceQueryer, query string, arguments ...any) error {
	var present int
	if err := queryer.QueryRowContext(ctx, query, arguments...).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func validatePlanTaskReferences(ctx context.Context, queryer logicalReferenceQueryer, taskIDs []string, requireEnabled bool) error {
	for _, taskID := range taskIDs {
		query := `SELECT 1 FROM tasks WHERE id=?`
		if requireEnabled {
			query += ` AND enabled=1`
		}
		if err := requireLogicalReference(ctx, queryer, query, taskID); err != nil {
			return ErrConflict
		}
	}
	return nil
}
