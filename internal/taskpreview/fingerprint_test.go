package taskpreview

import (
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestFingerprintChangesOnlyWithTaskScope(t *testing.T) {
	base := domain.Task{
		ID: "task-1", Name: "photos", Engine: domain.RsyncEngine, Kind: domain.RsyncTask,
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
		RepositoryID:    "repo-1",
		Rsync:           &domain.RsyncSource{Path: "/srv/photos", Exclusions: []string{"**/.cache"}, Delete: true},
		Enabled:         false,
		CreatedAt:       time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 7, 15, 2, 3, 4, 0, time.UTC),
	}
	want, err := Fingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 64 {
		t.Fatalf("fingerprint=%q", want)
	}

	cosmetic := base
	cosmetic.Name = "renamed"
	cosmetic.Enabled = true
	cosmetic.UpdatedAt = cosmetic.UpdatedAt.Add(time.Hour)
	got, err := Fingerprint(cosmetic)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cosmetic fields changed fingerprint: got %s want %s", got, want)
	}

	mutations := map[string]func(*domain.Task){
		"engine":     func(task *domain.Task) { task.Engine = domain.ResticEngine },
		"kind":       func(task *domain.Task) { task.Kind = domain.DirectoryTask },
		"agent":      func(task *domain.Task) { task.ExecutionTarget.AgentID = "agent-2" },
		"repository": func(task *domain.Task) { task.RepositoryID = "repo-2" },
		"source": func(task *domain.Task) {
			copySource := *task.Rsync
			copySource.Path = "/srv/documents"
			task.Rsync = &copySource
		},
		"exclusions": func(task *domain.Task) {
			copySource := *task.Rsync
			copySource.Exclusions = []string{"**/node_modules"}
			task.Rsync = &copySource
		},
		"delete": func(task *domain.Task) {
			copySource := *task.Rsync
			copySource.Delete = false
			task.Rsync = &copySource
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			fingerprint, err := Fingerprint(changed)
			if err != nil {
				t.Fatal(err)
			}
			if fingerprint == want {
				t.Fatalf("scope mutation %q retained fingerprint %s", name, fingerprint)
			}
		})
	}
}

func TestFingerprintNormalizesLegacyDefaults(t *testing.T) {
	legacy := domain.Task{Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv/photos"}}
	explicit := legacy
	explicit.Engine = domain.ResticEngine
	explicit.ExecutionTarget = execution.Target{Kind: execution.Local}

	left, err := Fingerprint(legacy)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Fingerprint(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("legacy defaults changed fingerprint: %s != %s", left, right)
	}
}
