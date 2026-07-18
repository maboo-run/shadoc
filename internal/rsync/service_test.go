package rsync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServiceBuildsAndPersistsLocalRsyncRun(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	storage := &serviceStoreFake{aggregate: store.RsyncExecution{Task: domain.Task{ID: "task-1", Enabled: true, Engine: domain.RsyncEngine, Kind: domain.RsyncTask, Rsync: &domain.RsyncSource{Path: "/source", DestinationHostID: "host-1", DestinationPath: "/target", Delete: true}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "preview-1", Fingerprint: "fingerprint-1", ConfirmedBy: "admin", ConfirmedAt: now, Summary: map[string]any{"deleteFiles": 2}, DeleteConfirmed: true}}, Host: domain.RemoteHost{ID: "host-1", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "known-host"}, PrivateKeySecretID: "key-1"}}
	engine := &serviceEngineFake{}
	service := NewService(storage, serviceSecretsFake{}, engine, func() time.Time { return now })
	record, err := service.Run(context.Background(), "task-1", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "success" || storage.finishedStatus != "success" {
		t.Fatalf("record=%+v finished=%q", record, storage.finishedStatus)
	}
	var definition Definition
	if err := json.Unmarshal(engine.assignment.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Destination.PrivateKey != "private-key" || definition.Destination.Path != "/target" {
		t.Fatalf("definition=%+v", definition)
	}
	confirmation, ok := storage.finishedSummary["scopeConfirmation"].(domain.TaskScopeConfirmation)
	if !ok || confirmation.Fingerprint != "fingerprint-1" || !confirmation.DeleteConfirmed {
		t.Fatalf("finished summary=%+v", storage.finishedSummary)
	}
}

func TestServicePersistsBoundedDetailedFailureSummary(t *testing.T) {
	now := time.Date(2026, 7, 18, 5, 30, 54, 0, time.UTC)
	storage := &serviceStoreFake{aggregate: store.RsyncExecution{
		Task: domain.Task{ID: "task-1", Enabled: true, Engine: domain.RsyncEngine, Kind: domain.RsyncTask, Rsync: &domain.RsyncSource{Path: "/source", DestinationHostID: "host-1", DestinationPath: "/target"}},
		Host: domain.RemoteHost{ID: "host-1", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "known-host"}, PrivateKeySecretID: "key-1",
	}}
	engine := &serviceEngineFake{
		outcome: execution.Outcome{Status: "failed", RawLog: "rsync: unrecognized option `--protect-args'\n" + strings.Repeat("x", 2048)},
		err:     errors.New("exit status 1 private-key"),
	}
	service := NewService(storage, serviceSecretsFake{}, engine, func() time.Time { return now })
	if _, err := service.Run(context.Background(), "task-1", "", "manual"); err == nil {
		t.Fatal("failed engine returned no error")
	}
	failure, _ := storage.finishedSummary["error"].(string)
	if !strings.Contains(failure, "unrecognized option `--protect-args'") || strings.Contains(failure, "private-key") || len([]rune(failure)) > 512 {
		t.Fatalf("failure summary=%q", failure)
	}
}

type serviceStoreFake struct {
	aggregate       store.RsyncExecution
	finishedStatus  string
	finishedSummary map[string]any
}

func (s *serviceStoreFake) LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error) {
	return s.aggregate, nil
}
func (s *serviceStoreFake) StartRun(context.Context, store.RunRecord) error { return nil }
func (s *serviceStoreFake) FinishRun(_ context.Context, _ string, status string, _ time.Time, _ int, _ string, summary map[string]any, _ string) error {
	s.finishedStatus = status
	s.finishedSummary = summary
	return nil
}

type serviceSecretsFake struct{}

func (serviceSecretsFake) Get(context.Context, string, string) ([]byte, error) {
	return []byte("private-key"), nil
}

type serviceEngineFake struct {
	assignment execution.Assignment
	outcome    execution.Outcome
	err        error
}

func (*serviceEngineFake) Kind() execution.EngineKind     { return "rsync" }
func (*serviceEngineFake) Validate(json.RawMessage) error { return nil }
func (e *serviceEngineFake) Run(_ context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	e.assignment = assignment
	if e.outcome.Status != "" || e.err != nil {
		return e.outcome, e.err
	}
	return execution.Outcome{Status: "succeeded", Summary: map[string]any{"files": 1}}, nil
}
