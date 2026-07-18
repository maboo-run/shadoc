package database

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
)

func TestMySQLAndPostgresPreparePurposeBoundExports(t *testing.T) {
	tests := []struct {
		name       string
		connector  Connector
		connection Connection
		wantFormat string
		wantFile   string
	}{
		{
			name:      "mysql sql",
			connector: NewMySQL(t.TempDir()),
			connection: Connection{Engine: MySQL, Purpose: Backup, Network: TCP,
				Host: "127.0.0.1", Port: 3306, Username: "backup", Password: "mysql-secret",
				DumpProgram: "/usr/bin/mysqldump"},
			wantFormat: "sql", wantFile: "gitea.sql",
		},
		{
			name:      "postgres custom",
			connector: NewPostgres(t.TempDir()),
			connection: Connection{Engine: PostgreSQL, Purpose: Backup, Network: TCP,
				Host: "127.0.0.1", Port: 5432, Username: "backup", Password: "pg-secret",
				DumpProgram: "/usr/bin/pg_dump"},
			wantFormat: "postgres-custom", wantFile: "gitea.dump",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, metadata, err := tt.connector.PrepareExport(context.Background(), tt.connection, "gitea")
			if err != nil {
				t.Fatalf("prepare export: %v", err)
			}
			if metadata.Format != tt.wantFormat || metadata.Filename != tt.wantFile || metadata.Database != "gitea" {
				t.Fatalf("metadata = %+v", metadata)
			}
			joined := prepared.Spec.Program + " " + strings.Join(prepared.Spec.Args, " ")
			if strings.Contains(joined, tt.connection.Password) {
				t.Fatalf("password leaked into argv: %s", joined)
			}
			info, err := os.Stat(prepared.CredentialPath)
			if err != nil {
				t.Fatalf("stat credentials: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("credential mode = %o", info.Mode().Perm())
			}
			contents, err := os.ReadFile(prepared.CredentialPath)
			if err != nil || !strings.Contains(string(contents), tt.connection.Password) {
				t.Fatalf("credential file did not contain secret: %v", err)
			}
			prepared.Cleanup()
			if _, err := os.Stat(prepared.CredentialPath); !os.IsNotExist(err) {
				t.Fatalf("credential file was not removed: %v", err)
			}
		})
	}
}

func TestPrepareRestoreUsesUniqueOwnershipMarkers(t *testing.T) {
	connector := NewMySQL(t.TempDir()).(RestoreConnector)
	connection := Connection{Engine: MySQL, Purpose: Restore, Network: TCP, Host: "db", Port: 3306, Username: "restore", Password: "secret", RestoreProgram: "/tools/mysql"}
	markers := make([]string, 0, 2)
	pattern := regexp.MustCompile(`__restic_control_restore_[0-9a-f]{24}`)
	for range 2 {
		prepared, err := connector.PrepareRestore(context.Background(), connection, "restored_db", "sql")
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Cleanup()
		marker := pattern.FindString(strings.Join(prepared.MarkCreated.Args, " "))
		if marker == "" {
			t.Fatalf("ownership marker missing from %v", prepared.MarkCreated.Args)
		}
		markers = append(markers, marker)
	}
	if markers[0] == markers[1] {
		t.Fatalf("restore ownership marker was reused: %q", markers[0])
	}
}

func TestExportRejectsRestoreConnection(t *testing.T) {
	connector := NewMySQL(t.TempDir())
	_, _, err := connector.PrepareExport(context.Background(), Connection{
		Engine: MySQL, Purpose: Restore, Network: TCP, Host: "db", Port: 3306,
		Username: "root", Password: "secret", DumpProgram: "/usr/bin/mysqldump",
	}, "gitea")
	if err == nil {
		t.Fatal("restore connection must not be accepted for backup")
	}
}

func TestPrepareRestoreUsesPurposeBoundCredentialsAndSafePreflight(t *testing.T) {
	tests := []struct {
		name          string
		connector     RestoreConnector
		connection    Connection
		format        string
		inspectMarker string
		createMarker  string
		importMarker  string
	}{
		{
			name: "mysql sql", connector: NewMySQL(t.TempDir()).(RestoreConnector), format: "sql",
			connection:    Connection{Engine: MySQL, Purpose: Restore, Network: TCP, Host: "db", Port: 3306, Username: "restore", Password: "mysql-secret", RestoreProgram: "/tools/mysql"},
			inspectMarker: "information_schema.tables", createMarker: "CREATE DATABASE", importMarker: "--database",
		},
		{
			name: "postgres custom", connector: NewPostgres(t.TempDir()).(RestoreConnector), format: "postgres-custom",
			connection:    Connection{Engine: PostgreSQL, Purpose: Restore, Network: TCP, Host: "db", Port: 5432, Username: "restore", Password: "pg-secret", RestoreProgram: "/tools/pg_restore", AdminProgram: "/tools/psql", CreateProgram: "/tools/createdb"},
			inspectMarker: "pg_catalog.pg_class", createMarker: "/tools/createdb", importMarker: "--exit-on-error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := tt.connector.PrepareRestore(context.Background(), tt.connection, "restored_db", tt.format)
			if err != nil {
				t.Fatalf("prepare restore: %v", err)
			}
			defer prepared.Cleanup()
			all := prepared.Inspect.Program + " " + strings.Join(prepared.Inspect.Args, " ") + " " + prepared.Create.Program + " " + strings.Join(prepared.Create.Args, " ") + " " + prepared.MarkCreated.Program + " " + strings.Join(prepared.MarkCreated.Args, " ") + " " + prepared.Import.Program + " " + strings.Join(prepared.Import.Args, " ") + " " + prepared.CleanupCheck.Program + " " + strings.Join(prepared.CleanupCheck.Args, " ") + " " + prepared.DropCreated.Program + " " + strings.Join(prepared.DropCreated.Args, " ") + " " + prepared.UnmarkCreated.Program + " " + strings.Join(prepared.UnmarkCreated.Args, " ")
			if strings.Contains(all, tt.connection.Password) {
				t.Fatalf("password leaked into argv: %s", all)
			}
			for _, marker := range []string{tt.inspectMarker, tt.createMarker, tt.importMarker} {
				if !strings.Contains(all, marker) {
					t.Fatalf("missing %q in %s", marker, all)
				}
			}
			if !prepared.EmptyOutput("0\n") || prepared.EmptyOutput("1\n") {
				t.Fatal("empty database result parser is unsafe")
			}
			for name, spec := range map[string]command.Spec{"mark": prepared.MarkCreated, "cleanup-check": prepared.CleanupCheck, "drop": prepared.DropCreated, "unmark": prepared.UnmarkCreated} {
				if spec.Program == "" {
					t.Fatalf("%s command is missing", name)
				}
			}
			if !strings.Contains(all, "__restic_control_restore_") || !prepared.CleanupAllowed("1\n") || prepared.CleanupAllowed("0\n") {
				t.Fatalf("unsafe restore cleanup guard: %s", all)
			}
			if tt.name == "mysql sql" && (!strings.Contains(all, "information_schema.user_privileges") || !strings.Contains(all, "PROCESS")) {
				t.Fatalf("mysql cleanup does not fail closed without PROCESS visibility: %s", all)
			}
			if tt.name == "postgres custom" && len(prepared.Import.Args) > 0 && prepared.Import.Args[len(prepared.Import.Args)-1] == "-" {
				t.Fatal("pg_restore treats an explicit dash as a filename instead of standard input")
			}
		})
	}
}

func TestPrepareRestoreRejectsBackupConnectionAndFormatMismatch(t *testing.T) {
	connector := NewMySQL(t.TempDir()).(RestoreConnector)
	connection := Connection{Engine: MySQL, Purpose: Backup, Network: TCP, Host: "db", Port: 3306, Username: "restore", Password: "secret", RestoreProgram: "/tools/mysql"}
	if _, err := connector.PrepareRestore(context.Background(), connection, "gitea", "sql"); err == nil {
		t.Fatal("backup connection accepted for restore")
	}
	connection.Purpose = Restore
	if _, err := connector.PrepareRestore(context.Background(), connection, "gitea", "postgres-custom"); err == nil {
		t.Fatal("format mismatch accepted")
	}
}

func TestConnectorsCarryTLSPolicyIntoNativeClients(t *testing.T) {
	mysql := NewMySQL(t.TempDir())
	preparedMySQL, _, err := mysql.PrepareExport(context.Background(), Connection{
		Engine: MySQL, Purpose: Backup, Network: TCP, Host: "db", Port: 3306,
		Username: "backup", Password: "secret", DumpProgram: "/tools/mysqldump",
		TLSMode: "verify-full", TLSCA: "/etc/ssl/db-ca.pem", TLSClientCert: "/etc/ssl/client.pem", TLSClientKey: "/etc/ssl/client.key",
	}, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	defer preparedMySQL.Cleanup()
	mysqlArgs := strings.Join(preparedMySQL.Spec.Args, " ")
	for _, expected := range []string{"--ssl-mode=VERIFY_IDENTITY", "--ssl-ca=/etc/ssl/db-ca.pem", "--ssl-cert=/etc/ssl/client.pem", "--ssl-key=/etc/ssl/client.key"} {
		if !strings.Contains(mysqlArgs, expected) {
			t.Fatalf("mysql TLS option %q missing from %s", expected, mysqlArgs)
		}
	}

	postgres := NewPostgres(t.TempDir())
	preparedPG, _, err := postgres.PrepareExport(context.Background(), Connection{
		Engine: PostgreSQL, Purpose: Backup, Network: TCP, Host: "db", Port: 5432,
		Username: "backup", Password: "secret", DumpProgram: "/tools/pg_dump",
		TLSMode: "required", TLSCA: "/etc/ssl/db-ca.pem", TLSClientCert: "/etc/ssl/client.pem", TLSClientKey: "/etc/ssl/client.key", TLSServerName: "db.internal",
	}, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	defer preparedPG.Cleanup()
	if preparedPG.Spec.Env["PGSSLMODE"] != "require" || preparedPG.Spec.Env["PGSSLROOTCERT"] != "/etc/ssl/db-ca.pem" || preparedPG.Spec.Env["PGSSLCERT"] != "/etc/ssl/client.pem" || preparedPG.Spec.Env["PGSSLKEY"] != "/etc/ssl/client.key" || preparedPG.Spec.Env["PGHOSTADDR"] != "db" || !strings.Contains(strings.Join(preparedPG.Spec.Args, " "), "--host db.internal") {
		t.Fatalf("postgres TLS environment = %+v", preparedPG.Spec.Env)
	}
}

func TestRestoreCreationUsesValidatedSnapshotEncodingAndCollation(t *testing.T) {
	mysql, err := NewMySQL(t.TempDir()).(RestoreConnector).PrepareRestore(context.Background(), Connection{
		Engine: MySQL, Purpose: Restore, Network: TCP, Host: "db", Port: 3306, Username: "restore", Password: "secret", RestoreProgram: "/tools/mysql",
		RestoreEncoding: "utf8mb4", RestoreCollation: "utf8mb4_0900_ai_ci",
	}, "restored_db", "sql")
	if err != nil {
		t.Fatal(err)
	}
	defer mysql.Cleanup()
	mysqlCreate := strings.Join(mysql.Create.Args, " ")
	if !strings.Contains(mysqlCreate, "CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci") {
		t.Fatalf("mysql create=%s", mysqlCreate)
	}

	postgres, err := NewPostgres(t.TempDir()).(RestoreConnector).PrepareRestore(context.Background(), Connection{
		Engine: PostgreSQL, Purpose: Restore, Network: TCP, Host: "db", Port: 5432, Username: "restore", Password: "secret", RestoreProgram: "/tools/pg_restore", AdminProgram: "/tools/psql", CreateProgram: "/tools/createdb",
		RestoreEncoding: "UTF8", RestoreCollation: "en_US.UTF-8",
	}, "restored_db", "postgres-custom")
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Cleanup()
	postgresCreate := strings.Join(postgres.Create.Args, " ")
	if !strings.Contains(postgresCreate, "--encoding UTF8") || !strings.Contains(postgresCreate, "--lc-collate en_US.UTF-8") {
		t.Fatalf("postgres create=%s", postgresCreate)
	}
}

func TestRestoreCreationRejectsUnsafeEncodingOrCollation(t *testing.T) {
	_, err := NewMySQL(t.TempDir()).(RestoreConnector).PrepareRestore(context.Background(), Connection{
		Engine: MySQL, Purpose: Restore, Network: TCP, Host: "db", Port: 3306, Username: "restore", Password: "secret", RestoreProgram: "/tools/mysql",
		RestoreEncoding: "utf8mb4; DROP DATABASE mysql", RestoreCollation: "utf8mb4_bin",
	}, "restored_db", "sql")
	if err == nil {
		t.Fatal("unsafe MySQL encoding accepted")
	}
}
