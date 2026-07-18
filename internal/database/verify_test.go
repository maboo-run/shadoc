package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
)

type verificationExecutor struct {
	specs []command.Spec
}

func (e *verificationExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.specs = append(e.specs, spec)
	if len(e.specs) == 1 {
		return command.Result{Stdout: "mysqldump  Ver 8.0.36 Distrib 8.0.36"}, nil
	}
	if len(e.specs) == 2 {
		return command.Result{Stdout: "mysql  Ver 8.0.36 Distrib 8.0.36"}, nil
	}
	return command.Result{Stdout: "8.0.36\nGRANT SELECT, SHOW VIEW ON *.* TO backup@%"}, nil
}

func TestSystemVerifierChecksClientIdentityAndAuthenticatedServerWithoutLeakingPassword(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"mysqldump", "mysql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executor := &verificationExecutor{}
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	result := (SystemVerifier{Executor: executor, TempRoot: dir, Now: func() time.Time { return now }}).Verify(context.Background(), domain.DatabaseConnection{
		Name: "source", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", ToolPaths: map[string]string{"dump": filepath.Join(dir, "mysqldump"), "admin": filepath.Join(dir, "mysql")},
	}, "super-secret-password")
	if result.Error != "" || result.ClientVersion != "8.0.36" || result.ServerVersion != "8.0.36" || !result.CheckedAt.Equal(now) {
		t.Fatalf("verification=%+v", result)
	}
	for _, spec := range executor.specs {
		if strings.Contains(strings.Join(spec.Args, " ")+strings.Join(mapValues(spec.Env), " "), "super-secret-password") {
			t.Fatalf("password leaked into process arguments or environment: %+v", spec)
		}
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

type postgresVerificationExecutor struct {
	calls int
	auth  command.Spec
}

func (e *postgresVerificationExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.calls++
	if e.calls == 1 {
		return command.Result{Stdout: "pg_dump (PostgreSQL) 16.3"}, nil
	}
	if e.calls == 2 {
		return command.Result{Stdout: "psql (PostgreSQL) 16.3"}, nil
	}
	e.auth = spec
	return command.Result{Stdout: "16.3|t\n"}, nil
}

func TestSystemVerifierUsesPostgresClientAndPermissionProbe(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pg_dump", "psql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executor := &postgresVerificationExecutor{}
	result := (SystemVerifier{Executor: executor, TempRoot: dir}).Verify(context.Background(), domain.DatabaseConnection{
		Name: "pg", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.UnixNetwork,
		SocketPath: "/var/run/postgresql", Username: "backup", TLS: domain.TLSConfig{Mode: "verify-full"},
		ToolPaths: map[string]string{"dump": filepath.Join(dir, "pg_dump"), "admin": filepath.Join(dir, "psql")},
	}, "postgres-secret")
	if result.Error != "" || result.ClientVersion != "16.3" || result.ServerVersion != "16.3" {
		t.Fatalf("verification=%+v", result)
	}
	joined := strings.Join(executor.auth.Args, " ")
	if !strings.Contains(joined, "has_database_privilege") || !strings.Contains(joined, "/var/run/postgresql") || executor.auth.Env["PGSSLMODE"] != "verify-full" {
		t.Fatalf("postgres auth spec=%+v", executor.auth)
	}
}
