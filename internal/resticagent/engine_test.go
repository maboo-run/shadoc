package resticagent

import (
	"context"
	"encoding/json"
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
