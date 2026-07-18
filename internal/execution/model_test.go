package execution

import "testing"

func TestZeroTargetNormalizesToLocal(t *testing.T) {
	got := Target{}.Normalized()
	if got.Kind != Local || got.AgentID != "" {
		t.Fatalf("target=%+v", got)
	}
}

func TestAgentTargetRequiresExactlyOneAgentID(t *testing.T) {
	missingAgentID := Target{Kind: Agent}
	if err := missingAgentID.Validate(); err == nil {
		t.Fatal("missing agent id accepted")
	}
	localWithAgentID := Target{Kind: Local, AgentID: "agent-1"}
	if err := localWithAgentID.Validate(); err == nil {
		t.Fatal("local target accepted agent id")
	}
	validAgent := Target{Kind: Agent, AgentID: "agent-1"}
	if err := validAgent.Validate(); err != nil {
		t.Fatalf("valid agent target: %v", err)
	}
}
