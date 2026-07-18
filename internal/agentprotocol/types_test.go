package agentprotocol

import (
	"strings"
	"testing"
	"time"
)

func TestAssignmentRejectsExpiredOrAgentMismatchedWork(t *testing.T) {
	at := time.Now()
	if err := (Assignment{Version: Version, AgentID: "agent-a", ID: "lease", TaskID: "task", Engine: "restic", ExpiresAt: at.Add(time.Hour)}).ValidateFor("agent-b", at); err == nil {
		t.Fatal("mismatched agent accepted")
	}
	if err := (Assignment{Version: Version, AgentID: "agent-a", ID: "lease", TaskID: "task", Engine: "restic", ExpiresAt: at.Add(-time.Second)}).ValidateFor("agent-a", at); err == nil {
		t.Fatal("expired assignment accepted")
	}
}

func TestHeartbeatRequiresBoundedStructuredRuntimeMetadata(t *testing.T) {
	valid := Heartbeat{
		Version: Version, AgentID: "agent-a", Capabilities: []string{"restic", "filesystem-browse"},
		Runtime: RuntimeInfo{
			BuildVersion: "v1.4.0", ProtocolMin: Version, ProtocolMax: Version,
			OS: "linux", Arch: "arm64", ResticVersion: "0.18.0", RsyncVersion: "3.4.1",
			ServiceURL: "https://control.example:9443",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid heartbeat rejected: %v", err)
	}

	invalid := []Heartbeat{
		{Version: Version, AgentID: "agent-a", Runtime: RuntimeInfo{ProtocolMin: 2, ProtocolMax: 1, OS: "linux", Arch: "amd64"}},
		{Version: Version, AgentID: "agent-a", Runtime: RuntimeInfo{ProtocolMin: 1, ProtocolMax: 1, OS: "plan9", Arch: "amd64"}},
		{Version: Version, AgentID: "agent-a", Runtime: RuntimeInfo{ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "mips"}},
		{Version: Version, AgentID: "agent-a", Runtime: RuntimeInfo{BuildVersion: strings.Repeat("x", 129), ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64"}},
		{Version: Version, AgentID: "agent-a", Capabilities: make([]string, 65), Runtime: RuntimeInfo{ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64"}},
	}
	for index, heartbeat := range invalid {
		if err := heartbeat.Validate(); err == nil {
			t.Fatalf("invalid heartbeat %d accepted: %+v", index, heartbeat)
		}
	}
}

func TestCertificateRenewalRequestRequiresMatchingCSRIdentity(t *testing.T) {
	if err := (RenewalRequest{Version: Version, AgentID: "agent-a", CSRPEM: "certificate request"}).Validate(); err != nil {
		t.Fatalf("structurally valid renewal request rejected: %v", err)
	}
	for _, request := range []RenewalRequest{
		{},
		{Version: Version + 1, AgentID: "agent-a", CSRPEM: "certificate request"},
		{Version: Version, AgentID: "bad id", CSRPEM: "certificate request"},
		{Version: Version, AgentID: "agent-a"},
		{Version: Version, AgentID: "agent-a", CSRPEM: strings.Repeat("x", 1<<20+1)},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid renewal request accepted: %+v", request)
		}
	}
}
