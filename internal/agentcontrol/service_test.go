package agentcontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestEnrollmentTokenCreatesOneAgent(t *testing.T) {
	now := time.Now().UTC()
	authority, err := NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := New(authority, func() time.Time { return now })
	token := service.NewEnrollmentToken(time.Minute)
	request := enrollmentRequest(t, "source-a")
	response, err := service.Enroll(context.Background(), token, request)
	if err != nil || response.CertificatePEM == "" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := service.Enroll(context.Background(), token, request); err == nil {
		t.Fatal("reused token accepted")
	}
}

func TestPersistentEnrollmentConsumesTokenAcrossServiceInstances(t *testing.T) {
	now := time.Now().UTC()
	authority, err := NewAuthority(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	storage := newFakeAgentStore()
	first := NewWithStore(authority, storage, func() time.Time { return now })
	token, err := first.CreateEnrollmentToken(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := enrollmentRequest(t, "source-persistent")
	second := NewWithStore(authority, storage, func() time.Time { return now })
	if _, err := second.Enroll(context.Background(), token, request); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Enroll(context.Background(), token, request); err == nil {
		t.Fatal("persisted token was reused")
	}
}

func enrollmentRequest(t *testing.T, agentID string) agentprotocol.EnrollmentRequest {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: agentID}}, private)
	if err != nil {
		t.Fatal(err)
	}
	return agentprotocol.EnrollmentRequest{Version: agentprotocol.Version, AgentID: agentID, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
}

type fakeAgentStore struct {
	tokens map[string]time.Time
	agents map[string]store.AgentRecord
}

func newFakeAgentStore() *fakeAgentStore {
	return &fakeAgentStore{tokens: map[string]time.Time{}, agents: map[string]store.AgentRecord{}}
}
func (f *fakeAgentStore) SaveAgentEnrollmentToken(_ context.Context, hash []byte, expires time.Time) error {
	f.tokens[string(hash)] = expires
	return nil
}
func (f *fakeAgentStore) ConsumeAgentEnrollmentToken(_ context.Context, hash []byte, at time.Time) error {
	expires, ok := f.tokens[string(hash)]
	if !ok || !expires.After(at) {
		return errors.New("not found")
	}
	delete(f.tokens, string(hash))
	return nil
}
func (f *fakeAgentStore) SaveAgent(_ context.Context, agent store.AgentRecord) error {
	f.agents[agent.ID] = agent
	return nil
}
