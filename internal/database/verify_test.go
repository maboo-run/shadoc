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
	specs       []command.Spec
	adminOutput string
}

func (e *verificationExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.specs = append(e.specs, spec)
	if len(e.specs) == 1 {
		return command.Result{Stdout: "mysqldump  Ver 8.0.36 Distrib 8.0.36"}, nil
	}
	if len(e.specs) == 2 {
		if e.adminOutput != "" {
			return command.Result{Stdout: e.adminOutput}, nil
		}
		return command.Result{Stdout: "mysql  Ver 8.0.36 Distrib 8.0.36"}, nil
	}
	return command.Result{Stdout: "8.0.36\nGRANT SELECT, SHOW VIEW ON *.* TO backup@%"}, nil
}

type nativeVerificationStub struct {
	result   Verification
	password string
}

func (s *nativeVerificationStub) Test(_ context.Context, _ domain.DatabaseConnection, password string) Verification {
	s.password = password
	return s.result
}

func TestSystemVerifierChecksClientIdentityAndAuthenticatedServerWithoutLeakingPassword(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"mysqldump", "mysql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executor := &verificationExecutor{}
	native := &nativeVerificationStub{result: Verification{ServerVersion: "8.0.36"}}
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	result := (SystemVerifier{Executor: executor, Native: native, Now: func() time.Time { return now }}).Verify(context.Background(), domain.DatabaseConnection{
		Name: "source", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", ToolPaths: map[string]string{"dump": filepath.Join(dir, "mysqldump"), "admin": filepath.Join(dir, "mysql")},
	}, "super-secret-password")
	if native.password != "super-secret-password" {
		t.Fatalf("native tester password=%q", native.password)
	}
	if result.Error != "" || result.ClientVersion != "8.0.36" || result.ServerVersion != "8.0.36" || !result.CheckedAt.Equal(now) {
		t.Fatalf("verification=%+v", result)
	}
	for _, spec := range executor.specs {
		if strings.Contains(strings.Join(spec.Args, " ")+strings.Join(mapValues(spec.Env), " "), "super-secret-password") {
			t.Fatalf("password leaked into process arguments or environment: %+v", spec)
		}
	}
}

func TestSystemVerifierRejectsDumpClientConfiguredAsMySQLAdmin(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "mysqldump")
	if err := os.WriteFile(program, []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	native := &nativeVerificationStub{result: Verification{ServerVersion: "8.0.36"}}
	result := (SystemVerifier{
		Executor: &verificationExecutor{adminOutput: "mysqldump  Ver 8.0.36 Distrib 8.0.36"}, Native: native,
	}).Verify(context.Background(), domain.DatabaseConnection{
		Name: "source", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", ToolPaths: map[string]string{"dump": program, "admin": program},
	}, "secret")
	if result.Error == "" || !strings.Contains(result.Error, "数据库管理客户端身份无法验证") {
		t.Fatalf("accepted mysqldump as mysql admin: %+v", result)
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
}

func (e *postgresVerificationExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.calls++
	if e.calls == 1 {
		return command.Result{Stdout: "pg_dump (PostgreSQL) 16.3"}, nil
	}
	if e.calls == 2 {
		return command.Result{Stdout: "psql (PostgreSQL) 16.3"}, nil
	}
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
	native := &nativeVerificationStub{result: Verification{ServerVersion: "16.3"}}
	result := (SystemVerifier{Executor: executor, Native: native}).Verify(context.Background(), domain.DatabaseConnection{
		Name: "pg", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.UnixNetwork,
		SocketPath: "/var/run/postgresql", Username: "backup", TLS: domain.TLSConfig{Mode: "verify-full"},
		ToolPaths: map[string]string{"dump": filepath.Join(dir, "pg_dump"), "admin": filepath.Join(dir, "psql")},
	}, "postgres-secret")
	if result.Error != "" || result.ClientVersion != "16.3" || result.ServerVersion != "16.3" {
		t.Fatalf("verification=%+v", result)
	}
	if executor.calls != 2 {
		t.Fatalf("expected only client version checks, got %d calls", executor.calls)
	}
}
