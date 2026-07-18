package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestSystemToolCheckerReportsTargetResticAndExactDatabaseClientsWithoutExecutingThem(t *testing.T) {
	directory := t.TempDir()
	available := filepath.Join(directory, "pg_dump")
	if err := os.WriteFile(available, []byte("not executable and must never run"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Repositories:        []domain.Repository{{ID: "repo-a", Engine: domain.ResticEngine}},
		DatabaseConnections: []domain.DatabaseConnection{{ID: "db-a", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, ToolPaths: map[string]string{"dump": available, "admin": filepath.Join(directory, "missing-psql")}}},
	}
	missing, err := (SystemToolChecker{ResticPath: filepath.Join(directory, "missing-restic")}).MissingTools(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing tools = %+v", missing)
	}
	if missing[0].Tool != "psql" || missing[1].Tool != "restic" {
		t.Fatalf("missing tools = %+v", missing)
	}
}
