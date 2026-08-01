package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	if resourceType == "tasks" {
		return s.taskDeletePreview(ctx, id)
	}
	table, _, err := deletableResource(resourceType)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	preview := ResourceDeletePreview{ResourceType: resourceType, ID: id, Dependencies: []ResourceDependency{}}
	if err := s.db.QueryRowContext(ctx, `SELECT name,updated_at FROM `+table+` WHERE id=?`, id).Scan(&preview.Name, &preview.UpdatedAt); err != nil {
		return ResourceDeletePreview{}, err
	}
	dependencies, err := resourceDeleteDependencies(ctx, s.db, resourceType, id)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	preview.Dependencies = dependencies
	preview.Deletable = len(preview.Dependencies) == 0
	if !preview.Deletable {
		preview.BlockedReason = "资源仍被其他配置引用"
	}
	return preview, nil
}

func resourceDeleteDependencies(ctx context.Context, queryer deletePreviewQueryer, resourceType, id string) ([]ResourceDependency, error) {
	queries := map[string][]struct{ dependencyType, query string }{
		"remote-hosts": {
			{"repositories", `SELECT name FROM repositories WHERE remote_host_id=? ORDER BY name`},
			{"tasks", `SELECT name FROM tasks WHERE engine='rsync' AND json_extract(source_json,'$.destinationHostId')=? ORDER BY name`},
		},
		"repositories": {
			{"tasks", `SELECT name FROM tasks WHERE repository_id=? ORDER BY name`},
		},
		"database-connections": {{"tasks", `SELECT name FROM tasks WHERE kind='database' AND json_extract(source_json,'$.connectionId')=? ORDER BY name`}},
	}
	result := make([]ResourceDependency, 0)
	for _, dependency := range queries[resourceType] {
		rows, err := queryer.QueryContext(ctx, dependency.query, id)
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
			result = append(result, ResourceDependency{Type: dependency.dependencyType, Count: len(names), Names: names})
		}
	}
	return result, nil
}

func (s *Store) taskDeletePreview(ctx context.Context, id string) (ResourceDeletePreview, error) {
	preview := ResourceDeletePreview{ResourceType: "tasks", ID: id, Dependencies: []ResourceDependency{}}
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT name,updated_at,enabled FROM tasks WHERE id=?`, id).Scan(&preview.Name, &preview.UpdatedAt, &enabled); err != nil {
		return ResourceDeletePreview{}, err
	}
	active, err := taskActiveWorkDependencies(ctx, s.db, id)
	if err != nil {
		return ResourceDeletePreview{}, err
	}
	preview.Dependencies = active
	preview.Deletable = enabled == 0 && len(active) == 0
	if enabled != 0 {
		preview.BlockedReason = "请先停用备份任务，再确认删除"
	} else if len(active) > 0 {
		preview.BlockedReason = "任务仍在执行，请等待运行、操作或 Agent 租约完成后再删除"
	}
	return preview, nil
}

func taskActiveWorkDependencies(ctx context.Context, queryer deletePreviewQueryer, id string) ([]ResourceDependency, error) {
	queries := []struct {
		dependencyType string
		query          string
	}{
		{"active-runs", `SELECT id FROM runs WHERE task_id=? AND status IN ('queued','running') ORDER BY id`},
		{"active-operations", `SELECT id FROM operations WHERE task_id=? AND status IN ('queued','running') ORDER BY id`},
		{"active-agent-leases", `SELECT id FROM agent_leases WHERE task_id=? AND status IN ('queued','running') AND completed_at IS NULL ORDER BY id`},
	}
	dependencies := make([]ResourceDependency, 0, len(queries))
	for _, dependency := range queries {
		rows, err := queryer.QueryContext(ctx, dependency.query, id)
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

func (s *Store) DeleteResourceVersioned(ctx context.Context, resourceType, id, expectedUpdatedAt string) ([]string, error) {
	if resourceType == "agents" {
		return nil, s.deleteAgentVersioned(ctx, id, expectedUpdatedAt)
	}
	if resourceType == "tasks" {
		return nil, s.deleteTaskVersioned(ctx, id, expectedUpdatedAt)
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
	dependencies, err := resourceDeleteDependencies(ctx, tx, resourceType, id)
	if err != nil {
		return nil, err
	}
	if len(dependencies) > 0 {
		return nil, ErrConflict
	}
	if resourceType == "repositories" {
		rows, err := tx.QueryContext(ctx, `SELECT secret_id FROM repository_key_revocations WHERE repository_id=? ORDER BY secret_id`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var secret string
			if err := rows.Scan(&secret); err != nil {
				_ = rows.Close()
				return nil, err
			}
			secrets = append(secrets, secret)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if err := deleteResourceOwnedRecords(ctx, tx, resourceType, id); err != nil {
		return nil, err
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

func deleteResourceOwnedRecords(ctx context.Context, tx *sql.Tx, resourceType, id string) error {
	var statements []string
	switch resourceType {
	case "remote-hosts":
		statements = []string{`UPDATE agents SET remote_host_id=NULL WHERE remote_host_id=?`}
	case "repositories":
		statements = []string{
			`UPDATE protection_draft_items SET repository_password_secret_id='' WHERE repository_id=?`,
			`DELETE FROM repository_key_revocations WHERE repository_id=?`,
			`DELETE FROM repository_capacities WHERE repository_id=?`,
			`DELETE FROM repository_capacity_policies WHERE repository_id=?`,
			`DELETE FROM repository_capacity_samples WHERE repository_id=?`,
			`DELETE FROM repository_maintenance WHERE repository_id=?`,
			`DELETE FROM maintenance_previews WHERE repository_id=?`,
			`DELETE FROM snapshot_metadata WHERE repository_id=?`,
			`DELETE FROM restore_verifications WHERE repository_id=?`,
			`DELETE FROM schedule_occurrences WHERE owner_kind='maintenance' AND owner_id=?`,
		}
	case "plans":
		statements = []string{
			`UPDATE runs SET plan_id=NULL WHERE plan_id=?`,
			`DELETE FROM plan_tasks WHERE plan_id=?`,
			`DELETE FROM schedule_occurrences WHERE owner_kind='plan' AND owner_id=?`,
		}
	case "protection-templates":
		statements = []string{`UPDATE protection_drafts SET template_id='' WHERE template_id=?`}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) deleteTaskVersioned(ctx context.Context, id, expectedUpdatedAt string) error {
	if expectedUpdatedAt == "" {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updatedAt, err := ensureTaskDeletableInTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if updatedAt != expectedUpdatedAt {
		return ErrConflict
	}
	if err := deleteTaskOwnedRecords(ctx, tx, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id=? AND updated_at=? AND enabled=0`, id, expectedUpdatedAt)
	if err != nil {
		return constraintError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func ensureTaskDeletableInTx(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var updatedAt string
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT updated_at,enabled FROM tasks WHERE id=?`, id).Scan(&updatedAt, &enabled); err != nil {
		return "", err
	}
	if enabled != 0 {
		return "", ErrConflict
	}
	active, err := taskActiveWorkDependencies(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if len(active) > 0 {
		return "", ErrConflict
	}
	return updatedAt, nil
}

func deleteTaskOwnedRecords(ctx context.Context, tx *sql.Tx, id string) error {
	if err := deleteTaskPlanAssociations(ctx, tx, id); err != nil {
		return err
	}
	if err := deleteTaskProtectionDrafts(ctx, tx, id); err != nil {
		return err
	}
	statements := []string{
		`DELETE FROM agent_leases WHERE task_id=?`,
		`DELETE FROM operations WHERE task_id=?`,
		`DELETE FROM runs WHERE task_id=?`,
		`DELETE FROM task_scope_previews WHERE task_id=?`,
		`DELETE FROM restore_verifications WHERE task_id=?`,
		`DELETE FROM restore_verification_policies WHERE task_id=?`,
		`DELETE FROM schedule_occurrences WHERE owner_kind='restore_verification' AND owner_id=?`,
		`DELETE FROM notification_deliveries WHERE state_key IN (SELECT state_key FROM alert_states WHERE object_type='task' AND object_id=?) OR state_key IN (SELECT state_key FROM alert_events WHERE object_type='task' AND object_id=?)`,
		`DELETE FROM notifications WHERE state_key IN (SELECT state_key FROM alert_states WHERE object_type='task' AND object_id=?) OR state_key IN (SELECT state_key FROM alert_events WHERE object_type='task' AND object_id=?)`,
		`DELETE FROM alert_events WHERE object_type='task' AND object_id=?`,
		`DELETE FROM alert_states WHERE object_type='task' AND object_id=?`,
	}
	for _, statement := range statements {
		arguments := []any{id}
		if strings.Count(statement, "?") == 2 {
			arguments = append(arguments, id)
		}
		if _, err := tx.ExecContext(ctx, statement, arguments...); err != nil {
			return err
		}
	}
	return nil
}

func deleteTaskProtectionDrafts(ctx context.Context, tx *sql.Tx, taskID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT d.id,d.plan_id
		FROM protection_drafts d
		JOIN protection_draft_items i ON i.draft_id=d.id
		WHERE i.task_id=?`, taskID)
	if err != nil {
		return err
	}
	type draftReference struct{ id, planID string }
	drafts := make([]draftReference, 0)
	for rows.Next() {
		var draft draftReference
		if err := rows.Scan(&draft.id, &draft.planID); err != nil {
			_ = rows.Close()
			return err
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM protection_draft_items WHERE task_id=?`, taskID); err != nil {
		return err
	}
	for _, draft := range drafts {
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM protection_draft_items WHERE draft_id=?`, draft.id).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM protection_drafts WHERE id=?`, draft.id); err != nil {
			return err
		}
		if draft.planID == "" {
			continue
		}
		var planTasks int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_tasks WHERE plan_id=?`, draft.planID).Scan(&planTasks); err != nil {
			return err
		}
		if planTasks != 0 {
			continue
		}
		if err := deleteResourceOwnedRecords(ctx, tx, "plans", draft.planID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plans WHERE id=?`, draft.planID); err != nil {
			return err
		}
	}
	return nil
}

func deleteTaskPlanAssociations(ctx context.Context, tx *sql.Tx, taskID string) error {
	planIDs := make(map[string]struct{})
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT plan_id FROM plan_tasks WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var planID string
		if err := rows.Scan(&planID); err != nil {
			_ = rows.Close()
			return err
		}
		planIDs[planID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT DISTINCT owner_id FROM schedule_occurrences
		WHERE owner_kind='plan' AND EXISTS (
			SELECT 1 FROM json_each(schedule_occurrences.target_ids_json) WHERE value=?
		)`, taskID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var planID string
		if err := rows.Scan(&planID); err != nil {
			_ = rows.Close()
			return err
		}
		planIDs[planID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for planID := range planIDs {
		if err := prunePlanOccurrencesForTask(ctx, tx, planID, taskID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plan_tasks WHERE plan_id=? AND task_id=?`, planID, taskID); err != nil {
			return err
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_tasks WHERE plan_id=?`, planID).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			continue
		}
		if err := deleteResourceOwnedRecords(ctx, tx, "plans", planID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plans WHERE id=?`, planID); err != nil {
			return err
		}
	}
	return nil
}

func prunePlanOccurrencesForTask(ctx context.Context, tx *sql.Tx, planID, taskID string) error {
	runIDs := make(map[string]struct{})
	rows, err := tx.QueryContext(ctx, `SELECT id FROM runs WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			_ = rows.Close()
			return err
		}
		runIDs[runID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	occurrences, err := tx.QueryContext(ctx, `
		SELECT id,target_ids_json,run_ids_json FROM schedule_occurrences
		WHERE owner_kind='plan' AND owner_id=?`, planID)
	if err != nil {
		return err
	}
	type occurrenceData struct {
		id, targetsJSON, runsJSON string
	}
	items := make([]occurrenceData, 0)
	for occurrences.Next() {
		var item occurrenceData
		if err := occurrences.Scan(&item.id, &item.targetsJSON, &item.runsJSON); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := occurrences.Err(); err != nil {
		return err
	}
	if err := occurrences.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var targets, runs []string
		if err := json.Unmarshal([]byte(item.targetsJSON), &targets); err != nil {
			return fmt.Errorf("decode plan occurrence targets: %w", err)
		}
		if err := json.Unmarshal([]byte(item.runsJSON), &runs); err != nil {
			return fmt.Errorf("decode plan occurrence runs: %w", err)
		}
		filteredTargets := make([]string, 0, len(targets))
		for _, target := range targets {
			if target != taskID {
				filteredTargets = append(filteredTargets, target)
			}
		}
		filteredRuns := make([]string, 0, len(runs))
		for _, runID := range runs {
			if _, belongs := runIDs[runID]; !belongs {
				filteredRuns = append(filteredRuns, runID)
			}
		}
		if len(filteredTargets) == len(targets) && len(filteredRuns) == len(runs) {
			continue
		}
		if len(filteredTargets) == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_occurrences WHERE id=?`, item.id); err != nil {
				return err
			}
			continue
		}
		encodedTargets, err := json.Marshal(filteredTargets)
		if err != nil {
			return err
		}
		encodedRuns, err := json.Marshal(filteredRuns)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedule_occurrences
			SET target_ids_json=?,run_ids_json=?,
				status=CASE WHEN status IN ('pending','running') THEN 'cancelled' ELSE status END,
				finished_at=CASE WHEN status IN ('pending','running') THEN COALESCE(finished_at,observed_at) ELSE finished_at END
			WHERE id=?`, string(encodedTargets), string(encodedRuns), item.id); err != nil {
			return err
		}
	}
	return nil
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
			dependencyType: "repositories",
			query: `SELECT name FROM repositories
				WHERE kind='local'
				  AND json_extract(local_target_json,'$.kind')='agent'
				  AND json_extract(local_target_json,'$.agentId')=?
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
	for _, statement := range []string{
		`DELETE FROM agent_certificates WHERE agent_id=?`,
		`DELETE FROM agent_filesystem_requests WHERE agent_id=?`,
		`DELETE FROM agent_restore_requests WHERE agent_id=?`,
		`DELETE FROM agent_leases WHERE agent_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
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
