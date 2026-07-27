package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type ResourceDependency struct {
	Type  string   `json:"type"`
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type ResourceDeletePreview struct {
	ResourceType  string               `json:"resourceType"`
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	UpdatedAt     string               `json:"updatedAt"`
	Dependencies  []ResourceDependency `json:"dependencies"`
	Deletable     bool                 `json:"deletable"`
	BlockedReason string               `json:"blockedReason,omitempty"`
}

func (s *Store) ResourceDeletePreview(ctx context.Context, resourceType, id string) (ResourceDeletePreview, error) {
	if resourceType == "agents" {
		return s.agentDeletePreview(ctx, id)
	}
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
	preview.Deletable = len(preview.Dependencies) == 0
	if !preview.Deletable {
		preview.BlockedReason = "资源仍被其他配置引用"
	}
	return preview, nil
}

func (s *Store) DeleteResourceVersioned(ctx context.Context, resourceType, id, expectedUpdatedAt string) ([]string, error) {
	if resourceType == "agents" {
		return nil, s.deleteAgentVersioned(ctx, id, expectedUpdatedAt)
	}
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
	if resourceType == "remote-hosts" {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET remote_host_id=NULL WHERE remote_host_id=?`, id); err != nil {
			return nil, constraintError(err)
		}
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

type deletePreviewQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type agentDeleteSnapshot struct {
	id, remoteHostID, certificateSerial, status, lastHeartbeatAt string
	createdAt, revokedAt, stoppedAt, uninstalledAt, drainingAt   string
}

func readAgentDeleteSnapshot(ctx context.Context, queryer deletePreviewQueryer, id string) (agentDeleteSnapshot, error) {
	var snapshot agentDeleteSnapshot
	err := queryer.QueryRowContext(ctx, `
		SELECT id,COALESCE(remote_host_id,''),certificate_serial,status,COALESCE(last_heartbeat_at,''),
			created_at,COALESCE(revoked_at,''),COALESCE(stopped_at,''),COALESCE(uninstalled_at,''),COALESCE(draining_at,'')
		FROM agents WHERE id=?`, id).Scan(
		&snapshot.id, &snapshot.remoteHostID, &snapshot.certificateSerial, &snapshot.status, &snapshot.lastHeartbeatAt,
		&snapshot.createdAt, &snapshot.revokedAt, &snapshot.stoppedAt, &snapshot.uninstalledAt, &snapshot.drainingAt,
	)
	return snapshot, err
}

func (snapshot agentDeleteSnapshot) version() string {
	value := strings.Join([]string{
		snapshot.id, snapshot.remoteHostID, snapshot.certificateSerial, snapshot.status, snapshot.lastHeartbeatAt,
		snapshot.createdAt, snapshot.revokedAt, snapshot.stoppedAt, snapshot.uninstalledAt, snapshot.drainingAt,
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func agentDeleteDependencies(ctx context.Context, queryer deletePreviewQueryer, id string) ([]ResourceDependency, error) {
	queries := []struct {
		dependencyType string
		query          string
	}{
		{
			dependencyType: "tasks",
			query: `SELECT name FROM tasks
				WHERE json_extract(execution_target_json,'$.kind')='agent'
				  AND json_extract(execution_target_json,'$.agentId')=?
				ORDER BY name`,
		},
		{
			dependencyType: "active-agent-operations",
			query: `SELECT id FROM operations
				WHERE target=? AND kind LIKE 'agent_%' AND status IN ('queued','running')
				ORDER BY id`,
		},
		{
			dependencyType: "active-agent-work",
			query: `SELECT 'lease:' || id FROM agent_leases
				WHERE agent_id=? AND status='running' AND completed_at IS NULL
				UNION ALL
				SELECT 'filesystem:' || id FROM agent_filesystem_requests
				WHERE agent_id=? AND status='running' AND completed_at IS NULL
				UNION ALL
				SELECT 'restore:' || id FROM agent_restore_requests
				WHERE agent_id=? AND status='running' AND completed_at IS NULL
				ORDER BY 1`,
		},
	}
	dependencies := make([]ResourceDependency, 0, len(queries))
	for _, dependency := range queries {
		arguments := []any{id}
		if dependency.dependencyType == "active-agent-work" {
			arguments = []any{id, id, id}
		}
		rows, err := queryer.QueryContext(ctx, dependency.query, arguments...)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return nil, err
			}
			names = append(names, name)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(names) > 0 {
			dependencies = append(dependencies, ResourceDependency{Type: dependency.dependencyType, Count: len(names), Names: names})
		}
	}
	return dependencies, nil
}

func (s *Store) agentDeletePreview(ctx context.Context, id string) (ResourceDeletePreview, error) {
	snapshot, err := readAgentDeleteSnapshot(ctx, s.db, id)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	dependencies, err := agentDeleteDependencies(ctx, s.db, id)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	preview := ResourceDeletePreview{
		ResourceType: "agents",
		ID:           snapshot.id,
		Name:         snapshot.id,
		UpdatedAt:    snapshot.version(),
		Dependencies: dependencies,
	}
	switch {
	case snapshot.revokedAt == "" && snapshot.uninstalledAt == "":
		preview.BlockedReason = "Agent 凭据仍有效；请先撤销凭据或完成停止并卸载"
	case len(dependencies) > 0:
		preview.BlockedReason = "Agent 仍被任务引用或存在进行中的管理操作"
	default:
		preview.Deletable = true
	}
	return preview, nil
}

func (s *Store) deleteAgentVersioned(ctx context.Context, id, expectedVersion string) error {
	if expectedVersion == "" {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := readAgentDeleteSnapshot(ctx, tx, id)
	if err != nil {
		return err
	}
	if snapshot.version() != expectedVersion || snapshot.revokedAt == "" && snapshot.uninstalledAt == "" {
		return ErrConflict
	}
	dependencies, err := agentDeleteDependencies(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(dependencies) > 0 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
	if err != nil {
		return constraintError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
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
