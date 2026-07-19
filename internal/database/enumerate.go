package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
)

const (
	maximumDatabaseCount = 1000
	maximumDatabaseBytes = 128
)

// Enumerator discovers logical databases through a previously verified backup
// connection. Implementations must use a fixed, read-only query.
type Enumerator interface {
	List(context.Context, domain.DatabaseConnection, string) ([]string, error)
}

type SystemEnumerator struct {
	Executor command.Executor
	TempRoot string
	Now      func() time.Time
}

func (e SystemEnumerator) List(ctx context.Context, connection domain.DatabaseConnection, password string) ([]string, error) {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	current := now().UTC()
	if err := connection.Validate(); err != nil || password == "" {
		return nil, errors.New("数据库连接配置或凭据无效")
	}
	if connection.Purpose != domain.BackupConnection {
		return nil, errors.New("只能枚举已验证的备份连接")
	}
	if connection.Status != "ready" || connection.Preflight.Error != "" || connection.Preflight.CheckedAt.IsZero() || connection.Preflight.CheckedAt.After(current) {
		return nil, errors.New("数据库连接预检缺失、失败或时间无效")
	}
	adminProgram := connection.ToolPaths["admin"]
	if !filepath.IsAbs(adminProgram) {
		return nil, errors.New("数据库管理客户端必须使用绝对路径")
	}
	info, err := os.Stat(adminProgram)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("数据库管理客户端不可执行")
	}
	spec, cleanup, err := enumerationSpec(connection, password, adminProgram, e.TempRoot)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	executor := e.Executor
	if executor == nil {
		executor = command.OSExecutor{OutputLimit: 256 << 10}
	}
	result, err := executor.Run(ctx, spec)
	if err != nil {
		return nil, errors.New("无法读取数据库列表，请检查网络、TLS、认证与权限")
	}
	return parseDatabaseNames(connection.Engine, result.Stdout)
}

func enumerationSpec(connection domain.DatabaseConnection, password, adminProgram, tempRoot string) (command.Spec, func(), error) {
	if tempRoot == "" {
		tempRoot = os.TempDir()
	}
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return command.Spec{}, func() {}, err
	}
	directory, err := os.MkdirTemp(tempRoot, "restic-control-db-list-")
	if err != nil {
		return command.Spec{}, func() {}, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return command.Spec{}, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	credential := filepath.Join(directory, "credentials")
	if connection.Engine == domain.MySQL {
		contents := "[client]\nuser=" + connection.Username + "\npassword=" + password + "\n"
		if connection.Network == domain.TCPNetwork {
			contents += fmt.Sprintf("protocol=tcp\nhost=%s\nport=%d\n", connection.Host, connection.Port)
		} else {
			contents += "protocol=socket\nsocket=" + connection.SocketPath + "\n"
		}
		if err := os.WriteFile(credential, []byte(contents), 0o600); err != nil {
			cleanup()
			return command.Spec{}, func() {}, err
		}
		args := append([]string{"--defaults-extra-file=" + credential}, mysqlTLSArgs(Connection{TLSMode: connection.TLS.Mode, TLSCA: connection.TLS.CA, TLSClientCert: connection.TLS.ClientCert, TLSClientKey: connection.TLS.ClientKey})...)
		args = append(args, "--batch", "--skip-column-names", "--execute", "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys') ORDER BY SCHEMA_NAME")
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
	port := connection.Port
	if port == 0 {
		port = 5432
	}
	contents := escape(host) + ":" + fmt.Sprint(port) + ":*:" + escape(connection.Username) + ":" + escape(password) + "\n"
	if err := os.WriteFile(credential, []byte(contents), 0o600); err != nil {
		cleanup()
		return command.Spec{}, func() {}, err
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
	if tlsMode == "" {
		tlsMode = "prefer"
	}
	environment := map[string]string{"PGPASSFILE": credential, "PGSSLMODE": tlsMode}
	if connection.TLS.CA != "" {
		environment["PGSSLROOTCERT"] = connection.TLS.CA
	}
	if connection.TLS.ClientCert != "" {
		environment["PGSSLCERT"] = connection.TLS.ClientCert
	}
	if connection.TLS.ClientKey != "" {
		environment["PGSSLKEY"] = connection.TLS.ClientKey
	}
	args := []string{"--no-psqlrc", "--host", host, "--username", connection.Username, "--tuples-only", "--no-align", "--command", "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname"}
	if connection.Network == domain.TCPNetwork {
		args = append(args[:4], append([]string{"--port", fmt.Sprint(connection.Port)}, args[4:]...)...)
	}
	return command.Spec{Program: adminProgram, Args: args, Env: environment}, cleanup, nil
}

func parseDatabaseNames(engine domain.DatabaseEngine, output string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !utf8.ValidString(name) || len(name) > maximumDatabaseBytes || strings.ContainsAny(name, "\x00\r\t") {
			return nil, errors.New("数据库返回了无效的逻辑库名称")
		}
		if engine == domain.MySQL {
			switch strings.ToLower(name) {
			case "information_schema", "mysql", "performance_schema", "sys":
				continue
			}
		}
		seen[name] = struct{}{}
		if len(seen) > maximumDatabaseCount {
			return nil, errors.New("逻辑库数量超过安全上限")
		}
	}
	items := make([]string, 0, len(seen))
	for name := range seen {
		items = append(items, name)
	}
	sort.Strings(items)
	return items, nil
}
