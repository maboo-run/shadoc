package taskpreview

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
)

func TestCatalogStoresSecureRelativeInventoryAndPagesByStableFilteredCursor(t *testing.T) {
	root := t.TempDir()
	catalog, err := newCatalog(root, func() time.Time { return time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	writer, err := catalog.begin("preview-safe")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []agentfilesystem.ScopeEntry{
		{Ordinal: 1, Path: "photos", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeIncluded},
		{Ordinal: 2, Path: "photos/a.jpg", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded, Size: 10},
		{Ordinal: 3, Path: "photos/private.key", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeUnreadable, ReasonCode: agentfilesystem.ScopeReasonPermission},
		{Ordinal: 4, Path: "cache/thumb.bin", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeExcluded, ReasonCode: agentfilesystem.ScopeReasonExclusionRule, RuleIndexes: []int{0}},
	} {
		if err := writer.write(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(catalog.path("preview-safe"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode=%o", info.Mode().Perm())
	}

	first, err := catalog.page("preview-safe", EntryQuery{View: "all", Type: "file", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].Path != "photos/a.jpg" || first.Items[1].Path != "photos/private.key" || first.NextCursor == "" {
		t.Fatalf("first=%+v", first)
	}
	second, err := catalog.page("preview-safe", EntryQuery{View: "all", Type: "file", Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Path != "cache/thumb.bin" || second.NextCursor != "" {
		t.Fatalf("second=%+v", second)
	}
	attention, err := catalog.page("preview-safe", EntryQuery{View: "attention", Search: "PRIVATE", Limit: 10})
	if err != nil || len(attention.Items) != 1 || attention.Items[0].Disposition != agentfilesystem.ScopeUnreadable {
		t.Fatalf("attention=%+v err=%v", attention, err)
	}
	if _, err := catalog.page("preview-safe", EntryQuery{View: "excluded", Cursor: first.NextCursor, Limit: 2}); !errors.Is(err, ErrInvalidEntryCursor) {
		t.Fatalf("filter-bound cursor error=%v", err)
	}
}

func TestCatalogBrowsesOneDirectoryLevelAndMarksPartialDirectories(t *testing.T) {
	catalog, err := newCatalog(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := catalog.begin("preview-tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []agentfilesystem.ScopeEntry{
		{Ordinal: 1, Path: "photos", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeIncluded},
		{Ordinal: 2, Path: "photos/2025", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeIncluded},
		{Ordinal: 3, Path: "photos/2025/a.jpg", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded, Size: 10},
		{Ordinal: 4, Path: "photos/cover.jpg", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded, Size: 20},
		{Ordinal: 5, Path: "photos/private.key", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeUnreadable, ReasonCode: agentfilesystem.ScopeReasonPermission},
		{Ordinal: 6, Path: "cache", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeExcluded, ReasonCode: agentfilesystem.ScopeReasonExclusionRule},
		{Ordinal: 7, Path: "cache/thumb.bin", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeExcluded, Size: 5, ReasonCode: agentfilesystem.ScopeReasonExclusionRule},
		{Ordinal: 8, Path: "README.md", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded, Size: 3},
		{Ordinal: 9, Path: "documents", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeIncluded},
		{Ordinal: 10, Path: "documents/empty-cache", Type: agentfilesystem.ScopeDirectory, Disposition: agentfilesystem.ScopeExcluded, ReasonCode: agentfilesystem.ScopeReasonExclusionRule},
	} {
		if err := writer.write(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}

	root, err := catalog.page("preview-tree", EntryQuery{Browse: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Items) != 4 {
		t.Fatalf("root items=%+v", root.Items)
	}
	if root.Items[0].Path != "photos" || root.Items[0].Coverage != "partial" ||
		root.Items[0].TotalFiles != 3 || root.Items[0].IncludedFiles != 2 || root.Items[0].UnreadableItems != 1 {
		t.Fatalf("photos aggregate=%+v", root.Items[0])
	}
	if root.Items[1].Path != "cache" || root.Items[1].Coverage != "excluded" || root.Items[1].ExcludedFiles != 1 {
		t.Fatalf("cache aggregate=%+v", root.Items[1])
	}
	if root.Items[3].Path != "documents" || root.Items[3].Coverage != "partial" || root.Items[3].ExcludedItems != 1 {
		t.Fatalf("documents aggregate=%+v", root.Items[3])
	}
	for _, item := range root.Items {
		if item.Path == "photos/2025/a.jpg" || item.Path == "photos/private.key" {
			t.Fatalf("nested entry leaked into root level: %+v", item)
		}
	}

	photos, err := catalog.page("preview-tree", EntryQuery{Browse: true, Parent: "photos", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(photos.Items) != 3 || photos.Items[0].Path != "photos/2025" || photos.Items[0].Coverage != "full" ||
		photos.Items[1].Path != "photos/cover.jpg" || photos.Items[2].Path != "photos/private.key" {
		t.Fatalf("photos children=%+v", photos.Items)
	}

	attention, err := catalog.page("preview-tree", EntryQuery{Browse: true, View: "attention", Limit: 20})
	if err != nil || len(attention.Items) != 2 || attention.Items[0].Path != "photos" || attention.Items[1].Path != "documents" {
		t.Fatalf("attention root=%+v err=%v", attention.Items, err)
	}
}

func TestCatalogRejectsUnsafePreviewIdentifiersAndRemovesExpiredFiles(t *testing.T) {
	now := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	catalog, err := newCatalog(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.begin("../escape"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("unsafe identifier error=%v", err)
	}
	unsafeWriter, err := catalog.begin("preview-unsafe-entry")
	if err != nil {
		t.Fatal(err)
	}
	if err := unsafeWriter.write(agentfilesystem.ScopeEntry{Ordinal: 1, Path: "../escape", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded}); err == nil {
		t.Fatal("parent-traversing inventory path was accepted")
	}
	unsafeWriter.abort()
	writer, err := catalog.begin("preview-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-catalogTTL - time.Second)
	if err := os.Chtimes(catalog.path("preview-old"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := catalog.cleanup(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(catalog.path("preview-old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired catalog still exists: %v", err)
	}
}

func TestCatalogSchedulesRemovalAsSoonAsACompletedInventoryExpires(t *testing.T) {
	catalog, err := newCatalog(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var scheduled time.Duration
	catalog.schedule = func(delay time.Duration, remove func()) {
		scheduled = delay
		remove()
	}
	writer, err := catalog.begin("preview-short-lived")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.write(agentfilesystem.ScopeEntry{
		Ordinal: 1, Path: "photo.jpg", Type: agentfilesystem.ScopeRegularFile, Disposition: agentfilesystem.ScopeIncluded,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if scheduled != catalogTTL {
		t.Fatalf("scheduled delay=%s", scheduled)
	}
	if _, err := os.Stat(catalog.path("preview-short-lived")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scheduled catalog removal did not run: %v", err)
	}
}
