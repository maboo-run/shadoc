package agentcontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/store"
)

const ClientCertificateLifetime = 365 * 24 * time.Hour

type AgentStore interface {
	SaveAgentEnrollmentToken(context.Context, []byte, time.Time) error
	ConsumeAgentEnrollmentToken(context.Context, []byte, time.Time) error
	SaveAgent(context.Context, store.AgentRecord) error
}

type Service struct {
	authority *Authority
	now       func() time.Time
	mu        sync.Mutex
	tokens    map[[32]byte]time.Time
	agents    map[string]struct{}
	storage   AgentStore
	hydrate   func(context.Context, store.AgentLease) (json.RawMessage, error)
}

func (s *Service) SetAssignmentHydrator(hydrate func(context.Context, store.AgentLease) (json.RawMessage, error)) {
	s.hydrate = hydrate
}

func NewWithStore(authority *Authority, storage AgentStore, now func() time.Time) *Service {
	service := New(authority, now)
	service.storage = storage
	return service
}

func New(authority *Authority, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{authority: authority, now: now, tokens: map[[32]byte]time.Time{}, agents: map[string]struct{}{}}
}

func (s *Service) CAPEM() string {
	if s == nil || s.authority == nil {
		return ""
	}
	return string(s.authority.PEM())
}

func (s *Service) NewEnrollmentToken(lifetime time.Duration) string {
	token, err := s.CreateEnrollmentToken(context.Background(), lifetime)
	if err != nil {
		panic(err)
	}
	return token
}

func (s *Service) CreateEnrollmentToken(ctx context.Context, lifetime time.Duration) (string, error) {
	if lifetime <= 0 {
		return "", errors.New("agent enrollment token lifetime must be positive")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	expires := s.now().UTC().Add(lifetime)
	if s.storage != nil {
		if err := s.storage.SaveAgentEnrollmentToken(ctx, hash[:], expires); err != nil {
			return "", err
		}
		return token, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[hash] = expires
	return token, nil
}

func (s *Service) Enroll(ctx context.Context, token string, request agentprotocol.EnrollmentRequest) (agentprotocol.EnrollmentResponse, error) {
	if s.authority == nil || request.Version != agentprotocol.Version || strings.TrimSpace(request.AgentID) == "" {
		return agentprotocol.EnrollmentResponse{}, errors.New("invalid agent enrollment request")
	}
	block, _ := pem.Decode([]byte(request.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return agentprotocol.EnrollmentResponse{}, errors.New("agent certificate request is required")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != request.AgentID {
		return agentprotocol.EnrollmentResponse{}, errors.New("invalid agent certificate request")
	}
	public, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return agentprotocol.EnrollmentResponse{}, errors.New("agent certificate request must use ed25519")
	}
	hash := sha256.Sum256([]byte(token))
	if s.storage != nil {
		if err := s.storage.ConsumeAgentEnrollmentToken(ctx, hash[:], s.now().UTC()); err != nil {
			return agentprotocol.EnrollmentResponse{}, errors.New("invalid or expired enrollment token")
		}
	} else {
		s.mu.Lock()
		expires, exists := s.tokens[hash]
		if !exists || !expires.After(s.now()) {
			s.mu.Unlock()
			return agentprotocol.EnrollmentResponse{}, errors.New("invalid or expired enrollment token")
		}
		delete(s.tokens, hash)
		if _, exists := s.agents[request.AgentID]; exists {
			s.mu.Unlock()
			return agentprotocol.EnrollmentResponse{}, errors.New("agent is already enrolled")
		}
		s.agents[request.AgentID] = struct{}{}
		s.mu.Unlock()
	}
	certificate, err := s.authority.IssueClient(request.AgentID, public, ClientCertificateLifetime)
	if err != nil {
		return agentprotocol.EnrollmentResponse{}, err
	}
	parsed, err := parseCertificate(certificate)
	if err != nil {
		return agentprotocol.EnrollmentResponse{}, err
	}
	if s.storage != nil {
		certificateNotAfter := parsed.NotAfter.UTC()
		record := store.AgentRecord{ID: request.AgentID, CertificateSerial: parsed.SerialNumber.String(), CertificateNotAfter: &certificateNotAfter, Status: "offline", CreatedAt: s.now().UTC()}
		var saveErr error
		if enrollmentStore, ok := s.storage.(interface {
			EnrollAgent(context.Context, store.AgentRecord) error
		}); ok {
			saveErr = enrollmentStore.EnrollAgent(ctx, record)
		} else {
			saveErr = s.storage.SaveAgent(ctx, record)
		}
		if saveErr != nil {
			return agentprotocol.EnrollmentResponse{}, saveErr
		}
	}
	return agentprotocol.EnrollmentResponse{Version: agentprotocol.Version, CertificatePEM: string(certificate), CAPEM: string(s.authority.PEM())}, nil
}

func (s *Service) Renew(ctx context.Context, authenticatedAgentID string, request agentprotocol.RenewalRequest) (agentprotocol.RenewalResponse, error) {
	if s == nil || s.authority == nil || request.Validate() != nil || request.AgentID != authenticatedAgentID {
		return agentprotocol.RenewalResponse{}, errors.New("invalid Agent certificate renewal request")
	}
	block, rest := pem.Decode([]byte(request.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(strings.TrimSpace(string(rest))) != 0 {
		return agentprotocol.RenewalResponse{}, errors.New("Agent certificate request is required")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != authenticatedAgentID {
		return agentprotocol.RenewalResponse{}, errors.New("invalid Agent certificate request")
	}
	public, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return agentprotocol.RenewalResponse{}, errors.New("Agent certificate request must use ed25519")
	}
	certificatePEM, err := s.authority.IssueClient(authenticatedAgentID, public, ClientCertificateLifetime)
	if err != nil {
		return agentprotocol.RenewalResponse{}, err
	}
	certificate, err := parseCertificate(certificatePEM)
	if err != nil {
		return agentprotocol.RenewalResponse{}, err
	}
	storage, ok := s.storage.(interface {
		SavePendingAgentCertificate(context.Context, string, string, time.Time, time.Time, time.Time) error
	})
	if !ok {
		return agentprotocol.RenewalResponse{}, errors.New("persistent Agent certificate storage is required")
	}
	if err := storage.SavePendingAgentCertificate(ctx, authenticatedAgentID, certificate.SerialNumber.String(), certificate.NotBefore.UTC(), certificate.NotAfter.UTC(), s.now().UTC()); err != nil {
		return agentprotocol.RenewalResponse{}, err
	}
	return agentprotocol.RenewalResponse{Version: agentprotocol.Version, CertificatePEM: string(certificatePEM), NotAfter: certificate.NotAfter.UTC()}, nil
}

func (s *Service) Authenticate(ctx context.Context, agentID, serial string) (bool, error) {
	if storage, ok := s.storage.(interface {
		AgentCertificateUsable(context.Context, string, string, time.Time) (bool, error)
	}); ok {
		return storage.AgentCertificateUsable(ctx, agentID, serial, s.now().UTC())
	}
	storage, ok := s.storage.(interface {
		AgentCertificateActive(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, errors.New("persistent agent store is required")
	}
	return storage.AgentCertificateActive(ctx, agentID, serial)
}

func (s *Service) Heartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	if err := heartbeat.Validate(); err != nil {
		return err
	}
	storage, ok := s.storage.(interface {
		HeartbeatAgent(context.Context, string, []string, time.Time) error
	})
	if !ok {
		return errors.New("persistent agent store is required")
	}
	return storage.HeartbeatAgent(ctx, heartbeat.AgentID, heartbeat.Capabilities, s.now().UTC())
}

func (s *Service) HeartbeatAuthenticated(ctx context.Context, heartbeat agentprotocol.Heartbeat, certificate *x509.Certificate) error {
	if err := heartbeat.Validate(); err != nil {
		return err
	}
	if certificate == nil || certificate.Subject.CommonName != heartbeat.AgentID || certificate.SerialNumber == nil {
		return errors.New("authenticated Agent certificate is required")
	}
	storage, ok := s.storage.(interface {
		RecordAgentHeartbeat(context.Context, store.AgentHeartbeat) error
	})
	if !ok {
		return s.Heartbeat(ctx, heartbeat)
	}
	return storage.RecordAgentHeartbeat(ctx, store.AgentHeartbeat{
		ID: heartbeat.AgentID, CertificateSerial: certificate.SerialNumber.String(), CertificateNotAfter: certificate.NotAfter.UTC(),
		Capabilities: heartbeat.Capabilities, BuildVersion: heartbeat.Runtime.BuildVersion,
		ProtocolMin: heartbeat.Runtime.ProtocolMin, ProtocolMax: heartbeat.Runtime.ProtocolMax,
		OS: heartbeat.Runtime.OS, Arch: heartbeat.Runtime.Arch, ResticVersion: heartbeat.Runtime.ResticVersion, RsyncVersion: heartbeat.Runtime.RsyncVersion,
		ServiceURL: heartbeat.Runtime.ServiceURL, RenewalStatus: heartbeat.Runtime.RenewalStatus, ObservedAt: s.now().UTC(),
	})
}

func (s *Service) Lease(ctx context.Context, agentID string) (agentprotocol.Assignment, error) {
	storage, ok := s.storage.(interface {
		ClaimAgentLease(context.Context, string, time.Time) (store.AgentLease, error)
	})
	if !ok {
		return agentprotocol.Assignment{}, errors.New("persistent agent store is required")
	}
	lease, err := storage.ClaimAgentLease(ctx, agentID, s.now().UTC())
	if err != nil {
		return agentprotocol.Assignment{}, err
	}
	if s.hydrate != nil {
		lease.Definition, err = s.hydrate(ctx, lease)
		if err != nil {
			failure := agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: lease.ID, AgentID: agentID, Status: "failed", Error: err.Error()}
			_ = s.Complete(context.WithoutCancel(ctx), failure)
			return agentprotocol.Assignment{}, err
		}
	}
	return agentprotocol.Assignment{Version: agentprotocol.Version, ID: lease.ID, AgentID: lease.AgentID, TaskID: lease.TaskID, Engine: lease.Engine, Definition: lease.Definition, ExpiresAt: lease.ExpiresAt}, nil
}

func (s *Service) Complete(ctx context.Context, result agentprotocol.Result) error {
	storage, ok := s.storage.(interface {
		CompleteAgentLease(context.Context, string, string, string, json.RawMessage, time.Time) error
	})
	if !ok {
		return errors.New("persistent agent lease storage is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return storage.CompleteAgentLease(ctx, result.AssignmentID, result.AgentID, result.Status, encoded, s.now().UTC())
}

func (s *Service) ClaimFilesystem(ctx context.Context, agentID string) (agentprotocol.Assignment, error) {
	storage, ok := s.storage.(interface {
		ClaimAgentFilesystemRequest(context.Context, string, time.Time) (store.AgentFilesystemRequest, error)
	})
	if !ok {
		return agentprotocol.Assignment{}, errors.New("filesystem request storage is unavailable")
	}
	request, err := storage.ClaimAgentFilesystemRequest(ctx, agentID, s.now().UTC())
	if err != nil {
		return agentprotocol.Assignment{}, err
	}
	return agentprotocol.Assignment{Version: agentprotocol.Version, ID: request.ID, AgentID: agentID, TaskID: "filesystem", Engine: "agent-filesystem", Definition: request.Definition, ExpiresAt: request.ExpiresAt}, nil
}

func (s *Service) CompleteFilesystem(ctx context.Context, result agentprotocol.Result) error {
	storage, ok := s.storage.(interface {
		CompleteAgentFilesystemRequest(context.Context, string, string, string, json.RawMessage, time.Time) error
	})
	if !ok {
		return errors.New("filesystem request storage is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return storage.CompleteAgentFilesystemRequest(ctx, result.AssignmentID, result.AgentID, result.Status, encoded, s.now().UTC())
}

func (s *Service) ClaimRestore(ctx context.Context, agentID string) (agentprotocol.Assignment, error) {
	storage, ok := s.storage.(interface {
		ClaimAgentRestoreRequest(context.Context, string, time.Time) (store.AgentRestoreRequest, error)
	})
	if !ok {
		return agentprotocol.Assignment{}, errors.New("Agent restore request storage is unavailable")
	}
	request, err := storage.ClaimAgentRestoreRequest(ctx, agentID, s.now().UTC())
	if err != nil {
		return agentprotocol.Assignment{}, err
	}
	definition := request.Definition
	if s.hydrate != nil {
		definition, err = s.hydrate(ctx, store.AgentLease{ID: request.ID, AgentID: agentID, TaskID: "restore", Engine: "restic-restore", Definition: request.Definition, ExpiresAt: request.ExpiresAt})
		if err != nil {
			failure := agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: request.ID, AgentID: agentID, Status: "failed", Error: err.Error()}
			_ = s.CompleteRestore(context.WithoutCancel(ctx), failure)
			return agentprotocol.Assignment{}, err
		}
	}
	return agentprotocol.Assignment{Version: agentprotocol.Version, ID: request.ID, AgentID: agentID, TaskID: "restore", Engine: "restic-restore", Definition: definition, ExpiresAt: request.ExpiresAt}, nil
}

func (s *Service) CompleteRestore(ctx context.Context, result agentprotocol.Result) error {
	storage, ok := s.storage.(interface {
		CompleteAgentRestoreRequest(context.Context, string, string, string, json.RawMessage, time.Time) error
	})
	if !ok {
		return errors.New("Agent restore request storage is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return storage.CompleteAgentRestoreRequest(ctx, result.AssignmentID, result.AgentID, result.Status, encoded, s.now().UTC())
}
