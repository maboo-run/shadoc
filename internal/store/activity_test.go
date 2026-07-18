package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestListActivityUsesStableFilterBoundCursorAcrossRunsAndOperations(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	for _, record := range []RunRecord{
		{ID: "run-z", TaskID: task.ID, Trigger: "schedule", Status: "running", StartedAt: started},
		{ID: "run-a", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started},
	} {
		if err := s.StartRun(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, record.ID, "success", started.Add(time.Minute), 1, "snapshot", map[string]any{"error": "bounded"}, "raw log must not enter activity"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateOperation(ctx, OperationRecord{ID: "operation-z", Kind: "repository_maintenance", Actor: "admin", RepositoryID: task.RepositoryID, Status: "failed", Stage: "failed", CreatedAt: started, ErrorSummary: "maintenance failed", Detail: map[string]any{"secret": "must not enter activity"}}); err != nil {
		t.Fatal(err)
	}

	filter := ActivityFilter{ObjectID: task.RepositoryID, Limit: 2}
	first, err := s.ListActivity(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "run-z" || first.Items[1].ID != "run-a" || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first=%+v", first)
	}
	if first.Total != 0 || first.PageSize != 2 {
		t.Fatalf("first pagination=%+v", first)
	}
	second, err := s.ListActivity(ctx, ActivityFilter{ObjectID: task.RepositoryID, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "operation-z" || second.Items[0].RecordType != ActivityOperation || second.Truncated || second.NextCursor != "" {
		t.Fatalf("second=%+v", second)
	}
	if second.Items[0].RepositoryName == "" || second.Items[0].ErrorSummary != "maintenance failed" {
		t.Fatalf("operation projection=%+v", second.Items[0])
	}

	_, err = s.ListActivity(ctx, ActivityFilter{ObjectID: task.RepositoryID, Status: "failed", Limit: 2, Cursor: first.NextCursor})
	if !errors.Is(err, ErrInvalidActivityCursor) {
		t.Fatalf("filter-mismatched cursor err=%v", err)
	}
}

func TestListActivitySupportsDirectNumberedPageNavigation(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	for index := 0; index < 7; index++ {
		record := RunRecord{ID: fmt.Sprintf("numbered-%d", index), TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started.Add(time.Duration(index) * time.Minute)}
		if err := s.StartRun(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListActivity(ctx, ActivityFilter{RecordType: ActivityRun, ObjectID: task.ID, Limit: 2, Page: 3})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 3 || page.PageSize != 2 || page.Total != 7 || len(page.Items) != 2 || page.Items[0].ID != "numbered-2" || page.Items[1].ID != "numbered-1" || !page.Truncated {
		t.Fatalf("page=%+v", page)
	}
	if _, err := s.ListActivity(ctx, ActivityFilter{Limit: 2, Page: 2, Cursor: "cursor"}); !errors.Is(err, ErrInvalidActivityFilter) {
		t.Fatalf("page with cursor err=%v", err)
	}
}

func TestListActivityAppliesEngineStatusTriggerKindAndTimeFilters(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	for index, record := range []RunRecord{
		{ID: "scheduled-success", TaskID: task.ID, Trigger: "schedule", Status: "running", StartedAt: base},
		{ID: "manual-failure", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: base.Add(time.Hour)},
	} {
		if err := s.StartRun(ctx, record); err != nil {
			t.Fatal(err)
		}
		status := "success"
		if index == 1 {
			status = "failed"
		}
		if err := s.FinishRun(ctx, record.ID, status, record.StartedAt.Add(time.Minute), 1, "", nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateOperation(ctx, OperationRecord{ID: "manual-operation", Kind: "directory_restore", Actor: "admin", TaskID: task.ID, Status: "failed", Stage: "failed", CreatedAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	from, to := base.Add(30*time.Minute), base.Add(90*time.Minute)
	page, err := s.ListActivity(ctx, ActivityFilter{RecordType: ActivityRun, ObjectID: task.ID, Engine: string(task.EffectiveEngine()), Status: "failed", Trigger: "manual", Kind: "backup", From: &from, To: &to, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "manual-failure" || page.Items[0].TaskName != task.Name || page.Items[0].Engine != string(task.EffectiveEngine()) {
		t.Fatalf("page=%+v", page)
	}
	operationPage, err := s.ListActivity(ctx, ActivityFilter{RecordType: ActivityOperation, Kind: "directory_restore", Status: "failed", Limit: 10})
	if err != nil || len(operationPage.Items) != 1 || operationPage.Items[0].ID != "manual-operation" {
		t.Fatalf("operations=%+v err=%v", operationPage, err)
	}
}

func TestListActivityCanReachEveryRetainedRunBeyondTenThousandRows(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10005; index++ {
		id := fmt.Sprintf("history-%05d", index)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,task_id,trigger,status,started_at,finished_at,attempt_count,summary_json) VALUES(?,?,?,?,?,?,?,?)`, id, task.ID, "schedule", "success", formatTime(base.Add(time.Duration(index)*time.Second)), formatTime(base.Add(time.Duration(index)*time.Second+time.Millisecond)), 1, `{}`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	filter := ActivityFilter{RecordType: ActivityRun, ObjectID: task.ID, Limit: 137}
	seen := map[string]bool{}
	for {
		page, err := s.ListActivity(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate activity %q", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
	}
	if len(seen) != 10005 || !seen["history-00000"] || !seen["history-10004"] {
		t.Fatalf("reachable=%d first=%v last=%v", len(seen), seen["history-00000"], seen["history-10004"])
	}
}

func TestActivityCursorQueryPlansSeekThroughOrderingIndexes(t *testing.T) {
	s, _ := createIdentityFixture(t)
	cursor := &activityCursor{
		OccurredAt: formatTime(time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)),
		RecordType: ActivityRun,
		ID:         "history-10000",
	}
	filter := ActivityFilter{Limit: 50}

	runQuery, runArgs := runActivityQuery(filter, cursor)
	operationQuery, operationArgs := operationActivityQuery(filter, cursor)
	combinedArgs := append(append(runArgs, operationArgs...), filter.Limit+1)
	combinedPlan := explainActivityPlan(t, s, runQuery+` UNION ALL `+operationQuery+` ORDER BY occurred_at DESC,record_type DESC,id DESC LIMIT ?`, combinedArgs...)
	if !strings.Contains(combinedPlan, "SEARCH r USING INDEX runs_activity_order") || !strings.Contains(combinedPlan, "SEARCH o USING INDEX operations_activity_order") {
		t.Fatalf("combined activity cursor must seek through both ordering indexes, plan:\n%s", combinedPlan)
	}

	filteredRun := ActivityFilter{Status: "failed", Trigger: "schedule", Limit: 50}
	filteredRunQuery, filteredRunArgs := runActivityQuery(filteredRun, cursor)
	filteredRunPlan := explainActivityPlan(t, s, filteredRunQuery+` ORDER BY occurred_at DESC,record_type DESC,id DESC LIMIT ?`, append(filteredRunArgs, filteredRun.Limit+1)...)
	if !strings.Contains(filteredRunPlan, "SEARCH r USING INDEX runs_activity_status_trigger") {
		t.Fatalf("filtered run cursor must use runs_activity_status_trigger, plan:\n%s", filteredRunPlan)
	}

	filteredOperation := ActivityFilter{Kind: "repository_maintenance", Status: "failed", Limit: 50}
	filteredOperationQuery, filteredOperationArgs := operationActivityQuery(filteredOperation, cursor)
	filteredOperationPlan := explainActivityPlan(t, s, filteredOperationQuery+` ORDER BY occurred_at DESC,record_type DESC,id DESC LIMIT ?`, append(filteredOperationArgs, filteredOperation.Limit+1)...)
	if !strings.Contains(filteredOperationPlan, "SEARCH o USING INDEX operations_activity_kind_status") {
		t.Fatalf("filtered operation cursor must use operations_activity_kind_status, plan:\n%s", filteredOperationPlan)
	}
}

func explainActivityPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := make([]string, 0, 8)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}
