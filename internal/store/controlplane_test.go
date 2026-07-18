package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestControlPlaneSnapshotIncludesDurableConfigurationAndSecretReferences(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct{ id, purpose string }{
		{"ssh-key", "ssh-private-key"}, {"repo-password", "repository-password"}, {"database-password", "database-password"}, {"ntfy-token", "ntfy-token"}, {"temporary-password", "temporary-database-restore-password"},
	} {
		if err := s.SaveSecret(ctx, item.id, item.purpose, []byte("encrypted-"+item.id), now); err != nil {
			t.Fatal(err)
		}
	}
	host := domain.RemoteHost{ID: "host-a", Name: "host A", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "backup.example ssh-ed25519 AAAA", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRemoteHost(ctx, host, "ssh-key"); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.SFTPRepository, RemoteHostID: host.ID, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRepository(ctx, repository, "repo-password"); err != nil {
		t.Fatal(err)
	}
	database := domain.DatabaseConnection{ID: "db-a", Name: "db A", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork, Host: "db.example", Port: 5432, Username: "backup", TLS: domain.TLSConfig{Mode: "verify-full", ClientKey: "/secure/client.key"}, ToolPaths: map[string]string{"pg_dump": "/usr/bin/pg_dump"}, Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDatabaseConnection(ctx, database, "database-password"); err != nil {
		t.Fatal(err)
	}
	temporary := database
	temporary.ID, temporary.Name, temporary.Purpose = "temporary-dbconn-old", "temporary", domain.RestoreConnection
	if err := s.CreateDatabaseConnection(ctx, temporary, "temporary-password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-a", Name: "task A", Engine: domain.ResticEngine, Kind: domain.DirectoryTask, ExecutionTarget: execution.Target{Kind: execution.Local}, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/source"}, Health: domain.TaskHealthPolicy{MaxSuccessAgeHours: 48}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "preview-secret-authority", Fingerprint: "fingerprint", ConfirmedBy: "admin", ConfirmedAt: now}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "plan-a", Name: "plan A", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	maintenance := domain.MaintenancePolicy{RepositoryID: repository.ID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "04:00"}, Timezone: "UTC", Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now, UpdatedAt: now}
	if err := s.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	restoreVerification := domain.RestoreVerificationPolicy{TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 168, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now, UpdatedAt: now}
	if err := s.SaveRestoreVerificationPolicy(ctx, restoreVerification); err != nil {
		t.Fatal(err)
	}
	occurrence := ScheduleOccurrence{ID: "occurrence-a", OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{task.ID}}
	if created, err := s.CreateScheduleOccurrence(ctx, occurrence); err != nil || !created {
		t.Fatalf("create occurrence: created=%v err=%v", created, err)
	}
	if claimed, err := s.ClaimScheduleOccurrence(ctx, occurrence.ID, now); err != nil || !claimed {
		t.Fatalf("claim occurrence: claimed=%v err=%v", claimed, err)
	}
	if err := s.FinishScheduleOccurrence(ctx, occurrence.ID, "success", []string{"run-must-not-export"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	verificationOccurrence := ScheduleOccurrence{ID: "verification-occurrence-a", OwnerKind: "restore_verification", OwnerID: task.ID, ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{task.ID}}
	if created, err := s.CreateScheduleOccurrence(ctx, verificationOccurrence); err != nil || !created {
		t.Fatalf("create verification occurrence: created=%v err=%v", created, err)
	}
	if claimed, err := s.ClaimScheduleOccurrence(ctx, verificationOccurrence.ID, now); err != nil || !claimed {
		t.Fatalf("claim verification occurrence: claimed=%v err=%v", claimed, err)
	}
	if err := s.FinishScheduleOccurrence(ctx, verificationOccurrence.ID, "success", []string{"verification-evidence-must-not-export"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	certificateNotAfter := now.Add(365 * 24 * time.Hour)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-a", RemoteHostID: host.ID, CertificateSerial: "serial-a", CertificateNotAfter: &certificateNotAfter, Capabilities: []string{"restic", "filesystem-browse"}, Status: "online", LastHeartbeatAt: &now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAgentRemoteHost(ctx, "agent-a", host.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgentServiceSettings(ctx, AgentServiceSettings{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "control.example", TLSNames: []string{"control.example"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLifecyclePolicy(ctx, LifecyclePolicy{RunDays: 90, RawLogDays: 7, AuditDays: 365, RawLogMaxBytes: 1 << 30}, now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	ntfy, _ := json.Marshal(map[string]any{"baseUrl": "https://ntfy.example", "topic": "backup", "tokenSecretId": "ntfy-token", "enabled": &enabled})
	if err := s.SetMetadata(ctx, "ntfy.config", string(ntfy)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, AuditRecord{OccurredAt: now, Actor: "admin", Action: "task.create", TargetType: "task", TargetID: task.ID, Detail: map[string]any{"enabled": true}}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.ControlPlaneSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RemoteHosts) != 1 || snapshot.RemoteHosts[0].PrivateKeySecretID != "ssh-key" {
		t.Fatalf("remote hosts = %+v", snapshot.RemoteHosts)
	}
	if len(snapshot.Repositories) != 1 || snapshot.Repositories[0].PasswordSecretID != "repo-password" {
		t.Fatalf("repositories = %+v", snapshot.Repositories)
	}
	if len(snapshot.DatabaseConnections) != 1 || snapshot.DatabaseConnections[0].Connection.ID != "db-a" || snapshot.DatabaseConnections[0].PasswordSecretID != "database-password" {
		t.Fatalf("database connections = %+v", snapshot.DatabaseConnections)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ScopeConfirmation.Present() {
		t.Fatalf("task transient confirmation was exported: %+v", snapshot.Tasks)
	}
	if len(snapshot.Plans) != 1 || len(snapshot.MaintenancePolicies) != 1 || len(snapshot.RestoreVerificationPolicies) != 1 || snapshot.RestoreVerificationPolicies[0].TaskID != task.ID {
		t.Fatalf("plans=%d maintenance=%d restoreVerification=%+v", len(snapshot.Plans), len(snapshot.MaintenancePolicies), snapshot.RestoreVerificationPolicies)
	}
	if len(snapshot.ScheduleWatermarks) != 2 || snapshot.ScheduleWatermarks[0].Status != "success" || snapshot.ScheduleWatermarks[1].Status != "success" {
		t.Fatalf("watermarks = %+v", snapshot.ScheduleWatermarks)
	}
	if len(snapshot.Agents) != 1 || snapshot.Agents[0].LastHeartbeatAt != nil || snapshot.Agents[0].CertificateNotAfter == nil || !snapshot.Agents[0].CertificateNotAfter.Equal(certificateNotAfter) {
		t.Fatalf("Agent heartbeat leaked into snapshot: %+v", snapshot.Agents)
	}
	if snapshot.AgentServiceSettings == nil || !snapshot.AgentServiceSettings.Enabled {
		t.Fatalf("Agent settings = %+v", snapshot.AgentServiceSettings)
	}
	if snapshot.Ntfy == nil || snapshot.Ntfy.TokenSecretID != "ntfy-token" || !snapshot.Ntfy.Enabled {
		t.Fatalf("ntfy = %+v", snapshot.Ntfy)
	}
	if len(snapshot.Audits) != 1 || snapshot.LifecyclePolicy.RunDays != 90 {
		t.Fatalf("audits=%+v lifecycle=%+v", snapshot.Audits, snapshot.LifecyclePolicy)
	}
}

func TestControlPlaneSnapshotKeepsOnlyLatestTerminalWatermark(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "repo-password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo-a", Name: "repo A", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/srv/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRepository(ctx, repository, "repo-password"); err != nil {
		t.Fatal(err)
	}
	maintenance := domain.MaintenancePolicy{RepositoryID: repository.ID, Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "04:00"}, Timezone: "UTC", Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: now, UpdatedAt: now}
	if err := s.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"success", "failed"} {
		at := now.Add(time.Duration(index) * time.Hour)
		item := ScheduleOccurrence{ID: "occurrence-" + status, OwnerKind: "maintenance", OwnerID: repository.ID, ScheduledAt: at, ObservedAt: at, Mode: "on_time", Status: "pending", TargetIDs: []string{repository.ID}}
		if _, err := s.CreateScheduleOccurrence(ctx, item); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimScheduleOccurrence(ctx, item.ID, at); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishScheduleOccurrence(ctx, item.ID, status, []string{"transient-run"}, at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	queued := ScheduleOccurrence{ID: "occurrence-pending", OwnerKind: "maintenance", OwnerID: repository.ID, ScheduledAt: now.Add(2 * time.Hour), ObservedAt: now.Add(2 * time.Hour), Mode: "on_time", Status: "pending", TargetIDs: []string{repository.ID}}
	if _, err := s.CreateScheduleOccurrence(ctx, queued); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.ControlPlaneSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ScheduleWatermarks) != 1 || snapshot.ScheduleWatermarks[0].Status != "failed" || !snapshot.ScheduleWatermarks[0].ScheduledAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("watermarks = %+v", snapshot.ScheduleWatermarks)
	}
}
