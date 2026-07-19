package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/maboo-run/shadoc/internal/command"
)

type Engine string
type Purpose string
type Network string

const (
	MySQL      Engine  = "mysql"
	PostgreSQL Engine  = "postgresql"
	Backup     Purpose = "backup"
	Restore    Purpose = "restore"
	TCP        Network = "tcp"
	Unix       Network = "unix"
)

type Connection struct {
	Engine           Engine
	Purpose          Purpose
	Network          Network
	Host             string
	Port             int
	SocketPath       string
	Username         string
	Password         string
	DumpProgram      string
	RestoreProgram   string
	AdminProgram     string
	CreateProgram    string
	TLSMode          string
	TLSCA            string
	TLSClientCert    string
	TLSClientKey     string
	TLSServerName    string
	RestoreEncoding  string
	RestoreCollation string
}

type SnapshotMetadata struct {
	Engine        Engine `json:"engine"`
	Database      string `json:"database"`
	Format        string `json:"format"`
	Filename      string `json:"filename"`
	ServerVersion string `json:"serverVersion,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
	Encoding      string `json:"encoding,omitempty"`
	Collation     string `json:"collation,omitempty"`
}

type PreparedCommand struct {
	Spec           command.Spec
	CredentialPath string
	Cleanup        func()
}

type Connector interface {
	PrepareExport(context.Context, Connection, string) (PreparedCommand, SnapshotMetadata, error)
}

// PreflightDumpArguments returns the fixed, engine-specific arguments used to
// validate a logical export without copying table data or writing a Restic
// snapshot. The caller must still use the prepared command and its cleanup.
func PreflightDumpArguments(engine Engine, args []string) ([]string, error) {
	result := append([]string(nil), args...)
	switch engine {
	case MySQL:
		for index, argument := range result {
			if argument != "--" {
				continue
			}
			withFlag := make([]string, 0, len(result)+1)
			withFlag = append(withFlag, result[:index]...)
			withFlag = append(withFlag, "--no-data")
			withFlag = append(withFlag, result[index:]...)
			return withFlag, nil
		}
		return nil, errors.New("MySQL dump command is missing its database delimiter")
	case PostgreSQL:
		for index, argument := range result {
			if argument != "--dbname" {
				continue
			}
			withFlag := make([]string, 0, len(result)+1)
			withFlag = append(withFlag, result[:index]...)
			withFlag = append(withFlag, "--schema-only")
			withFlag = append(withFlag, result[index:]...)
			return withFlag, nil
		}
		return nil, errors.New("PostgreSQL dump command is missing its database option")
	default:
		return nil, fmt.Errorf("unsupported database engine %q", engine)
	}
}

type PreparedMetadata struct {
	Server command.Spec
	Client command.Spec
	Parse  func(string, string) (SnapshotMetadata, error)
}

type MetadataConnector interface {
	Connector
	PrepareMetadata(context.Context, Connection, string, string) (PreparedMetadata, error)
}

type PreparedRestore struct {
	Exists         command.Spec
	Inspect        command.Spec
	Create         command.Spec
	MarkCreated    command.Spec
	Import         command.Spec
	CleanupCheck   command.Spec
	DropCreated    command.Spec
	UnmarkCreated  command.Spec
	EmptyOutput    func(string) bool
	ExistsOutput   func(string) bool
	MissingOutput  func(string) bool
	CleanupAllowed func(string) bool
	Cleanup        func()
}

type RestoreConnector interface {
	Connector
	PrepareRestore(context.Context, Connection, string, string) (PreparedRestore, error)
}

type baseConnector struct {
	tempRoot string
}

func restoreMarker() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate restore ownership marker: %w", err)
	}
	return "__restic_control_restore_" + hex.EncodeToString(raw), nil
}

func (b baseConnector) credentialFile(prefix, content string) (string, func(), error) {
	if err := os.MkdirAll(b.tempRoot, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create database temp root: %w", err)
	}
	dir, err := os.MkdirTemp(b.tempRoot, prefix+"-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create database temp directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write database credentials: %w", err)
	}
	return path, cleanup, nil
}

func validateExport(connection Connection, database string, engine Engine) error {
	if connection.Engine != engine {
		return fmt.Errorf("connector engine mismatch: %s", connection.Engine)
	}
	if connection.Purpose != Backup {
		return errors.New("backup requires a backup-purpose connection")
	}
	if strings.TrimSpace(connection.Username) == "" || connection.Password == "" || strings.TrimSpace(connection.DumpProgram) == "" || strings.TrimSpace(database) == "" {
		return errors.New("database, program and credentials are required")
	}
	if !filepath.IsAbs(connection.DumpProgram) {
		return errors.New("database dump program must be an absolute path")
	}
	if err := validateTLS(connection, engine); err != nil {
		return err
	}
	switch connection.Network {
	case TCP:
		if connection.Host == "" || connection.Port < 1 || connection.Port > 65535 {
			return errors.New("tcp connection requires host and port")
		}
	case Unix:
		if !filepath.IsAbs(connection.SocketPath) {
			return errors.New("unix connection requires absolute socket path")
		}
	default:
		return errors.New("unsupported database network")
	}
	return nil
}

func validateRestore(connection Connection, database string, engine Engine, format string) error {
	if connection.Engine != engine || connection.Purpose != Restore {
		return errors.New("restore requires a matching restore-purpose connection")
	}
	wantFormat := "sql"
	if engine == PostgreSQL {
		wantFormat = "postgres-custom"
	}
	if format != wantFormat {
		return fmt.Errorf("snapshot format %q is incompatible with %s", format, engine)
	}
	if strings.TrimSpace(connection.Username) == "" || connection.Password == "" || strings.TrimSpace(connection.RestoreProgram) == "" || !safeDatabaseName(database) {
		return errors.New("safe database name, restore program and credentials are required")
	}
	if engine == PostgreSQL && (connection.AdminProgram == "" || connection.CreateProgram == "") {
		return errors.New("postgres restore requires psql and createdb programs")
	}
	for _, program := range []string{connection.RestoreProgram, connection.AdminProgram, connection.CreateProgram} {
		if program != "" && !filepath.IsAbs(program) {
			return errors.New("database restore programs must use absolute paths")
		}
	}
	if err := validateTLS(connection, engine); err != nil {
		return err
	}
	if (connection.RestoreEncoding == "") != (connection.RestoreCollation == "") {
		return errors.New("restore encoding and collation must be configured together")
	}
	if connection.RestoreEncoding != "" {
		if engine == MySQL && (!mysqlCreationValue.MatchString(connection.RestoreEncoding) || !mysqlCreationValue.MatchString(connection.RestoreCollation)) {
			return errors.New("unsafe MySQL restore encoding or collation")
		}
		if engine == PostgreSQL && (!postgresCreationValue.MatchString(connection.RestoreEncoding) || !postgresCreationValue.MatchString(connection.RestoreCollation)) {
			return errors.New("unsafe PostgreSQL restore encoding or collation")
		}
	}
	switch connection.Network {
	case TCP:
		if connection.Host == "" || connection.Port < 1 || connection.Port > 65535 {
			return errors.New("tcp connection requires host and port")
		}
	case Unix:
		if !filepath.IsAbs(connection.SocketPath) {
			return errors.New("unix connection requires absolute socket path")
		}
	default:
		return errors.New("unsupported database network")
	}
	return nil
}

var mysqlCreationValue = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var postgresCreationValue = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

func validateTLS(connection Connection, engine Engine) error {
	switch connection.TLSMode {
	case "", "preferred", "required", "verify-ca", "verify-full", "disabled":
	default:
		return fmt.Errorf("unsupported TLS mode %q", connection.TLSMode)
	}
	if (connection.TLSClientCert == "") != (connection.TLSClientKey == "") {
		return errors.New("TLS client certificate and private key must be configured together")
	}
	if engine == MySQL && connection.TLSServerName != "" && !strings.EqualFold(connection.TLSServerName, connection.Host) {
		return errors.New("MySQL TLS server name must match the configured host")
	}
	return nil
}

var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]+$`)

func safeDatabaseName(value string) bool { return databaseNamePattern.MatchString(value) }

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func dumpFilename(database, extension string) string {
	name := strings.Trim(unsafeFilename.ReplaceAllString(database, "_"), "._-")
	if name == "" {
		name = "database"
	}
	return name + extension
}
