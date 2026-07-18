package agentrestore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/restic"
)

type recordingRunner struct {
	operation restic.Operation
	fail      bool
}

func (r *recordingRunner) Execute(_ context.Context, operation restic.Operation) (restic.Result, error) {
	r.operation = operation
	for index, argument := range operation.Arguments {
		if argument == "--target" && index+1 < len(operation.Arguments) {
			if err := os.WriteFile(filepath.Join(operation.Arguments[index+1], "restored.txt"), []byte("ok"), 0o600); err != nil {
				return restic.Result{Outcome: restic.Failure}, err
			}
		}
	}
	if r.fail {
		return restic.Result{Outcome: restic.Failure}, errors.New("injected failure")
	}
	return restic.Result{Outcome: restic.Success}, nil
}

func TestEngineRestoresSelectedFileIntoNewTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "restored")
	runner := &recordingRunner{}
	definition, _ := json.Marshal(Definition{
		Repository: restic.Repository{Location: "/repo", Password: "secret"},
		SnapshotID: "snapshot", SourcePath: "/srv/photos", Target: target,
		Includes: []string{"album/one.jpg"}, DownloadKiBPerSecond: 128,
	})
	outcome, err := New("posix", []string{root}, runner).Run(t.Context(), execution.Assignment{Definition: definition})
	if err != nil || outcome.Status != "succeeded" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if _, err := os.Stat(filepath.Join(target, "restored.txt")); err != nil {
		t.Fatalf("restored target was not committed: %v", err)
	}
	want := []string{"--limit-download", "128", "snapshot:/srv/photos", "--target"}
	if !slices.Equal(runner.operation.Arguments[:4], want) || !slices.Equal(runner.operation.Arguments[len(runner.operation.Arguments)-2:], []string{"--include", "album/one.jpg"}) {
		t.Fatalf("arguments=%q", runner.operation.Arguments)
	}
}

func TestEngineRejectsTargetOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "secret"}, SnapshotID: "snapshot", SourcePath: "/srv", Target: filepath.Join(filepath.Dir(root), "outside")})
	if err := New("posix", []string{root}, &recordingRunner{}).Validate(definition); err == nil {
		t.Fatal("target outside allowed roots was accepted")
	}
}

func TestEngineValidatesWindowsTargetsWithoutHostPathSemantics(t *testing.T) {
	engine := New("windows", []string{`D:\Restore`}, &recordingRunner{})
	valid, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "secret"}, SnapshotID: "snapshot", SourcePath: `D:\Photos`, Target: `D:\Restore\Photos`})
	if err := engine.Validate(valid); err != nil {
		t.Fatalf("valid Windows restore target: %v", err)
	}
	for _, target := range []string{`D:relative`, `C:\Restore\Photos`, `D:\Restore\..\Secrets`} {
		raw, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "secret"}, SnapshotID: "snapshot", SourcePath: `D:\Photos`, Target: target})
		if err := engine.Validate(raw); err == nil {
			t.Fatalf("unsafe Windows restore target %q accepted", target)
		}
	}
}

func TestEngineLeavesFailedRestoreInStagingWithoutCreatingFinalTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "restored")
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "secret"}, SnapshotID: "snapshot", SourcePath: "/srv", Target: target})
	if _, err := New("posix", []string{root}, &recordingRunner{fail: true}).Run(t.Context(), execution.Assignment{Definition: definition}); err == nil {
		t.Fatal("restore failure was hidden")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final target exists after failure: %v", err)
	}
}
