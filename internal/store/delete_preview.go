package store

import (
	"context"
	"fmt"
)

type ResourceDependency struct {
	Type  string   `json:"type"`
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type ResourceDeletePreview struct {
	ResourceType string               `json:"resourceType"`
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	UpdatedAt    string               `json:"updatedAt"`
	Dependencies []ResourceDependency `json:"dependencies"`
}

func (s *Store) ResourceDeletePreview(ctx context.Context, resourceType, id string) (ResourceDeletePreview, error) {
	table, _, err := deletableResource(resourceType)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	preview := ResourceDeletePreview{ResourceType: resourceType, ID: id, Dependencies: []ResourceDependency{}}
	if err := s.db.QueryRowContext(ctx, `SELECT name,updated_at FROM `+table+` WHERE id=?`, id).Scan(&preview.Name, &preview.UpdatedAt); err != nil {
		return ResourceDeletePreview{}, err
	}
	queries := map[string][]struct{ dependencyType, query string }{
		"remote-hosts":         {{"repositories", `SELECT name FROM repositories WHERE remote_host_id=? ORDER BY name`}},
		"repositories":         {{"tasks", `SELECT name FROM tasks WHERE repository_id=? ORDER BY name`}},
		"database-connections": {{"tasks", `SELECT name FROM tasks WHERE kind='database' AND json_extract(source_json,'$.connectionId')=? ORDER BY name`}},
		"tasks":                {{"plans", `SELECT DISTINCT p.name FROM plans p JOIN plan_tasks pt ON pt.plan_id=p.id WHERE pt.task_id=? ORDER BY p.name`}},
	}
	for _, dependency := range queries[resourceType] {
		rows, err := s.db.QueryContext(ctx, dependency.query, id)
		if err != nil {
			return ResourceDeletePreview{}, err
		}
		names := make([]string, 0)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return ResourceDeletePreview{}, err
			}
			names = append(names, name)
		}
		if err := rows.Close(); err != nil {
			return ResourceDeletePreview{}, err
		}
		if len(names) > 0 {
			preview.Dependencies = append(preview.Dependencies, ResourceDependency{Type: dependency.dependencyType, Count: len(names), Names: names})
		}
	}
	return preview, nil
}

func (s *Store) DeleteResourceVersioned(ctx context.Context, resourceType, id, expectedUpdatedAt string) ([]string, error) {
	table, secretColumns, err := deletableResource(resourceType)
	if err != nil || expectedUpdatedAt == "" {
		return nil, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var updatedAt string
	query := `SELECT updated_at`
	for _, secretColumn := range secretColumns {
		query += `,COALESCE(` + secretColumn + `,'')`
	}
	query += ` FROM ` + table + ` WHERE id=?`
	secrets := make([]string, len(secretColumns))
	scanTargets := make([]any, 0, len(secrets)+1)
	scanTargets = append(scanTargets, &updatedAt)
	for index := range secrets {
		scanTargets = append(scanTargets, &secrets[index])
	}
	scanErr := tx.QueryRowContext(ctx, query, id).Scan(scanTargets...)
	if scanErr != nil {
		return nil, scanErr
	}
	if updatedAt != expectedUpdatedAt {
		return nil, ErrConflict
	}
	deleteResult, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=? AND updated_at=?`, id, expectedUpdatedAt)
	if err != nil {
		return nil, constraintError(err)
	}
	affected, err := deleteResult.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, constraintError(err)
	}
	result := secrets[:0]
	for _, secret := range secrets {
		if secret != "" {
			result = append(result, secret)
		}
	}
	return result, nil
}

func deletableResource(resourceType string) (table string, secretColumns []string, err error) {
	switch resourceType {
	case "remote-hosts":
		return "remote_hosts", []string{"private_key_secret_id"}, nil
	case "repositories":
		return "repositories", []string{"password_secret_id", "backend_secret_id"}, nil
	case "database-connections":
		return "database_connections", []string{"password_secret_id"}, nil
	case "tasks":
		return "tasks", nil, nil
	case "plans":
		return "plans", nil, nil
	case "protection-templates":
		return "protection_templates", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported resource type %q", resourceType)
	}
}
