package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/protectionsetup"
)

func (s *Store) CreateProtectionTemplate(ctx context.Context, item protectionsetup.Template) error {
	retention, _ := json.Marshal(item.Retention)
	resources, _ := json.Marshal(item.Resources)
	health, _ := json.Marshal(item.Health)
	schedule, _ := json.Marshal(item.Schedule)
	_, err := s.db.ExecContext(ctx, `INSERT INTO protection_templates(id,name,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.Name, string(retention), string(resources), string(health), string(schedule), item.Timezone, item.MaxParallel, item.CatchUpWindowMinutes, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	return constraintError(err)
}

func (s *Store) ProtectionTemplate(ctx context.Context, id string) (protectionsetup.Template, error) {
	var item protectionsetup.Template
	var retention, resources, health, schedule, created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,created_at,updated_at FROM protection_templates WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &retention, &resources, &health, &schedule, &item.Timezone, &item.MaxParallel, &item.CatchUpWindowMinutes, &created, &updated)
	if err != nil {
		return item, err
	}
	if err := decodeProtectionTemplate(&item, retention, resources, health, schedule, created, updated); err != nil {
		return protectionsetup.Template{}, err
	}
	return item, nil
}

func (s *Store) ListProtectionTemplates(ctx context.Context) ([]protectionsetup.Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,created_at,updated_at FROM protection_templates ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]protectionsetup.Template, 0)
	for rows.Next() {
		var item protectionsetup.Template
		var retention, resources, health, schedule, created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &retention, &resources, &health, &schedule, &item.Timezone, &item.MaxParallel, &item.CatchUpWindowMinutes, &created, &updated); err != nil {
			return nil, err
		}
		if err := decodeProtectionTemplate(&item, retention, resources, health, schedule, created, updated); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func decodeProtectionTemplate(item *protectionsetup.Template, retention, resources, health, schedule, created, updated string) error {
	if json.Unmarshal([]byte(retention), &item.Retention) != nil || json.Unmarshal([]byte(resources), &item.Resources) != nil || json.Unmarshal([]byte(health), &item.Health) != nil || json.Unmarshal([]byte(schedule), &item.Schedule) != nil {
		return errors.New("decode protection template policy")
	}
	var err error
	if item.CreatedAt, err = parseTime(created); err != nil {
		return err
	}
	item.UpdatedAt, err = parseTime(updated)
	return err
}

func (s *Store) CreateProtectionDraft(ctx context.Context, draft protectionsetup.Draft) error {
	target, err := json.Marshal(draft.ExecutionTarget.Normalized())
	if err != nil {
		return err
	}
	retention, _ := json.Marshal(draft.Retention)
	resources, _ := json.Marshal(draft.Resources)
	health, _ := json.Marshal(draft.Health)
	schedule, _ := json.Marshal(draft.Schedule)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO protection_drafts(id,name,template_id,execution_target_json,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,notification_mode,plan_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		draft.ID, draft.Name, draft.TemplateID, string(target), string(retention), string(resources), string(health), string(schedule), draft.Timezone, draft.MaxParallel, draft.CatchUpWindowMinutes, string(draft.NotificationMode), draft.PlanID, string(draft.Status), formatTime(draft.CreatedAt), formatTime(draft.UpdatedAt))
	if err != nil {
		return constraintError(err)
	}
	for _, item := range draft.Items {
		kind, source, err := encodeProtectionSource(item)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO protection_draft_items(id,draft_id,position,task_name,source_kind,source_json,repository_id,repository_name,repository_kind,remote_host_id,repository_path,repository_password_secret_id,task_id,status,error_summary,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, draft.ID, item.Position, item.TaskName, kind, string(source), item.RepositoryID, item.RepositoryName, string(item.RepositoryKind), item.RemoteHostID, item.RepositoryPath, item.PasswordSecretID, item.TaskID, string(item.Status), boundedSetupError(item.Error), formatTime(item.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return constraintError(err)
	}
	return nil
}

func (s *Store) ProtectionDraft(ctx context.Context, id string) (protectionsetup.Draft, error) {
	items, err := s.listProtectionDrafts(ctx, `WHERE id=?`, id)
	if err != nil {
		return protectionsetup.Draft{}, err
	}
	if len(items) == 0 {
		return protectionsetup.Draft{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (s *Store) ListProtectionDrafts(ctx context.Context) ([]protectionsetup.Draft, error) {
	return s.listProtectionDrafts(ctx, "")
}

func (s *Store) listProtectionDrafts(ctx context.Context, where string, args ...any) ([]protectionsetup.Draft, error) {
	query := `SELECT id,name,template_id,execution_target_json,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,notification_mode,plan_id,status,created_at,updated_at FROM protection_drafts ` + where + ` ORDER BY updated_at DESC,id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]protectionsetup.Draft, 0)
	for rows.Next() {
		var item protectionsetup.Draft
		var target, retention, resources, health, schedule, notificationMode, status, created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &item.TemplateID, &target, &retention, &resources, &health, &schedule, &item.Timezone, &item.MaxParallel, &item.CatchUpWindowMinutes, &notificationMode, &item.PlanID, &status, &created, &updated); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(target), &item.ExecutionTarget) != nil || json.Unmarshal([]byte(retention), &item.Retention) != nil || json.Unmarshal([]byte(resources), &item.Resources) != nil || json.Unmarshal([]byte(health), &item.Health) != nil || json.Unmarshal([]byte(schedule), &item.Schedule) != nil {
			return nil, errors.New("decode protection draft policy")
		}
		item.NotificationMode, item.Status = protectionsetup.NotificationMode(notificationMode), protectionsetup.DraftStatus(status)
		if item.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		children, err := s.protectionDraftItems(ctx, items[index].ID)
		if err != nil {
			return nil, err
		}
		items[index].Items = children
	}
	return items, nil
}

func (s *Store) protectionDraftItems(ctx context.Context, draftID string) ([]protectionsetup.DraftItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,draft_id,position,task_name,source_kind,source_json,repository_id,repository_name,repository_kind,remote_host_id,repository_path,repository_password_secret_id,task_id,status,error_summary,updated_at FROM protection_draft_items WHERE draft_id=? ORDER BY position,id`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]protectionsetup.DraftItem, 0)
	for rows.Next() {
		var item protectionsetup.DraftItem
		var kind, source, repositoryKind, status, updated string
		if err := rows.Scan(&item.ID, &item.DraftID, &item.Position, &item.TaskName, &kind, &source, &item.RepositoryID, &item.RepositoryName, &repositoryKind, &item.RemoteHostID, &item.RepositoryPath, &item.PasswordSecretID, &item.TaskID, &status, &item.Error, &updated); err != nil {
			return nil, err
		}
		if err := decodeProtectionSource(&item, kind, source); err != nil {
			return nil, err
		}
		item.RepositoryKind, item.Status = domain.RepositoryKind(repositoryKind), protectionsetup.ItemStatus(status)
		if item.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateProtectionDraftStatus(ctx context.Context, id, status string, updated time.Time) error {
	switch protectionsetup.DraftStatus(status) {
	case protectionsetup.DraftPending, protectionsetup.DraftApplying, protectionsetup.DraftPartial, protectionsetup.DraftReady, protectionsetup.DraftCancelled:
	default:
		return errors.New("invalid protection draft status")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE protection_drafts SET status=?,updated_at=? WHERE id=?`, status, formatTime(updated), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecoverInterruptedProtectionDrafts returns process-owned setup work to a
// resumable state. Item results and staged secret references are retained so a
// retry can continue idempotently without rolling back completed resources.
func (s *Store) RecoverInterruptedProtectionDrafts(ctx context.Context, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE protection_drafts SET status=?,updated_at=? WHERE status=?`, string(protectionsetup.DraftPartial), formatTime(at), string(protectionsetup.DraftApplying))
	if err != nil {
		return 0, fmt.Errorf("recover interrupted protection drafts: %w", err)
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) UpdateProtectionDraftItem(ctx context.Context, item protectionsetup.DraftItem) error {
	switch item.Status {
	case protectionsetup.ItemPending, protectionsetup.ItemFailed, protectionsetup.ItemReady, protectionsetup.ItemRetained, protectionsetup.ItemCancelled:
	default:
		return errors.New("invalid protection draft item status")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE protection_draft_items SET repository_password_secret_id=?,status=?,error_summary=?,updated_at=? WHERE id=? AND draft_id=?`, item.PasswordSecretID, string(item.Status), boundedSetupError(item.Error), formatTime(item.UpdatedAt), item.ID, item.DraftID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func encodeProtectionSource(item protectionsetup.DraftItem) (string, []byte, error) {
	if item.Directory != nil && item.Database == nil {
		value, err := json.Marshal(item.Directory)
		return string(domain.DirectoryTask), value, err
	}
	if item.Database != nil && item.Directory == nil {
		value, err := json.Marshal(item.Database)
		return string(domain.DatabaseTask), value, err
	}
	return "", nil, errors.New("protection draft item requires exactly one source")
}

func decodeProtectionSource(item *protectionsetup.DraftItem, kind, source string) error {
	switch domain.TaskKind(kind) {
	case domain.DirectoryTask:
		item.Directory = &domain.DirectorySource{}
		return json.Unmarshal([]byte(source), item.Directory)
	case domain.DatabaseTask:
		item.Database = &domain.DatabaseSource{}
		return json.Unmarshal([]byte(source), item.Database)
	default:
		return fmt.Errorf("unsupported protection source kind %q", kind)
	}
}

func boundedSetupError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 512 {
		return value
	}
	return value[:512]
}
