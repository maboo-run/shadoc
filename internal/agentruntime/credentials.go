package agentruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
)

const (
	agentKeyFile  = "agent.key"
	agentCertFile = "agent.crt"
	agentCAFile   = "ca.crt"
)

func Enroll(ctx context.Context, serviceURL, agentID, token string, caPEM []byte, dataDir string) error {
	if agentID == "" || token == "" || len(caPEM) == 0 || dataDir == "" {
		return errors.New("service URL, agent ID, enrollment token, CA, and data directory are required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: agentID}}, private)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("valid agent service CA PEM is required")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}}
	requestBody, err := json.Marshal(agentprotocol.EnrollmentRequest{Version: agentprotocol.Version, Token: token, AgentID: agentID, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serviceURL, "/")+"/enroll", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return errors.New("agent enrollment was rejected")
	}
	var enrolled agentprotocol.EnrollmentResponse
	if err := json.NewDecoder(response.Body).Decode(&enrolled); err != nil {
		return err
	}
	if enrolled.Version != agentprotocol.Version || enrolled.CertificatePEM == "" {
		return errors.New("invalid agent enrollment response")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return err
	}
	files := []struct {
		name string
		body []byte
		mode os.FileMode
	}{{agentKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600}, {agentCertFile, []byte(enrolled.CertificatePEM), 0o600}, {agentCAFile, caPEM, 0o644}}
	_ = public
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(dataDir, file.name), file.body, file.mode); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(dataDir, file.name), file.mode); err != nil {
			return err
		}
	}
	return nil
}

func MTLSHTTPClient(dataDir string) (*http.Client, error) {
	if _, err := tls.LoadX509KeyPair(filepath.Join(dataDir, agentCertFile), filepath.Join(dataDir, agentKeyFile)); err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(dataDir, agentCAFile))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid stored agent CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certificate, err := tls.LoadX509KeyPair(filepath.Join(dataDir, agentCertFile), filepath.Join(dataDir, agentKeyFile))
			return &certificate, err
		},
	}}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}

func (c *HTTPControl) EnableCertificateRenewal(dataDir string) error {
	if c == nil || strings.TrimSpace(dataDir) == "" {
		return errors.New("Agent credential directory is required")
	}
	c.credentialDir = filepath.Clean(dataDir)
	if _, err := c.CertificateNotAfter(); err != nil {
		c.credentialDir = ""
		return err
	}
	return nil
}

func (c *HTTPControl) CertificateNotAfter() (time.Time, error) {
	if c == nil || c.credentialDir == "" {
		return time.Time{}, errors.New("Agent certificate renewal is not configured")
	}
	certificate, err := readAgentCertificate(filepath.Join(c.credentialDir, agentCertFile))
	if err != nil {
		return time.Time{}, err
	}
	return certificate.NotAfter.UTC(), nil
}

func (c *HTTPControl) RenewCertificate(ctx context.Context, agentID string) (time.Time, error) {
	if c == nil || c.credentialDir == "" || strings.TrimSpace(agentID) == "" {
		return time.Time{}, errors.New("Agent certificate renewal is not configured")
	}
	private, err := readAgentPrivateKey(filepath.Join(c.credentialDir, agentKeyFile))
	if err != nil {
		return time.Time{}, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: agentID}}, private)
	if err != nil {
		return time.Time{}, err
	}
	request := agentprotocol.RenewalRequest{
		Version: agentprotocol.Version, AgentID: agentID,
		CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
	}
	var response agentprotocol.RenewalResponse
	if err := c.post(ctx, "/renew", request, &response, http.StatusOK); err != nil {
		return time.Time{}, err
	}
	certificate, err := validateRenewedCertificate(c.credentialDir, agentID, private, response)
	if err != nil {
		return time.Time{}, err
	}
	if err := replaceCredentialFile(filepath.Join(c.credentialDir, agentCertFile), []byte(response.CertificatePEM), 0o600); err != nil {
		return time.Time{}, err
	}
	if closer, ok := c.client.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return certificate.NotAfter.UTC(), nil
}

func readAgentCertificate(path string) (*x509.Certificate, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("stored Agent certificate is invalid")
	}
	return x509.ParseCertificate(block.Bytes)
}

func readAgentPrivateKey(path string) (ed25519.PrivateKey, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("stored Agent private key is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("stored Agent private key must use Ed25519")
	}
	return private, nil
}

func validateRenewedCertificate(dataDir, agentID string, private ed25519.PrivateKey, response agentprotocol.RenewalResponse) (*x509.Certificate, error) {
	if response.Version != agentprotocol.Version || response.CertificatePEM == "" || response.NotAfter.IsZero() {
		return nil, errors.New("invalid Agent certificate renewal response")
	}
	block, rest := pem.Decode([]byte(response.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("renewed Agent certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(public, private.Public().(ed25519.PublicKey)) || certificate.Subject.CommonName != agentID || !certificate.NotAfter.Equal(response.NotAfter) {
		return nil, errors.New("renewed Agent certificate does not match its identity and private key")
	}
	caPEM, err := os.ReadFile(filepath.Join(dataDir, agentCAFile))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("stored Agent CA is invalid")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fmt.Errorf("verify renewed Agent certificate: %w", err)
	}
	return certificate, nil
}

func replaceCredentialFile(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-renewing-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		removeTemporary = false
		return nil
	}
	backup := path + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	removeTemporary = false
	if err := os.Chmod(path, mode); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
