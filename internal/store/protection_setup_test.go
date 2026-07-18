package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/protectionsetup"
)

func TestProtectionTemplateAndDraftRoundTripAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restic-control.db")
	storage, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	template := protectionsetup.Template{
		ID: "template-a", Name: "Daily", Retention: domain.RetentionPolicy{KeepDaily: 7}, Resources: domain.ResourcePolicy{Compression: "auto"},
		Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 2, CatchUpWindowMinutes: 60,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := storage.CreateProtectionTemplate(context.Background(), template); err != nil {
		t.Fatal(err)
	}
	draft := protectionsetup.Draft{
		ID: "draft-a", Name: "Database protection", TemplateID: template.ID, ExecutionTarget: execution.Target{Kind: execution.Local},
		Retention: template.Retention, Resources: template.Resources, Schedule: template.Schedule, Timezone: template.Timezone,
		MaxParallel: 2, CatchUpWindowMinutes: 60, NotificationMode: protectionsetup.NotificationConfigured,
		PlanID: "plan-a", Status: protectionsetup.DraftPending, CreatedAt: now, UpdatedAt: now,
		Items: []protectionsetup.DraftItem{
			{ID: "item-a", DraftID: "draft-a", Position: 0, TaskName: "Accounts", Database: &domain.DatabaseSource{ConnectionID: "connection", Database: "accounts"}, RepositoryID: "repo-a", RepositoryName: "Accounts repo", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/accounts", TaskID: "task-a", Status: protectionsetup.ItemPending, PasswordSecretID: "secret-a", UpdatedAt: now},
			{ID: "item-b", DraftID: "draft-a", Position: 1, TaskName: "Orders", Database: &domain.DatabaseSource{ConnectionID: "connection", Database: "orders"}, RepositoryID: "repo-b", RepositoryName: "Orders repo", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/orders", TaskID: "task-b", Status: protectionsetup.ItemPending, PasswordSecretID: "secret-b", UpdatedAt: now},
		},
	}
	if err := storage.CreateProtectionDraft(context.Background(), draft); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	loaded, err := storage.ProtectionDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != draft.Name || loaded.TemplateID != template.ID || len(loaded.Items) != 2 || loaded.Items[0].Database.Database != "accounts" || loaded.Items[0].PasswordSecretID != "secret-a" {
		t.Fatalf("loaded=%+v", loaded)
	}
	listed, err := storage.ListProtectionTemplates(context.Background())
	if err != nil || len(listed) != 1 || listed[0] != template {
		t.Fatalf("templates=%+v err=%v", listed, err)
	}
	loaded.Items[0].Status = protectionsetup.ItemFailed
	loaded.Items[0].Error = "安全的逐项错误"
	loaded.Items[0].UpdatedAt = now.Add(time.Minute)
	if err := storage.UpdateProtectionDraftItem(context.Background(), loaded.Items[0]); err != nil {
		t.Fatal(err)
	}
	if err := storage.UpdateProtectionDraftStatus(context.Background(), draft.ID, string(protectionsetup.DraftPartial), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	updated, err := storage.ProtectionDraft(context.Background(), draft.ID)
	if err != nil || updated.Status != protectionsetup.DraftPartial || updated.Items[0].Status != protectionsetup.ItemFailed || updated.Items[0].Error == "" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestProtectionDraftMappingsHaveDatabaseBackedIdentityIsolation(t *testing.T) {
	storage, err := Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Now().UTC()
	base := protectionsetup.Draft{
		ID: "draft-a", Name: "A", ExecutionTarget: execution.Target{Kind: execution.Local}, Retention: domain.RetentionPolicy{}, Resources: domain.ResourcePolicy{},
		Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 1, NotificationMode: protectionsetup.NotificationNone,
		PlanID: "plan-a", Status: protectionsetup.DraftPending, CreatedAt: now, UpdatedAt: now,
		Items: []protectionsetup.DraftItem{{ID: "item-a", DraftID: "draft-a", TaskName: "A", Position: 0, Directory: &domain.DirectorySource{Path: "/source"}, RepositoryID: "repo-a", RepositoryName: "A", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/a", TaskID: "task-a", Status: protectionsetup.ItemPending, PasswordSecretID: "secret-a", UpdatedAt: now}},
	}
	if err := storage.CreateProtectionDraft(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	conflict := base
	conflict.ID, conflict.Name, conflict.PlanID = "draft-b", "B", "plan-b"
	conflict.Items = append([]protectionsetup.DraftItem(nil), base.Items...)
	conflict.Items[0].DraftID, conflict.Items[0].ID = conflict.ID, "item-b"
	if err := storage.CreateProtectionDraft(context.Background(), conflict); err != ErrConflict {
		t.Fatalf("identity conflict error=%v", err)
	}
	if drafts, err := storage.ListProtectionDrafts(context.Background()); err != nil || len(drafts) != 1 {
		t.Fatalf("atomic drafts=%+v err=%v", drafts, err)
	}
}

func TestRecoverInterruptedProtectionDraftsMakesApplyingDraftsResumable(t *testing.T) {
	storage, err := Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Now().UTC()
	draft := protectionsetup.Draft{
		ID: "draft-interrupted", Name: "Interrupted", ExecutionTarget: execution.Target{Kind: execution.Local},
		Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 1,
		NotificationMode: protectionsetup.NotificationNone, PlanID: "plan-interrupted", Status: protectionsetup.DraftApplying, CreatedAt: now, UpdatedAt: now,
		Items: []protectionsetup.DraftItem{{ID: "item-interrupted", DraftID: "draft-interrupted", Position: 0, TaskName: "Photos", Directory: &domain.DirectorySource{Path: "/photos"}, RepositoryID: "repo-interrupted", RepositoryName: "Photos", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/photos", PasswordSecretID: "secret", TaskID: "task-interrupted", Status: protectionsetup.ItemPending, UpdatedAt: now}},
	}
	if err := storage.CreateProtectionDraft(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	recovered, err := storage.RecoverInterruptedProtectionDrafts(t.Context(), now.Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	loaded, err := storage.ProtectionDraft(t.Context(), draft.ID)
	if err != nil || loaded.Status != protectionsetup.DraftPartial {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}
