package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/store"
)

type managedUpdatePersistenceFake struct {
	record       store.OperationRecord
	stages       []string
	finishStatus string
	finishStage  string
	errorSummary string
	finishDetail map[string]any
	audits       []store.AuditRecord
}

func (f *managedUpdatePersistenceFake) Operation(context.Context, string) (store.OperationRecord, error) {
	return f.record, nil
}
func (f *managedUpdatePersistenceFake) UpdateOperationStage(_ context.Context, _ string, stage string, _ map[string]any) error {
	f.stages = append(f.stages, stage)
	return nil
}
func (f *managedUpdatePersistenceFake) FinishOperation(_ context.Context, _ string, status, stage string, _ time.Time, summary string, detail map[string]any) error {
	f.finishStatus, f.finishStage, f.errorSummary, f.finishDetail = status, stage, summary, detail
	return nil
}
func (f *managedUpdatePersistenceFake) AppendAudit(_ context.Context, audit store.AuditRecord) error {
	f.audits = append(f.audits, audit)
	return nil
}

type managedApplicationInstallerFake struct {
	err    error
	stages []string
	called bool
}

func (f *managedApplicationInstallerFake) UpdateWithReporter(_ context.Context, _ string, reporter appinstall.UpdateReporter) error {
	f.called = true
	for _, stage := range f.stages {
		if err := reporter.Stage(stage); err != nil {
			return err
		}
	}
	return f.err
}

func TestPerformManagedUpdatePersistsSuccessAndGenericRollbackFailure(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		installer    *managedApplicationInstallerFake
		wantStatus   string
		wantStage    string
		wantRollback bool
		wantVerified bool
	}{
		{name: "success", installer: &managedApplicationInstallerFake{stages: []string{"downloading_release", "health_verified"}}, wantStatus: "success", wantStage: "completed"},
		{name: "rollback verified", installer: &managedApplicationInstallerFake{stages: []string{"replacing_binary", "rolling_back", "verifying_rollback", "rollback_verified"}, err: errors.New("secret-token-from-process-output")}, wantStatus: "failed", wantStage: "rolled_back", wantRollback: true, wantVerified: true},
		{name: "rollback unverified", installer: &managedApplicationInstallerFake{stages: []string{"replacing_binary", "rolling_back", "verifying_rollback"}, err: errors.New("secret-token-from-process-output")}, wantStatus: "failed", wantStage: "rollback_failed", wantRollback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			persistence := &managedUpdatePersistenceFake{record: store.OperationRecord{
				ID: "op_0123456789abcdef01234567", Kind: "application_update", Actor: "admin", Target: "v1.3.0", Status: "running", Stage: "launching_updater",
			}}
			err := performManagedUpdate(t.Context(), persistence, test.installer, persistence.record.ID, "v1.3.0", func() time.Time { return now })
			if test.installer.err == nil && err != nil {
				t.Fatal(err)
			}
			if persistence.finishStatus != test.wantStatus || persistence.finishStage != test.wantStage || persistence.finishDetail["rollbackAttempted"] != test.wantRollback || persistence.finishDetail["rollbackVerified"] != test.wantVerified || len(persistence.audits) != 1 {
				t.Fatalf("persistence=%+v", persistence)
			}
			if strings.Contains(persistence.errorSummary, "secret-token") || strings.Contains(persistence.audits[0].Detail["stage"].(string), "secret-token") {
				t.Fatalf("raw updater error was persisted: %+v", persistence)
			}
		})
	}
}

func TestPerformManagedUpdateRejectsMismatchedDurableOperation(t *testing.T) {
	persistence := &managedUpdatePersistenceFake{record: store.OperationRecord{Kind: "application_update", Status: "running", Target: "v1.2.0"}}
	installer := &managedApplicationInstallerFake{}
	if err := performManagedUpdate(t.Context(), persistence, installer, "op_0123456789abcdef01234567", "v1.3.0", time.Now); err == nil || installer.called {
		t.Fatalf("err=%v installer.called=%t", err, installer.called)
	}
}
