package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/httpapi"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

const scaleRecordCountPerType = 5010

func TestExecutionHistoryBeyondTenThousandMixedRecordsHasNoCursorOrExportGaps(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.db")
	storage, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	now := time.Now().UTC()
	repository := domain.Repository{
		ID: "scale-repository", Name: "scale repository", Kind: domain.LocalRepository,
		Path: t.TempDir(), Status: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := storage.SaveSecret(ctx, "scale-password", "repository-password", []byte("encrypted fixture"), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(ctx, repository, "scale-password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		ID: "scale-task", Name: "scale task", Kind: domain.DirectoryTask, RepositoryID: repository.ID,
		Directory: &domain.DirectorySource{Path: t.TempDir()}, CreatedAt: now, UpdatedAt: now,
	}
	if err := storage.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	base := now.Add(-4 * time.Hour).Truncate(time.Second)
	seedMixedExecutionHistory(t, databasePath, task.ID, repository.ID, base)

	filter := store.ActivityFilter{Limit: 197}
	seen := make(map[string]bool, 2*scaleRecordCountPerType)
	pageCount := 0
	for {
		page, err := storage.ListActivity(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		pageCount++
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate cursor result %q", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
	}
	if got, want := len(seen), 2*scaleRecordCountPerType; got != want {
		t.Fatalf("cursor rows=%d want=%d", got, want)
	}
	if pageCount < 50 {
		t.Fatalf("cursor pagination did not cross a deep retained window: pages=%d", pageCount)
	}
	for _, id := range []string{"scale-run-00000", "scale-operation-02505", "scale-operation-05009"} {
		if !seen[id] {
			t.Fatalf("early/middle/late retained record %q is unreachable", id)
		}
	}

	vaultStore, err := vault.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.New(storage, vaultStore, time.Now)
	server := httpapi.NewWithRuntime(storage, auth.New(storage, time.Now), secrets, httpapi.Runtime{
		Paths: compat.ToolPaths{Restic: "/bin/echo"}, DataDir: t.TempDir(),
	})
	setup := request(t, server, http.MethodPost, "/api/setup", map[string]any{
		"username": "admin", "password": "correct horse battery staple",
	}, nil, "")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup=%d %s", setup.Code, setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]

	exported := request(t, server, http.MethodGet, "/api/activity/export?status=failed", nil, cookie, "")
	if exported.Code != http.StatusOK {
		t.Fatalf("activity export=%d %s", exported.Code, exported.Body.String())
	}
	rows, err := csv.NewReader(bytes.NewReader(exported.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantFailed := 2 * (scaleRecordCountPerType / 3)
	if got := len(rows) - 1; got != wantFailed {
		t.Fatalf("filtered export rows=%d want=%d", got, wantFailed)
	}
	exportedIDs := make(map[string]bool, wantFailed)
	for _, row := range rows[1:] {
		if len(row) < 5 || row[4] != "failed" {
			t.Fatalf("unexpected filtered export row=%v", row)
		}
		if exportedIDs[row[1]] {
			t.Fatalf("duplicate export row %q", row[1])
		}
		exportedIDs[row[1]] = true
	}
	for _, id := range []string{"scale-run-00000", "scale-operation-02505", "scale-operation-05007"} {
		if !exportedIDs[id] {
			t.Fatalf("filtered export omitted %q", id)
		}
	}

	for _, path := range []string{"/api/runs?limit=2", "/api/operations?limit=2"} {
		legacy := request(t, server, http.MethodGet, path, nil, cookie, "")
		if legacy.Code != http.StatusOK || legacy.Body.Len() == 0 || legacy.Body.String()[0] != '[' {
			t.Fatalf("legacy endpoint %s=%d %s", path, legacy.Code, legacy.Body.String())
		}
	}
}

func seedMixedExecutionHistory(t *testing.T, databasePath, taskID, repositoryID string, base time.Time) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	runStatement, err := tx.PrepareContext(t.Context(), `
		INSERT INTO runs(id,task_id,trigger,status,started_at,finished_at,attempt_count,summary_json,duration_ms,files_processed,bytes_processed)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer runStatement.Close()
	operationStatement, err := tx.PrepareContext(t.Context(), `
		INSERT INTO operations(id,kind,actor,repository_id,task_id,status,stage,created_at,started_at,finished_at,attempt_count,error_summary,detail_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer operationStatement.Close()
	for index := 0; index < scaleRecordCountPerType; index++ {
		status := "success"
		if index%3 == 0 {
			status = "failed"
		}
		runAt := base.Add(time.Duration(2*index) * time.Second)
		if _, err := runStatement.ExecContext(t.Context(),
			fmt.Sprintf("scale-run-%05d", index), taskID, "schedule", status,
			runAt.Format(time.RFC3339Nano), runAt.Add(time.Second).Format(time.RFC3339Nano), 1, `{}`,
			1000+index, 10+index, 1024+index,
		); err != nil {
			t.Fatal(err)
		}
		operationAt := runAt.Add(time.Second)
		if _, err := operationStatement.ExecContext(t.Context(),
			fmt.Sprintf("scale-operation-%05d", index), "repository_maintenance", "admin", repositoryID, taskID,
			status, status, operationAt.Format(time.RFC3339Nano), operationAt.Format(time.RFC3339Nano), operationAt.Add(time.Second).Format(time.RFC3339Nano),
			1, "", `{}`,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
