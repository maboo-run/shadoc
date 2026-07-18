package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskScopePreviewBindsTaskFingerprintAndDeleteConfirmation(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	preview := TaskScopePreview{
		ID: "scope-preview-1", TaskID: task.ID, Fingerprint: "fingerprint-1",
		Summary: map[string]any{"includedFiles": 12, "deleteFiles": 2}, RequiresDeleteConfirmation: true,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := s.CreateTaskScopePreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.TaskScopePreview(ctx, preview.ID)
	if err != nil || loaded.TaskID != task.ID || loaded.Summary["includedFiles"] != float64(12) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	for name, test := range map[string]struct {
		taskID      string
		fingerprint string
		confirmed   bool
	}{
		"task":                {taskID: "other", fingerprint: preview.Fingerprint, confirmed: true},
		"fingerprint":         {taskID: task.ID, fingerprint: "changed", confirmed: true},
		"delete confirmation": {taskID: task.ID, fingerprint: preview.Fingerprint, confirmed: false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.ConsumeTaskScopePreview(ctx, preview.ID, test.taskID, test.fingerprint, "admin", test.confirmed, now.Add(time.Minute))
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	confirmation, err := s.ConsumeTaskScopePreview(ctx, preview.ID, task.ID, preview.Fingerprint, "admin", true, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.PreviewID != preview.ID || confirmation.Fingerprint != preview.Fingerprint || confirmation.ConfirmedBy != "admin" || !confirmation.DeleteConfirmed || confirmation.Summary["deleteFiles"] != float64(2) {
		t.Fatalf("confirmation=%+v", confirmation)
	}
	if _, err := s.ConsumeTaskScopePreview(ctx, preview.ID, task.ID, preview.Fingerprint, "admin", true, now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused preview err=%v", err)
	}
}

func TestTaskScopePreviewExpires(t *testing.T) {
	s, task := createIdentityFixture(t)
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	preview := TaskScopePreview{ID: "expired", TaskID: task.ID, Fingerprint: "fingerprint", Summary: map[string]any{}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateTaskScopePreview(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeTaskScopePreview(context.Background(), preview.ID, task.ID, preview.Fingerprint, "admin", false, preview.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired preview err=%v", err)
	}
}
