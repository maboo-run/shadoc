//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/backup"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/dbrestore"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/restic"
	coreRestore "github.com/maboo-run/shadoc/internal/restore"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestRealMySQLBackupRestore(t *testing.T) {
	host := os.Getenv("RESTIC_CONTROL_E2E_MYSQL_HOST")
	portText := os.Getenv("RESTIC_CONTROL_E2E_MYSQL_PORT")
	user := os.Getenv("RESTIC_CONTROL_E2E_MYSQL_USER")
	password := os.Getenv("RESTIC_CONTROL_E2E_MYSQL_PASSWORD")
	requireReleaseConfiguration(t, "real-mysql", host, portText, user, password)
	port := parsePort(t, "real-mysql", portText)
	dump := configuredProgram(t, "real-mysql", "RESTIC_CONTROL_E2E_MYSQL_DUMP", "mysqldump")
	client := configuredProgram(t, "real-mysql", "RESTIC_CONTROL_E2E_MYSQL_CLIENT", "mysql")
	resticPath := configuredProgram(t, "real-mysql", "RESTIC_CONTROL_E2E_RESTIC", "restic")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	sourceDB, targetDB, occupiedDB := "rc_mysql_src_"+suffix, "rc_mysql_dst_"+suffix, "rc_mysql_full_"+suffix
	credential := mysqlCredentialFile(t, user, password, host, port)
	mysql := func(databaseName, query string) string {
		args := []string{"--defaults-extra-file=" + credential, "--ssl-mode=PREFERRED", "--batch", "--skip-column-names"}
		if databaseName != "" {
			args = append(args, "--database", databaseName)
		}
		args = append(args, "--execute", query)
		return runCommand(t, ctx, command.Spec{Program: client, Args: args})
	}
	cleanup := func(name string) { mysql("", "DROP DATABASE IF EXISTS `"+name+"`") }
	defer cleanup(sourceDB)
	defer cleanup(targetDB)
	defer cleanup(occupiedDB)
	mysql("", "CREATE DATABASE `"+sourceDB+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	mysql(sourceDB, "CREATE TABLE items(id INT PRIMARY KEY, label VARCHAR(40), payload BLOB); INSERT INTO items VALUES (1,'alpha',X'000102'),(2,'beta',X'10203040'); CREATE PROCEDURE item_count() SELECT COUNT(*) AS total FROM items")

	fixture := newDatabaseFixture(t, ctx, resticPath)
	defer fixture.Close()
	backupConnection := fixture.addConnection(t, ctx, domain.DatabaseConnection{
		ID: "mysql-backup", Name: "mysql backup", Engine: domain.MySQL, Purpose: domain.BackupConnection,
		Network: domain.TCPNetwork, Host: host, Port: port, Username: user, TLS: domain.TLSConfig{Mode: "preferred"},
		ToolPaths: map[string]string{"dump": dump, "admin": client}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, password)
	restoreConnection := fixture.addConnection(t, ctx, domain.DatabaseConnection{
		ID: "mysql-restore", Name: "mysql restore", Engine: domain.MySQL, Purpose: domain.RestoreConnection,
		Network: domain.TCPNetwork, Host: host, Port: port, Username: user, TLS: domain.TLSConfig{Mode: "preferred"},
		ToolPaths: map[string]string{"restore": client, "admin": client}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, password)
	snapshot := fixture.backupDatabase(t, ctx, backupConnection, sourceDB, database.NewMySQL(fixture.tempRoot), database.NewPostgres(fixture.tempRoot))
	fixture.restoreDatabase(t, ctx, restoreConnection, snapshot, targetDB)
	if got := strings.TrimSpace(mysql(targetDB, "SELECT CONCAT(label,':',OCTET_LENGTH(payload)) FROM items ORDER BY id; CALL item_count()")); got != "alpha:3\nbeta:4\n2" {
		t.Fatalf("restored MySQL values=%q", got)
	}
	mysql("", "CREATE DATABASE `"+occupiedDB+"`")
	mysql(occupiedDB, "CREATE TABLE occupied(id INT)")
	err := fixture.restore(ctx, restoreConnection, snapshot, occupiedDB)
	if !errors.Is(err, coreRestore.ErrTargetNotEmpty) {
		t.Fatalf("non-empty MySQL target err=%v", err)
	}
	recordCheck("real-mysql", "passed", host+":"+portText)
}

func TestRealPostgreSQLBackupRestore(t *testing.T) {
	host := os.Getenv("RESTIC_CONTROL_E2E_POSTGRES_HOST")
	portText := os.Getenv("RESTIC_CONTROL_E2E_POSTGRES_PORT")
	user := os.Getenv("RESTIC_CONTROL_E2E_POSTGRES_USER")
	password := os.Getenv("RESTIC_CONTROL_E2E_POSTGRES_PASSWORD")
	requireReleaseConfiguration(t, "real-postgresql", host, portText, user, password)
	port := parsePort(t, "real-postgresql", portText)
	dump := configuredProgram(t, "real-postgresql", "RESTIC_CONTROL_E2E_POSTGRES_DUMP", "pg_dump")
	restoreProgram := configuredProgram(t, "real-postgresql", "RESTIC_CONTROL_E2E_POSTGRES_RESTORE", "pg_restore")
	psql := configuredProgram(t, "real-postgresql", "RESTIC_CONTROL_E2E_POSTGRES_CLIENT", "psql")
	createdb := configuredProgram(t, "real-postgresql", "RESTIC_CONTROL_E2E_POSTGRES_CREATEDB", "createdb")
	resticPath := configuredProgram(t, "real-postgresql", "RESTIC_CONTROL_E2E_RESTIC", "restic")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	sourceDB, targetDB, occupiedDB := "rc_pg_src_"+suffix, "rc_pg_dst_"+suffix, "rc_pg_full_"+suffix
	baseArgs := []string{"--no-password", "--host", host, "--port", portText, "--username", user}
	env := map[string]string{"PGPASSWORD": password, "PGSSLMODE": "prefer"}
	psqlRun := func(databaseName, query string) string {
		args := append(append([]string{}, baseArgs...), "--dbname", databaseName, "--tuples-only", "--no-align", "--command", query)
		return runCommand(t, ctx, command.Spec{Program: psql, Args: args, Env: env})
	}
	create := func(name string) {
		runCommand(t, ctx, command.Spec{Program: createdb, Args: append(append([]string{}, baseArgs...), name), Env: env})
	}
	drop := func(name string) {
		psqlRun("postgres", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='"+name+"' AND pid <> pg_backend_pid()")
		psqlRun("postgres", "DROP DATABASE IF EXISTS \""+name+"\"")
	}
	defer drop(sourceDB)
	defer drop(targetDB)
	defer drop(occupiedDB)
	create(sourceDB)
	psqlRun(sourceDB, "CREATE TABLE items(id integer PRIMARY KEY,label text,payload bytea); INSERT INTO items VALUES (1,'alpha',decode('000102','hex')),(2,'beta',decode('10203040','hex')); CREATE FUNCTION item_count() RETURNS bigint LANGUAGE SQL AS 'SELECT COUNT(*) FROM items'")

	fixture := newDatabaseFixture(t, ctx, resticPath)
	defer fixture.Close()
	backupConnection := fixture.addConnection(t, ctx, domain.DatabaseConnection{
		ID: "pg-backup", Name: "postgres backup", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection,
		Network: domain.TCPNetwork, Host: host, Port: port, Username: user, TLS: domain.TLSConfig{Mode: "preferred"},
		ToolPaths: map[string]string{"dump": dump, "admin": psql}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, password)
	restoreConnection := fixture.addConnection(t, ctx, domain.DatabaseConnection{
		ID: "pg-restore", Name: "postgres restore", Engine: domain.PostgreSQL, Purpose: domain.RestoreConnection,
		Network: domain.TCPNetwork, Host: host, Port: port, Username: user, TLS: domain.TLSConfig{Mode: "preferred"},
		ToolPaths: map[string]string{"restore": restoreProgram, "admin": psql, "create": createdb}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, password)
	snapshot := fixture.backupDatabase(t, ctx, backupConnection, sourceDB, database.NewMySQL(fixture.tempRoot), database.NewPostgres(fixture.tempRoot))
	fixture.restoreDatabase(t, ctx, restoreConnection, snapshot, targetDB)
	if got := strings.TrimSpace(psqlRun(targetDB, "SELECT label || ':' || octet_length(payload)::text FROM items ORDER BY id; SELECT item_count()")); got != "alpha:3\nbeta:4\n2" {
		t.Fatalf("restored PostgreSQL values=%q", got)
	}
	create(occupiedDB)
	psqlRun(occupiedDB, "CREATE TABLE occupied(id integer)")
	err := fixture.restore(ctx, restoreConnection, snapshot, occupiedDB)
	if !errors.Is(err, coreRestore.ErrTargetNotEmpty) {
		t.Fatalf("non-empty PostgreSQL target err=%v", err)
	}
	recordCheck("real-postgresql", "passed", host+":"+portText)
}

func TestDatabaseBackupPreflightUsesDumpWithoutResticWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("database backup fixture uses a temporary POSIX client fixture")
	}
	resticPath := configuredProgram(t, "database-backup-preflight", "RESTIC_CONTROL_E2E_RESTIC", "restic")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixture := newDatabaseFixture(t, ctx, resticPath)
	defer fixture.Close()

	dump := writeExecutableFixture(t, filepath.Join(fixture.root, "mysqldump"), `#!/bin/sh
case "$*" in
  *--version*) printf 'mysqldump  Ver 8.4.5\n' ; exit 0 ;;
esac
printf 'CREATE TABLE items(id INT PRIMARY KEY);\nINSERT INTO items VALUES (1);\n'
`)
	admin := writeExecutableFixture(t, filepath.Join(fixture.root, "mysql"), `#!/bin/sh
case "$*" in
  *--version*) printf 'mysql  Ver 8.4.5\n' ; exit 0 ;;
  *--execute*) printf '8.4.5\tutf8mb4\tutf8mb4_0900_ai_ci\n' ; exit 0 ;;
esac
exit 1
`)
	now := time.Now().UTC()
	connectionID := fixture.addConnection(t, ctx, domain.DatabaseConnection{
		ID: "mysql-test", Name: "fixture mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection,
		Network: domain.TCPNetwork, Host: "127.0.0.1", Port: 3306, Username: "backup", Status: "ready",
		Preflight: domain.DatabasePreflight{CheckedAt: now, ClientVersion: "8.4.5", ServerVersion: "8.4.5"},
		ToolPaths: map[string]string{"dump": dump, "admin": admin}, CreatedAt: now, UpdatedAt: now,
	}, "fixture-password")
	task := domain.Task{ID: "database-test-task", Name: "database test", Kind: domain.DatabaseTask, RepositoryID: "repo", Database: &domain.DatabaseSource{ConnectionID: connectionID, Database: "fixture"}, CreatedAt: now, UpdatedAt: now}
	if err := fixture.store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	service := backup.New(fixture.store, fixture.secrets, fixture.runner.engine, database.NewMySQL(fixture.tempRoot), database.NewPostgres(fixture.tempRoot), time.Now)
	service.SetMetadataExecutor(fixture.executor)
	if err := service.PreflightDatabaseBackup(ctx, task.ID); err != nil {
		t.Fatalf("database backup preflight failed: %v", err)
	}
	snapshots, err := fixture.repo.Snapshots(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("test snapshot was not cleaned up: %+v", snapshots)
	}
	failingDump := writeExecutableFixture(t, filepath.Join(fixture.root, "mysqldump-failing"), `#!/bin/sh
printf 'fixture dump failed\n' >&2
exit 23
`)
	connections, err := fixture.store.ListDatabaseConnections(ctx)
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	connections[0].ToolPaths["dump"] = failingDump
	connections[0].UpdatedAt = time.Now().UTC()
	if _, err := fixture.store.UpdateDatabaseConnection(ctx, connections[0], ""); err != nil {
		t.Fatal(err)
	}
	if err := service.PreflightDatabaseBackup(ctx, task.ID); err == nil {
		t.Fatal("database backup preflight accepted a failing dump client")
	}
	recordCheck("database-backup-preflight", "passed", "schema-only dump and read-only Restic verification passed without creating a snapshot")
}

func writeExecutableFixture(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

type databaseFixture struct {
	t        *testing.T
	root     string
	tempRoot string
	server   *sftpTestServer
	store    *store.Store
	secrets  *secret.Manager
	repo     *repository.Service
	runner   diagnosticRunner
	executor command.OSExecutor
}

func newDatabaseFixture(t *testing.T, ctx context.Context, resticPath string) *databaseFixture {
	t.Helper()
	root := t.TempDir()
	server := startSFTPServer(t, filepath.Join(root, "remote"))
	s, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.New(s, v, time.Now)
	keyID, _ := secrets.Put(ctx, "ssh-private-key", server.ClientPrivateKey)
	passwordID, _ := secrets.Put(ctx, "repository-password", []byte("database-e2e-repository-password"))
	now := time.Now().UTC()
	if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host", Name: "sftp", Host: "127.0.0.1", Port: server.Port, Username: "backup", HostFingerprint: server.KnownHostsLine, CreatedAt: now, UpdatedAt: now}, keyID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "database", RemoteHostID: "host", Path: "repository", Status: "uninitialized", CreatedAt: now, UpdatedAt: now}, passwordID); err != nil {
		t.Fatal(err)
	}
	runner := diagnosticRunner{engine: restic.New(resticPath, command.OSExecutor{}, filepath.Join(root, "restic-run")), t: t}
	repo := repository.New(s, secrets, runner)
	if err := repo.Initialize(ctx, "repo"); err != nil {
		t.Fatalf("initialize database E2E repository: %v", err)
	}
	return &databaseFixture{t: t, root: root, tempRoot: filepath.Join(root, "db-run"), server: server, store: s, secrets: secrets, repo: repo, runner: runner, executor: command.OSExecutor{}}
}

func (f *databaseFixture) Close() {
	_ = f.store.Close()
	f.server.Close()
}

func (f *databaseFixture) addConnection(t *testing.T, ctx context.Context, connection domain.DatabaseConnection, password string) string {
	t.Helper()
	purpose := "database-" + string(connection.Purpose) + "-password"
	secretID, err := f.secrets.Put(ctx, purpose, []byte(password))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateDatabaseConnection(ctx, connection, secretID); err != nil {
		t.Fatal(err)
	}
	return connection.ID
}

func (f *databaseFixture) backupDatabase(t *testing.T, ctx context.Context, connectionID, databaseName string, mysql, postgres database.Connector) string {
	t.Helper()
	now := time.Now().UTC()
	task := domain.Task{ID: "database-task", Name: "database", Kind: domain.DatabaseTask, RepositoryID: "repo", Database: &domain.DatabaseSource{ConnectionID: connectionID, Database: databaseName}, Resources: domain.ResourcePolicy{Compression: "auto"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := f.store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	service := backup.New(f.store, f.secrets, f.runner, mysql, postgres, time.Now)
	service.SetMetadataExecutor(f.executor)
	run, err := service.Run(ctx, task.ID, "", "e2e")
	if err != nil || run.Status != "success" || run.SnapshotID == "" {
		t.Fatalf("database backup run=%+v err=%v", run, err)
	}
	return run.SnapshotID
}

func (f *databaseFixture) restoreDatabase(t *testing.T, ctx context.Context, connectionID, snapshot, databaseName string) {
	t.Helper()
	if err := f.restore(ctx, connectionID, snapshot, databaseName); err != nil {
		t.Fatalf("restore database: %v", err)
	}
}

func (f *databaseFixture) restore(ctx context.Context, connectionID, snapshot, databaseName string) error {
	service := dbrestore.New(f.store, f.secrets, f.repo, f.executor, f.tempRoot)
	return service.Restore(ctx, dbrestore.Request{RepositoryID: "repo", SnapshotID: snapshot, ConnectionID: connectionID, Database: databaseName})
}

func parsePort(t *testing.T, name, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		requireReleaseConfiguration(t, name, "")
	}
	return port
}

func configuredProgram(t *testing.T, name, envName, fallback string) string {
	t.Helper()
	program := os.Getenv(envName)
	if program == "" {
		program, _ = exec.LookPath(fallback)
	}
	if program == "" || !filepath.IsAbs(program) {
		requireReleaseConfiguration(t, name, "")
	}
	return program
}

func mysqlCredentialFile(t *testing.T, user, password, host string, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mysql.cnf")
	content := fmt.Sprintf("[client]\nuser=%s\npassword=%s\nprotocol=tcp\nhost=%s\nport=%d\n", user, password, host, port)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCommand(t *testing.T, ctx context.Context, spec command.Spec) string {
	t.Helper()
	result, err := (command.OSExecutor{}).Run(ctx, spec)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("command %s failed exit=%d err=%v stderr=%s", filepath.Base(spec.Program), result.ExitCode, err, result.Stderr)
	}
	return result.Stdout
}
