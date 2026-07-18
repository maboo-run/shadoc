package agentfilesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopePreviewIncludesEverythingUntilRulesAreExplicitlyEnabled(t *testing.T) {
	root := t.TempDir()
	writeScopeFile(t, filepath.Join(root, "photos", "a.jpg"), 30)
	writeScopeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), 20)
	writeScopeFile(t, filepath.Join(root, ".cache", "thumb"), 10)

	withoutRules, err := scanScope(root, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if withoutRules.IncludedFiles != 3 || withoutRules.IncludedBytes != 60 || withoutRules.ExcludedFiles != 0 {
		t.Fatalf("default preview=%+v", withoutRules)
	}
	assertSuggestion(t, withoutRules.Suggestions, "**/node_modules", 1, 20)
	assertSuggestion(t, withoutRules.Suggestions, "**/.cache", 1, 10)

	withRule, err := scanScope(root, []string{"**/node_modules"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if withRule.IncludedFiles != 2 || withRule.IncludedBytes != 40 || withRule.ExcludedFiles != 1 || withRule.ExcludedBytes != 20 {
		t.Fatalf("explicit preview=%+v", withRule)
	}
	if len(withRule.ActiveRules) != 1 || withRule.ActiveRules[0].Rule != "**/node_modules" || withRule.ActiveRules[0].MatchedFiles != 1 {
		t.Fatalf("active rules=%+v", withRule.ActiveRules)
	}
}

func TestScopePreviewMarksTruncationInsteadOfReturningACompleteLookingResult(t *testing.T) {
	root := t.TempDir()
	writeScopeFile(t, filepath.Join(root, "a"), 1)
	writeScopeFile(t, filepath.Join(root, "b"), 1)
	writeScopeFile(t, filepath.Join(root, "c"), 1)

	preview, err := scanScope(root, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Truncated || preview.ScannedItems != 2 {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestScopePreviewReportsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-zero directories")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	writeScopeFile(t, filepath.Join(blocked, "secret"), 5)
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	preview, err := scanScope(root, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if preview.UnreadableItems == 0 {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestDoubleStarScopePatternsMatchDirectoriesAndDescendants(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		match   bool
	}{
		{pattern: "**/node_modules", path: "node_modules", match: true},
		{pattern: "**/node_modules", path: "apps/web/node_modules", match: true},
		{pattern: "cache/**", path: "cache", match: true},
		{pattern: "cache/**", path: "cache/nested/file", match: true},
		{pattern: "*.tmp", path: "nested/file.tmp", match: false},
		{pattern: "**/*.tmp", path: "nested/file.tmp", match: true},
	}
	for _, test := range tests {
		if got := matchScopePattern(test.pattern, test.path); got != test.match {
			t.Fatalf("matchScopePattern(%q, %q)=%v want %v", test.pattern, test.path, got, test.match)
		}
	}
}

func writeScopeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSuggestion(t *testing.T, suggestions []ScopeSuggestion, rule string, files int, bytes int64) {
	t.Helper()
	for _, suggestion := range suggestions {
		if suggestion.Rule == rule {
			if suggestion.MatchedFiles != files || suggestion.EstimatedBytes != bytes || suggestion.Reason == "" {
				t.Fatalf("suggestion=%+v", suggestion)
			}
			return
		}
	}
	t.Fatalf("suggestion %q missing from %+v", rule, suggestions)
}
