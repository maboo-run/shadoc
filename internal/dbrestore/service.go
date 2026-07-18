package dbrestore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
	core "github.com/maboo-run/shadoc/internal/restore"
	"github.com/maboo-run/shadoc/internal/store"
)

type Store interface {
	LoadDatabaseConnectionExecution(context.Context, string) (store.DatabaseConnectionExecution, error)
}
type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}
type Repository interface {
	Dump(context.Context, string, string, string, int, io.Writer) error
	Snapshots(context.Context, string) ([]repositoryservice.Snapshot, error)
}
type Request struct {
	RepositoryID   string `json:"repositoryId"`
	SnapshotID     string `json:"snapshotId"`
	Filename       string `json:"filename"`
	ConnectionID   string `json:"connectionId"`
	Database       string `json:"database"`
	Format         string `json:"format"`
	ConfirmationID string `json:"confirmationId,omitempty"`
	// DownloadKiBPerSecond is derived server-side from the bound task policy.
	// It is deliberately excluded from client JSON input.
	DownloadKiBPerSecond int `json:"-"`
}
type Service struct {
	store      Store
	secrets    Secrets
	repository Repository
	executor   command.Executor
	tempRoot   string
}

func New(s Store, secrets Secrets, repository Repository, executor command.Executor, tempRoot string) *Service {
	return &Service{store: s, secrets: secrets, repository: repository, executor: executor, tempRoot: tempRoot}
}

type boundDumper struct {
	repository           Repository
	repositoryID         string
	downloadKiBPerSecond int
}

func (b boundDumper) Dump(ctx context.Context, snapshot, filename string, output io.Writer) error {
	return b.repository.Dump(ctx, b.repositoryID, snapshot, filename, b.downloadKiBPerSecond, output)
}

type PreflightResult struct {
	Metadata database.SnapshotMetadata `json:"metadata"`
	Target   core.PreflightResult      `json:"target"`
}

func (s *Service) Preflight(ctx context.Context, r Request) (PreflightResult, error) {
	request, metadata, err := s.prepare(ctx, r)
	if err != nil {
		return PreflightResult{}, err
	}
	target, err := core.New(s.executor, boundDumper{repository: s.repository, repositoryID: r.RepositoryID, downloadKiBPerSecond: r.DownloadKiBPerSecond}).Preflight(ctx, request)
	return PreflightResult{Metadata: metadata, Target: target}, err
}

func (s *Service) Restore(ctx context.Context, r Request) error {
	request, _, err := s.prepare(ctx, r)
	if err != nil {
		return err
	}
	service := core.New(s.executor, boundDumper{repository: s.repository, repositoryID: r.RepositoryID, downloadKiBPerSecond: r.DownloadKiBPerSecond})
	return service.Restore(ctx, request)
}

func (s *Service) prepare(ctx context.Context, r Request) (core.Request, database.SnapshotMetadata, error) {
	if r.DownloadKiBPerSecond < 0 {
		return core.Request{}, database.SnapshotMetadata{}, errors.New("download limit cannot be negative")
	}
	record, err := s.store.LoadDatabaseConnectionExecution(ctx, r.ConnectionID)
	if err != nil {
		return core.Request{}, database.SnapshotMetadata{}, err
	}
	if record.Connection.Purpose != domain.RestoreConnection {
		return core.Request{}, database.SnapshotMetadata{}, errors.New("a restore-purpose connection is required")
	}
	snapshots, err := s.repository.Snapshots(ctx, r.RepositoryID)
	if err != nil {
		return core.Request{}, database.SnapshotMetadata{}, err
	}
	found := false
	var metadata database.SnapshotMetadata
	for _, snapshot := range snapshots {
		if snapshot.ID != r.SnapshotID {
			continue
		}
		found = true
		metadata, err = database.DecodeMetadataTags(snapshot.Tags)
		if err != nil {
			return core.Request{}, database.SnapshotMetadata{}, fmt.Errorf("decode snapshot database metadata: %w", err)
		}
		break
	}
	if !found {
		return core.Request{}, database.SnapshotMetadata{}, errors.New("selected snapshot does not exist")
	}
	if metadata.Engine != database.Engine(record.Connection.Engine) {
		return core.Request{}, database.SnapshotMetadata{}, errors.New("snapshot database metadata is missing or incompatible")
	}
	if r.Format != "" && r.Format != metadata.Format {
		return core.Request{}, database.SnapshotMetadata{}, fmt.Errorf("requested format does not match snapshot metadata")
	}
	if r.Filename != "" && r.Filename != metadata.Filename {
		return core.Request{}, database.SnapshotMetadata{}, fmt.Errorf("requested filename does not match snapshot metadata")
	}
	r.Format = metadata.Format
	r.Filename = metadata.Filename
	clientVersion, versionErr := s.executor.Run(ctx, command.Spec{Program: record.Connection.ToolPaths["restore"], Args: []string{"--version"}})
	if versionErr != nil || clientVersion.ExitCode != 0 {
		return core.Request{}, database.SnapshotMetadata{}, errors.New("restore client compatibility probe failed")
	}
	if err := database.CheckRestoreClientCompatibility(metadata, clientVersion.Stdout+"\n"+clientVersion.Stderr); err != nil {
		return core.Request{}, database.SnapshotMetadata{}, err
	}
	password, err := s.secrets.Get(ctx, record.PasswordSecretID, "database-restore-password")
	if err != nil {
		return core.Request{}, database.SnapshotMetadata{}, err
	}
	c := record.Connection
	connection := database.Connection{Engine: database.Engine(c.Engine), Purpose: database.Restore, Network: database.Network(c.Network), Host: c.Host, Port: c.Port, SocketPath: c.SocketPath, Username: c.Username, Password: string(password), RestoreProgram: c.ToolPaths["restore"], AdminProgram: c.ToolPaths["admin"], CreateProgram: c.ToolPaths["create"], TLSMode: c.TLS.Mode, TLSCA: c.TLS.CA, TLSClientCert: c.TLS.ClientCert, TLSClientKey: c.TLS.ClientKey, TLSServerName: c.TLS.ServerName, RestoreEncoding: metadata.Encoding, RestoreCollation: metadata.Collation}
	var connector database.RestoreConnector
	if c.Engine == domain.MySQL {
		connector = database.NewMySQL(s.tempRoot).(database.RestoreConnector)
	} else {
		connector = database.NewPostgres(s.tempRoot).(database.RestoreConnector)
	}
	return core.Request{Connector: connector, Connection: connection, Database: r.Database, Format: r.Format, SnapshotID: r.SnapshotID, Filename: r.Filename}, metadata, nil
}
