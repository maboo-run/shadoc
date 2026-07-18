package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestControlPlaneImportConsumesMatchingPreviewAndActivatesConservatively(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	preview := ControlPlaneImportPreview{ID: "cp-preview-a", BundleSHA256: hash, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err := s.SaveControlPlaneImportPreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, purpose string }{{"new-ssh", "ssh-private-key"}, {"new-repo", "repository-password"}, {"new-db", "database-backup-password"}, {"new-ntfy", "ntfy-token"}} {
		if err := s.SaveSecret(ctx, item.id, item.purpose, []byte("target-vault-ciphertext"), now); err != nil {
			t.Fatal(err)
		}
	}
	host := domain.RemoteHost{ID: "host-a", Name: "host A", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "backup.example ssh-ed25519 AAAA", CreatedAt: now, UpdatedAt: now}
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.SFTPRepository, RemoteHostID: host.ID, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	database := domain.DatabaseConnection{ID: "db-a", Name: "db A", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork, Host: "db.example", Port: 5432, Username: "backup", ToolPaths: map[string]string{"pg_dump": "/usr/bin/pg_dump"}, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now, ClientVersion: "17", ServerVersion: "17"}, CreatedAt: now, UpdatedAt: now}
	task := domain.Task{ID: "task-a", Name: "task A", Engine: domain.ResticEngine, Kind: domain.DirectoryTask, ExecutionTarget: execution.Target{Kind: execution.Local}, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/source"}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "source-preview", Fingerprint: "source-fingerprint", ConfirmedBy: "source-admin", ConfirmedAt: now}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	plan := domain.Plan{ID: "plan-a", Name: "plan A", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	maintenance := domain.MaintenancePolicy{RepositoryID: repository.ID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "04:00"}, Timezone: "UTC", Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-48 * time.Hour), UpdatedAt: now}
	restoreVerification := domain.RestoreVerificationPolicy{TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 168, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-24 * time.Hour), UpdatedAt: now}
	enabled := true
	request := ControlPlaneImportRequest{
		PreviewID: preview.ID, BundleSHA256: hash, ImportedAt: now,
		RemoteHosts:         []ControlPlaneRemoteHost{{Host: host, PrivateKeySecretID: "new-ssh"}},
		Repositories:        []ControlPlaneRepository{{Repository: repository, PasswordSecretID: "new-repo"}},
		DatabaseConnections: []ControlPlaneDatabaseConnection{{Connection: database, PasswordSecretID: "new-db"}},
		Tasks:               []domain.Task{task}, Plans: []domain.Plan{plan}, MaintenancePolicies: []domain.MaintenancePolicy{maintenance}, RestoreVerificationPolicies: []domain.RestoreVerificationPolicy{restoreVerification},
		LifecyclePolicy:      LifecyclePolicy{RunDays: 90, RawLogDays: 7, AuditDays: 365, RawLogMaxBytes: 1 << 30},
		ScheduleWatermarks:   []ControlPlaneScheduleWatermark{{OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: now.Add(-time.Hour), ObservedAt: now.Add(-time.Hour), Mode: "on_time", Status: "success"}, {OwnerKind: "restore_verification", OwnerID: task.ID, ScheduledAt: now.Add(-time.Hour), ObservedAt: now.Add(-time.Hour), Mode: "on_time", Status: "success"}},
		Agents:               []AgentRecord{{ID: "agent-a", RemoteHostID: host.ID, CertificateSerial: "serial-a", CertificateNotAfter: timePointer(now.Add(365 * 24 * time.Hour)), Capabilities: []string{"restic"}, Status: "online", LastHeartbeatAt: &now, CreatedAt: now}},
		AgentServiceSettings: &AgentServiceSettings{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "control.example", TLSNames: []string{"control.example"}},
		Ntfy:                 &ControlPlaneNtfy{BaseURL: "https://ntfy.example", Topic: "backup", TokenSecretID: "new-ntfy", Enabled: enabled},
		Audits:               []AuditRecord{{OccurredAt: now.Add(-time.Hour), Actor: "source-admin", Action: "task.create", TargetType: "task", TargetID: task.ID, Detail: map[string]any{"source": "recovery"}}},
	}
	wrong := request
	wrong.BundleSHA256 = strings.Repeat("b", 64)
	if err := s.ImportControlPlane(ctx, wrong); !errors.Is(err, ErrControlPlaneImportPreview) {
		t.Fatalf("wrong hash error = %v", err)
	}
	if repositories, _ := s.ListRepositories(ctx); len(repositories) != 0 {
		t.Fatalf("wrong preview mutated repositories: %+v", repositories)
	}

	if err := s.ImportControlPlane(ctx, request); err != nil {
		t.Fatal(err)
	}
	executionData, err := s.LoadTaskExecution(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executionData.Repository.Status != "disconnected" || executionData.RepositoryPasswordSecretID != "new-repo" || executionData.PrivateKeySecretID != "new-ssh" {
		t.Fatalf("repository execution = %+v", executionData)
	}
	tasks, _ := s.ListTasks(ctx)
	if len(tasks) != 1 || tasks[0].Enabled || tasks[0].ScopeConfirmation.Present() {
		t.Fatalf("imported tasks = %+v", tasks)
	}
	plans, _ := s.ListPlans(ctx)
	if len(plans) != 1 || plans[0].Enabled || !plans[0].ScheduleAnchorAt.Equal(plan.ScheduleAnchorAt) {
		t.Fatalf("imported plans = %+v", plans)
	}
	policies, _ := s.ListMaintenancePolicies(ctx)
	if len(policies) != 1 || policies[0].Enabled || !policies[0].ScheduleAnchorAt.Equal(maintenance.ScheduleAnchorAt) {
		t.Fatalf("imported maintenance = %+v", policies)
	}
	restorePolicies, _ := s.ListRestoreVerificationPolicies(ctx)
	if len(restorePolicies) != 1 || restorePolicies[0].Enabled || !restorePolicies[0].ScheduleAnchorAt.Equal(restoreVerification.ScheduleAnchorAt) {
		t.Fatalf("imported restore verification = %+v", restorePolicies)
	}
	connections, _ := s.ListDatabaseConnections(ctx)
	if len(connections) != 1 || connections[0].Status != "draft" || !connections[0].Preflight.CheckedAt.IsZero() {
		t.Fatalf("imported database connections = %+v", connections)
	}
	agents, _ := s.ListAgents(ctx)
	if len(agents) != 1 || agents[0].Status != "offline" || agents[0].LastHeartbeatAt != nil || len(agents[0].Capabilities) != 0 {
		t.Fatalf("imported Agents = %+v", agents)
	}
	if usable, err := s.AgentCertificateUsable(ctx, "agent-a", "serial-a", now); err != nil || !usable {
		t.Fatalf("imported Agent certificate usable=%t err=%v", usable, err)
	}
	settings, exists, _ := s.LoadAgentServiceSettings(ctx)
	if !exists || settings.Enabled {
		t.Fatalf("imported Agent settings exists=%v value=%+v", exists, settings)
	}
	watermarks, _ := s.LatestScheduleOccurrences(ctx, "plan")
	if len(watermarks) != 1 || len(watermarks[plan.ID].RunIDs) != 0 || watermarks[plan.ID].Status != "success" {
		t.Fatalf("imported watermarks = %+v", watermarks)
	}
	restoreWatermarks, _ := s.LatestScheduleOccurrences(ctx, "restore_verification")
	if len(restoreWatermarks) != 1 || restoreWatermarks[task.ID].Status != "success" || len(restoreWatermarks[task.ID].RunIDs) != 0 {
		t.Fatalf("imported restore verification watermarks = %+v", restoreWatermarks)
	}
	policy, _ := s.LoadLifecyclePolicy(ctx)
	if policy.RunDays != 90 {
		t.Fatalf("lifecycle policy = %+v", policy)
	}
	ntfyJSON, err := s.Metadata(ctx, "ntfy.config")
	if err != nil {
		t.Fatal(err)
	}
	var ntfyStored struct {
		TokenSecretID string `json:"tokenSecretId"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(ntfyJSON), &ntfyStored); err != nil {
		t.Fatal(err)
	}
	if ntfyStored.TokenSecretID != "new-ntfy" || ntfyStored.Enabled == nil || *ntfyStored.Enabled {
		t.Fatalf("ntfy configuration = %s", ntfyJSON)
	}
	audits, _ := s.ListAudits(ctx, 10)
	if len(audits) != 1 || audits[0].Action != "task.create" {
		t.Fatalf("audits = %+v", audits)
	}
	if err := s.ImportControlPlane(ctx, request); !errors.Is(err, ErrControlPlaneImportPreview) {
		t.Fatalf("replayed preview error = %v", err)
	}
}

func TestControlPlaneImportRollsBackPreviewConsumptionOnConflict(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("c", 64)
	preview := ControlPlaneImportPreview{ID: "cp-preview-conflict", BundleSHA256: hash, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.SaveControlPlaneImportPreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, "existing-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	existing := domain.RemoteHost{ID: "existing", Name: "duplicate name", Host: "existing.example", Port: 22, Username: "backup", HostFingerprint: "existing.example ssh-ed25519 AAAA", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRemoteHost(ctx, existing, "existing-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, "import-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	incoming := existing
	incoming.ID, incoming.Host = "incoming", "incoming.example"
	request := ControlPlaneImportRequest{PreviewID: preview.ID, BundleSHA256: hash, ImportedAt: now, RemoteHosts: []ControlPlaneRemoteHost{{Host: incoming, PrivateKeySecretID: "import-key"}}, Repositories: []ControlPlaneRepository{}, DatabaseConnections: []ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{}, ScheduleWatermarks: []ControlPlaneScheduleWatermark{}, Agents: []AgentRecord{}, Audits: []AuditRecord{}}
	if err := s.ImportControlPlane(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	var consumed any
	if err := s.db.QueryRowContext(ctx, `SELECT consumed_at FROM controlplane_import_previews WHERE id=?`, preview.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatalf("preview was consumed despite rollback: %v", consumed)
	}
}

func TestControlPlaneImportRejectsExpiredPreviewWithoutMutation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("d", 64)
	preview := ControlPlaneImportPreview{ID: "cp-preview-expired", BundleSHA256: hash, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := s.SaveControlPlaneImportPreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	request := ControlPlaneImportRequest{PreviewID: preview.ID, BundleSHA256: hash, ImportedAt: now, RemoteHosts: []ControlPlaneRemoteHost{}, Repositories: []ControlPlaneRepository{}, DatabaseConnections: []ControlPlaneDatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{}, ScheduleWatermarks: []ControlPlaneScheduleWatermark{}, Agents: []AgentRecord{}, Audits: []AuditRecord{}}
	if err := s.ImportControlPlane(ctx, request); !errors.Is(err, ErrControlPlaneImportPreview) {
		t.Fatalf("expired preview error = %v", err)
	}
}
