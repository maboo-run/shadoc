package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/store"
)

const importPreviewLifetime = 15 * time.Minute

var ErrControlPlaneImportConflict = errors.New("control-plane import conflicts with existing target configuration")

type controlPlaneImportStore interface {
	SaveControlPlaneImportPreview(context.Context, store.ControlPlaneImportPreview) error
	ImportControlPlane(context.Context, store.ControlPlaneImportRequest) error
}

type SecretVault interface {
	SecretReader
	Put(context.Context, string, []byte) (string, error)
	Delete(context.Context, string) error
}

type ImportToolChecker interface {
	MissingTools(context.Context, Manifest) ([]MissingTool, error)
}

type AgentCARecovery interface {
	AgentCAConflict(context.Context) (bool, error)
	InstallAgentCA(context.Context, AgentCAMaterial) (rollback func() error, err error)
}

type MissingTool struct {
	Tool       string   `json:"tool"`
	Path       string   `json:"path,omitempty"`
	RequiredBy []string `json:"requiredBy"`
}

type ImportConflict struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId,omitempty"`
	Field        string `json:"field"`
	Value        string `json:"value,omitempty"`
	ExistingID   string `json:"existingId,omitempty"`
}

type RevalidationItem struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Action       string `json:"action"`
}

type ImportPreview struct {
	PreviewID                string             `json:"previewId,omitempty"`
	ExpiresAt                time.Time          `json:"expiresAt,omitempty"`
	CanImport                bool               `json:"canImport"`
	SourceApplicationVersion string             `json:"sourceApplicationVersion"`
	ResourceCounts           map[string]int     `json:"resourceCounts"`
	Conflicts                []ImportConflict   `json:"conflicts"`
	MissingTools             []MissingTool      `json:"missingTools"`
	Revalidation             []RevalidationItem `json:"revalidation"`
	ExcludedTransientClasses []string           `json:"excludedTransientClasses"`
	RestartRequired          bool               `json:"restartRequired"`
	Warnings                 []string           `json:"warnings"`
}

type ImportResult struct {
	ImportedCounts  map[string]int     `json:"importedCounts"`
	Revalidation    []RevalidationItem `json:"revalidation"`
	RestartRequired bool               `json:"restartRequired"`
}

func (s *Service) PreflightImport(ctx context.Context, encoded []byte, passphrase string) (ImportPreview, error) {
	storage, ok := s.store.(controlPlaneImportStore)
	if !ok {
		return ImportPreview{}, errors.New("control-plane import storage is unavailable")
	}
	opened, err := OpenBundle(encoded, passphrase)
	if err != nil {
		return ImportPreview{}, err
	}
	defer clearProtectedPayload(&opened.Protected)
	current, err := s.store.ControlPlaneSnapshot(ctx)
	if err != nil {
		return ImportPreview{}, fmt.Errorf("inspect target control plane: %w", err)
	}
	conflicts, err := s.importConflicts(ctx, current, opened)
	if err != nil {
		return ImportPreview{}, err
	}
	missing := []MissingTool{}
	if s.tools != nil {
		missing, err = s.tools.MissingTools(ctx, opened.Manifest)
		if err != nil {
			return ImportPreview{}, fmt.Errorf("check target tools: %w", err)
		}
	}
	for index := range missing {
		missing[index].RequiredBy = append([]string(nil), missing[index].RequiredBy...)
		sort.Strings(missing[index].RequiredBy)
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i].Tool+"\x00"+missing[i].Path < missing[j].Tool+"\x00"+missing[j].Path
	})
	preview := ImportPreview{
		CanImport: len(conflicts) == 0, SourceApplicationVersion: opened.Header.SourceApplicationVersion,
		ResourceCounts: opened.Header.ResourceCounts, Conflicts: conflicts, MissingTools: missing,
		Revalidation: revalidationItems(opened.Manifest), ExcludedTransientClasses: append([]string(nil), opened.Header.ExcludedTransientClasses...),
		RestartRequired: opened.Protected.AgentCA != nil, Warnings: []string{},
	}
	if len(missing) > 0 {
		preview.Warnings = append(preview.Warnings, "one or more required tools must be installed before revalidation")
	}
	if preview.RestartRequired {
		preview.Warnings = append(preview.Warnings, "the control service must restart before imported Agent identities can be re-enabled")
	}
	if !preview.CanImport {
		return preview, nil
	}
	previewID, err := randomImportPreviewID()
	if err != nil {
		return ImportPreview{}, err
	}
	now := s.now().UTC()
	preview.PreviewID, preview.ExpiresAt = previewID, now.Add(importPreviewLifetime)
	hash := sha256.Sum256(encoded)
	if err := storage.SaveControlPlaneImportPreview(ctx, store.ControlPlaneImportPreview{ID: previewID, BundleSHA256: hex.EncodeToString(hash[:]), CreatedAt: now, ExpiresAt: preview.ExpiresAt}); err != nil {
		return ImportPreview{}, fmt.Errorf("save control-plane import preview: %w", err)
	}
	return preview, nil
}

func (s *Service) Import(ctx context.Context, encoded []byte, passphrase, previewID string) (ImportResult, error) {
	storage, ok := s.store.(controlPlaneImportStore)
	if !ok {
		return ImportResult{}, errors.New("control-plane import storage is unavailable")
	}
	vault, ok := s.secrets.(SecretVault)
	if !ok {
		return ImportResult{}, errors.New("target secret vault does not support recovery import")
	}
	opened, err := OpenBundle(encoded, passphrase)
	if err != nil {
		return ImportResult{}, err
	}
	defer clearProtectedPayload(&opened.Protected)
	current, err := s.store.ControlPlaneSnapshot(ctx)
	if err != nil {
		return ImportResult{}, fmt.Errorf("inspect target control plane: %w", err)
	}
	conflicts, err := s.importConflicts(ctx, current, opened)
	if err != nil {
		return ImportResult{}, err
	}
	if len(conflicts) != 0 {
		return ImportResult{}, ErrControlPlaneImportConflict
	}
	secretIDs := map[string]string{}
	createdSecrets := make([]string, 0, len(opened.Protected.Secrets))
	rollbackSecrets := func(cause error) error {
		failures := []error{cause}
		for index := len(createdSecrets) - 1; index >= 0; index-- {
			if err := vault.Delete(context.WithoutCancel(ctx), createdSecrets[index]); err != nil {
				failures = append(failures, fmt.Errorf("roll back imported secret: %w", err))
			}
		}
		return errors.Join(failures...)
	}
	for _, item := range opened.Protected.Secrets {
		id, err := vault.Put(ctx, item.Purpose, item.Value)
		if err != nil {
			return ImportResult{}, rollbackSecrets(fmt.Errorf("restore protected secret for %s: %w", secretReference(item), err))
		}
		createdSecrets = append(createdSecrets, id)
		secretIDs[secretReference(item)] = id
	}
	var rollbackCA func() error
	if opened.Protected.AgentCA != nil {
		if s.caRecovery == nil {
			return ImportResult{}, rollbackSecrets(errors.New("Agent CA recovery is unavailable"))
		}
		rollbackCA, err = s.caRecovery.InstallAgentCA(ctx, *opened.Protected.AgentCA)
		if err != nil {
			return ImportResult{}, rollbackSecrets(fmt.Errorf("restore Agent CA: %w", err))
		}
	}
	hash := sha256.Sum256(encoded)
	request := importRequest(opened.Manifest, secretIDs, previewID, hex.EncodeToString(hash[:]), s.now().UTC())
	if err := storage.ImportControlPlane(ctx, request); err != nil {
		failures := []error{err}
		if rollbackCA != nil {
			if rollbackErr := rollbackCA(); rollbackErr != nil {
				failures = append(failures, fmt.Errorf("roll back Agent CA: %w", rollbackErr))
			}
		}
		return ImportResult{}, rollbackSecrets(errors.Join(failures...))
	}
	return ImportResult{ImportedCounts: opened.Header.ResourceCounts, Revalidation: revalidationItems(opened.Manifest), RestartRequired: opened.Protected.AgentCA != nil}, nil
}

func (s *Service) importConflicts(ctx context.Context, current store.ControlPlaneSnapshotData, opened OpenedBundle) ([]ImportConflict, error) {
	conflicts := configurationConflicts(current, opened.Manifest)
	if opened.Protected.AgentCA != nil {
		if s.caRecovery == nil {
			conflicts = append(conflicts, ImportConflict{ResourceType: "agent_ca", Field: "availability", Value: "recovery unavailable"})
		} else {
			exists, err := s.caRecovery.AgentCAConflict(ctx)
			if err != nil {
				return nil, fmt.Errorf("inspect target Agent CA: %w", err)
			}
			if exists {
				conflicts = append(conflicts, ImportConflict{ResourceType: "agent_ca", Field: "existing_material", Value: "target Agent CA already exists"})
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		left := conflicts[i].ResourceType + "\x00" + conflicts[i].ResourceID + "\x00" + conflicts[i].Field + "\x00" + conflicts[i].Value
		right := conflicts[j].ResourceType + "\x00" + conflicts[j].ResourceID + "\x00" + conflicts[j].Field + "\x00" + conflicts[j].Value
		return left < right
	})
	return conflicts, nil
}

func configurationConflicts(current store.ControlPlaneSnapshotData, incoming Manifest) []ImportConflict {
	conflicts := []ImportConflict{}
	hostIDs, hostNames := map[string]string{}, map[string]string{}
	for _, item := range current.RemoteHosts {
		hostIDs[item.Host.ID], hostNames[item.Host.Name] = item.Host.ID, item.Host.ID
	}
	for _, item := range incoming.RemoteHosts {
		conflicts = appendIdentityConflicts(conflicts, "remote_host", item.ID, item.Name, hostIDs, hostNames)
	}
	repositoryIDs, repositoryNames, repositoryLocations := map[string]string{}, map[string]string{}, map[string]string{}
	for _, item := range current.Repositories {
		repositoryIDs[item.Repository.ID], repositoryNames[item.Repository.Name] = item.Repository.ID, item.Repository.ID
		repositoryLocations[repositoryLocation(item.Repository)] = item.Repository.ID
	}
	for _, item := range incoming.Repositories {
		conflicts = appendIdentityConflicts(conflicts, "repository", item.ID, item.Name, repositoryIDs, repositoryNames)
		if existing := repositoryLocations[repositoryLocation(item)]; existing != "" {
			conflicts = append(conflicts, ImportConflict{ResourceType: "repository", ResourceID: item.ID, Field: "location", Value: item.Path, ExistingID: existing})
		}
	}
	databaseIDs, databaseNames := map[string]string{}, map[string]string{}
	for _, item := range current.DatabaseConnections {
		databaseIDs[item.Connection.ID], databaseNames[item.Connection.Name] = item.Connection.ID, item.Connection.ID
	}
	for _, item := range incoming.DatabaseConnections {
		conflicts = appendIdentityConflicts(conflicts, "database_connection", item.ID, item.Name, databaseIDs, databaseNames)
	}
	taskIDs, taskNames := map[string]string{}, map[string]string{}
	for _, item := range current.Tasks {
		taskIDs[item.ID], taskNames[item.Name] = item.ID, item.ID
	}
	for _, item := range incoming.Tasks {
		conflicts = appendIdentityConflicts(conflicts, "task", item.ID, item.Name, taskIDs, taskNames)
	}
	planIDs, planNames := map[string]string{}, map[string]string{}
	for _, item := range current.Plans {
		planIDs[item.ID], planNames[item.Name] = item.ID, item.ID
	}
	for _, item := range incoming.Plans {
		conflicts = appendIdentityConflicts(conflicts, "plan", item.ID, item.Name, planIDs, planNames)
	}
	agentIDs, agentSerials := map[string]string{}, map[string]string{}
	for _, item := range current.Agents {
		agentIDs[item.ID], agentSerials[item.CertificateSerial] = item.ID, item.ID
	}
	for _, item := range incoming.Agents {
		if existing := agentIDs[item.ID]; existing != "" {
			conflicts = append(conflicts, ImportConflict{ResourceType: "agent", ResourceID: item.ID, Field: "id", Value: item.ID, ExistingID: existing})
		}
		if existing := agentSerials[item.CertificateSerial]; existing != "" {
			conflicts = append(conflicts, ImportConflict{ResourceType: "agent", ResourceID: item.ID, Field: "certificate_serial", Value: item.CertificateSerial, ExistingID: existing})
		}
	}
	if current.Ntfy != nil && incoming.Ntfy != nil {
		conflicts = append(conflicts, ImportConflict{ResourceType: "notification", ResourceID: "ntfy", Field: "existing_configuration", ExistingID: "ntfy"})
	}
	if current.Webhook != nil && incoming.Webhook != nil {
		conflicts = append(conflicts, ImportConflict{ResourceType: "notification", ResourceID: "webhook", Field: "existing_configuration", ExistingID: "webhook"})
	}
	if current.AgentServiceSettings != nil && incoming.AgentServiceSettings != nil {
		conflicts = append(conflicts, ImportConflict{ResourceType: "agent_service", ResourceID: "primary", Field: "existing_configuration", ExistingID: "primary"})
	}
	return conflicts
}

func appendIdentityConflicts(conflicts []ImportConflict, resourceType, id, name string, ids, names map[string]string) []ImportConflict {
	if existing := ids[id]; existing != "" {
		conflicts = append(conflicts, ImportConflict{ResourceType: resourceType, ResourceID: id, Field: "id", Value: id, ExistingID: existing})
	}
	if existing := names[name]; existing != "" {
		conflicts = append(conflicts, ImportConflict{ResourceType: resourceType, ResourceID: id, Field: "name", Value: name, ExistingID: existing})
	}
	return conflicts
}

func repositoryLocation(item domain.Repository) string {
	if item.EffectiveKind() == domain.LocalRepository {
		return "local\x00" + item.Path
	}
	if item.EffectiveKind() == domain.S3Repository && item.S3 != nil {
		return "s3\x00" + item.S3.Endpoint + "\x00" + item.S3.Bucket + "\x00" + item.Path
	}
	return "sftp\x00" + item.RemoteHostID + "\x00" + item.Path
}

func revalidationItems(manifest Manifest) []RevalidationItem {
	result := make([]RevalidationItem, 0, len(manifest.Repositories)+len(manifest.DatabaseConnections)+len(manifest.Tasks)+len(manifest.Plans)+len(manifest.MaintenancePolicies)+len(manifest.RestoreVerificationPolicies)+len(manifest.Agents)+1)
	for _, item := range manifest.Repositories {
		action := "validate_rsync_target"
		if item.EffectiveEngine() == domain.ResticEngine {
			action = "verify_existing_repository_read_only"
		}
		result = append(result, RevalidationItem{ResourceType: "repository", ResourceID: item.ID, Action: action})
	}
	for _, item := range manifest.DatabaseConnections {
		result = append(result, RevalidationItem{ResourceType: "database_connection", ResourceID: item.ID, Action: "run_connection_preflight"})
	}
	for _, item := range manifest.Agents {
		result = append(result, RevalidationItem{ResourceType: "agent", ResourceID: item.ID, Action: "restart_service_and_wait_for_heartbeat"})
	}
	for _, item := range manifest.Tasks {
		result = append(result, RevalidationItem{ResourceType: "task", ResourceID: item.ID, Action: "preview_scope_then_enable"})
	}
	for _, item := range manifest.Plans {
		result = append(result, RevalidationItem{ResourceType: "plan", ResourceID: item.ID, Action: "enable_after_tasks"})
	}
	for _, item := range manifest.MaintenancePolicies {
		result = append(result, RevalidationItem{ResourceType: "maintenance", ResourceID: item.RepositoryID, Action: "run_dry_run_then_enable"})
	}
	for _, item := range manifest.RestoreVerificationPolicies {
		result = append(result, RevalidationItem{ResourceType: "restore_verification", ResourceID: item.TaskID, Action: "review_selection_then_enable"})
	}
	if manifest.Ntfy != nil {
		result = append(result, RevalidationItem{ResourceType: "notification", ResourceID: "ntfy", Action: "send_test_then_enable"})
	}
	if manifest.Webhook != nil {
		result = append(result, RevalidationItem{ResourceType: "notification", ResourceID: "webhook", Action: "send_test_then_enable"})
	}
	return result
}

func importRequest(manifest Manifest, secretIDs map[string]string, previewID, bundleHash string, importedAt time.Time) store.ControlPlaneImportRequest {
	request := store.ControlPlaneImportRequest{
		PreviewID: previewID, BundleSHA256: bundleHash, ImportedAt: importedAt,
		RemoteHosts: make([]store.ControlPlaneRemoteHost, 0, len(manifest.RemoteHosts)), Repositories: make([]store.ControlPlaneRepository, 0, len(manifest.Repositories)),
		DatabaseConnections: make([]store.ControlPlaneDatabaseConnection, 0, len(manifest.DatabaseConnections)), Tasks: append([]domain.Task(nil), manifest.Tasks...), Plans: append([]domain.Plan(nil), manifest.Plans...),
		MaintenancePolicies:         append([]domain.MaintenancePolicy(nil), manifest.MaintenancePolicies...),
		RestoreVerificationPolicies: append([]domain.RestoreVerificationPolicy(nil), manifest.RestoreVerificationPolicies...),
		LifecyclePolicy:             store.LifecyclePolicy{RunDays: manifest.LifecyclePolicy.RunDays, RawLogDays: manifest.LifecyclePolicy.RawLogDays, AuditDays: manifest.LifecyclePolicy.AuditDays, RawLogMaxBytes: manifest.LifecyclePolicy.RawLogMaxBytes},
		ScheduleWatermarks:          make([]store.ControlPlaneScheduleWatermark, 0, len(manifest.ScheduleWatermarks)), Agents: make([]store.AgentRecord, 0, len(manifest.Agents)), Audits: make([]store.AuditRecord, 0, len(manifest.Audits)),
	}
	for _, item := range manifest.RemoteHosts {
		request.RemoteHosts = append(request.RemoteHosts, store.ControlPlaneRemoteHost{Host: item, PrivateKeySecretID: secretIDs[secretReferenceParts("remote_host", item.ID, "private_key")]})
	}
	for _, item := range manifest.Repositories {
		secretID := ""
		backendSecretID := ""
		if item.EffectiveEngine() == domain.ResticEngine {
			secretID = secretIDs[secretReferenceParts("repository", item.ID, "password")]
		}
		if item.EffectiveKind() == domain.S3Repository {
			backendSecretID = secretIDs[secretReferenceParts("repository", item.ID, "s3_credentials")]
		}
		request.Repositories = append(request.Repositories, store.ControlPlaneRepository{Repository: item, PasswordSecretID: secretID, BackendSecretID: backendSecretID})
	}
	for _, item := range manifest.DatabaseConnections {
		request.DatabaseConnections = append(request.DatabaseConnections, store.ControlPlaneDatabaseConnection{Connection: item, PasswordSecretID: secretIDs[secretReferenceParts("database_connection", item.ID, "password")]})
	}
	for _, item := range manifest.ScheduleWatermarks {
		request.ScheduleWatermarks = append(request.ScheduleWatermarks, store.ControlPlaneScheduleWatermark{OwnerKind: item.OwnerKind, OwnerID: item.OwnerID, ScheduledAt: item.ScheduledAt, ObservedAt: item.ObservedAt, Mode: item.Mode, Status: item.Status})
	}
	for _, item := range manifest.Agents {
		request.Agents = append(request.Agents, store.AgentRecord{ID: item.ID, RemoteHostID: item.RemoteHostID, CertificateSerial: item.CertificateSerial, CertificateNotAfter: item.CertificateNotAfter, Capabilities: append([]string(nil), item.Capabilities...), Status: item.Status, CreatedAt: item.CreatedAt, RevokedAt: item.RevokedAt})
	}
	if manifest.AgentServiceSettings != nil {
		request.AgentServiceSettings = &store.AgentServiceSettings{Enabled: manifest.AgentServiceSettings.Enabled, ListenHost: manifest.AgentServiceSettings.ListenHost, Port: manifest.AgentServiceSettings.Port, AdvertisedHost: manifest.AgentServiceSettings.AdvertisedHost, TLSNames: append([]string(nil), manifest.AgentServiceSettings.TLSNames...)}
	}
	if manifest.Ntfy != nil {
		request.Ntfy = &store.ControlPlaneNtfy{BaseURL: manifest.Ntfy.BaseURL, Topic: manifest.Ntfy.Topic, TokenSecretID: secretIDs[secretReferenceParts("notification", "ntfy", "token")], Enabled: false}
	}
	if manifest.Webhook != nil {
		enabled := false
		secretID := secretIDs[secretReferenceParts("notification", "webhook", "auth_secret")]
		request.Webhook = &notificationconfig.Webhook{Endpoint: manifest.Webhook.Endpoint, AuthMode: manifest.Webhook.AuthMode, SecretID: secretID, Enabled: &enabled}
	}
	if manifest.Email != nil {
		enabled := false
		secretID := secretIDs[secretReferenceParts("notification", "email", "password")]
		request.Email = &notificationconfig.Email{Host: manifest.Email.Host, Port: manifest.Email.Port, TLSMode: manifest.Email.TLSMode, From: manifest.Email.From, To: append([]string(nil), manifest.Email.To...), Username: manifest.Email.Username, PasswordSecretID: secretID, Enabled: &enabled}
	}
	for _, item := range manifest.Audits {
		request.Audits = append(request.Audits, store.AuditRecord{OccurredAt: item.OccurredAt, Actor: item.Actor, Action: item.Action, TargetType: item.TargetType, TargetID: item.TargetID, Detail: item.Detail})
	}
	return request
}

func randomImportPreviewID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate control-plane import preview id: %w", err)
	}
	return "cpimp_" + hex.EncodeToString(raw), nil
}
