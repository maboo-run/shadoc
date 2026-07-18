package agentcontrol

import (
	"bytes"
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadOrCreateAuthorityPersistsIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	first, err := LoadOrCreateAuthority(dir, time.Now)
	if err != nil {
		t.Fatalf("create authority: %v", err)
	}
	second, err := LoadOrCreateAuthority(dir, time.Now)
	if err != nil {
		t.Fatalf("load authority: %v", err)
	}
	if !bytes.Equal(first.Certificate().Raw, second.Certificate().Raw) {
		t.Fatal("authority identity changed after reload")
	}
	info, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %o", info.Mode().Perm())
	}
}

func TestLoadOrCreateServerCertificateProducesCAAuthenticatedTLSIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	authority, err := LoadOrCreateAuthority(dir, time.Now)
	if err != nil {
		t.Fatalf("authority: %v", err)
	}
	certificate, err := LoadOrCreateServerCertificate(dir, authority, "127.0.0.1:9443", nil, time.Now)
	if err != nil {
		t.Fatalf("server certificate: %v", err)
	}
	if len(certificate.Certificate) == 0 {
		t.Fatal("server certificate chain is empty")
	}
	config := ServerTLSConfig(authority, certificate)
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("unexpected TLS config: min=%d auth=%d", config.MinVersion, config.ClientAuth)
	}
}
