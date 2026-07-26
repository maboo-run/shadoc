package resticagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/restic"
)

type metricRunner struct{}

func (metricRunner) Execute(context.Context, restic.Operation) (restic.Result, error) {
	return restic.Result{Outcome: restic.Success, SnapshotID: "snapshot", Summary: map[string]any{"filesProcessed": int64(4), "bytesChanged": int64(512)}}, nil
}

type leakingS3Runner struct{}

func (leakingS3Runner) Execute(_ context.Context, operation restic.Operation) (restic.Result, error) {
	return restic.Result{Outcome: restic.Success, Stdout: operation.Repository.S3AccessKey + " " + operation.Repository.S3SecretKey}, nil
}

type scriptedRunner struct {
	operations []restic.Operation
	results    []restic.Result
	errors     []error
}

func (r *scriptedRunner) Execute(_ context.Context, operation restic.Operation) (restic.Result, error) {
	r.operations = append(r.operations, operation)
	index := len(r.operations) - 1
	var result restic.Result
	if index < len(r.results) {
		result = r.results[index]
	}
	var err error
	if index < len(r.errors) {
		err = r.errors[index]
	}
	return result, err
}

func TestAgentResticEnginePreservesParsedMetrics(t *testing.T) {
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "password"}, Directory: restic.DirectoryBackup{Path: "/source"}})
	outcome, err := New(metricRunner{}).Run(context.Background(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Summary["filesProcessed"] != int64(4) || outcome.Summary["bytesChanged"] != int64(512) || outcome.Summary["exitCode"] != 0 {
		t.Fatalf("summary=%+v", outcome.Summary)
	}
}

func TestAgentResticEngineRedactsS3Credentials(t *testing.T) {
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "s3:https://objects.example.com/backup", Password: "password", S3AccessKey: "access-private", S3SecretKey: "secret-private", S3Region: "us-east-1", S3BucketLookup: "dns"}, Directory: restic.DirectoryBackup{Path: "/source"}})
	outcome, err := New(leakingS3Runner{}).Run(context.Background(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RawLog != "[redacted] [redacted]" {
		t.Fatalf("raw log=%q", outcome.RawLog)
	}
}

func TestAgentResticEngineReportsPartialOnlyAfterProtectingSnapshot(t *testing.T) {
	runner := &scriptedRunner{results: []restic.Result{
		{Outcome: restic.Partial, SnapshotID: "partial-1", Summary: map[string]any{"filesExpected": int64(5), "filesProcessed": int64(4)}},
		{Outcome: restic.Success},
	}}
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "password"}, Directory: restic.DirectoryBackup{Path: "/source"}})
	outcome, err := New(runner).Run(t.Context(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "partial" || outcome.SnapshotID != "partial-1" || outcome.Summary["partialSnapshotProtected"] != true {
		t.Fatalf("outcome=%+v", outcome)
	}
	if len(runner.operations) != 2 || runner.operations[1].Kind != restic.TagSnapshot {
		t.Fatalf("operations=%+v", runner.operations)
	}
	if got := runner.operations[1].Arguments; len(got) != 3 || got[0] != "--add" || got[1] != "rc:protected-partial" || got[2] != "partial-1" {
		t.Fatalf("tag arguments=%v", got)
	}
}

func TestAgentResticEngineMarksRepositoryRecoveryWhenPartialProtectionFails(t *testing.T) {
	runner := &scriptedRunner{
		results: []restic.Result{
			{Outcome: restic.Partial, SnapshotID: "partial-1", Summary: map[string]any{"filesExpected": int64(5), "filesProcessed": int64(4)}},
			{Outcome: restic.Failure},
		},
		errors: []error{nil, errors.New("tag failed")},
	}
	definition, _ := json.Marshal(Definition{Repository: restic.Repository{Location: "/repo", Password: "password"}, Directory: restic.DirectoryBackup{Path: "/source"}})
	outcome, err := New(runner).Run(t.Context(), execution.Assignment{Definition: definition})
	if err == nil || outcome.Status != "failed" || outcome.Summary["unprotectedPartialSnapshot"] != "partial-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestAgentResticEngineProtectsPendingPartialBeforeStartingAnotherBackup(t *testing.T) {
	runner := &scriptedRunner{results: []restic.Result{
		{Outcome: restic.Success},
		{Outcome: restic.Success, SnapshotID: "snapshot-2"},
	}}
	definition, _ := json.Marshal(Definition{
		Repository:               restic.Repository{Location: "/repo", Password: "password"},
		Directory:                restic.DirectoryBackup{Path: "/source"},
		PendingPartialSnapshotID: "partial-1",
	})
	outcome, err := New(runner).Run(t.Context(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "succeeded" || outcome.Summary["pendingPartialProtected"] != "partial-1" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if len(runner.operations) != 2 || runner.operations[0].Kind != restic.TagSnapshot || runner.operations[1].Kind != restic.BackupDirectory {
		t.Fatalf("operations=%+v", runner.operations)
	}
}
