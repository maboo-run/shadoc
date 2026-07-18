package database

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maboo-run/shadoc/internal/command"
)

type mysqlConnector struct{ baseConnector }

func NewMySQL(tempRoot string) Connector {
	return &mysqlConnector{baseConnector{tempRoot: tempRoot}}
}

func (c *mysqlConnector) PrepareMetadata(_ context.Context, connection Connection, database, credentialPath string) (PreparedMetadata, error) {
	if err := validateExport(connection, database, MySQL); err != nil {
		return PreparedMetadata{}, err
	}
	if !filepath.IsAbs(connection.AdminProgram) || credentialPath == "" {
		return PreparedMetadata{}, errors.New("mysql metadata requires an absolute mysql client path and credentials")
	}
	args := append([]string{"--defaults-extra-file=" + credentialPath}, mysqlTLSArgs(connection)...)
	args = append(args, "--database", database, "--batch", "--skip-column-names", "--execute", "SELECT VERSION(), @@character_set_database, @@collation_database")
	return PreparedMetadata{
		Server: command.Spec{Program: connection.AdminProgram, Args: args},
		Client: command.Spec{Program: connection.DumpProgram, Args: []string{"--version"}},
		Parse:  func(server, client string) (SnapshotMetadata, error) { return ParseMetadata(MySQL, server, client) },
	}, nil
}

func (c *mysqlConnector) PrepareExport(_ context.Context, connection Connection, database string) (PreparedCommand, SnapshotMetadata, error) {
	if err := validateExport(connection, database, MySQL); err != nil {
		return PreparedCommand{}, SnapshotMetadata{}, err
	}
	var config strings.Builder
	config.WriteString("[client]\nuser=")
	config.WriteString(connection.Username)
	config.WriteString("\npassword=")
	config.WriteString(connection.Password)
	config.WriteString("\n")
	if connection.Network == TCP {
		config.WriteString("protocol=tcp\nhost=")
		config.WriteString(connection.Host)
		config.WriteString("\nport=")
		config.WriteString(strconv.Itoa(connection.Port))
		config.WriteString("\n")
	} else {
		config.WriteString("protocol=socket\nsocket=")
		config.WriteString(connection.SocketPath)
		config.WriteString("\n")
	}
	credentialPath, cleanup, err := c.credentialFile("mysql", config.String())
	if err != nil {
		return PreparedCommand{}, SnapshotMetadata{}, err
	}
	args := []string{
		"--defaults-extra-file=" + credentialPath,
		"--single-transaction", "--quick", "--routines", "--events", "--triggers", "--hex-blob",
		"--", database,
	}
	args = append(args[:1], append(mysqlTLSArgs(connection), args[1:]...)...)
	return PreparedCommand{
		Spec:           command.Spec{Program: connection.DumpProgram, Args: args},
		CredentialPath: credentialPath,
		Cleanup:        cleanup,
	}, SnapshotMetadata{Engine: MySQL, Database: database, Format: "sql", Filename: dumpFilename(database, ".sql")}, nil
}

func (c *mysqlConnector) PrepareRestore(_ context.Context, connection Connection, database, format string) (PreparedRestore, error) {
	if err := validateRestore(connection, database, MySQL, format); err != nil {
		return PreparedRestore{}, err
	}
	marker, err := restoreMarker()
	if err != nil {
		return PreparedRestore{}, err
	}
	var config strings.Builder
	config.WriteString("[client]\nuser=")
	config.WriteString(connection.Username)
	config.WriteString("\npassword=")
	config.WriteString(connection.Password)
	config.WriteString("\n")
	if connection.Network == TCP {
		config.WriteString("protocol=tcp\nhost=")
		config.WriteString(connection.Host)
		config.WriteString("\nport=")
		config.WriteString(strconv.Itoa(connection.Port))
		config.WriteString("\n")
	} else {
		config.WriteString("protocol=socket\nsocket=")
		config.WriteString(connection.SocketPath)
		config.WriteString("\n")
	}
	credentialPath, cleanup, err := c.credentialFile("mysql-restore", config.String())
	if err != nil {
		return PreparedRestore{}, err
	}
	base := append([]string{"--defaults-extra-file=" + credentialPath}, mysqlTLSArgs(connection)...)
	encodedName := fmt.Sprintf("%x", database)
	existsQuery := "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = CONVERT(0x" + encodedName + " USING utf8mb4)"
	inspectQuery := "SELECT (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = CONVERT(0x" + encodedName + " USING utf8mb4)) + (SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = CONVERT(0x" + encodedName + " USING utf8mb4)) + (SELECT COUNT(*) FROM information_schema.events WHERE event_schema = CONVERT(0x" + encodedName + " USING utf8mb4)) + (SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = CONVERT(0x" + encodedName + " USING utf8mb4))"
	// Do not use IF NOT EXISTS: a concurrently created target must fail rather than
	// silently importing into a database that was never inspected.
	createQuery := "CREATE DATABASE `" + strings.ReplaceAll(database, "`", "``") + "`"
	if connection.RestoreEncoding != "" {
		createQuery += " CHARACTER SET " + connection.RestoreEncoding + " COLLATE " + connection.RestoreCollation
	}
	quotedDatabase := "`" + strings.ReplaceAll(database, "`", "``") + "`"
	quotedMarker := "`" + marker + "`"
	markQuery := "CREATE TABLE " + quotedDatabase + "." + quotedMarker + " (operation_guard TINYINT NOT NULL PRIMARY KEY); INSERT INTO " + quotedDatabase + "." + quotedMarker + " VALUES (1)"
	cleanupCheckQuery := "SELECT ((SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = CONVERT(0x" + encodedName + " USING utf8mb4) AND table_name = '" + marker + "') = 1 AND (SELECT COUNT(*) FROM information_schema.user_privileges WHERE privilege_type = 'PROCESS' AND grantee = CONCAT(QUOTE(SUBSTRING_INDEX(CURRENT_USER(), '@', 1)), '@', QUOTE(SUBSTRING_INDEX(CURRENT_USER(), '@', -1)))) = 1 AND (SELECT COUNT(*) FROM information_schema.processlist WHERE db = CONVERT(0x" + encodedName + " USING utf8mb4) AND id <> CONNECTION_ID()) = 0)"
	dropQuery := "DROP DATABASE " + quotedDatabase
	unmarkQuery := "DROP TABLE " + quotedDatabase + "." + quotedMarker
	return PreparedRestore{
		Exists:        command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--batch", "--skip-column-names", "--execute", existsQuery)},
		Inspect:       command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--batch", "--skip-column-names", "--execute", inspectQuery)},
		Create:        command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--execute", createQuery)},
		MarkCreated:   command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--execute", markQuery)},
		Import:        command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--database", database)},
		CleanupCheck:  command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--batch", "--skip-column-names", "--execute", cleanupCheckQuery)},
		DropCreated:   command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--execute", dropQuery)},
		UnmarkCreated: command.Spec{Program: connection.RestoreProgram, Args: append(append([]string{}, base...), "--execute", unmarkQuery)},
		EmptyOutput:   func(output string) bool { return strings.TrimSpace(output) == "0" }, Cleanup: cleanup,
		ExistsOutput:   func(output string) bool { return strings.TrimSpace(output) == "1" },
		MissingOutput:  func(output string) bool { return strings.TrimSpace(output) == "0" },
		CleanupAllowed: func(output string) bool { return strings.TrimSpace(output) == "1" },
	}, nil
}

func mysqlTLSArgs(connection Connection) []string {
	mode := strings.ToUpper(strings.ReplaceAll(connection.TLSMode, "-", "_"))
	if mode == "" {
		mode = "PREFERRED"
	}
	if mode == "VERIFY_FULL" {
		mode = "VERIFY_IDENTITY"
	}
	args := []string{"--ssl-mode=" + mode}
	if connection.TLSCA != "" {
		args = append(args, "--ssl-ca="+connection.TLSCA)
	}
	if connection.TLSClientCert != "" {
		args = append(args, "--ssl-cert="+connection.TLSClientCert)
	}
	if connection.TLSClientKey != "" {
		args = append(args, "--ssl-key="+connection.TLSClientKey)
	}
	return args
}
