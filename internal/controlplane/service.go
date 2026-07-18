package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type SnapshotStore interface {
	ControlPlaneSnapshot(context.Context) (store.ControlPlaneSnapshotData, error)
}

type SecretReader interface {
	Get(context.Context, string, string) ([]byte, error)
}

type AgentCAExporter interface {
	ExportAgentCA(context.Context) (*AgentCAMaterial, error)
}

type Service struct {
	store      SnapshotStore
	secrets    SecretReader
	authority  AgentCAExporter
	version    string
	now        func() time.Time
	kdf        KDFWorkFactor
	tools      ImportToolChecker
	caRecovery AgentCARecovery
}

func NewService(storage SnapshotStore, secrets SecretReader, authority AgentCAExporter, version string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{store: storage, secrets: secrets, authority: authority, version: strings.TrimSpace(version), now: now}
	if recovery, ok := authority.(AgentCARecovery); ok {
		service.caRecovery = recovery
	}
	return service
}

func (s *Service) SetImportToolChecker(checker ImportToolChecker) { s.tools = checker }
func (s *Service) SetAgentCARecovery(recovery AgentCARecovery)    { s.caRecovery = recovery }

func (s *Service) Export(ctx context.Context, passphrase string) ([]byte, error) {
	if s == nil || s.store == nil || s.secrets == nil {
		return nil, errors.New("control-plane export dependencies are unavailable")
	}
	snapshot, err := s.store.ControlPlaneSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot control-plane configuration: %w", err)
	}
	manifest := manifestFromSnapshot(snapshot)
	protected := ProtectedPayload{Secrets: make([]ProtectedSecret, 0, len(snapshot.RemoteHosts)+len(snapshot.Repositories)+len(snapshot.DatabaseConnections)+1)}
	defer clearProtectedPayload(&protected)
	for _, item := range snapshot.RemoteHosts {
		value, err := s.secrets.Get(ctx, item.PrivateKeySecretID, "ssh-private-key")
		if err != nil {
			return nil, fmt.Errorf("read private key for remote host %q: %w", item.Host.ID, err)
		}
		protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "remote_host", ResourceID: item.Host.ID, Field: "private_key", Purpose: "ssh-private-key", Value: value})
	}
	for _, item := range snapshot.Repositories {
		if item.Repository.EffectiveEngine() != domain.ResticEngine {
			continue
		}
		value, err := s.secrets.Get(ctx, item.PasswordSecretID, "repository-password")
		if err != nil {
			return nil, fmt.Errorf("read password for repository %q: %w", item.Repository.ID, err)
		}
		protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "repository", ResourceID: item.Repository.ID, Field: "password", Purpose: "repository-password", Value: value})
		if item.Repository.EffectiveKind() == domain.S3Repository {
			value, err := s.secrets.Get(ctx, item.BackendSecretID, s3backend.CredentialPurpose)
			if err != nil {
				return nil, fmt.Errorf("read S3 credentials for repository %q: %w", item.Repository.ID, err)
			}
			protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "repository", ResourceID: item.Repository.ID, Field: "s3_credentials", Purpose: s3backend.CredentialPurpose, Value: value})
		}
	}
	for _, item := range snapshot.DatabaseConnections {
		purpose := "database-" + string(item.Connection.Purpose) + "-password"
		value, err := s.secrets.Get(ctx, item.PasswordSecretID, purpose)
		if err != nil {
			return nil, fmt.Errorf("read password for database connection %q: %w", item.Connection.ID, err)
		}
		protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "database_connection", ResourceID: item.Connection.ID, Field: "password", Purpose: purpose, Value: value})
	}
	if snapshot.Ntfy != nil && snapshot.Ntfy.TokenSecretID != "" {
		value, err := s.secrets.Get(ctx, snapshot.Ntfy.TokenSecretID, "ntfy-token")
		if err != nil {
			return nil, fmt.Errorf("read ntfy token: %w", err)
		}
		protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "notification", ResourceID: "ntfy", Field: "token", Purpose: "ntfy-token", Value: value})
	}
	if snapshot.Webhook != nil && snapshot.Webhook.SecretID != "" {
		value, err := s.secrets.Get(ctx, snapshot.Webhook.SecretID, notificationconfig.WebhookSecretPurpose)
		if err != nil {
			return nil, fmt.Errorf("read webhook authentication secret: %w", err)
		}
		protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "notification", ResourceID: "webhook", Field: "auth_secret", Purpose: notificationconfig.WebhookSecretPurpose, Value: value})
	}
	if s.authority != nil {
		material, err := s.authority.ExportAgentCA(ctx)
		if err != nil {
			return nil, fmt.Errorf("read Agent CA material: %w", err)
		}
		protected.AgentCA = material
	}
	return SealBundle(manifest, protected, SealOptions{Passphrase: passphrase, CreatedAt: s.now().UTC(), SourceApplicationVersion: s.version, KDF: s.kdf})
}

func manifestFromSnapshot(snapshot store.ControlPlaneSnapshotData) Manifest {
	manifest := Manifest{
		RemoteHosts: make([]domain.RemoteHost, 0, len(snapshot.RemoteHosts)), Repositories: make([]domain.Repository, 0, len(snapshot.Repositories)),
		DatabaseConnections: make([]domain.DatabaseConnection, 0, len(snapshot.DatabaseConnections)), Tasks: append([]domain.Task(nil), snapshot.Tasks...),
		Plans: append([]domain.Plan(nil), snapshot.Plans...), MaintenancePolicies: append([]domain.MaintenancePolicy(nil), snapshot.MaintenancePolicies...), RestoreVerificationPolicies: append([]domain.RestoreVerificationPolicy(nil), snapshot.RestoreVerificationPolicies...),
		LifecyclePolicy:    LifecyclePolicy{RunDays: snapshot.LifecyclePolicy.RunDays, RawLogDays: snapshot.LifecyclePolicy.RawLogDays, AuditDays: snapshot.LifecyclePolicy.AuditDays, RawLogMaxBytes: snapshot.LifecyclePolicy.RawLogMaxBytes},
		ScheduleWatermarks: make([]ScheduleWatermark, 0, len(snapshot.ScheduleWatermarks)), Agents: make([]AgentIdentity, 0, len(snapshot.Agents)), Audits: make([]AuditEntry, 0, len(snapshot.Audits)),
	}
	for _, item := range snapshot.RemoteHosts {
		manifest.RemoteHosts = append(manifest.RemoteHosts, item.Host)
	}
	for _, item := range snapshot.Repositories {
		repository := item.Repository
		repository.Capacity, repository.LastRun, repository.NextRun = nil, nil, ""
		manifest.Repositories = append(manifest.Repositories, repository)
	}
	for _, item := range snapshot.DatabaseConnections {
		manifest.DatabaseConnections = append(manifest.DatabaseConnections, item.Connection)
	}
	for index := range manifest.Tasks {
		manifest.Tasks[index].ScopeConfirmation = domain.TaskScopeConfirmation{}
	}
	for _, item := range snapshot.ScheduleWatermarks {
		manifest.ScheduleWatermarks = append(manifest.ScheduleWatermarks, ScheduleWatermark{OwnerKind: item.OwnerKind, OwnerID: item.OwnerID, ScheduledAt: item.ScheduledAt, ObservedAt: item.ObservedAt, Mode: item.Mode, Status: item.Status})
	}
	for _, item := range snapshot.Agents {
		manifest.Agents = append(manifest.Agents, AgentIdentity{ID: item.ID, RemoteHostID: item.RemoteHostID, CertificateSerial: item.CertificateSerial, CertificateNotAfter: item.CertificateNotAfter, Capabilities: append([]string(nil), item.Capabilities...), Status: item.Status, CreatedAt: item.CreatedAt, RevokedAt: item.RevokedAt})
	}
	if snapshot.AgentServiceSettings != nil {
		manifest.AgentServiceSettings = &AgentServiceSettings{Enabled: snapshot.AgentServiceSettings.Enabled, ListenHost: snapshot.AgentServiceSettings.ListenHost, Port: snapshot.AgentServiceSettings.Port, AdvertisedHost: snapshot.AgentServiceSettings.AdvertisedHost, TLSNames: append([]string(nil), snapshot.AgentServiceSettings.TLSNames...)}
	}
	if snapshot.Ntfy != nil {
		manifest.Ntfy = &NtfySettings{BaseURL: snapshot.Ntfy.BaseURL, Topic: snapshot.Ntfy.Topic, Enabled: snapshot.Ntfy.Enabled, HasToken: snapshot.Ntfy.TokenSecretID != ""}
	}
	if snapshot.Webhook != nil {
		manifest.Webhook = &WebhookSettings{Endpoint: snapshot.Webhook.Endpoint, AuthMode: snapshot.Webhook.AuthMode, Enabled: snapshot.Webhook.EnabledValue(), HasSecret: snapshot.Webhook.SecretID != ""}
	}
	for _, item := range snapshot.Audits {
		manifest.Audits = append(manifest.Audits, AuditEntry{OccurredAt: item.OccurredAt, Actor: item.Actor, Action: item.Action, TargetType: item.TargetType, TargetID: item.TargetID, Detail: item.Detail})
	}
	return manifest
}

func clearProtectedPayload(payload *ProtectedPayload) {
	if payload == nil {
		return
	}
	for index := range payload.Secrets {
		clearBytes(payload.Secrets[index].Value)
	}
	if payload.AgentCA != nil {
		clearBytes(payload.AgentCA.PrivateKeyPEM)
	}
}

// AgentCAFileSource reads the Service's dedicated Agent CA without creating or
// modifying PKI files. An absent CA is valid; an incomplete CA fails closed.
type AgentCAFileSource struct{ Directory string }

func (source AgentCAFileSource) ExportAgentCA(ctx context.Context) (*AgentCAMaterial, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	certificatePath := filepath.Join(source.Directory, "ca.crt")
	keyPath := filepath.Join(source.Directory, "ca.key")
	certificate, certificateErr := os.ReadFile(certificatePath)
	key, keyErr := os.ReadFile(keyPath)
	if errors.Is(certificateErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		return nil, nil
	}
	if certificateErr != nil || keyErr != nil {
		return nil, errors.New("Agent CA files are incomplete or unreadable")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Agent CA private key permissions must not allow group or other access")
	}
	material := &AgentCAMaterial{CertificatePEM: certificate, PrivateKeyPEM: key}
	if err := validateAgentCA(*material); err != nil {
		clearBytes(key)
		return nil, err
	}
	return material, nil
}

func (source AgentCAFileSource) AgentCAConflict(ctx context.Context) (bool, error) {
	if strings.TrimSpace(source.Directory) == "" {
		return false, errors.New("Agent CA directory is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, name := range []string{"ca.crt", "ca.key", "server.crt", "server.key"} {
		_, err := os.Stat(filepath.Join(source.Directory, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func (source AgentCAFileSource) InstallAgentCA(ctx context.Context, material AgentCAMaterial) (func() error, error) {
	if err := validateAgentCA(material); err != nil {
		return nil, err
	}
	conflict, err := source.AgentCAConflict(ctx)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, errors.New("target Agent CA material already exists")
	}
	if err := os.MkdirAll(source.Directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(source.Directory, 0o700); err != nil {
		return nil, err
	}
	keyPath, certificatePath := filepath.Join(source.Directory, "ca.key"), filepath.Join(source.Directory, "ca.crt")
	if err := writeRecoveryFile(keyPath, material.PrivateKeyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeRecoveryFile(certificatePath, material.CertificatePEM, 0o644); err != nil {
		_ = os.Remove(keyPath)
		return nil, err
	}
	rollback := func() error {
		certificateErr := os.Remove(certificatePath)
		if errors.Is(certificateErr, os.ErrNotExist) {
			certificateErr = nil
		}
		keyErr := os.Remove(keyPath)
		if errors.Is(keyErr, os.ErrNotExist) {
			keyErr = nil
		}
		return errors.Join(certificateErr, keyErr)
	}
	return rollback, nil
}

func writeRecoveryFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
