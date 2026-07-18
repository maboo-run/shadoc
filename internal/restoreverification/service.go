package restoreverification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

const maximumVerificationFiles = 100_000

type Storage interface {
	RestoreVerificationPolicy(context.Context, string) (domain.RestoreVerificationPolicy, error)
	LoadTaskExecution(context.Context, string) (store.TaskExecution, error)
	LatestSuccessfulRun(context.Context, string) (store.RunRecord, error)
	CreateRestoreVerification(context.Context, store.RestoreVerificationRecord) error
	FinishRestoreVerification(context.Context, string, store.RestoreVerificationFinish) error
	RestoreVerification(context.Context, string) (store.RestoreVerificationRecord, error)
	ResolveRestoreVerificationCleanup(context.Context, string) error
}

type Repository interface {
	RestoreDirectory(context.Context, string, string, string, []string, int) error
}

type Service struct {
	storage    Storage
	repository Repository
	root       string
	now        func() time.Time
	newID      func() string
	removeAll  func(string) error
	activeMu   sync.Mutex
	active     map[string]bool
}

func New(storage Storage, repository Repository, root string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{storage: storage, repository: repository, root: filepath.Clean(root), now: now, newID: verificationID, removeAll: os.RemoveAll, active: map[string]bool{}}
}

func (s *Service) Run(ctx context.Context, taskID, trigger string) (store.RestoreVerificationRecord, error) {
	if s.storage == nil || s.repository == nil || strings.TrimSpace(taskID) == "" || (trigger != "manual" && trigger != "scheduled") {
		return store.RestoreVerificationRecord{}, errors.New("restore verification service, task and trigger are required")
	}
	if !filepath.IsAbs(s.root) || s.root == string(filepath.Separator) {
		return store.RestoreVerificationRecord{}, errors.New("restore verification root must be an application-owned absolute directory")
	}
	s.activeMu.Lock()
	if s.active[taskID] {
		s.activeMu.Unlock()
		return store.RestoreVerificationRecord{}, errors.New("restore verification is already running for this task")
	}
	s.active[taskID] = true
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		delete(s.active, taskID)
		s.activeMu.Unlock()
	}()
	policy, err := s.storage.RestoreVerificationPolicy(ctx, taskID)
	if err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	if err := policy.Validate(); err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	if trigger == "scheduled" && !policy.Enabled {
		return store.RestoreVerificationRecord{}, errors.New("restore verification policy is disabled")
	}
	executionState, err := s.storage.LoadTaskExecution(ctx, taskID)
	if err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	if err := validateExecution(executionState); err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	latest, err := s.storage.LatestSuccessfulRun(ctx, taskID)
	if err != nil {
		return store.RestoreVerificationRecord{}, errors.New("restore verification requires a complete successful snapshot")
	}
	if latest.SnapshotID == "" {
		return store.RestoreVerificationRecord{}, errors.New("restore verification requires a complete successful snapshot")
	}
	id := s.newID()
	if !safeVerificationID(id) {
		return store.RestoreVerificationRecord{}, errors.New("restore verification id is invalid")
	}
	startedAt := s.now().UTC()
	record := store.RestoreVerificationRecord{
		ID: id, TaskID: taskID, RepositoryID: executionState.Task.RepositoryID, SnapshotID: latest.SnapshotID,
		SelectionPath: policy.SelectionPath, Trigger: trigger, Status: "running", StartedAt: startedAt, CleanupStatus: "pending",
	}
	if err := s.storage.CreateRestoreVerification(context.WithoutCancel(ctx), record); err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	attemptDirectory := filepath.Join(s.root, id)
	evidence, workErr := s.execute(ctx, attemptDirectory, executionState, latest.SnapshotID, policy)
	cleanupErr := s.removeAll(attemptDirectory)
	status, cleanupStatus := "success", "removed"
	if workErr != nil {
		status = "failed"
		if errors.Is(workErr, context.Canceled) || errors.Is(workErr, context.DeadlineExceeded) || ctx.Err() != nil {
			status = "cancelled"
		}
	}
	if cleanupErr != nil {
		status, cleanupStatus = "cleanup_required", "required"
	}
	finishedAt := s.now().UTC()
	finish := store.RestoreVerificationFinish{
		Status: status, FinishedAt: finishedAt, FileCount: evidence.FileCount, ByteCount: evidence.ByteCount,
		ManifestSHA256: evidence.ManifestSHA256, CleanupStatus: cleanupStatus, ErrorSummary: verificationErrorSummary(workErr, cleanupErr),
	}
	if err := s.storage.FinishRestoreVerification(context.WithoutCancel(ctx), id, finish); err != nil {
		return record, err
	}
	finished, readErr := s.storage.RestoreVerification(context.WithoutCancel(ctx), id)
	if readErr != nil {
		return record, readErr
	}
	if workErr != nil || cleanupErr != nil {
		return finished, errors.New(finish.ErrorSummary)
	}
	return finished, nil
}

func validateExecution(value store.TaskExecution) error {
	task := value.Task
	if !task.Enabled || task.EffectiveEngine() != domain.ResticEngine || task.Kind != domain.DirectoryTask || task.Directory == nil || task.EffectiveExecutionTarget().Kind != execution.Local {
		return errors.New("restore verification requires an enabled local Restic directory task")
	}
	if value.Repository.ID == "" || value.Repository.Status != "ready" {
		return errors.New("restore verification repository is not ready")
	}
	return nil
}

type verificationEvidence struct {
	FileCount      int
	ByteCount      int64
	ManifestSHA256 string
}

func (s *Service) execute(ctx context.Context, attemptDirectory string, executionState store.TaskExecution, snapshotID string, policy domain.RestoreVerificationPolicy) (verificationEvidence, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return verificationEvidence{}, errors.New("create restore verification root")
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return verificationEvidence{}, errors.New("protect restore verification root")
	}
	if err := os.Mkdir(attemptDirectory, 0o700); err != nil {
		return verificationEvidence{}, errors.New("create restore verification attempt")
	}
	target := filepath.Join(attemptDirectory, "restored")
	if err := s.repository.RestoreDirectory(ctx, executionState.Task.RepositoryID, snapshotID, target, []string{policy.SelectionPath}, executionState.Task.Resources.DownloadKiBPerSecond); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return verificationEvidence{}, err
		}
		return verificationEvidence{}, errors.New("restore verification could not restore the selected snapshot content")
	}
	evidence, err := hashRestoredTree(ctx, target, policy.MaximumBytes)
	if err != nil {
		return evidence, err
	}
	return evidence, nil
}

func hashRestoredTree(ctx context.Context, root string, maximumBytes int64) (verificationEvidence, error) {
	digest := sha256.New()
	_, _ = io.WriteString(digest, "restore-verification-manifest/v1\n")
	evidence := verificationEvidence{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("read restored verification content")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("inspect restored verification content")
		}
		if !info.Mode().IsRegular() {
			return errors.New("restored verification content contains a symlink or special file")
		}
		if evidence.FileCount >= maximumVerificationFiles {
			return fmt.Errorf("restore verification exceeds the %d-file safety limit", maximumVerificationFiles)
		}
		if info.Size() < 0 || info.Size() > maximumBytes-evidence.ByteCount {
			return errors.New("restored verification content exceeds the configured byte limit")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("restored verification content escaped its controlled root")
		}
		relative = filepath.ToSlash(relative)
		writeManifestHeader(digest, relative, info.Size())
		file, err := os.Open(path)
		if err != nil {
			return errors.New("open restored verification file")
		}
		remaining := maximumBytes - evidence.ByteCount
		read, copyErr := io.Copy(digest, io.LimitReader(file, remaining+1))
		closeErr := file.Close()
		if read > remaining {
			evidence.ByteCount += remaining
			return errors.New("restored verification content exceeds the configured byte limit")
		}
		evidence.ByteCount += read
		if copyErr != nil || closeErr != nil || read != info.Size() {
			return errors.New("read restored verification file")
		}
		_, _ = io.WriteString(digest, "\n")
		evidence.FileCount++
		return nil
	})
	if err != nil {
		return evidence, err
	}
	if evidence.FileCount == 0 {
		return evidence, errors.New("restore verification selection contained no regular files")
	}
	evidence.ManifestSHA256 = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	return evidence, nil
}

func writeManifestHeader(digest hash.Hash, relative string, size int64) {
	_, _ = io.WriteString(digest, strconv.Itoa(len(relative)))
	_, _ = io.WriteString(digest, ":")
	_, _ = io.WriteString(digest, relative)
	_, _ = io.WriteString(digest, ":")
	_, _ = io.WriteString(digest, strconv.FormatInt(size, 10))
	_, _ = io.WriteString(digest, "\n")
}

func (s *Service) Cleanup(ctx context.Context, id string) (store.RestoreVerificationRecord, error) {
	if !safeVerificationID(id) || !filepath.IsAbs(s.root) || s.root == string(filepath.Separator) {
		return store.RestoreVerificationRecord{}, errors.New("restore verification cleanup identity is invalid")
	}
	record, err := s.storage.RestoreVerification(ctx, id)
	if err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	if record.CleanupStatus != "required" {
		return store.RestoreVerificationRecord{}, errors.New("restore verification does not require cleanup")
	}
	target := filepath.Join(s.root, id)
	if relative, err := filepath.Rel(s.root, target); err != nil || relative != id {
		return store.RestoreVerificationRecord{}, errors.New("restore verification cleanup target is invalid")
	}
	if err := s.removeAll(target); err != nil {
		return store.RestoreVerificationRecord{}, errors.New("restore verification cleanup failed")
	}
	if err := s.storage.ResolveRestoreVerificationCleanup(context.WithoutCancel(ctx), id); err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	return s.storage.RestoreVerification(context.WithoutCancel(ctx), id)
}

func verificationErrorSummary(workErr, cleanupErr error) string {
	parts := make([]string, 0, 2)
	if workErr != nil {
		parts = append(parts, workErr.Error())
	}
	if cleanupErr != nil {
		parts = append(parts, "restore verification temporary content requires cleanup")
	}
	return strings.Join(parts, "; ")
}

func verificationID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return "restore_verification_" + hex.EncodeToString(value)
}

func safeVerificationID(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
