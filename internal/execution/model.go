// Package execution defines the engine-neutral task execution contract.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TargetKind string

const (
	Local TargetKind = "local"
	Agent TargetKind = "agent"
)

type Target struct {
	Kind    TargetKind `json:"kind"`
	AgentID string     `json:"agentId,omitempty"`
}

func (t Target) Normalized() Target {
	if t.Kind == "" {
		t.Kind = Local
	}
	return t
}

func (t Target) Validate() error {
	t = t.Normalized()
	switch t.Kind {
	case Local:
		if t.AgentID != "" {
			return errors.New("local execution target cannot reference an agent")
		}
	case Agent:
		if strings.TrimSpace(t.AgentID) == "" {
			return errors.New("agent execution target requires an agent id")
		}
	default:
		return fmt.Errorf("unsupported execution target %q", t.Kind)
	}
	return nil
}

type EngineKind string

type Assignment struct {
	ID         string          `json:"id"`
	TaskID     string          `json:"taskId"`
	Engine     EngineKind      `json:"engine"`
	Target     Target          `json:"target"`
	Definition json.RawMessage `json:"definition"`
	ExpiresAt  time.Time       `json:"expiresAt"`
}

type Outcome struct {
	Status     string         `json:"status"`
	SnapshotID string         `json:"snapshotId,omitempty"`
	Summary    map[string]any `json:"summary,omitempty"`
	RawLog     string         `json:"rawLog,omitempty"`
}

type Engine interface {
	Kind() EngineKind
	Validate(json.RawMessage) error
	Run(context.Context, Assignment) (Outcome, error)
}

type Registry interface {
	Engine(EngineKind) (Engine, error)
}
