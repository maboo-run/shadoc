package controlplane

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

type trackingVault struct {
	manager *secret.Manager
	created []string
	deleted []string
}

func (v *trackingVault) Get(ctx context.Context, id, purpose string) ([]byte, error) {
	return v.manager.Get(ctx, id, purpose)
}
func (v *trackingVault) Put(ctx context.Context, purpose string, value []byte) (string, error) {
	id, err := v.manager.Put(ctx, purpose, value)
	if err == nil {
		v.created = append(v.created, id)
	}
	return id, err
}
func (v *trackingVault) Delete(ctx context.Context, id string) error {
	err := v.manager.Delete(ctx, id)
	if err == nil {
		v.deleted = append(v.deleted, id)
	}
	return err
}

type toolCheckerStub struct{ missing []MissingTool }

func (s toolCheckerStub) MissingTools(context.Context, Manifest) ([]MissingTool, error) {
	return s.missing, nil
}

type caRecoveryStub struct {
	conflict   bool
	installed  bool
	rolledBack bool
}

func (s *caRecoveryStub) AgentCAConflict(context.Context) (bool, error) { return s.conflict, nil }
func (s *caRecoveryStub) InstallAgentCA(context.Context, AgentCAMaterial) (func() error, error) {
	s.installed = true
	return func() error { s.installed = false; s.rolledBack = true; return nil }, nil
}

func TestImportPreflightAndApplyUseOneTimeBundleBoundPreview(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	target, secrets := openRecoveryTarget(t, now)
	caRecovery := &caRecoveryStub{}
	service := NewService(target, secrets, nil, "target-version", func() time.Time { return now })
	service.kdf = testKDF()
	service.tools = toolCheckerStub{missing: []MissingTool{{Tool: "restic", RequiredBy: []string{"repo-a"}}}}
	service.caRecovery = caRecovery
	bundle := importableRecoveryBundle(t, now, "/srv/repo")

	preview, err := service.PreflightImport(context.Background(), bundle, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanImport || preview.PreviewID == "" || preview.ResourceCounts["repositories"] != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.ResourceCounts["restoreVerificationPolicies"] != 1 {
		t.Fatalf("restore verification policy count missing: %+v", preview.ResourceCounts)
	}
	if len(preview.MissingTools) != 1 || !preview.RestartRequired || len(preview.Revalidation) < 4 {
		t.Fatalf("preview evidence = %+v", preview)
	}
	if len(secrets.created) != 0 || caRecovery.installed {
		t.Fatal("preflight mutated the target vault or Agent CA")
	}
	if repositories, _ := target.ListRepositories(context.Background()); len(repositories) != 0 {
		t.Fatalf("preflight created repositories: %+v", repositories)
	}

	result, err := service.Import(context.Background(), bundle, "correct horse battery staple", preview.PreviewID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RestartRequired || result.ImportedCounts["repositories"] != 1 || !caRecovery.installed {
		t.Fatalf("import result = %+v", result)
	}
	repositories, _ := target.ListRepositories(context.Background())
	if len(repositories) != 1 || repositories[0].Status != "disconnected" {
		t.Fatalf("repositories = %+v", repositories)
	}
	tasks, _ := target.ListTasks(context.Background())
	if len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("tasks = %+v", tasks)
	}
	plans, _ := target.ListPlans(context.Background())
	if len(plans) != 1 || plans[0].Enabled {
		t.Fatalf("plans = %+v", plans)
	}
	restorePolicies, _ := target.ListRestoreVerificationPolicies(context.Background())
	if len(restorePolicies) != 1 || restorePolicies[0].Enabled {
		t.Fatalf("restore verification policies = %+v", restorePolicies)
	}
	restoreWatermarks, _ := target.LatestScheduleOccurrences(context.Background(), "restore_verification")
	if len(restoreWatermarks) != 1 || restoreWatermarks["task-a"].Status != "success" {
		t.Fatalf("restore verification watermarks = %+v", restoreWatermarks)
	}
	agents, _ := target.ListAgents(context.Background())
	if len(agents) != 1 || agents[0].Status != "offline" {
		t.Fatalf("Agents = %+v", agents)
	}
	if len(secrets.created) != 1 || len(secrets.deleted) != 0 {
		t.Fatalf("vault created=%v deleted=%v", secrets.created, secrets.deleted)
	}
}

func TestImportRollsBackTargetSecretsAndAgentCAWhenPreviewDoesNotMatchBundle(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	target, secrets := openRecoveryTarget(t, now)
	caRecovery := &caRecoveryStub{}
	service := NewService(target, secrets, nil, "target-version", func() time.Time { return now })
	service.kdf = testKDF()
	service.caRecovery = caRecovery
	first := importableRecoveryBundle(t, now, "/srv/repo-a")
	preview, err := service.PreflightImport(context.Background(), first, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second := importableRecoveryBundle(t, now, "/srv/repo-b")
	if _, err := service.Import(context.Background(), second, "correct horse battery staple", preview.PreviewID); !errors.Is(err, store.ErrControlPlaneImportPreview) {
		t.Fatalf("mismatched preview error = %v", err)
	}
	if len(secrets.created) != 1 || len(secrets.deleted) != 1 || secrets.created[0] != secrets.deleted[0] {
		t.Fatalf("vault rollback created=%v deleted=%v", secrets.created, secrets.deleted)
	}
	if !caRecovery.rolledBack || caRecovery.installed {
		t.Fatalf("Agent CA rollback installed=%v rolledBack=%v", caRecovery.installed, caRecovery.rolledBack)
	}
	if repositories, _ := target.ListRepositories(context.Background()); len(repositories) != 0 {
		t.Fatalf("failed import created repositories: %+v", repositories)
	}
	if _, err := target.LoadSecret(context.Background(), secrets.created[0]); err == nil {
		t.Fatal("rolled-back target secret remains persisted")
	}
}

func TestImportPreflightReportsConflictsWithoutIssuingPreview(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	target, secrets := openRecoveryTarget(t, now)
	secretID, err := secrets.Put(context.Background(), "repository-password", []byte("existing-password"))
	if err != nil {
		t.Fatal(err)
	}
	existing := domain.Repository{ID: "existing", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/other/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := target.CreateRepository(context.Background(), existing, secretID); err != nil {
		t.Fatal(err)
	}
	caRecovery := &caRecoveryStub{conflict: true}
	service := NewService(target, secrets, nil, "target-version", func() time.Time { return now })
	service.kdf = testKDF()
	service.caRecovery = caRecovery
	preview, err := service.PreflightImport(context.Background(), importableRecoveryBundle(t, now, "/srv/repo"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanImport || preview.PreviewID != "" || len(preview.Conflicts) < 2 {
		t.Fatalf("conflict preview = %+v", preview)
	}
}

func openRecoveryTarget(t *testing.T, now time.Time) (*store.Store, *trackingVault) {
	t.Helper()
	target, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	crypt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	manager := secret.New(target, crypt, func() time.Time { return now })
	return target, &trackingVault{manager: manager}
}

func importableRecoveryBundle(t *testing.T, now time.Time, path string) []byte {
	t.Helper()
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: path, Status: "ready", CreatedAt: now, UpdatedAt: now}
	task := domain.Task{ID: "task-a", Name: "task A", Engine: domain.ResticEngine, Kind: domain.DirectoryTask, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/source"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	plan := domain.Plan{ID: "plan-a", Name: "plan A", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-time.Hour), CreatedAt: now, UpdatedAt: now}
	maintenance := domain.MaintenancePolicy{RepositoryID: repository.ID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "04:00"}, Timezone: "UTC", Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-time.Hour), UpdatedAt: now}
	restoreVerification := domain.RestoreVerificationPolicy{TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 168, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now.Add(-time.Hour), UpdatedAt: now}
	manifest := Manifest{RemoteHosts: []domain.RemoteHost{}, Repositories: []domain.Repository{repository}, DatabaseConnections: []domain.DatabaseConnection{}, Tasks: []domain.Task{task}, Plans: []domain.Plan{plan}, MaintenancePolicies: []domain.MaintenancePolicy{maintenance}, RestoreVerificationPolicies: []domain.RestoreVerificationPolicy{restoreVerification}, ScheduleWatermarks: []ScheduleWatermark{{OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: now.Add(-30 * time.Minute), ObservedAt: now.Add(-30 * time.Minute), Mode: "on_time", Status: "success"}, {OwnerKind: "restore_verification", OwnerID: task.ID, ScheduledAt: now.Add(-30 * time.Minute), ObservedAt: now.Add(-30 * time.Minute), Mode: "on_time", Status: "success"}}, Agents: []AgentIdentity{{ID: "agent-a", CertificateSerial: "serial-a", Capabilities: []string{"restic"}, Status: "online", CreatedAt: now}}, Audits: []AuditEntry{}, LifecyclePolicy: LifecyclePolicy{RunDays: 365, RawLogDays: 30, AuditDays: 365, RawLogMaxBytes: 1 << 30}}
	protected := ProtectedPayload{Secrets: []ProtectedSecret{{ResourceType: "repository", ResourceID: repository.ID, Field: "password", Purpose: "repository-password", Value: []byte("repository-password")}}}
	ca := validAgentCA(t, now)
	protected.AgentCA = &ca
	encoded, err := SealBundle(manifest, protected, SealOptions{Passphrase: "correct horse battery staple", CreatedAt: now, SourceApplicationVersion: "source-version", KDF: testKDF()})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
