package agentruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentcontrol"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
)

func TestHTTPControlRenewsCertificateWithExistingKeyAndAtomicCredentialReplacement(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	authority, err := agentcontrol.NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldCertificate, err := authority.IssueClient("agent-a", public, 20*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	for path, content := range map[string][]byte{
		agentKeyFile: keyPEM, agentCertFile: oldCertificate, agentCAFile: authority.PEM(),
	} {
		if err := os.WriteFile(filepath.Join(directory, path), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transport := roundTripFunc(func(request *http.Request) *http.Response {
		var renewal agentprotocol.RenewalRequest
		if err := json.NewDecoder(request.Body).Decode(&renewal); err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode([]byte(renewal.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.PublicKey.(ed25519.PublicKey), public) {
			t.Fatalf("renewal CSR did not use the existing key: err=%v", err)
		}
		certificatePEM, err := authority.IssueClient("agent-a", csr.PublicKey.(ed25519.PublicKey), 365*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, _ := json.Marshal(agentprotocol.RenewalResponse{Version: 1, CertificatePEM: string(certificatePEM), NotAfter: now.Add(365 * 24 * time.Hour)})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(responseBody))}
	})
	control, err := NewHTTPControl("https://control.example:9443", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.EnableCertificateRenewal(directory); err != nil {
		t.Fatal(err)
	}
	oldExpiry, err := control.CertificateNotAfter()
	if err != nil || !oldExpiry.Equal(now.Add(20*24*time.Hour)) {
		t.Fatalf("old expiry=%s err=%v", oldExpiry, err)
	}
	newExpiry, err := control.RenewCertificate(t.Context(), "agent-a")
	if err != nil || !newExpiry.Equal(now.Add(365*24*time.Hour)) {
		t.Fatalf("new expiry=%s err=%v", newExpiry, err)
	}
	storedKey, err := os.ReadFile(filepath.Join(directory, agentKeyFile))
	if err != nil || !bytes.Equal(storedKey, keyPEM) {
		t.Fatalf("private key changed or became unreadable: err=%v", err)
	}
	storedCertificate, err := os.ReadFile(filepath.Join(directory, agentCertFile))
	if err != nil || bytes.Equal(storedCertificate, oldCertificate) {
		t.Fatalf("certificate was not replaced: err=%v", err)
	}
	info, err := os.Stat(filepath.Join(directory, agentCertFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("certificate mode=%v err=%v", info.Mode(), err)
	}
}

func TestRuntimeRenewsWithinThirtyDaysBeforeSendingHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	runtime := New("agent-a", nil, func() time.Time { return now })
	runtime.SetRuntimeInfo(agentprotocol.RuntimeInfo{BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64"})
	control := &renewingControl{expires: now.Add(29 * 24 * time.Hour), renewedExpiry: now.Add(365 * 24 * time.Hour)}
	if err := runtime.Step(t.Context(), control, nil); err != nil {
		t.Fatal(err)
	}
	if !control.renewed || control.heartbeat.Runtime.RenewalStatus != "healthy" {
		t.Fatalf("renewed=%v heartbeat=%+v", control.renewed, control.heartbeat)
	}
}

type renewingControl struct {
	heartbeatRecordingControl
	expires       time.Time
	renewedExpiry time.Time
	renewed       bool
}

func (c *renewingControl) CertificateNotAfter() (time.Time, error) { return c.expires, nil }
func (c *renewingControl) RenewCertificate(context.Context, string) (time.Time, error) {
	c.renewed = true
	c.expires = c.renewedExpiry
	return c.expires, nil
}
