package localfilesystem

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/execution"
)

type metadataStore struct {
	values map[string]string
}

func (s *metadataStore) Metadata(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", sql.ErrNoRows
	}
	return value, nil
}

func (s *metadataStore) SetMetadata(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func TestServicePersistsCanonicalAllowedRootsAndRejectsUnsafeSettings(t *testing.T) {
	storage := &metadataStore{}
	service, err := New(t.Context(), storage, "posix")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	settings, err := service.SaveSettings(t.Context(), []string{nested, root, root})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.Roots, []string{root}) {
		t.Fatalf("roots=%v", settings.Roots)
	}
	var persisted Settings
	if err := json.Unmarshal([]byte(storage.values[settingsMetadataKey]), &persisted); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(persisted.Roots, []string{root}) {
		t.Fatalf("persisted=%v", persisted.Roots)
	}
	if _, err := service.SaveSettings(t.Context(), []string{"relative/path"}); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("relative root error=%v", err)
	}
	if !slices.Equal(service.Settings().Roots, []string{root}) {
		t.Fatalf("failed save changed active roots: %v", service.Settings().Roots)
	}
}

func TestServiceBrowsesAndCreatesOnlyInsideResolvedAllowedRoots(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-directory"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	service, err := New(t.Context(), &metadataStore{}, "posix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(t.Context(), []string{root}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Browse(t.Context(), root)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Name != "inside" {
		t.Fatalf("entries=%+v", result.Entries)
	}
	created := filepath.Join(root, "new", "child")
	if err := service.CreateDirectory(t.Context(), created); err != nil {
		t.Fatalf("create: %v", err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("created directory info=%v err=%v", info, err)
	}
	if _, err := service.Browse(t.Context(), outside); err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("outside browse error=%v", err)
	}
	if _, err := service.Browse(t.Context(), link); err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("symlink escape error=%v", err)
	}
}

func TestServiceActsAsDynamicScopePreviewEngine(t *testing.T) {
	root := t.TempDir()
	service, err := New(t.Context(), &metadataStore{}, "posix")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(t.Context(), []string{root}); err != nil {
		t.Fatal(err)
	}
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.PreviewScope, Path: root, Limit: 10})
	outcome, err := service.Run(t.Context(), execution.Assignment{Definition: definition})
	if err != nil || outcome.Status != "succeeded" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if _, err := service.SaveSettings(t.Context(), []string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(t.Context(), execution.Assignment{Definition: definition}); err == nil {
		t.Fatal("scope preview used stale allowed roots")
	}
}
