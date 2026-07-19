package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
)

type Verification struct {
	CheckedAt     time.Time
	ClientVersion string
	ServerVersion string
	Error         string
}

type Verifier interface {
	Verify(context.Context, domain.DatabaseConnection, string) Verification
}

type ConnectionTester interface {
	Test(context.Context, domain.DatabaseConnection, string) Verification
}

type SystemVerifier struct {
	Executor command.Executor
	Now      func() time.Time
	Native   ConnectionTester
}

func (v SystemVerifier) Verify(ctx context.Context, connection domain.DatabaseConnection, password string) Verification {
	now := v.Now
	if now == nil {
		now = time.Now
	}
	result := Verification{CheckedAt: now().UTC()}
	fail := func(err error) Verification { result.Error = err.Error(); return result }
	native := v.Native
	if native == nil {
		native = NativeTester{Now: v.Now}
	}
	result = native.Test(ctx, connection, password)
	if result.CheckedAt.IsZero() {
		result.CheckedAt = now().UTC()
	}
	if result.Error != "" {
		return result
	}
	executor := v.Executor
	if executor == nil {
		executor = command.OSExecutor{}
	}
	versionProgram, adminProgram, err := verificationPrograms(connection)
	if err != nil {
		return fail(err)
	}
	programs := []string{versionProgram, adminProgram}
	if connection.Engine == domain.PostgreSQL && connection.Purpose == domain.RestoreConnection {
		programs = append(programs, connection.ToolPaths["create"])
	}
	for _, program := range programs {
		info, statErr := os.Stat(program)
		if statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return fail(errors.New("数据库客户端不可执行"))
		}
	}
	versionRun, err := executor.Run(ctx, command.Spec{Program: versionProgram, Args: []string{"--version"}})
	if err != nil {
		return fail(errors.New("无法执行数据库客户端版本检查"))
	}
	engine := Engine(connection.Engine)
	result.ClientVersion = parseClientVersion(engine, versionRun.Stdout+"\n"+versionRun.Stderr)
	if result.ClientVersion == "" || !expectedClientIdentity(connection, versionRun.Stdout+versionRun.Stderr) {
		return fail(errors.New("数据库客户端身份或版本无法验证"))
	}
	adminVersion, err := executor.Run(ctx, command.Spec{Program: adminProgram, Args: []string{"--version"}})
	if err != nil || !expectedAdminIdentity(connection, adminVersion.Stdout+adminVersion.Stderr) {
		return fail(errors.New("数据库管理客户端身份无法验证"))
	}
	if connection.Engine == domain.PostgreSQL && connection.Purpose == domain.RestoreConnection {
		createVersion, createErr := executor.Run(ctx, command.Spec{Program: connection.ToolPaths["create"], Args: []string{"--version"}})
		if createErr != nil || !strings.Contains(strings.ToLower(createVersion.Stdout+createVersion.Stderr), "createdb") {
			return fail(errors.New("数据库创建客户端身份无法验证"))
		}
	}
	clientMajor, clientErr := versionMajor(result.ClientVersion)
	serverMajor, serverErr := versionMajor(result.ServerVersion)
	if clientErr != nil || serverErr != nil || clientMajor < serverMajor {
		return fail(errors.New("数据库客户端版本低于服务端版本，无法保证兼容性"))
	}
	return result
}

func verificationPrograms(connection domain.DatabaseConnection) (string, string, error) {
	version := connection.ToolPaths["dump"]
	if connection.Purpose == domain.RestoreConnection {
		version = connection.ToolPaths["restore"]
	}
	admin := connection.ToolPaths["admin"]
	if !filepath.IsAbs(version) || !filepath.IsAbs(admin) {
		return "", "", errors.New("数据库客户端路径缺失或不是绝对路径")
	}
	return version, admin, nil
}

func expectedClientIdentity(connection domain.DatabaseConnection, output string) bool {
	value := strings.ToLower(output)
	if connection.Engine == domain.MySQL {
		if connection.Purpose == domain.RestoreConnection {
			return expectedMySQLAdminIdentity(value)
		}
		return expectedMySQLDumpIdentity(value)
	}
	if connection.Purpose == domain.RestoreConnection {
		return strings.Contains(value, "pg_restore")
	}
	return strings.Contains(value, "pg_dump")
}

func expectedAdminIdentity(connection domain.DatabaseConnection, output string) bool {
	value := strings.ToLower(output)
	if connection.Engine == domain.MySQL {
		return expectedMySQLAdminIdentity(value)
	}
	return strings.Contains(value, "psql")
}

func expectedMySQLDumpIdentity(output string) bool {
	return strings.Contains(output, "mysqldump") || strings.Contains(output, "mariadb-dump")
}

func expectedMySQLAdminIdentity(output string) bool {
	return (strings.Contains(output, "mysql") && !strings.Contains(output, "mysqldump")) ||
		(strings.Contains(output, "mariadb") && !strings.Contains(output, "mariadb-dump"))
}
