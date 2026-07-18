package agentcontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestRenewalKeepsOldCertificateUntilNewCertificateHeartbeats(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	authority, err := NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithStore(authority, storage, func() time.Time { return now })
	token, err := service.CreateEnrollmentToken(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldRequest := certificateRequest(t, "agent-a")
	oldEnrollment, err := service.Enroll(t.Context(), token, agentprotocol.EnrollmentRequest{Version: oldRequest.Version, AgentID: oldRequest.AgentID, CSRPEM: oldRequest.CSRPEM})
	if err != nil {
		t.Fatal(err)
	}
	oldCertificate := parseTestCertificate(t, oldEnrollment.CertificatePEM)
	_ = oldKey

	_, renewalRequest := certificateRequest(t, "agent-a")
	renewed, err := service.Renew(t.Context(), "agent-a", renewalRequest)
	if err != nil {
		t.Fatal(err)
	}
	newCertificate := parseTestCertificate(t, renewed.CertificatePEM)
	if newCertificate.SerialNumber.Cmp(oldCertificate.SerialNumber) == 0 || !newCertificate.NotAfter.Equal(now.Add(365*24*time.Hour)) {
		t.Fatalf("renewed certificate=%+v", newCertificate)
	}
	for _, serial := range []string{oldCertificate.SerialNumber.String(), newCertificate.SerialNumber.String()} {
		usable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", serial, now)
		if err != nil || !usable {
			t.Fatalf("certificate %s usable=%v err=%v", serial, usable, err)
		}
	}
	heartbeat := agentprotocol.Heartbeat{Version: 1, AgentID: "agent-a", Runtime: agentprotocol.RuntimeInfo{BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64"}}
	if err := service.HeartbeatAuthenticated(context.Background(), heartbeat, newCertificate); err != nil {
		t.Fatal(err)
	}
	oldUsable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", oldCertificate.SerialNumber.String(), now)
	if err != nil || oldUsable {
		t.Fatalf("old certificate remained usable=%v err=%v", oldUsable, err)
	}
}

func TestContinuouslyOnlineAgentCanRollCertificatesBeyondOneYear(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := started
	storage, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	authority, err := NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithStore(authority, storage, func() time.Time { return now })
	token, err := service.CreateEnrollmentToken(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, initialRequest := certificateRequest(t, "agent-a")
	if _, err := service.Enroll(t.Context(), token, agentprotocol.EnrollmentRequest{Version: 1, AgentID: "agent-a", CSRPEM: initialRequest.CSRPEM}); err != nil {
		t.Fatal(err)
	}
	heartbeat := agentprotocol.Heartbeat{Version: 1, AgentID: "agent-a", Runtime: agentprotocol.RuntimeInfo{BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64"}}
	for _, elapsedDays := range []int{335, 670} {
		now = started.Add(time.Duration(elapsedDays) * 24 * time.Hour)
		_, request := certificateRequest(t, "agent-a")
		response, err := service.Renew(t.Context(), "agent-a", request)
		if err != nil {
			t.Fatalf("renew at day %d: %v", elapsedDays, err)
		}
		certificate := parseTestCertificate(t, response.CertificatePEM)
		if err := service.HeartbeatAuthenticated(t.Context(), heartbeat, certificate); err != nil {
			t.Fatalf("activate at day %d: %v", elapsedDays, err)
		}
	}
	now = started.Add(730 * 24 * time.Hour)
	agents, err := storage.ListAgents(t.Context())
	if err != nil || len(agents) != 1 || agents[0].CertificateNotAfter == nil || !agents[0].CertificateNotAfter.After(now) {
		t.Fatalf("Agent did not retain a future certificate after two years: agents=%+v err=%v", agents, err)
	}
	usable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", agents[0].CertificateSerial, now)
	if err != nil || !usable {
		t.Fatalf("two-year Agent certificate usable=%v err=%v", usable, err)
	}
}

func TestEnrollmentTokenCannotReplaceAnActiveAgentIdentity(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	authority, err := NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithStore(authority, storage, func() time.Time { return now })
	firstToken, _ := service.CreateEnrollmentToken(t.Context(), time.Minute)
	_, firstCSR := certificateRequest(t, "agent-a")
	first, err := service.Enroll(t.Context(), firstToken, agentprotocol.EnrollmentRequest{Version: 1, AgentID: "agent-a", CSRPEM: firstCSR.CSRPEM})
	if err != nil {
		t.Fatal(err)
	}
	firstCertificate := parseTestCertificate(t, first.CertificatePEM)
	secondToken, _ := service.CreateEnrollmentToken(t.Context(), time.Minute)
	_, secondCSR := certificateRequest(t, "agent-a")
	if _, err := service.Enroll(t.Context(), secondToken, agentprotocol.EnrollmentRequest{Version: 1, AgentID: "agent-a", CSRPEM: secondCSR.CSRPEM}); err == nil {
		t.Fatal("active Agent identity was silently replaced by enrollment")
	}
	usable, err := storage.AgentCertificateUsable(t.Context(), "agent-a", firstCertificate.SerialNumber.String(), now)
	if err != nil || !usable {
		t.Fatalf("original identity usable=%v err=%v", usable, err)
	}
}

func certificateRequest(t *testing.T, agentID string) (ed25519.PrivateKey, agentprotocol.RenewalRequest) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: agentID}}, private)
	if err != nil {
		t.Fatal(err)
	}
	return private, agentprotocol.RenewalRequest{Version: agentprotocol.Version, AgentID: agentID, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
}

func parseTestCertificate(t *testing.T, value string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		t.Fatal("certificate PEM missing")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
