package agentcontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestAuthorityIssuesClientCertificateBoundToAgent(t *testing.T) {
	authority, err := NewAuthority(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := authority.IssueClient("agent-source", public, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseCertificate(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "agent-source" || !cert.NotAfter.After(time.Now()) {
		t.Fatalf("certificate=%+v", cert)
	}
	if err := cert.CheckSignatureFrom(authority.Certificate()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		t.Fatalf("public key=%T", cert.PublicKey)
	}
}
