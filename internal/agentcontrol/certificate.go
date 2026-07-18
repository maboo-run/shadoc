package agentcontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

type Authority struct {
	certificate *x509.Certificate
	signer      ed25519.PrivateKey
	now         func() time.Time
}

func NewAuthority(now func() time.Time) (*Authority, error) {
	if now == nil {
		now = time.Now
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	issued := now().UTC()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "restic-control-agent-ca"}, NotBefore: issued.Add(-time.Minute), NotAfter: issued.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{certificate: certificate, signer: private, now: now}, nil
}

func (a *Authority) Certificate() *x509.Certificate { return a.certificate }

func (a *Authority) PEM() []byte {
	if a == nil || a.certificate == nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.certificate.Raw})
}

func (a *Authority) IssueClient(agentID string, public ed25519.PublicKey, lifetime time.Duration) ([]byte, error) {
	if a == nil || a.certificate == nil || len(a.signer) == 0 {
		return nil, errors.New("agent certificate authority is required")
	}
	if agentID == "" || len(public) == 0 || lifetime <= 0 {
		return nil, errors.New("agent id, public key, and certificate lifetime are required")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	issued := a.now().UTC()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: agentID}, NotBefore: issued.Add(-time.Minute), NotAfter: issued.Add(lifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, public, a.signer)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func parseCertificate(value []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate PEM is required")
	}
	return x509.ParseCertificate(block.Bytes)
}
