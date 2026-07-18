package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentHeartbeatPersistsStructuredRuntimeAndCertificateFacts(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	certificateExpiry := now.Add(365 * 24 * time.Hour)
	if err := storage.SaveAgent(t.Context(), AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "offline", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordAgentHeartbeat(t.Context(), AgentHeartbeat{
		ID: "agent-a", CertificateSerial: "serial-a", CertificateNotAfter: certificateExpiry,
		Capabilities: []string{"restic", "rsync"}, BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1,
		OS: "linux", Arch: "arm64", ResticVersion: "0.18.0", RsyncVersion: "3.4.1",
		ServiceURL: "https://control.example:9443", RenewalStatus: "healthy", ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	agents, err := storage.ListAgents(t.Context())
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	agent := agents[0]
	if agent.BuildVersion != "v1.4.0" || agent.ProtocolMin != 1 || agent.ProtocolMax != 1 || agent.OS != "linux" || agent.Arch != "arm64" {
		t.Fatalf("runtime metadata not persisted: %+v", agent)
	}
	if agent.ResticVersion != "0.18.0" || agent.RsyncVersion != "3.4.1" || agent.ServiceURL != "https://control.example:9443" || agent.RenewalStatus != "healthy" {
		t.Fatalf("tool/connection metadata not persisted: %+v", agent)
	}
	if agent.CertificateNotAfter == nil || !agent.CertificateNotAfter.Equal(certificateExpiry) || agent.LastHeartbeatAt == nil || !agent.LastHeartbeatAt.Equal(now) {
		t.Fatalf("certificate or heartbeat facts not persisted: %+v", agent)
	}
	active, err := storage.AgentCertificateUsable(t.Context(), agent.ID, "serial-a", now)
	if err != nil || !active {
		t.Fatalf("current certificate active=%v err=%v", active, err)
	}
}

func TestOpenBackfillsLegacyAgentCertificateAsActiveUntilObserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-agent-certificate.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  certificate_serial TEXT NOT NULL UNIQUE,
  capabilities_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'offline',
  last_heartbeat_at TEXT,
  created_at TEXT NOT NULL,
  revoked_at TEXT
);
INSERT INTO agents VALUES ('legacy-agent','legacy-serial','[]','offline',NULL,'2026-07-01T00:00:00Z',NULL);
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	active, err := storage.AgentCertificateUsable(context.Background(), "legacy-agent", "legacy-serial", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || !active {
		t.Fatalf("legacy certificate active=%v err=%v", active, err)
	}
	var status string
	var notAfter sql.NullString
	if err := storage.db.QueryRow(`SELECT status,not_after FROM agent_certificates WHERE serial='legacy-serial'`).Scan(&status, &notAfter); err != nil {
		t.Fatal(err)
	}
	if status != "active" || notAfter.Valid {
		t.Fatalf("backfill status=%q notAfter=%v", status, notAfter)
	}
}

func TestPendingAgentCertificatePreservesOldIdentityUntilNewHeartbeat(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	oldExpiry, newExpiry := now.Add(20*24*time.Hour), now.Add(365*24*time.Hour)
	if err := storage.SaveAgent(t.Context(), AgentRecord{ID: "agent-a", CertificateSerial: "old-serial", CertificateNotAfter: &oldExpiry, Status: "online", CreatedAt: now.Add(-300 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := storage.SavePendingAgentCertificate(t.Context(), "agent-a", "new-serial", now.Add(-time.Minute), newExpiry, now); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []string{"old-serial", "new-serial"} {
		usable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", serial, now)
		if err != nil || !usable {
			t.Fatalf("certificate %s usable=%v err=%v", serial, usable, err)
		}
	}
	if err := storage.RecordAgentHeartbeat(t.Context(), AgentHeartbeat{
		ID: "agent-a", CertificateSerial: "new-serial", CertificateNotAfter: newExpiry,
		BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64", ObservedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	oldUsable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", "old-serial", now.Add(time.Minute))
	if err != nil || oldUsable {
		t.Fatalf("old certificate usable=%v err=%v", oldUsable, err)
	}
	newUsable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", "new-serial", now.Add(time.Minute))
	if err != nil || !newUsable {
		t.Fatalf("new certificate usable=%v err=%v", newUsable, err)
	}
	agents, _ := storage.ListAgents(t.Context())
	if len(agents) != 1 || agents[0].CertificateSerial != "new-serial" || agents[0].CertificateNotAfter == nil || !agents[0].CertificateNotAfter.Equal(newExpiry) {
		t.Fatalf("activated Agent=%+v", agents)
	}
}

func TestRecoverInterruptedAgentDrainsResumesAssignments(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := storage.SaveAgent(t.Context(), AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := storage.BeginAgentDrain(t.Context(), "agent-a", now); err != nil {
		t.Fatal(err)
	}
	recovered, err := storage.RecoverInterruptedAgentDrains(t.Context())
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	agents, err := storage.ListAgents(t.Context())
	if err != nil || len(agents) != 1 || agents[0].DrainingAt != nil {
		t.Fatalf("Agent drain was not cleared: agents=%+v err=%v", agents, err)
	}
}
