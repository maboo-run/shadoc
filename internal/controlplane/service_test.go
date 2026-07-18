package controlplane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type snapshotStub struct {
	data store.ControlPlaneSnapshotData
	err  error
}

func (s snapshotStub) ControlPlaneSnapshot(context.Context) (store.ControlPlaneSnapshotData, error) {
	return s.data, s.err
}

type secretReaderStub struct {
	values map[string][]byte
	calls  []string
	issued [][]byte
	err    error
}

func (s *secretReaderStub) Get(_ context.Context, id, purpose string) ([]byte, error) {
	s.calls = append(s.calls, id+":"+purpose)
	if s.err != nil {
		return nil, s.err
	}
	value := append([]byte(nil), s.values[id]...)
	s.issued = append(s.issued, value)
	return value, nil
}

type caSourceStub struct {
	material *AgentCAMaterial
	err      error
}

func (s caSourceStub) ExportAgentCA(context.Context) (*AgentCAMaterial, error) {
	return s.material, s.err
}

func TestServiceExportsStoreConfigurationSecretsAndAgentCA(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	host := domain.RemoteHost{ID: "host-a", Name: "host A", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "backup.example ssh-ed25519 AAAA", CreatedAt: now, UpdatedAt: now}
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.SFTPRepository, RemoteHostID: host.ID, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	snapshot := store.ControlPlaneSnapshotData{
		RemoteHosts:         []store.ControlPlaneRemoteHost{{Host: host, PrivateKeySecretID: "ssh-secret"}},
		Repositories:        []store.ControlPlaneRepository{{Repository: repository, PasswordSecretID: "repository-secret"}},
		DatabaseConnections: []store.ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{},
		ScheduleWatermarks: []store.ControlPlaneScheduleWatermark{}, Agents: []store.AgentRecord{}, Audits: []store.AuditRecord{},
		Ntfy: &store.ControlPlaneNtfy{BaseURL: "https://ntfy.example", Topic: "backup", TokenSecretID: "ntfy-secret", Enabled: true},
	}
	reader := &secretReaderStub{values: map[string][]byte{"ssh-secret": []byte("ssh-private-value"), "repository-secret": []byte("repository-password-value"), "ntfy-secret": []byte("ntfy-token-value")}}
	ca := validAgentCA(t, now)
	caPrivateKey := append([]byte(nil), ca.PrivateKeyPEM...)
	service := NewService(snapshotStub{data: snapshot}, reader, caSourceStub{material: &ca}, "1.2.3", func() time.Time { return now })
	service.kdf = testKDF()

	encoded, err := service.Export(context.Background(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ssh-private-value")) || bytes.Contains(encoded, []byte("repository-password-value")) || bytes.Contains(encoded, caPrivateKey) {
		t.Fatal("export leaked a protected value")
	}
	opened, err := OpenBundle(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Manifest.RemoteHosts) != 1 || len(opened.Manifest.Repositories) != 1 || opened.Manifest.Ntfy == nil || !opened.Manifest.Ntfy.HasToken {
		t.Fatalf("manifest = %+v", opened.Manifest)
	}
	if len(opened.Protected.Secrets) != 3 || opened.Protected.AgentCA == nil {
		t.Fatalf("protected payload = %+v", opened.Protected)
	}
	wantedCalls := []string{"ssh-secret:ssh-private-key", "repository-secret:repository-password", "ntfy-secret:ntfy-token"}
	if !equalStrings(reader.calls, wantedCalls) {
		t.Fatalf("secret calls = %v", reader.calls)
	}
	for _, issued := range reader.issued {
		if !allZero(issued) {
			t.Fatalf("decrypted secret buffer was not cleared: %q", issued)
		}
	}
}

func TestServiceClearsTransientAuthorityAndRuntimeObservations(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	task := domain.Task{ID: "task-a", Name: "task A", Engine: domain.ResticEngine, Kind: domain.DirectoryTask, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/source"}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "must-clear", Fingerprint: "fingerprint", ConfirmedBy: "admin", ConfirmedAt: now}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	restoreVerification := domain.RestoreVerificationPolicy{TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 168, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-time.Hour), UpdatedAt: now}
	snapshot := store.ControlPlaneSnapshotData{
		Repositories: []store.ControlPlaneRepository{{Repository: repository, PasswordSecretID: "repository-secret"}},
		RemoteHosts:  []store.ControlPlaneRemoteHost{}, DatabaseConnections: []store.ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{task},
		Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{}, RestoreVerificationPolicies: []domain.RestoreVerificationPolicy{restoreVerification}, ScheduleWatermarks: []store.ControlPlaneScheduleWatermark{},
		Agents: []store.AgentRecord{{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", LastHeartbeatAt: &now, CreatedAt: now}}, Audits: []store.AuditRecord{},
	}
	reader := &secretReaderStub{values: map[string][]byte{"repository-secret": []byte("repository-password")}}
	ca := validAgentCA(t, now)
	service := NewService(snapshotStub{data: snapshot}, reader, caSourceStub{material: &ca}, "1.2.3", func() time.Time { return now })
	service.kdf = testKDF()
	encoded, err := service.Export(context.Background(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenBundle(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest.Tasks[0].ScopeConfirmation.Present() {
		t.Fatalf("scope confirmation was exported: %+v", opened.Manifest.Tasks[0].ScopeConfirmation)
	}
	if len(opened.Manifest.RestoreVerificationPolicies) != 1 || opened.Manifest.RestoreVerificationPolicies[0].TaskID != task.ID {
		t.Fatalf("restore verification policy missing: %+v", opened.Manifest.RestoreVerificationPolicies)
	}
	if len(opened.Manifest.Agents) != 1 || opened.Manifest.Agents[0].Status != "online" {
		t.Fatalf("Agent identity missing: %+v", opened.Manifest.Agents)
	}
	manifestJSON, _ := json.Marshal(opened.Manifest)
	if bytes.Contains(manifestJSON, []byte("must-clear")) || bytes.Contains(manifestJSON, []byte("lastHeartbeat")) {
		t.Fatalf("transient runtime authority leaked: %s", manifestJSON)
	}
}

func TestServiceExportsS3CredentialsOnlyInsideProtectedPayload(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	repository := domain.Repository{
		ID: "repo-s3", Name: "object archive", Engine: domain.ResticEngine, Kind: domain.S3Repository, Path: "photos", Status: "ready",
		S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1", CredentialsConfigured: true}, CreatedAt: now, UpdatedAt: now,
	}
	credentials, err := s3backend.EncodeCredentials(s3backend.Credentials{AccessKey: "export-access-private", SecretKey: "export-secret-private"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.ControlPlaneSnapshotData{
		RemoteHosts: []store.ControlPlaneRemoteHost{}, Repositories: []store.ControlPlaneRepository{{Repository: repository, PasswordSecretID: "repository-secret", BackendSecretID: "backend-secret"}},
		DatabaseConnections: []store.ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{}, ScheduleWatermarks: []store.ControlPlaneScheduleWatermark{}, Agents: []store.AgentRecord{}, Audits: []store.AuditRecord{},
	}
	reader := &secretReaderStub{values: map[string][]byte{"repository-secret": []byte("repository-password"), "backend-secret": credentials}}
	service := NewService(snapshotStub{data: snapshot}, reader, nil, "1.2.3", func() time.Time { return now })
	service.kdf = testKDF()
	encoded, err := service.Export(context.Background(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("export-access-private")) || bytes.Contains(encoded, []byte("export-secret-private")) {
		t.Fatal("recovery document exposed plaintext S3 credentials")
	}
	opened, err := OpenBundle(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Protected.Secrets) != 2 || opened.Manifest.Repositories[0].BackendSecretID != "" {
		t.Fatalf("opened bundle=%+v", opened)
	}
	found := false
	for _, item := range opened.Protected.Secrets {
		found = found || item.Field == "s3_credentials" && item.Purpose == s3backend.CredentialPurpose
	}
	if !found {
		t.Fatalf("missing protected S3 credential: %+v", opened.Protected.Secrets)
	}
}

func TestServiceFailsClosedWhenSnapshotSecretOrCAFails(t *testing.T) {
	now := time.Now().UTC()
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	snapshot := store.ControlPlaneSnapshotData{RemoteHosts: []store.ControlPlaneRemoteHost{}, Repositories: []store.ControlPlaneRepository{{Repository: repository, PasswordSecretID: "repository-secret"}}, DatabaseConnections: []store.ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{}, ScheduleWatermarks: []store.ControlPlaneScheduleWatermark{}, Agents: []store.AgentRecord{}, Audits: []store.AuditRecord{}}
	service := NewService(snapshotStub{data: snapshot}, &secretReaderStub{err: errors.New("vault unavailable")}, nil, "1.2.3", func() time.Time { return now })
	service.kdf = testKDF()
	if _, err := service.Export(context.Background(), "correct horse battery staple"); err == nil || !bytes.Contains([]byte(err.Error()), []byte("repository")) {
		t.Fatalf("secret failure = %v", err)
	}
	service = NewService(snapshotStub{data: snapshot}, &secretReaderStub{values: map[string][]byte{"repository-secret": []byte("password")}}, caSourceStub{err: errors.New("read CA failed")}, "1.2.3", func() time.Time { return now })
	service.kdf = testKDF()
	if _, err := service.Export(context.Background(), "correct horse battery staple"); err == nil || !bytes.Contains([]byte(err.Error()), []byte("Agent CA")) {
		t.Fatalf("CA failure = %v", err)
	}
}

func TestAgentCAFileSourceIsReadOnlyAndRequiresPrivatePermissions(t *testing.T) {
	directory := t.TempDir()
	source := AgentCAFileSource{Directory: directory}
	material, err := source.ExportAgentCA(context.Background())
	if err != nil || material != nil {
		t.Fatalf("absent CA material=%v err=%v", material, err)
	}
	valid := validAgentCA(t, time.Now().UTC())
	if err := os.WriteFile(filepath.Join(directory, "ca.crt"), valid.CertificatePEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ca.key"), valid.PrivateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	material, err = source.ExportAgentCA(context.Background())
	if err != nil || material == nil {
		t.Fatalf("valid CA material=%v err=%v", material, err)
	}
	if err := os.Chmod(filepath.Join(directory, "ca.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExportAgentCA(context.Background()); err == nil || !bytes.Contains([]byte(err.Error()), []byte("permissions")) {
		t.Fatalf("permissive key error=%v", err)
	}

	recovery := AgentCAFileSource{Directory: filepath.Join(t.TempDir(), "agent-pki")}
	if conflict, err := recovery.AgentCAConflict(context.Background()); err != nil || conflict {
		t.Fatalf("fresh target conflict=%v err=%v", conflict, err)
	}
	rollback, err := recovery.InstallAgentCA(context.Background(), valid)
	if err != nil {
		t.Fatal(err)
	}
	if conflict, err := recovery.AgentCAConflict(context.Background()); err != nil || !conflict {
		t.Fatalf("installed target conflict=%v err=%v", conflict, err)
	}
	keyInfo, err := os.Stat(filepath.Join(recovery.Directory, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("restored key mode=%v", keyInfo.Mode().Perm())
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if conflict, err := recovery.AgentCAConflict(context.Background()); err != nil || conflict {
		t.Fatalf("rolled-back target conflict=%v err=%v", conflict, err)
	}
}

func validAgentCA(t *testing.T, now time.Time) AgentCAMaterial {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "restic-control-agent-ca"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return AgentCAMaterial{CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
