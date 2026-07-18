package database

import (
	"context"
	"errors"
	"fmt"
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

type SystemVerifier struct {
	Executor command.Executor
	TempRoot string
	Now      func() time.Time
}

func (v SystemVerifier) Verify(ctx context.Context, connection domain.DatabaseConnection, password string) Verification {
	now := v.Now
	if now == nil {
		now = time.Now
	}
	result := Verification{CheckedAt: now().UTC()}
	fail := func(err error) Verification { result.Error = err.Error(); return result }
	if connection.Validate() != nil || password == "" {
		return fail(errors.New("数据库连接配置或凭据无效"))
	}
	executor := v.Executor
	if executor == nil {
		executor = command.OSExecutor{}
	}
	versionProgram, adminProgram, err := verificationPrograms(connection)
	if err != nil {
		return fail(err)
	}
	for _, program := range []string{versionProgram, adminProgram} {
		info, statErr := os.Stat(program)
		if statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return fail(fmt.Errorf("数据库客户端不可执行：%s", program))
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
	spec, cleanup, err := verificationSpec(connection, password, adminProgram, v.TempRoot)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	authRun, err := executor.Run(ctx, spec)
	if err != nil {
		return fail(errors.New("数据库网络、TLS、认证或权限预检失败"))
	}
	output := strings.TrimSpace(authRun.Stdout)
	if connection.Engine == domain.PostgreSQL {
		parts := strings.Split(output, "|")
		if len(parts) != 2 || strings.TrimSpace(parts[1]) != "t" {
			return fail(errors.New("数据库账号缺少当前用途所需权限"))
		}
		result.ServerVersion = strings.TrimSpace(parts[0])
	} else {
		lines := strings.Split(output, "\n")
		if len(lines) < 2 || !strings.Contains(strings.ToUpper(strings.Join(lines[1:], "\n")), "GRANT") {
			return fail(errors.New("无法验证数据库账号权限"))
		}
		result.ServerVersion = strings.TrimSpace(lines[0])
	}
	if result.ServerVersion == "" {
		return fail(errors.New("无法读取数据库服务端版本"))
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
		return "", "", errors.New("数据库客户端必须使用绝对路径")
	}
	return version, admin, nil
}

func expectedClientIdentity(connection domain.DatabaseConnection, output string) bool {
	value := strings.ToLower(output)
	if connection.Engine == domain.MySQL {
		return strings.Contains(value, "mysqldump")
	}
	return strings.Contains(value, "pg_dump") || strings.Contains(value, "pg_restore")
}

func expectedAdminIdentity(connection domain.DatabaseConnection, output string) bool {
	value := strings.ToLower(output)
	if connection.Engine == domain.MySQL {
		return strings.Contains(value, "mysql")
	}
	return strings.Contains(value, "psql")
}

func verificationSpec(connection domain.DatabaseConnection, password, adminProgram, tempRoot string) (command.Spec, func(), error) {
	if tempRoot == "" {
		tempRoot = os.TempDir()
	}
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return command.Spec{}, func() {}, err
	}
	dir, err := os.MkdirTemp(tempRoot, "restic-control-db-preflight-")
	if err != nil {
		return command.Spec{}, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	credential := filepath.Join(dir, "credentials")
	if connection.Engine == domain.MySQL {
		content := "[client]\nuser=" + connection.Username + "\npassword=" + password + "\n"
		if connection.Network == domain.TCPNetwork {
			content += fmt.Sprintf("protocol=tcp\nhost=%s\nport=%d\n", connection.Host, connection.Port)
		} else {
			content += "protocol=socket\nsocket=" + connection.SocketPath + "\n"
		}
		if err := os.WriteFile(credential, []byte(content), 0o600); err != nil {
			cleanup()
			return command.Spec{}, func() {}, err
		}
		args := append([]string{"--defaults-extra-file=" + credential}, mysqlTLSArgs(Connection{TLSMode: connection.TLS.Mode, TLSCA: connection.TLS.CA, TLSClientCert: connection.TLS.ClientCert, TLSClientKey: connection.TLS.ClientKey})...)
		args = append(args, "--batch", "--skip-column-names", "--execute", "SELECT VERSION(); SHOW GRANTS FOR CURRENT_USER()")
		return command.Spec{Program: adminProgram, Args: args}, cleanup, nil
	}
	host := connection.Host
	if connection.Network == domain.UnixNetwork {
		host = connection.SocketPath
	}
	escape := func(value string) string {
		value = strings.ReplaceAll(value, `\`, `\\`)
		return strings.ReplaceAll(value, ":", `\:`)
	}
	credentialPort := connection.Port
	if credentialPort == 0 {
		credentialPort = 5432
	}
	if err := os.WriteFile(credential, []byte(escape(host)+":"+fmt.Sprint(credentialPort)+":*:"+escape(connection.Username)+":"+escape(password)+"\n"), 0o600); err != nil {
		cleanup()
		return command.Spec{}, func() {}, err
	}
	privilege := "CONNECT"
	if connection.Purpose == domain.RestoreConnection {
		privilege = "CREATE"
	}
	tlsMode := connection.TLS.Mode
	switch tlsMode {
	case "preferred":
		tlsMode = "prefer"
	case "required":
		tlsMode = "require"
	case "disabled":
		tlsMode = "disable"
	}
	env := map[string]string{"PGPASSFILE": credential, "PGSSLMODE": tlsMode}
	if env["PGSSLMODE"] == "" {
		env["PGSSLMODE"] = "prefer"
	}
	if connection.TLS.CA != "" {
		env["PGSSLROOTCERT"] = connection.TLS.CA
	}
	if connection.TLS.ClientCert != "" {
		env["PGSSLCERT"] = connection.TLS.ClientCert
	}
	if connection.TLS.ClientKey != "" {
		env["PGSSLKEY"] = connection.TLS.ClientKey
	}
	args := []string{"--no-psqlrc", "--host", host, "--username", connection.Username, "--tuples-only", "--no-align", "--command", "SELECT current_setting('server_version') || '|' || has_database_privilege(current_user,current_database(),'" + privilege + "')"}
	if connection.Network == domain.TCPNetwork {
		args = append(args[:4], append([]string{"--port", fmt.Sprint(connection.Port)}, args[4:]...)...)
	}
	return command.Spec{Program: adminProgram, Args: args, Env: env}, cleanup, nil
}
