package store

import (
	"context"
	"testing"
	"time"
)

func TestResourceDeletePreviewIncludesNamedDependenciesAndVersion(t *testing.T) {
	s, task := createIdentityFixture(t)
	preview, err := s.ResourceDeletePreview(context.Background(), "repositories", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "repo" || preview.UpdatedAt == "" || len(preview.Dependencies) != 1 || preview.Dependencies[0].Type != "tasks" || preview.Dependencies[0].Count != 1 || preview.Dependencies[0].Names[0] != task.Name {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestVersionedDeleteRejectsResourceChangedAfterPreview(t *testing.T) {
	s, _ := createIdentityFixture(t)
	ctx := context.Background()
	preview, err := s.ResourceDeletePreview(ctx, "tasks", "task")
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.ListTasks(ctx)
	changed := tasks[0]
	changed.Name = "changed"
	changed.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := s.UpdateTask(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", "task", preview.UpdatedAt); err != ErrConflict {
		t.Fatalf("versioned delete err=%v", err)
	}
	loaded, _ := s.ListTasks(ctx)
	if len(loaded) != 1 || loaded[0].Name != "changed" {
		t.Fatalf("tasks=%+v", loaded)
	}
}
