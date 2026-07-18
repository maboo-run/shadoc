package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestEnabledAgentTaskRequiresFreshCompatibleHeartbeatAndCapability(t *testing.T) {
	server := newResourceTestServer(t)
	storage := server.store.(*store.Store)
	now := time.Now().UTC()
	task := domain.Task{
		Engine: domain.RsyncEngine, Enabled: true,
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"},
	}
	agent := store.AgentRecord{
		ID: "agent-a", CertificateSerial: "serial-a", Status: "online", Capabilities: []string{"rsync"},
		BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64",
		LastHeartbeatAt: pointerTime(now), CreatedAt: now.Add(-time.Hour),
	}
	if err := storage.SaveAgent(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	if err := validateTaskActivation(t.Context(), storage, task); err != nil {
		t.Fatalf("fresh compatible Agent rejected: %v", err)
	}

	agent.LastHeartbeatAt = pointerTime(now.Add(-3 * time.Minute))
	if err := storage.SaveAgent(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	if err := validateTaskActivation(t.Context(), storage, task); err == nil || !strings.Contains(err.Error(), "离线") {
		t.Fatalf("stale Agent activation error=%v", err)
	}

	agent.LastHeartbeatAt = pointerTime(now)
	agent.ProtocolMin, agent.ProtocolMax = 2, 2
	if err := storage.SaveAgent(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	if err := validateTaskActivation(t.Context(), storage, task); err == nil || !strings.Contains(err.Error(), "协议") {
		t.Fatalf("incompatible Agent activation error=%v", err)
	}

	task.Enabled = false
	if err := validateTaskActivation(t.Context(), storage, task); err != nil {
		t.Fatalf("incompatible Agent must remain saveable as a draft: %v", err)
	}
}

func TestAgentListExposesStructuredCompatibilityAndCertificateHealth(t *testing.T) {
	server := newResourceTestServer(t)
	server.applicationVersion = "v1.4.0"
	cookie := setupSession(t, server)
	now := time.Now().UTC()
	expires := now.Add(20 * 24 * time.Hour)
	storage := server.store.(*store.Store)
	if err := storage.SaveSecret(t.Context(), "host-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRemoteHost(t.Context(), domain.RemoteHost{ID: "host-a", Name: "host A", Host: "host-a.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "host-key"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveAgent(t.Context(), store.AgentRecord{
		ID: "agent-a", RemoteHostID: "host-a", CertificateSerial: "serial-a", CertificateNotAfter: &expires,
		Capabilities: []string{"restic"}, BuildVersion: "v1.3.0", ProtocolMin: 1, ProtocolMax: 1,
		OS: "linux", Arch: "arm64", ResticVersion: "0.18.0", ServiceURL: "https://old-control.example:9443",
		Status: "online", LastHeartbeatAt: &now, CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, server, http.MethodGet, "/api/agents", nil, cookie)
	var agents []struct {
		BuildVersion        string `json:"buildVersion"`
		ProtocolCompatible  bool   `json:"protocolCompatible"`
		CompatibilityStatus string `json:"compatibilityStatus"`
		CertificateStatus   string `json:"certificateStatus"`
		CertificateNotAfter string `json:"certificateNotAfter"`
		ResticVersion       string `json:"resticVersion"`
		Platform            string `json:"platform"`
		UpgradeAvailable    bool   `json:"upgradeAvailable"`
		TargetVersion       string `json:"targetVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(agents) != 1 || agents[0].BuildVersion != "v1.3.0" || !agents[0].ProtocolCompatible || agents[0].CompatibilityStatus != "compatible" {
		t.Fatalf("agents=%+v status=%d body=%s", agents, response.Code, response.Body.String())
	}
	if agents[0].CertificateStatus != "expiring_30" || agents[0].CertificateNotAfter == "" || agents[0].ResticVersion != "0.18.0" || agents[0].Platform != "linux/arm64" || !agents[0].UpgradeAvailable || agents[0].TargetVersion != "v1.4.0" {
		t.Fatalf("runtime health=%+v", agents[0])
	}
}

func TestAgentListTaskEligibilityRequiresReportedBackupEngine(t *testing.T) {
	server := newResourceTestServer(t)
	cookie := setupSession(t, server)
	now := time.Now().UTC()
	if err := server.store.(*store.Store).SaveAgent(t.Context(), store.AgentRecord{
		ID: "filesystem-only", CertificateSerial: "serial-filesystem", Status: "online",
		Capabilities: []string{"filesystem-browse", "repository-capacity"},
		BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64",
		LastHeartbeatAt: &now, CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, server, http.MethodGet, "/api/agents", nil, cookie)
	var agents []struct {
		CompatibilityStatus string `json:"compatibilityStatus"`
		TaskEligible        bool   `json:"taskEligible"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(agents) != 1 || agents[0].CompatibilityStatus != "compatible" || agents[0].TaskEligible {
		t.Fatalf("agents=%+v status=%d body=%s", agents, response.Code, response.Body.String())
	}
}

func TestAgentListOffersSameVersionRepairForManagedLinuxAgentMissingResticInstallCapability(t *testing.T) {
	server := newResourceTestServer(t)
	server.applicationVersion = "v1.4.0"
	cookie := setupSession(t, server)
	now := time.Now().UTC()
	storage := server.store.(*store.Store)
	if err := storage.SaveSecret(t.Context(), "host-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRemoteHost(t.Context(), domain.RemoteHost{ID: "host-a", Name: "host A", Host: "host-a.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "host-key"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveAgent(t.Context(), store.AgentRecord{
		ID: "agent-a", RemoteHostID: "host-a", CertificateSerial: "serial-a", Status: "online",
		Capabilities: []string{"filesystem-browse"}, BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1,
		OS: "linux", Arch: "amd64", LastHeartbeatAt: &now, CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, server, http.MethodGet, "/api/agents", nil, cookie)
	var agents []struct {
		Capabilities     []string `json:"capabilities"`
		UpgradeAvailable bool     `json:"upgradeAvailable"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(agents) != 1 || !agents[0].UpgradeAvailable || slices.Contains(agents[0].Capabilities, agentprotocol.ManagedResticInstallCapability) {
		t.Fatalf("agents=%+v status=%d body=%s", agents, response.Code, response.Body.String())
	}
}

func pointerTime(value time.Time) *time.Time { return &value }
