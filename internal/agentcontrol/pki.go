package agentcontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	caCertificateFile = "ca.crt"
	caKeyFile         = "ca.key"
	serverCertFile    = "server.crt"
	serverKeyFile     = "server.key"
)

func LoadOrCreateAuthority(dir string, now func() time.Time) (*Authority, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	certificatePath := filepath.Join(dir, caCertificateFile)
	keyPath := filepath.Join(dir, caKeyFile)
	certificatePEM, certificateErr := os.ReadFile(certificatePath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certificateErr == nil && keyErr == nil {
		if err := os.Chmod(keyPath, 0o600); err != nil {
			return nil, err
		}
		return authorityFromPEM(certificatePEM, keyPEM, now)
	}
	if (!errors.Is(certificateErr, os.ErrNotExist) && certificateErr != nil) || (!errors.Is(keyErr, os.ErrNotExist) && keyErr != nil) {
		return nil, errors.Join(certificateErr, keyErr)
	}
	if !errors.Is(certificateErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return nil, errors.New("incomplete agent certificate authority files")
	}
	authority, err := NewAuthority(now)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(authority.signer)
	if err != nil {
		return nil, err
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certificatePath, authority.PEM(), 0o644); err != nil {
		return nil, err
	}
	return authority, nil
}

func authorityFromPEM(certificatePEM, keyPEM []byte, now func() time.Time) (*Authority, error) {
	certificate, err := parseCertificate(certificatePEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("agent CA private key PEM is required")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("agent CA private key must use Ed25519")
	}
	if now == nil {
		now = time.Now
	}
	return &Authority{certificate: certificate, signer: signer, now: now}, nil
}

func LoadOrCreateServerCertificate(dir string, authority *Authority, listen string, names []string, now func() time.Time) (tls.Certificate, error) {
	certificatePath := filepath.Join(dir, serverCertFile)
	keyPath := filepath.Join(dir, serverKeyFile)
	if certificatePEM, certificateErr := os.ReadFile(certificatePath); certificateErr == nil {
		keyPEM, keyErr := os.ReadFile(keyPath)
		if keyErr != nil {
			return tls.Certificate{}, keyErr
		}
		loaded, loadErr := tls.X509KeyPair(certificatePEM, keyPEM)
		if loadErr == nil && certificateMatchesNames(loaded, names) {
			return loaded, nil
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	issued := time.Now().UTC()
	if now != nil {
		issued = now().UTC()
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "restic-control-agent-service"}, NotBefore: issued.Add(-time.Minute), NotAfter: issued.AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return tls.Certificate{}, err
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else if host != "" && net.ParseIP(host) == nil {
		template.DNSNames = append(template.DNSNames, host)
	} else {
		if len(names) == 0 {
			return tls.Certificate{}, errors.New("agent server certificate names are required for an unspecified listen address")
		}
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if strings.TrimSpace(name) != "" {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority.certificate, public, authority.signer)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certificatePath, certificatePEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certificatePEM, keyPEM)
}

func certificateMatchesNames(certificate tls.Certificate, names []string) bool {
	if len(certificate.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || time.Until(leaf.NotAfter) < 30*24*time.Hour {
		return false
	}
	for _, name := range names {
		if err := leaf.VerifyHostname(name); err != nil {
			return false
		}
	}
	return true
}

func ServerTLSConfig(authority *Authority, certificate tls.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(authority.Certificate())
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}
}

func writePrivateFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
