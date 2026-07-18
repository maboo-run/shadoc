package repositorycapacity

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maboo-run/shadoc/internal/execution"
)

type fakeProbe struct {
	definition Definition
	capacity   Capacity
}

func (p *fakeProbe) Probe(_ context.Context, definition Definition) (Capacity, error) {
	p.definition = definition
	return p.capacity, nil
}

func TestEngineReturnsStructuredCapacityWithoutRawOutput(t *testing.T) {
	probe := &fakeProbe{capacity: Capacity{TotalBytes: 1000, AvailableBytes: 400}}
	engine := NewEngine(probe)
	definition, _ := json.Marshal(Definition{Kind: "local", Path: "/srv/repository"})

	outcome, err := engine.Run(context.Background(), execution.Assignment{Engine: Kind, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "succeeded" || outcome.RawLog != "" || outcome.Summary["totalBytes"] != uint64(1000) || outcome.Summary["availableBytes"] != uint64(400) {
		t.Fatalf("outcome=%+v", outcome)
	}
	if probe.definition.Path != "/srv/repository" {
		t.Fatalf("definition=%+v", probe.definition)
	}
}

func TestDefinitionRejectsUnpinnedOrIncompleteSFTPTarget(t *testing.T) {
	engine := NewEngine(&fakeProbe{})
	unsafe, _ := json.Marshal(Definition{Kind: "sftp", Path: "/backup", Host: "backup.example", Port: 22, Username: "backup", PrivateKey: "secret"})
	if err := engine.Validate(unsafe); err == nil {
		t.Fatal("SFTP target without a pinned host key was accepted")
	}
	unknown, _ := json.Marshal(Definition{Kind: "shell", Path: "/backup"})
	if err := engine.Validate(unknown); err == nil {
		t.Fatal("unknown capacity probe kind was accepted")
	}
}

func TestSystemProbeReadsLocalFilesystemCapacity(t *testing.T) {
	capacity, err := (SystemProbe{}).Probe(context.Background(), Definition{Kind: "local", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if capacity.TotalBytes == 0 || capacity.AvailableBytes == 0 || capacity.AvailableBytes > capacity.TotalBytes {
		t.Fatalf("capacity=%+v", capacity)
	}
}
