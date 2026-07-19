package dbrestore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/maboo-run/shadoc/internal/database"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
)

// DumpFileRepository is the repository-side part of restoring a database
// snapshot as a local dump file. It deliberately has no database connection
// or secret dependency.
type DumpFileRepository interface {
	Snapshots(context.Context, string) ([]repositoryservice.Snapshot, error)
	RestoreDumpFile(context.Context, string, string, string, string, int) error
}

type DumpFileRequest struct {
	RepositoryID string `json:"repositoryId"`
	SnapshotID   string `json:"snapshotId"`
	// TargetDirectory is an existing local directory. The final filename is
	// taken from the trusted snapshot metadata during preflight.
	TargetDirectory string `json:"target"`
	ConfirmationID  string `json:"confirmationId,omitempty"`
	// DownloadKiBPerSecond is derived server-side from the bound task policy.
	DownloadKiBPerSecond int `json:"-"`
}

type DumpFilePreflightResult struct {
	Metadata database.SnapshotMetadata `json:"metadata"`
	// Target is the final file path inside TargetDirectory.
	Target   string `json:"target"`
	Behavior string `json:"behavior"`
}

type DumpFileService struct {
	repository DumpFileRepository
}

func NewDumpFileService(repository DumpFileRepository) *DumpFileService {
	return &DumpFileService{repository: repository}
}

func (s *DumpFileService) Preflight(ctx context.Context, request DumpFileRequest) (DumpFilePreflightResult, error) {
	if s == nil || s.repository == nil {
		return DumpFilePreflightResult{}, errors.New("dump file restore is unavailable")
	}
	if strings.TrimSpace(request.RepositoryID) == "" || strings.TrimSpace(request.SnapshotID) == "" {
		return DumpFilePreflightResult{}, errors.New("repository and snapshot are required")
	}
	if request.DownloadKiBPerSecond < 0 {
		return DumpFilePreflightResult{}, errors.New("download limit cannot be negative")
	}
	directory, err := repositoryservice.ValidateExistingDirectoryTarget(request.TargetDirectory)
	if err != nil {
		return DumpFilePreflightResult{}, err
	}
	metadata, err := s.snapshotMetadata(ctx, request.RepositoryID, request.SnapshotID)
	if err != nil {
		return DumpFilePreflightResult{}, err
	}
	if !safeDumpFilename(metadata.Filename) {
		return DumpFilePreflightResult{}, errors.New("database snapshot filename is unsafe")
	}
	target, err := repositoryservice.ValidateNewFileTarget(filepath.Join(directory, metadata.Filename))
	if err != nil {
		return DumpFilePreflightResult{}, fmt.Errorf("dump file %q cannot be created in output directory: %w", metadata.Filename, err)
	}
	return DumpFilePreflightResult{Metadata: metadata, Target: target, Behavior: "create_file"}, nil
}

func (s *DumpFileService) Restore(ctx context.Context, request DumpFileRequest) error {
	result, err := s.Preflight(ctx, request)
	if err != nil {
		return err
	}
	return s.repository.RestoreDumpFile(ctx, request.RepositoryID, request.SnapshotID, result.Metadata.Filename, result.Target, request.DownloadKiBPerSecond)
}

func (s *DumpFileService) snapshotMetadata(ctx context.Context, repositoryID, snapshotID string) (database.SnapshotMetadata, error) {
	snapshots, err := s.repository.Snapshots(ctx, repositoryID)
	if err != nil {
		return database.SnapshotMetadata{}, fmt.Errorf("load restore snapshot: %w", err)
	}
	for _, snapshot := range snapshots {
		if snapshot.ID != snapshotID {
			continue
		}
		metadata, decodeErr := database.DecodeMetadataTags(snapshot.Tags)
		if decodeErr != nil {
			return database.SnapshotMetadata{}, fmt.Errorf("decode snapshot database metadata: %w", decodeErr)
		}
		return metadata, nil
	}
	return database.SnapshotMetadata{}, errors.New("selected snapshot does not exist")
}

func safeDumpFilename(filename string) bool {
	if filename == "" || filepath.Base(filename) != filename || filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\\x00\r\n") {
		return false
	}
	return true
}
