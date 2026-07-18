package database

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maboo-run/shadoc/internal/command"
)

type postgresConnector struct{ baseConnector }

func NewPostgres(tempRoot string) Connector {
	return &postgresConnector{baseConnector{tempRoot: tempRoot}}
}

func (c *postgresConnector) PrepareMetadata(_ context.Context, connection Connection, database, credentialPath string) (PreparedMetadata, error) {
	if err := validateExport(connection, database, PostgreSQL); err != nil {
		return PreparedMetadata{}, err
	}
	if !filepath.IsAbs(connection.AdminProgram) || credentialPath == "" {
		return PreparedMetadata{}, errors.New("postgres metadata requires an absolute psql path and credentials")
	}
	host := postgresHost(connection)
	args := []string{"--no-password", "--host", host}
	if connection.Network == TCP {
		args = append(args, "--port", strconv.Itoa(connection.Port))
	}
	args = append(args, "--username", connection.Username, "--dbname", database, "--tuples-only", "--no-align", "--field-separator=|", "--command", "SELECT current_setting('server_version'), current_setting('server_encoding'), datcollate FROM pg_catalog.pg_database WHERE datname = current_database()")
	return PreparedMetadata{
		Server: command.Spec{Program: connection.AdminProgram, Args: args, Env: postgresEnvironment(connection, credentialPath)},
		Client: command.Spec{Program: connection.DumpProgram, Args: []string{"--version"}},
		Parse: func(server, client string) (SnapshotMetadata, error) {
			return ParseMetadata(PostgreSQL, server, client)
		},
	}, nil
}

func (c *postgresConnector) PrepareExport(_ context.Context, connection Connection, database string) (PreparedCommand, SnapshotMetadata, error) {
	if err := validateExport(connection, database, PostgreSQL); err != nil {
		return PreparedCommand{}, SnapshotMetadata{}, err
	}
	host := postgresHost(connection)
	port := strconv.Itoa(connection.Port)
	if connection.Network == Unix {
		host = connection.SocketPath
		port = "*"
	}
	passfile := strings.Join([]string{
		escapePGPass(host), escapePGPass(port), escapePGPass(database), escapePGPass(connection.Username), escapePGPass(connection.Password),
	}, ":") + "\n"
	credentialPath, cleanup, err := c.credentialFile("postgres", passfile)
	if err != nil {
		return PreparedCommand{}, SnapshotMetadata{}, err
	}
	args := []string{"--format=custom", "--no-password", "--host", host}
	if connection.Network == TCP {
		args = append(args, "--port", strconv.Itoa(connection.Port))
	}
	args = append(args, "--username", connection.Username, "--dbname", database)
	return PreparedCommand{
		Spec: command.Spec{
			Program: connection.DumpProgram,
			Args:    args,
			Env:     postgresEnvironment(connection, credentialPath),
		},
		CredentialPath: credentialPath,
		Cleanup:        cleanup,
	}, SnapshotMetadata{Engine: PostgreSQL, Database: database, Format: "postgres-custom", Filename: dumpFilename(database, ".dump")}, nil
}

func (c *postgresConnector) PrepareRestore(_ context.Context, connection Connection, database, format string) (PreparedRestore, error) {
	if err := validateRestore(connection, database, PostgreSQL, format); err != nil {
		return PreparedRestore{}, err
	}
	marker, err := restoreMarker()
	if err != nil {
		return PreparedRestore{}, err
	}
	host, port := postgresHost(connection), strconv.Itoa(connection.Port)
	if connection.Network == Unix {
		host, port = connection.SocketPath, "*"
	}
	passfile := strings.Join([]string{escapePGPass(host), escapePGPass(port), "*", escapePGPass(connection.Username), escapePGPass(connection.Password)}, ":") + "\n"
	credentialPath, cleanup, err := c.credentialFile("postgres-restore", passfile)
	if err != nil {
		return PreparedRestore{}, err
	}
	env := postgresEnvironment(connection, credentialPath)
	connectionArgs := []string{"--no-password", "--host", host, "--username", connection.Username}
	if connection.Network == TCP {
		connectionArgs = append(connectionArgs, "--port", strconv.Itoa(connection.Port))
	}
	existsArgs := append(append([]string{}, connectionArgs...), "--dbname", "postgres", "--tuples-only", "--no-align", "--command", "SELECT COUNT(*) FROM pg_catalog.pg_database WHERE datname = '"+strings.ReplaceAll(database, "'", "''")+"'")
	inspectArgs := append(append([]string{}, connectionArgs...), "--dbname", database, "--tuples-only", "--no-align", "--command", "SELECT (SELECT COUNT(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema') + (SELECT COUNT(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema') + (SELECT COUNT(*) FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema' AND t.typtype IN ('e','d','c'))")
	createArgs := append(append([]string{}, connectionArgs...), database)
	if connection.RestoreEncoding != "" {
		createArgs = append(append([]string{}, connectionArgs...), "--encoding", connection.RestoreEncoding, "--lc-collate", connection.RestoreCollation, database)
	}
	// With no filename pg_restore reads its custom archive from standard input.
	// An explicit "-" is interpreted as a literal filename by supported clients.
	importArgs := append(append([]string{}, connectionArgs...), "--dbname", database, "--exit-on-error")
	quotedDatabase := `"` + strings.ReplaceAll(database, `"`, `""`) + `"`
	quotedMarker := `"` + marker + `"`
	markArgs := append(append([]string{}, connectionArgs...), "--dbname", database, "--command", "CREATE TABLE "+quotedMarker+" (operation_guard boolean PRIMARY KEY); INSERT INTO "+quotedMarker+" VALUES (true)")
	cleanupCheckArgs := append(append([]string{}, connectionArgs...), "--dbname", database, "--tuples-only", "--no-align", "--command", "SELECT CASE WHEN to_regclass('public."+marker+"') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datname = '"+strings.ReplaceAll(database, "'", "''")+"' AND pid <> pg_backend_pid()) THEN 1 ELSE 0 END")
	dropArgs := append(append([]string{}, connectionArgs...), "--dbname", "postgres", "--command", "DROP DATABASE "+quotedDatabase)
	unmarkArgs := append(append([]string{}, connectionArgs...), "--dbname", database, "--command", "DROP TABLE "+quotedMarker)
	return PreparedRestore{
		Exists:        command.Spec{Program: connection.AdminProgram, Args: existsArgs, Env: env},
		Inspect:       command.Spec{Program: connection.AdminProgram, Args: inspectArgs, Env: env},
		Create:        command.Spec{Program: connection.CreateProgram, Args: createArgs, Env: env},
		MarkCreated:   command.Spec{Program: connection.AdminProgram, Args: markArgs, Env: env},
		Import:        command.Spec{Program: connection.RestoreProgram, Args: importArgs, Env: env},
		CleanupCheck:  command.Spec{Program: connection.AdminProgram, Args: cleanupCheckArgs, Env: env},
		DropCreated:   command.Spec{Program: connection.AdminProgram, Args: dropArgs, Env: env},
		UnmarkCreated: command.Spec{Program: connection.AdminProgram, Args: unmarkArgs, Env: env},
		EmptyOutput:   func(output string) bool { return strings.TrimSpace(output) == "0" }, Cleanup: cleanup,
		ExistsOutput:   func(output string) bool { return strings.TrimSpace(output) == "1" },
		MissingOutput:  func(output string) bool { return strings.TrimSpace(output) == "0" },
		CleanupAllowed: func(output string) bool { return strings.TrimSpace(output) == "1" },
	}, nil
}

func postgresHost(connection Connection) string {
	if connection.Network == TCP && connection.TLSServerName != "" {
		return connection.TLSServerName
	}
	return connection.Host
}

func postgresEnvironment(connection Connection, passfile string) map[string]string {
	env := map[string]string{"PGPASSFILE": passfile}
	mode := connection.TLSMode
	switch mode {
	case "preferred":
		mode = "prefer"
	case "required":
		mode = "require"
	case "disabled":
		mode = "disable"
	}
	if mode != "" {
		env["PGSSLMODE"] = mode
	}
	if connection.TLSCA != "" {
		env["PGSSLROOTCERT"] = connection.TLSCA
	}
	if connection.TLSClientCert != "" {
		env["PGSSLCERT"] = connection.TLSClientCert
	}
	if connection.TLSClientKey != "" {
		env["PGSSLKEY"] = connection.TLSClientKey
	}
	if connection.TLSServerName != "" && connection.Network == TCP {
		env["PGHOSTADDR"] = connection.Host
	}
	return env
}

func escapePGPass(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, ":", `\:`)
}
