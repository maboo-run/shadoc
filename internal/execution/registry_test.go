package execution

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegistryReturnsRegisteredEngine(t *testing.T) {
	engine := fakeEngine{kind: "restic"}
	registry := NewRegistry(engine)

	got, err := registry.Engine("restic")
	if err != nil {
		t.Fatal(err)
	}
	if got != engine {
		t.Fatalf("engine=%T, want %T", got, engine)
	}
}

func TestRegistryRejectsUnknownEngine(t *testing.T) {
	registry := NewRegistry(fakeEngine{kind: "restic"})
	if _, err := registry.Engine("rsync"); err == nil {
		t.Fatal("unknown engine accepted")
	}
}

type fakeEngine struct{ kind EngineKind }

func (f fakeEngine) Kind() EngineKind { return f.kind }

func (fakeEngine) Validate(json.RawMessage) error { return nil }

func (fakeEngine) Run(context.Context, Assignment) (Outcome, error) { return Outcome{}, nil }
