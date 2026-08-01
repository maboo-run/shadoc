package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestLogicalReferencesRejectMissingParents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC)

	t.Run("disabled plan task", func(t *testing.T) {
		s := openTestStore(t)
		err := s.CreatePlan(ctx, domain.Plan{
			ID: "plan", Name: "plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"},
			Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{"missing-task"}, CreatedAt: now, UpdatedAt: now,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("create plan error=%v", err)
		}
	})

	t.Run("resource secrets and host", func(t *testing.T) {
		s := openTestStore(t)
		host := domain.RemoteHost{ID: "host", Name: "host", Host: "host.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateRemoteHost(ctx, host, "missing-secret"); !errors.Is(err, ErrConflict) {
			t.Fatalf("create remote host error=%v", err)
		}
		if err := s.SaveSecret(ctx, "pass", "repository-password", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
		repository := domain.Repository{ID: "repo", Name: "repo", Kind: domain.SFTPRepository, RemoteHostID: "missing-host", Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateRepository(ctx, repository, "pass"); !errors.Is(err, ErrConflict) {
			t.Fatalf("create repository error=%v", err)
		}
		connection := domain.DatabaseConnection{ID: "connection", Name: "connection", Engine: domain.MySQL, Purpose: domain.BackupConnection, Username: "backup", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDatabaseConnection(ctx, connection, "missing-secret"); !errors.Is(err, ErrConflict) {
			t.Fatalf("create database connection error=%v", err)
		}
	})

	t.Run("referenced secret deletion", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecret(ctx, "key", "ssh-private-key", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
		host := domain.RemoteHost{ID: "host", Name: "host", Host: "host.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateRemoteHost(ctx, host, "key"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSecret(ctx, "key"); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete referenced secret error=%v", err)
		}
		if _, err := s.DeleteRemoteHost(ctx, host.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSecret(ctx, "key"); err != nil {
			t.Fatalf("delete unreferenced secret: %v", err)
		}
	})

	t.Run("notification metadata secrets", func(t *testing.T) {
		s := openTestStore(t)
		for _, fixture := range []struct {
			id, key, value string
		}{
			{id: "ntfy-token", key: "ntfy.config", value: `{"tokenSecretId":"ntfy-token"}`},
			{id: "webhook-secret", key: "webhook.config", value: `{"secretId":"webhook-secret"}`},
			{id: "smtp-password", key: "email.config", value: `{"passwordSecretId":"smtp-password"}`},
		} {
			if err := s.SaveSecret(ctx, fixture.id, "notification-secret", []byte("cipher"), now); err != nil {
				t.Fatal(err)
			}
			if err := s.SetMetadata(ctx, fixture.key, fixture.value); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteSecret(ctx, fixture.id); !errors.Is(err, ErrConflict) {
				t.Fatalf("delete %s metadata secret error=%v", fixture.key, err)
			}
			if err := s.SetMetadata(ctx, fixture.key, `{}`); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteSecret(ctx, fixture.id); err != nil {
				t.Fatalf("delete unlinked %s metadata secret: %v", fixture.key, err)
			}
		}
	})

	t.Run("run task", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.StartRun(ctx, RunRecord{ID: "run", TaskID: "missing-task", Trigger: "manual", Status: "running", StartedAt: now}); !errors.Is(err, ErrConflict) {
			t.Fatalf("start run error=%v", err)
		}
		if err := s.CreateOperation(ctx, OperationRecord{ID: "operation", Kind: "task_scope_inventory", Actor: "admin", TaskID: "missing-task", Status: "queued", Stage: "queued", CreatedAt: now}); !errors.Is(err, ErrConflict) {
			t.Fatalf("create task operation error=%v", err)
		}
	})

	t.Run("schedule occurrence owner and target", func(t *testing.T) {
		s, task := createIdentityFixture(t)
		if _, err := s.CreateScheduleOccurrence(ctx, ScheduleOccurrence{ID: "missing-owner", OwnerKind: "plan", OwnerID: "missing-plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing schedule owner error=%v", err)
		}
		if err := s.CreatePlan(ctx, domain.Plan{ID: "plan", Name: "plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateScheduleOccurrence(ctx, ScheduleOccurrence{ID: "missing-target", OwnerKind: "plan", OwnerID: "plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{"missing-task"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing schedule target error=%v", err)
		}
	})

	t.Run("rsync destination host", func(t *testing.T) {
		s := openTestStore(t)
		task := domain.Task{
			ID: "rsync-task", Name: "rsync-task", Engine: domain.RsyncEngine, Kind: domain.RsyncTask,
			Rsync:     &domain.RsyncSource{Path: "/source", DestinationHostID: "missing-host", DestinationPath: "/destination"},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateTask(ctx, task); !errors.Is(err, ErrConflict) {
			t.Fatalf("create rsync task error=%v", err)
		}
	})

	t.Run("task execution Agent", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecret(ctx, "pass", "repository-password", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "pass"); err != nil {
			t.Fatal(err)
		}
		task := domain.Task{
			ID: "agent-task", Name: "agent-task", Kind: domain.DirectoryTask, RepositoryID: "repo",
			Directory: &domain.DirectorySource{Path: "/source"}, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "missing-agent"},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateTask(ctx, task); !errors.Is(err, ErrConflict) {
			t.Fatalf("create Agent task error=%v", err)
		}
	})

	t.Run("task scope preview", func(t *testing.T) {
		s := openTestStore(t)
		err := s.CreateTaskScopePreview(ctx, TaskScopePreview{ID: "preview", TaskID: "missing-task", Fingerprint: "fingerprint", CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("create task scope preview error=%v", err)
		}
	})

	t.Run("restore verification task and repository", func(t *testing.T) {
		s := openTestStore(t)
		err := s.CreateRestoreVerification(ctx, RestoreVerificationRecord{
			ID: "verification", TaskID: "missing-task", RepositoryID: "missing-repository", SnapshotID: "snapshot",
			SelectionPath: "sample", Trigger: "manual", Status: "running", StartedAt: now,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("create restore verification error=%v", err)
		}
	})

	t.Run("snapshot repository", func(t *testing.T) {
		s := openTestStore(t)
		err := s.SaveSnapshotMetadata(ctx, "missing-repository", "snapshot", database.SnapshotMetadata{Engine: database.MySQL}, now)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("save snapshot metadata error=%v", err)
		}
	})

	t.Run("repository-owned records", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveRepositoryCapacity(ctx, "missing-repository", domain.RepositoryCapacity{TotalBytes: 100, AvailableBytes: 50, CheckedAt: now}); !errors.Is(err, ErrConflict) {
			t.Fatalf("save repository capacity error=%v", err)
		}
		if err := s.CreateMaintenancePreview(ctx, MaintenancePreview{ID: "preview", RepositoryID: "missing-repository", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("create maintenance preview error=%v", err)
		}
	})

	t.Run("Agent requests and lease", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveAgent(ctx, AgentRecord{ID: "agent", RemoteHostID: "missing-host", CertificateSerial: "serial", CreatedAt: now}); !errors.Is(err, ErrConflict) {
			t.Fatalf("save Agent error=%v", err)
		}
		request := AgentFilesystemRequest{ID: "request", AgentID: "missing-agent", Definition: json.RawMessage(`{}`), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateAgentFilesystemRequest(ctx, request); !errors.Is(err, ErrConflict) {
			t.Fatalf("create filesystem request error=%v", err)
		}
		lease := AgentLease{ID: "lease", AgentID: "missing-agent", TaskID: "missing-task", Engine: "restic", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateAgentLease(ctx, lease); !errors.Is(err, ErrConflict) {
			t.Fatalf("create Agent lease error=%v", err)
		}
		if err := s.BindAgentRemoteHost(ctx, "agent", "missing-host"); !errors.Is(err, ErrConflict) {
			t.Fatalf("bind Agent to missing host error=%v", err)
		}
	})
}
