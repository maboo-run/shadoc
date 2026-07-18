package agentfilesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/execution"
)

func TestEngineBrowsesAndCreatesOnlyInsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "photos"), 0o750); err != nil {
		t.Fatal(err)
	}
	engine := New("posix", []string{root})
	browse, _ := json.Marshal(Definition{Operation: Browse, Path: root})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: browse})
	if err != nil {
		t.Fatal(err)
	}
	entries, ok := outcome.Summary["entries"].([]Entry)
	if !ok || len(entries) != 1 || entries[0].Name != "photos" || !entries[0].Directory {
		t.Fatalf("summary=%#v", outcome.Summary)
	}

	created := filepath.Join(root, "archive", "daily")
	create, _ := json.Marshal(Definition{Operation: CreateDirectory, Path: created})
	if _, err := engine.Run(context.Background(), execution.Assignment{Definition: create}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("created directory info=%v err=%v", info, err)
	}

	outside, _ := json.Marshal(Definition{Operation: Browse, Path: filepath.Dir(root)})
	if err := engine.Validate(outside); err == nil {
		t.Fatal("path outside allowed roots accepted")
	}
}

func TestWindowsPathValidationIsPlatformIndependent(t *testing.T) {
	engine := New("windows", []string{`D:\Backup`})
	valid, _ := json.Marshal(Definition{Operation: Browse, Path: `D:\Backup\Photos`})
	if err := engine.Validate(valid); err != nil {
		t.Fatalf("valid Windows path: %v", err)
	}
	for _, value := range []string{`D:relative`, `C:\Windows`, `D:\Backup\..\Secrets`} {
		raw, _ := json.Marshal(Definition{Operation: Browse, Path: value})
		if err := engine.Validate(raw); err == nil {
			t.Fatalf("unsafe Windows path %q accepted", value)
		}
	}
}

func TestPOSIXRootAllowsDescendantDirectories(t *testing.T) {
	engine := New("posix", []string{"/"})
	for _, path := range []string{"/", "/home", "/home/example"} {
		raw, _ := json.Marshal(Definition{Operation: Browse, Path: path})
		if err := engine.Validate(raw); err != nil {
			t.Fatalf("path %q inside root was rejected: %v", path, err)
		}
	}
}

func TestEngineRejectsSymlinkEscape(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX symlink semantics")
	}
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	engine := New("posix", []string{root})
	for _, definition := range []Definition{
		{Operation: Browse, Path: link},
		{Operation: CreateDirectory, Path: filepath.Join(link, "escaped")},
	} {
		raw, _ := json.Marshal(definition)
		if _, err := engine.Run(context.Background(), execution.Assignment{Definition: raw}); err == nil {
			t.Fatalf("symlink escape accepted: %+v", definition)
		}
	}
}

func TestEngineRunsDeclarativeScopePreviewInsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	writeScopeFile(t, filepath.Join(root, "photos", "one.jpg"), 12)
	engine := New("posix", []string{root})
	raw, _ := json.Marshal(Definition{Operation: PreviewScope, Path: root, Exclusions: []string{}, Limit: 100})
	if err := engine.Validate(raw); err != nil {
		t.Fatal(err)
	}
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: raw})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "succeeded" || outcome.Summary["includedFiles"] != 1 {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestEngineRejectsUnsafeScopePreviewDefinition(t *testing.T) {
	engine := New("posix", []string{"/srv"})
	for _, definition := range []Definition{
		{Operation: PreviewScope, Path: "/outside", Limit: 100},
		{Operation: PreviewScope, Path: "/srv/data", Exclusions: []string{"safe\nunsafe"}, Limit: 100},
		{Operation: PreviewScope, Path: "/srv/data", Limit: MaxScopeItems + 1},
	} {
		raw, _ := json.Marshal(definition)
		if err := engine.Validate(raw); err == nil {
			t.Fatalf("unsafe definition accepted: %+v", definition)
		}
	}
}
