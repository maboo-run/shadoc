package database

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
)

type enumerationExecutor struct {
	result command.Result
	specs  []command.Spec
	mode   os.FileMode
	secret string
}

func (e *enumerationExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.specs = append(e.specs, spec)
	credential := ""
	for _, argument := range spec.Args {
		if strings.HasPrefix(argument, "--defaults-extra-file=") {
			credential = strings.TrimPrefix(argument, "--defaults-extra-file=")
		}
	}
	if credential == "" {
		credential = spec.Env["PGPASSFILE"]
	}
	if credential != "" {
		info, err := os.Stat(credential)
		if err != nil {
			return command.Result{}, err
		}
		e.mode = info.Mode().Perm()
		contents, err := os.ReadFile(credential)
		if err != nil {
			return command.Result{}, err
		}
		e.secret = string(contents)
	}
	return e.result, nil
}

func TestSystemEnumeratorListsMySQLDatabasesWithFixedQueryAndProtectedCredential(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	admin := filepath.Join(directory, "mysql")
	if err := os.WriteFile(admin, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := &enumerationExecutor{result: command.Result{Stdout: "metrics\ninformation_schema\napp\nmetrics\n"}}
	items, err := (SystemEnumerator{Executor: executor, TempRoot: directory, Now: func() time.Time { return now }}).List(t.Context(), domain.DatabaseConnection{
		Name: "production", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", ToolPaths: map[string]string{"admin": admin},
		Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now.Add(-time.Hour)},
	}, "database-secret")
	if err != nil {
		t.Fatalf("list databases: %v", err)
	}
	if !slices.Equal(items, []string{"app", "metrics"}) {
		t.Fatalf("items=%v", items)
	}
	if len(executor.specs) != 1 {
		t.Fatalf("specs=%d", len(executor.specs))
	}
	joined := strings.Join(executor.specs[0].Args, " ")
	if !strings.Contains(joined, "INFORMATION_SCHEMA.SCHEMATA") || strings.Contains(joined, "database-secret") {
		t.Fatalf("unsafe or unexpected mysql arguments: %s", joined)
	}
	if executor.mode != 0o600 || !strings.Contains(executor.secret, "database-secret") {
		t.Fatalf("credential mode=%o contents=%q", executor.mode, executor.secret)
	}
}

func TestSystemEnumeratorListsPostgreSQLDatabasesWithoutShellOrPasswordEnvironment(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	admin := filepath.Join(directory, "psql")
	if err := os.WriteFile(admin, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := &enumerationExecutor{result: command.Result{Stdout: "postgres\ncustomers\n"}}
	items, err := (SystemEnumerator{Executor: executor, TempRoot: directory, Now: func() time.Time { return now }}).List(t.Context(), domain.DatabaseConnection{
		Name: "production", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.UnixNetwork,
		SocketPath: "/var/run/postgresql", Username: "backup", TLS: domain.TLSConfig{Mode: "verify-full"}, ToolPaths: map[string]string{"admin": admin},
		Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now.Add(-time.Hour)},
	}, "postgres-secret")
	if err != nil {
		t.Fatalf("list databases: %v", err)
	}
	if !slices.Equal(items, []string{"customers", "postgres"}) {
		t.Fatalf("items=%v", items)
	}
	spec := executor.specs[0]
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "pg_database") || spec.Program != admin || spec.Env["PGPASSFILE"] == "" || spec.Env["PGPASSWORD"] != "" {
		t.Fatalf("postgres spec=%+v", spec)
	}
	if strings.Contains(joined+strings.Join(mapValues(spec.Env), " "), "postgres-secret") || executor.mode != 0o600 {
		t.Fatalf("password leaked or credential mode=%o", executor.mode)
	}
}

func TestSystemEnumeratorRejectsConnectionsWithoutValidBackupPreflight(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	admin := filepath.Join(t.TempDir(), "mysql")
	if err := os.WriteFile(admin, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := domain.DatabaseConnection{
		Name: "production", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", ToolPaths: map[string]string{"admin": admin},
		Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now.Add(-25 * time.Hour)},
	}
	executor := &enumerationExecutor{result: command.Result{Stdout: "app\n"}}
	service := SystemEnumerator{Executor: executor, Now: func() time.Time { return now }}
	if items, err := service.List(t.Context(), base, "secret"); err != nil || !slices.Equal(items, []string{"app"}) {
		t.Fatalf("old but valid preflight items=%v err=%v", items, err)
	}
	executor.specs = nil
	base.Preflight.CheckedAt = now.Add(time.Second)
	if _, err := service.List(t.Context(), base, "secret"); err == nil || !strings.Contains(err.Error(), "预检") {
		t.Fatalf("future preflight error=%v", err)
	}
	base.Preflight.CheckedAt = now
	base.Preflight.Error = "failed"
	if _, err := service.List(t.Context(), base, "secret"); err == nil || !strings.Contains(err.Error(), "预检") {
		t.Fatalf("failed preflight error=%v", err)
	}
	base.Purpose = domain.RestoreConnection
	if _, err := service.List(t.Context(), base, "secret"); err == nil || !strings.Contains(err.Error(), "备份连接") {
		t.Fatalf("restore connection error=%v", err)
	}
	if len(executor.specs) != 0 {
		t.Fatalf("unsafe execution occurred: %d", len(executor.specs))
	}
}
