package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type Store interface {
	LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error)
	LoadRemoteHostExecution(context.Context, string) (store.RemoteHostExecution, error)
	CreateRepository(context.Context, domain.Repository, string) error
	UpdateRepositoryStatus(context.Context, string, string) error
	CommitRepositoryPasswordRotation(context.Context, string, string, string, string, time.Time) error
	PendingRepositoryKeyRevocation(context.Context, string) (store.RepositoryKeyRevocation, bool, error)
	CompleteRepositoryKeyRevocation(context.Context, string, string) error
}
type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
	Put(context.Context, string, []byte) (string, error)
	Delete(context.Context, string) error
}
type Runner interface {
	Execute(context.Context, restic.Operation) (restic.Result, error)
}
type Locker interface {
	With(context.Context, string, func() error) error
}
type Service struct {
	store   Store
	secrets Secrets
	runner  Runner
	locker  Locker
}

func (s *Service) SetLocker(locker Locker) { s.locker = locker }
func (s *Service) locked(ctx context.Context, id string, operation func() error) error {
	if s.locker == nil {
		return operation()
	}
	return s.locker.With(ctx, id, operation)
}

type Snapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Hostname string    `json:"hostname"`
}
type SnapshotNode struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Path    string `json:"path"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mtime,omitempty"`
}

type SnapshotContentsQuery struct {
	Path      string
	Search    string
	Cursor    string
	Limit     int
	Recursive bool
}

type SnapshotContentsPage struct {
	Items      []SnapshotNode `json:"items"`
	Path       string         `json:"path,omitempty"`
	Search     string         `json:"search,omitempty"`
	Recursive  bool           `json:"recursive"`
	Truncated  bool           `json:"truncated"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type SnapshotDiffQuery struct {
	Path  string
	Limit int
}

type SnapshotChange struct {
	Path     string `json:"path"`
	Change   string `json:"change"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	Previous int64  `json:"previousSize,omitempty"`
}

type SnapshotDiff struct {
	FromSnapshotID    string           `json:"fromSnapshotId"`
	ToSnapshotID      string           `json:"toSnapshotId"`
	Path              string           `json:"path,omitempty"`
	Added             int              `json:"added"`
	Modified          int              `json:"modified"`
	Removed           int              `json:"removed"`
	Items             []SnapshotChange `json:"items"`
	ExamplesTruncated bool             `json:"examplesTruncated"`
	Incomplete        bool             `json:"incomplete"`
}
type Key struct {
	ID      string `json:"id"`
	Current bool   `json:"current"`
}

type MaintenanceSummary struct {
	KeepCount   int `json:"keepCount"`
	RemoveCount int `json:"removeCount"`
}

type DirectoryRestoreError struct {
	Stage        string
	ResidualPath string
	Err          error
}

func (e *DirectoryRestoreError) Error() string {
	if e.ResidualPath == "" {
		return fmt.Sprintf("directory restore %s failed: %v", e.Stage, e.Err)
	}
	return fmt.Sprintf("directory restore %s failed; isolated residual remains at %s: %v", e.Stage, e.ResidualPath, e.Err)
}

func (e *DirectoryRestoreError) Unwrap() error { return e.Err }

func (e *DirectoryRestoreError) RestoreStage() string { return e.Stage }

func (e *DirectoryRestoreError) RestoreResidualPath() string { return e.ResidualPath }

func New(s Store, secrets Secrets, runner Runner) *Service {
	return &Service{store: s, secrets: secrets, runner: runner}
}

func (s *Service) repositoryMaterial(ctx context.Context, definition domain.Repository, host domain.RemoteHost, privateKeySecretID, password string, credentials *s3backend.Credentials) (restic.Repository, error) {
	if definition.EffectiveKind() == domain.LocalRepository {
		return restic.Repository{Location: definition.Path, Password: password}, nil
	}
	if definition.EffectiveKind() == domain.S3Repository {
		if credentials == nil {
			encoded, err := s.secrets.Get(ctx, definition.BackendSecretID, s3backend.CredentialPurpose)
			if err != nil {
				return restic.Repository{}, err
			}
			decoded, err := s3backend.DecodeCredentials(encoded)
			clear(encoded)
			if err != nil {
				return restic.Repository{}, err
			}
			credentials = &decoded
		}
		return s3backend.Material(definition, password, *credentials)
	}
	if strings.TrimSpace(host.HostFingerprint) == "" {
		return restic.Repository{}, errors.New("SSH host key is not pinned")
	}
	key, err := s.secrets.Get(ctx, privateKeySecretID, "ssh-private-key")
	if err != nil {
		return restic.Repository{}, err
	}
	endpoint := host.Host
	if strings.Contains(endpoint, ":") {
		endpoint = "[" + strings.Trim(endpoint, "[]") + "]"
	}
	return restic.Repository{
		Location:      "sftp:" + host.Username + "@" + endpoint + ":" + definition.Path,
		Password:      password,
		SSHPrivateKey: key,
		SSHPort:       host.Port,
		KnownHosts:    []byte(host.HostFingerprint),
	}, nil
}

func (s *Service) material(ctx context.Context, id string) (store.RepositoryExecution, restic.Repository, error) {
	aggregate, err := s.store.LoadRepositoryExecution(ctx, id)
	if err != nil {
		return aggregate, restic.Repository{}, err
	}
	password, err := s.secrets.Get(ctx, aggregate.RepositoryPasswordSecretID, "repository-password")
	if err != nil {
		return aggregate, restic.Repository{}, err
	}
	repo, err := s.repositoryMaterial(ctx, aggregate.Repository, aggregate.Host, aggregate.PrivateKeySecretID, string(password), nil)
	return aggregate, repo, err
}

// ConnectExisting proves access using a fixed read-only Restic operation before
// persisting either the candidate repository or its password.
func (s *Service) ConnectExisting(ctx context.Context, candidate domain.Repository, password string, credentials *s3backend.Credentials) (snapshots []Snapshot, err error) {
	if candidate.ID == "" || candidate.EffectiveEngine() != domain.ResticEngine || password == "" {
		return nil, errors.New("existing Restic repository identity and password are required")
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	err = s.locked(ctx, candidate.ID, func() error {
		var host domain.RemoteHost
		privateKeySecretID := ""
		if candidate.EffectiveKind() == domain.SFTPRepository {
			remote, loadErr := s.store.LoadRemoteHostExecution(ctx, candidate.RemoteHostID)
			if loadErr != nil {
				return loadErr
			}
			host, privateKeySecretID = remote.Host, remote.PrivateKeySecretID
		}
		material, materialErr := s.repositoryMaterial(ctx, candidate, host, privateKeySecretID, password, credentials)
		if materialErr != nil {
			return materialErr
		}
		snapshots, materialErr = s.verifyReadOnly(ctx, material)
		if materialErr != nil {
			return materialErr
		}
		secretID, putErr := s.secrets.Put(ctx, "repository-password", []byte(password))
		if putErr != nil {
			return putErr
		}
		backendSecretID := ""
		keep := false
		defer func() {
			if !keep {
				_ = s.secrets.Delete(context.WithoutCancel(ctx), secretID)
				if backendSecretID != "" {
					_ = s.secrets.Delete(context.WithoutCancel(ctx), backendSecretID)
				}
			}
		}()
		if candidate.EffectiveKind() == domain.S3Repository {
			if credentials == nil {
				return errors.New("S3 credentials are required")
			}
			encoded, encodeErr := s3backend.EncodeCredentials(*credentials)
			if encodeErr != nil {
				return encodeErr
			}
			backendSecretID, putErr = s.secrets.Put(ctx, s3backend.CredentialPurpose, encoded)
			clear(encoded)
			if putErr != nil {
				return putErr
			}
			candidate.BackendSecretID = backendSecretID
		}
		candidate.Status = "ready"
		if createErr := s.store.CreateRepository(ctx, candidate, secretID); createErr != nil {
			return createErr
		}
		keep = true
		return nil
	})
	return snapshots, err
}

// VerifyExisting reconnects only repositories imported in the disconnected
// state. A failed check leaves their local status unchanged.
func (s *Service) VerifyExisting(ctx context.Context, id string) (snapshots []Snapshot, err error) {
	err = s.locked(ctx, id, func() error {
		aggregate, material, loadErr := s.material(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if aggregate.Repository.EffectiveEngine() != domain.ResticEngine || aggregate.Repository.Status != "disconnected" {
			return fmt.Errorf("repository cannot be reconnected while status is %s", aggregate.Repository.Status)
		}
		snapshots, loadErr = s.verifyReadOnly(ctx, material)
		if loadErr != nil {
			return loadErr
		}
		return s.store.UpdateRepositoryStatus(ctx, id, "ready")
	})
	return snapshots, err
}

func (s *Service) verifyReadOnly(ctx context.Context, material restic.Repository) ([]Snapshot, error) {
	result, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.VerifyRepository, Repository: material})
	if err != nil {
		return nil, errors.New("existing repository could not be verified read only")
	}
	var snapshots []Snapshot
	if err := json.Unmarshal([]byte(result.Stdout), &snapshots); err != nil || snapshots == nil {
		if err == nil {
			err = errors.New("snapshot response is not an array")
		}
		return nil, fmt.Errorf("decode existing repository snapshots: %w", err)
	}
	return snapshots, nil
}
func (s *Service) operation(ctx context.Context, id string, kind restic.OperationKind, args []string) (restic.Result, error) {
	_, repo, err := s.material(ctx, id)
	if err != nil {
		return restic.Result{}, err
	}
	return s.runner.Execute(ctx, restic.Operation{Kind: kind, Repository: repo, Arguments: args})
}
func (s *Service) Initialize(ctx context.Context, id string) error {
	var err error
	lockErr := s.locked(ctx, id, func() error {
		aggregate, repo, loadErr := s.material(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if aggregate.Repository.Status != "uninitialized" {
			return fmt.Errorf("repository cannot be initialized while status is %s", aggregate.Repository.Status)
		}
		_, err = s.runner.Execute(ctx, restic.Operation{Kind: restic.InitializeRepo, Repository: repo})
		return err
	})
	if lockErr != nil {
		err = lockErr
	}
	if err == nil {
		if updateErr := s.store.UpdateRepositoryStatus(context.WithoutCancel(ctx), id, "ready"); updateErr != nil {
			return fmt.Errorf("record initialized repository status: %w", updateErr)
		}
	}
	return err
}
func (s *Service) RotatePassword(ctx context.Context, id, newPassword string) error {
	return s.locked(ctx, id, func() error { return s.rotatePasswordUnlocked(ctx, id, newPassword) })
}
func (s *Service) rotatePasswordUnlocked(ctx context.Context, id, newPassword string) error {
	if len(newPassword) < 12 {
		return errors.New("repository password must have at least 12 characters")
	}
	aggregate, oldRepo, err := s.material(ctx, id)
	if err != nil {
		return err
	}
	if _, pending, err := s.store.PendingRepositoryKeyRevocation(ctx, id); err != nil {
		return err
	} else if pending {
		return errors.New("repository has an old key awaiting explicit revocation")
	}
	listed, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.ListKeys, Repository: oldRepo})
	if err != nil {
		return err
	}
	var keys []Key
	if err := json.Unmarshal([]byte(listed.Stdout), &keys); err != nil {
		return fmt.Errorf("decode repository keys: %w", err)
	}
	oldKeyID := ""
	for _, key := range keys {
		if key.Current {
			oldKeyID = key.ID
			break
		}
	}
	if oldKeyID == "" {
		return errors.New("current repository key was not identified")
	}
	newSecretID, err := s.secrets.Put(ctx, "repository-password", []byte(newPassword))
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.secrets.Delete(context.WithoutCancel(ctx), newSecretID)
		}
	}()
	if _, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.AddKey, Repository: oldRepo, NewPassword: newPassword}); err != nil {
		return err
	}
	newRepo := oldRepo
	newRepo.Password = newPassword
	if _, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.ListSnapshots, Repository: newRepo}); err != nil {
		return fmt.Errorf("verify new repository password: %w", err)
	}
	if err := s.store.CommitRepositoryPasswordRotation(ctx, id, newSecretID, oldKeyID, aggregate.RepositoryPasswordSecretID, time.Now().UTC()); err != nil {
		return err
	}
	keep = true
	return nil
}

type PasswordRotationStatus struct {
	Pending   bool      `json:"pending"`
	OldKeyID  string    `json:"oldKeyId,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

func (s *Service) PasswordRotationStatus(ctx context.Context, id string) (PasswordRotationStatus, error) {
	pending, ok, err := s.store.PendingRepositoryKeyRevocation(ctx, id)
	if err != nil {
		return PasswordRotationStatus{}, err
	}
	if !ok {
		return PasswordRotationStatus{}, nil
	}
	return PasswordRotationStatus{Pending: true, OldKeyID: pending.KeyID, CreatedAt: pending.CreatedAt}, nil
}

func (s *Service) RevokeOldPassword(ctx context.Context, id string) error {
	return s.locked(ctx, id, func() error {
		pending, ok, err := s.store.PendingRepositoryKeyRevocation(ctx, id)
		if err != nil || !ok {
			return err
		}
		_, currentRepo, err := s.material(ctx, id)
		if err != nil {
			return err
		}
		listed, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.ListKeys, Repository: currentRepo})
		if err != nil {
			return err
		}
		var keys []Key
		if err := json.Unmarshal([]byte(listed.Stdout), &keys); err != nil {
			return fmt.Errorf("decode repository keys: %w", err)
		}
		for _, key := range keys {
			if key.ID == pending.KeyID {
				if _, err := s.runner.Execute(ctx, restic.Operation{Kind: restic.RemoveKey, Repository: currentRepo, Arguments: []string{pending.KeyID}}); err != nil {
					return err
				}
				break
			}
		}
		return s.store.CompleteRepositoryKeyRevocation(ctx, id, pending.KeyID)
	})
}
func (s *Service) Snapshots(ctx context.Context, id string) ([]Snapshot, error) {
	result, err := s.operation(ctx, id, restic.ListSnapshots, nil)
	if err != nil {
		return nil, err
	}
	var snapshots []Snapshot
	if err := json.Unmarshal([]byte(result.Stdout), &snapshots); err != nil {
		return nil, fmt.Errorf("decode snapshots: %w", err)
	}
	return snapshots, nil
}

const (
	defaultSnapshotPageSize = 200
	maximumSnapshotPageSize = 500
	maximumSnapshotLineSize = 1 << 20
)

type snapshotContentsCursor struct {
	Offset      int    `json:"offset"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Service) BrowseSnapshotContents(ctx context.Context, id, snapshotID string, query SnapshotContentsQuery) (SnapshotContentsPage, error) {
	query, err := normalizeSnapshotContentsQuery(snapshotID, query)
	if err != nil {
		return SnapshotContentsPage{}, err
	}
	offset, err := snapshotContentsOffset(snapshotID, query)
	if err != nil {
		return SnapshotContentsPage{}, err
	}
	page := SnapshotContentsPage{
		Items:     make([]SnapshotNode, 0, query.Limit),
		Path:      query.Path,
		Search:    query.Search,
		Recursive: query.Recursive,
	}
	matched := 0
	err = s.streamSnapshotContents(ctx, id, snapshotID, query.Path, query.Recursive, func(node SnapshotNode) error {
		if !snapshotNodeMatches(node, query) {
			return nil
		}
		if matched < offset {
			matched++
			return nil
		}
		if len(page.Items) < query.Limit {
			page.Items = append(page.Items, node)
			matched++
			return nil
		}
		page.Truncated = true
		return nil
	})
	if err != nil {
		return SnapshotContentsPage{}, err
	}
	if page.Truncated {
		page.NextCursor = encodeSnapshotContentsCursor(snapshotID, query, offset+len(page.Items))
	}
	return page, nil
}

func normalizeSnapshotContentsQuery(snapshotID string, query SnapshotContentsQuery) (SnapshotContentsQuery, error) {
	if !safeSnapshotID(snapshotID) {
		return SnapshotContentsQuery{}, errors.New("valid snapshot id is required")
	}
	query.Path = strings.TrimSpace(query.Path)
	if strings.ContainsAny(query.Path, "\x00\r\n") {
		return SnapshotContentsQuery{}, errors.New("snapshot path contains an invalid character")
	}
	if query.Path != "" {
		query.Path = filepath.Clean(query.Path)
		if !filepath.IsAbs(query.Path) {
			return SnapshotContentsQuery{}, errors.New("snapshot path must be absolute")
		}
	}
	query.Search = strings.TrimSpace(query.Search)
	if len(query.Search) > 256 || strings.ContainsAny(query.Search, "\x00\r\n") {
		return SnapshotContentsQuery{}, errors.New("snapshot search is invalid")
	}
	if query.Limit == 0 {
		query.Limit = defaultSnapshotPageSize
	}
	if query.Limit < 1 || query.Limit > maximumSnapshotPageSize {
		return SnapshotContentsQuery{}, fmt.Errorf("snapshot page limit must be between 1 and %d", maximumSnapshotPageSize)
	}
	if query.Search != "" {
		query.Recursive = true
	}
	return query, nil
}

func snapshotContentsOffset(snapshotID string, query SnapshotContentsQuery) (int, error) {
	if query.Cursor == "" {
		return 0, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return 0, errors.New("snapshot cursor is invalid")
	}
	var cursor snapshotContentsCursor
	if err := json.Unmarshal(encoded, &cursor); err != nil || cursor.Offset < 1 || cursor.Fingerprint != snapshotContentsFingerprint(snapshotID, query) {
		return 0, errors.New("snapshot cursor does not match this query")
	}
	return cursor.Offset, nil
}

func encodeSnapshotContentsCursor(snapshotID string, query SnapshotContentsQuery, offset int) string {
	encoded, _ := json.Marshal(snapshotContentsCursor{Offset: offset, Fingerprint: snapshotContentsFingerprint(snapshotID, query)})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func snapshotContentsFingerprint(snapshotID string, query SnapshotContentsQuery) string {
	canonical := fmt.Sprintf("snapshot-contents/v1\nsnapshot=%s\npath=%s\nsearch=%s\nrecursive=%t\nlimit=%d\n", snapshotID, query.Path, strings.ToLower(query.Search), query.Recursive, query.Limit)
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%x", digest[:16])
}

func snapshotNodeMatches(node SnapshotNode, query SnapshotContentsQuery) bool {
	if query.Path != "" {
		relative, err := filepath.Rel(query.Path, node.Path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
		if !query.Recursive && filepath.Dir(node.Path) != query.Path {
			return false
		}
	}
	if query.Search == "" {
		return true
	}
	needle := strings.ToLower(query.Search)
	return strings.Contains(strings.ToLower(node.Path), needle) || strings.Contains(strings.ToLower(node.Name), needle)
}

func (s *Service) streamSnapshotContents(ctx context.Context, id, snapshotID, snapshotPath string, recursive bool, visit func(SnapshotNode) error) error {
	args := []string{snapshotID}
	if snapshotPath != "" {
		args = append(args, snapshotPath)
	}
	if recursive {
		args = append(args, "--recursive")
	}
	lines := &snapshotLineWriter{visit: func(line []byte) error {
		node, ok, err := decodeSnapshotNode(line)
		if err != nil || !ok {
			return err
		}
		return visit(node)
	}}
	result, err := s.operationWithOutput(ctx, id, restic.ListSnapshotContents, args, lines)
	if lines.written == 0 && result.Stdout != "" {
		_, _ = lines.Write([]byte(result.Stdout))
	}
	lines.finish()
	if err != nil {
		return err
	}
	if lines.err != nil {
		return fmt.Errorf("decode snapshot contents: %w", lines.err)
	}
	return nil
}

func (s *Service) operationWithOutput(ctx context.Context, id string, kind restic.OperationKind, args []string, output io.Writer) (restic.Result, error) {
	_, repo, err := s.material(ctx, id)
	if err != nil {
		return restic.Result{}, err
	}
	return s.runner.Execute(ctx, restic.Operation{Kind: kind, Repository: repo, Arguments: args, Output: output})
}

func decodeSnapshotNode(line []byte) (SnapshotNode, bool, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return SnapshotNode{}, false, nil
	}
	var raw struct {
		StructType string `json:"struct_type"`
		SnapshotNode
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return SnapshotNode{}, false, err
	}
	clean := filepath.Clean(raw.Path)
	if raw.StructType != "node" || (raw.Type != "dir" && raw.Type != "file") || !filepath.IsAbs(clean) || clean != raw.Path {
		return SnapshotNode{}, false, nil
	}
	raw.Path = clean
	return raw.SnapshotNode, true, nil
}

type snapshotLineWriter struct {
	pending    []byte
	visit      func([]byte) error
	err        error
	written    int
	discarding bool
}

func (w *snapshotLineWriter) Write(input []byte) (int, error) {
	originalLength := len(input)
	w.written += originalLength
	for len(input) > 0 {
		newline := bytes.IndexByte(input, '\n')
		part := input
		if newline >= 0 {
			part = input[:newline]
		}
		if !w.discarding {
			if len(w.pending)+len(part) > maximumSnapshotLineSize {
				if w.err == nil {
					w.err = errors.New("snapshot content line exceeds the safety limit")
				}
				w.pending = w.pending[:0]
				w.discarding = true
			} else {
				w.pending = append(w.pending, part...)
			}
		}
		if newline < 0 {
			break
		}
		if !w.discarding && len(w.pending) != 0 && w.err == nil {
			w.err = w.visit(w.pending)
		}
		w.pending = w.pending[:0]
		w.discarding = false
		input = input[newline+1:]
	}
	return originalLength, nil
}

func (w *snapshotLineWriter) finish() {
	if !w.discarding && len(w.pending) != 0 && w.err == nil {
		w.err = w.visit(w.pending)
	}
	w.pending = nil
}

const (
	defaultSnapshotDiffExamples = 100
	maximumSnapshotDiffExamples = 500
	maximumSnapshotDiffNodes    = 1_000_000
)

func (s *Service) CompareSnapshots(ctx context.Context, id, fromSnapshotID, toSnapshotID string, query SnapshotDiffQuery) (SnapshotDiff, error) {
	if !safeSnapshotID(fromSnapshotID) || !safeSnapshotID(toSnapshotID) || fromSnapshotID == toSnapshotID {
		return SnapshotDiff{}, errors.New("two different valid snapshot ids are required")
	}
	query.Path = strings.TrimSpace(query.Path)
	if strings.ContainsAny(query.Path, "\x00\r\n") {
		return SnapshotDiff{}, errors.New("snapshot path contains an invalid character")
	}
	if query.Path != "" {
		query.Path = filepath.Clean(query.Path)
		if !filepath.IsAbs(query.Path) {
			return SnapshotDiff{}, errors.New("snapshot path must be absolute")
		}
	}
	if query.Limit == 0 {
		query.Limit = defaultSnapshotDiffExamples
	}
	if query.Limit < 1 || query.Limit > maximumSnapshotDiffExamples {
		return SnapshotDiff{}, fmt.Errorf("snapshot diff example limit must be between 1 and %d", maximumSnapshotDiffExamples)
	}
	before, beforeIncomplete, err := s.snapshotIndex(ctx, id, fromSnapshotID, query.Path)
	if err != nil {
		return SnapshotDiff{}, err
	}
	after, afterIncomplete, err := s.snapshotIndex(ctx, id, toSnapshotID, query.Path)
	if err != nil {
		return SnapshotDiff{}, err
	}
	diff := SnapshotDiff{
		FromSnapshotID: fromSnapshotID,
		ToSnapshotID:   toSnapshotID,
		Path:           query.Path,
		Items:          make([]SnapshotChange, 0, query.Limit),
		Incomplete:     beforeIncomplete || afterIncomplete,
	}
	changes := make([]SnapshotChange, 0)
	for path, current := range after {
		previous, exists := before[path]
		if !exists {
			diff.Added++
			changes = append(changes, SnapshotChange{Path: path, Change: "added", Type: current.Type, Size: current.Size})
			continue
		}
		if previous.Type != current.Type || previous.Size != current.Size || previous.ModTime != current.ModTime {
			diff.Modified++
			changes = append(changes, SnapshotChange{Path: path, Change: "modified", Type: current.Type, Size: current.Size, Previous: previous.Size})
		}
	}
	for path, previous := range before {
		if _, exists := after[path]; exists {
			continue
		}
		diff.Removed++
		changes = append(changes, SnapshotChange{Path: path, Change: "removed", Type: previous.Type, Previous: previous.Size})
	}
	sort.Slice(changes, func(left, right int) bool {
		if changes[left].Path == changes[right].Path {
			return changes[left].Change < changes[right].Change
		}
		return changes[left].Path < changes[right].Path
	})
	if len(changes) > query.Limit {
		diff.ExamplesTruncated = true
		changes = changes[:query.Limit]
	}
	diff.Items = changes
	return diff, nil
}

func (s *Service) snapshotIndex(ctx context.Context, id, snapshotID, snapshotPath string) (map[string]SnapshotNode, bool, error) {
	index := make(map[string]SnapshotNode)
	incomplete := false
	err := s.streamSnapshotContents(ctx, id, snapshotID, snapshotPath, true, func(node SnapshotNode) error {
		if snapshotPath != "" && !snapshotNodeMatches(node, SnapshotContentsQuery{Path: snapshotPath, Recursive: true}) {
			return nil
		}
		if _, exists := index[node.Path]; !exists && len(index) >= maximumSnapshotDiffNodes {
			incomplete = true
			return nil
		}
		index[node.Path] = node
		return nil
	})
	return index, incomplete, err
}

func safeSnapshotID(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}
func (s *Service) Dump(ctx context.Context, id, snapshotID, filename string, downloadKiBPerSecond int, output io.Writer) error {
	if snapshotID == "" || filename == "" || output == nil {
		return errors.New("snapshot, filename and output are required")
	}
	if downloadKiBPerSecond < 0 {
		return errors.New("download limit cannot be negative")
	}
	_, repo, err := s.material(ctx, id)
	if err != nil {
		return err
	}
	args := make([]string, 0, 4)
	if downloadKiBPerSecond > 0 {
		args = append(args, "--limit-download", strconv.Itoa(downloadKiBPerSecond))
	}
	args = append(args, snapshotID, filename)
	_, err = s.runner.Execute(ctx, restic.Operation{Kind: restic.DumpSnapshot, Repository: repo, Arguments: args, Output: output})
	return err
}

// ValidateExistingDirectoryTarget validates a local output directory without
// creating it. Dump-file restore intentionally accepts an existing directory;
// the file created inside it is checked separately.
func ValidateExistingDirectoryTarget(target string) (string, error) {
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "." || target == string(filepath.Separator) || !filepath.IsAbs(target) || strings.ContainsAny(target, "\x00\r\n") {
		return "", errors.New("restore dump output directory must be an absolute path")
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("restore dump output directory must already exist")
		}
		return "", fmt.Errorf("inspect restore dump output directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("restore dump output target must be a directory")
	}
	return target, nil
}

// ValidateNewFileTarget validates a local file target without creating it.
// Restore-to-file uses a non-existing path so a mistaken restore cannot
// overwrite an unrelated dump or other user data.
func ValidateNewFileTarget(target string) (string, error) {
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "." || target == string(filepath.Separator) || !filepath.IsAbs(target) || strings.ContainsAny(target, "\x00\r\n") {
		return "", errors.New("restore file target must be an absolute path")
	}
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New("restore file target must not already exist")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect restore file target: %w", err)
	}
	parentInfo, err := os.Stat(filepath.Dir(target))
	if err != nil || !parentInfo.IsDir() {
		return "", errors.New("restore file target parent must be an existing directory")
	}
	return target, nil
}

// RestoreDumpFile materializes one database dump from a Restic snapshot. It
// writes to a 0600 staging file in the destination directory, then publishes
// it with a no-overwrite hard-link commit so a concurrent target creation is
// never replaced.
func (s *Service) RestoreDumpFile(ctx context.Context, id, snapshotID, filename, target string, downloadKiBPerSecond int) error {
	if !safeSnapshotID(snapshotID) || !safeDumpFilename(filename) {
		return errors.New("snapshot and dump filename are invalid")
	}
	if downloadKiBPerSecond < 0 {
		return errors.New("download limit cannot be negative")
	}
	return s.locked(ctx, id, func() error {
		cleanTarget, err := ValidateNewFileTarget(target)
		if err != nil {
			return err
		}
		parent := filepath.Dir(cleanTarget)
		staging, err := os.CreateTemp(parent, "."+filepath.Base(cleanTarget)+".restic-control-dump-")
		if err != nil {
			return fmt.Errorf("create dump staging file: %w", err)
		}
		stagingPath := staging.Name()
		removeStaging := true
		defer func() {
			_ = staging.Close()
			if removeStaging {
				_ = os.Remove(stagingPath)
			}
		}()
		if err := staging.Chmod(0o600); err != nil {
			return fmt.Errorf("secure dump staging file: %w", err)
		}
		if err := s.Dump(ctx, id, snapshotID, filename, downloadKiBPerSecond, staging); err != nil {
			return fmt.Errorf("restore database dump: %w", err)
		}
		if err := staging.Sync(); err != nil {
			return fmt.Errorf("flush restored dump: %w", err)
		}
		if err := staging.Close(); err != nil {
			return fmt.Errorf("close restored dump: %w", err)
		}
		if err := os.Link(stagingPath, cleanTarget); err != nil {
			return fmt.Errorf("publish restored dump: %w", err)
		}
		if err := os.Remove(stagingPath); err != nil {
			return fmt.Errorf("remove dump staging file: %w", err)
		}
		removeStaging = false
		return nil
	})
}

func safeDumpFilename(filename string) bool {
	return filename != "" && filepath.Base(filename) == filename && filename != "." && filename != ".." && !strings.ContainsAny(filename, "/\\\x00\r\n")
}
func (s *Service) RestoreDirectory(ctx context.Context, id, snapshotID, target string, includes []string, downloadKiBPerSecond int) error {
	return s.locked(ctx, id, func() error {
		return s.restoreDirectoryUnlocked(ctx, id, snapshotID, target, includes, downloadKiBPerSecond)
	})
}

func (s *Service) restoreDirectoryUnlocked(ctx context.Context, id, snapshotID, target string, includes []string, downloadKiBPerSecond int) error {
	if downloadKiBPerSecond < 0 {
		return errors.New("download limit cannot be negative")
	}
	preflight, err := s.PreflightDirectoryRestore(ctx, id, snapshotID, target, includes)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".restic-control-restore-")
	if err != nil {
		return fmt.Errorf("create directory restore staging: %w", err)
	}
	args := make([]string, 0, 5+2*len(preflight.Includes))
	if downloadKiBPerSecond > 0 {
		args = append(args, "--limit-download", strconv.Itoa(downloadKiBPerSecond))
	}
	args = append(args, snapshotID+":"+filepath.ToSlash(preflight.SourcePath), "--target", staging)
	for _, include := range preflight.Includes {
		args = append(args, "--include", include)
	}
	if _, err := s.operation(ctx, id, restic.RestoreDirectory, args); err != nil {
		return &DirectoryRestoreError{Stage: "restore", ResidualPath: staging, Err: err}
	}
	if _, err := os.Lstat(target); err == nil {
		return &DirectoryRestoreError{Stage: "commit", ResidualPath: staging, Err: errors.New("restore target appeared while the operation was running")}
	} else if !os.IsNotExist(err) {
		return &DirectoryRestoreError{Stage: "commit", ResidualPath: staging, Err: fmt.Errorf("inspect final restore target: %w", err)}
	}
	if err := os.Rename(staging, target); err != nil {
		return &DirectoryRestoreError{Stage: "commit", ResidualPath: staging, Err: err}
	}
	return nil
}

type DirectoryRestorePreflight struct {
	SourcePath string   `json:"sourcePath"`
	Target     string   `json:"target"`
	Includes   []string `json:"includes"`
}

func (s *Service) PreflightDirectoryRestore(ctx context.Context, id, snapshotID, target string, includes []string) (DirectoryRestorePreflight, error) {
	if snapshotID == "" || !filepath.IsAbs(target) {
		return DirectoryRestorePreflight{}, errors.New("snapshot and absolute restore target are required")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return DirectoryRestorePreflight{}, errors.New("restore target must not already exist")
	}
	parent := filepath.Dir(target)
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return DirectoryRestorePreflight{}, errors.New("restore target parent must be an existing directory")
	}
	result, err := s.PreflightDirectoryRestoreSelection(ctx, id, snapshotID, includes)
	if err != nil {
		return DirectoryRestorePreflight{}, err
	}
	result.Target = target
	return result, nil
}

// PreflightDirectoryRestoreSelection validates repository-side facts without
// inspecting a local target. Agent restores pair this with a target preflight
// executed on the selected Agent.
func (s *Service) PreflightDirectoryRestoreSelection(ctx context.Context, id, snapshotID string, includes []string) (DirectoryRestorePreflight, error) {
	if snapshotID == "" {
		return DirectoryRestorePreflight{}, errors.New("snapshot is required")
	}
	normalizedIncludes, err := normalizeRestoreIncludes(includes)
	if err != nil {
		return DirectoryRestorePreflight{}, err
	}
	snapshots, err := s.Snapshots(ctx, id)
	if err != nil {
		return DirectoryRestorePreflight{}, fmt.Errorf("load restore snapshot: %w", err)
	}
	var selected *Snapshot
	for index := range snapshots {
		if snapshots[index].ID == snapshotID {
			selected = &snapshots[index]
			break
		}
	}
	if selected == nil {
		return DirectoryRestorePreflight{}, errors.New("selected restore snapshot does not exist")
	}
	if len(selected.Paths) != 1 || !filepath.IsAbs(selected.Paths[0]) {
		return DirectoryRestorePreflight{}, errors.New("directory restore requires a snapshot with exactly one absolute source path")
	}
	if len(normalizedIncludes) > 0 {
		if err := s.verifySnapshotIncludes(ctx, id, snapshotID, selected.Paths[0], normalizedIncludes); err != nil {
			return DirectoryRestorePreflight{}, fmt.Errorf("verify restore selections: %w", err)
		}
	}
	return DirectoryRestorePreflight{SourcePath: selected.Paths[0], Includes: normalizedIncludes}, nil
}

func (s *Service) verifySnapshotIncludes(ctx context.Context, id, snapshotID, sourcePath string, includes []string) error {
	root := filepath.Clean(sourcePath)
	wanted := make(map[string]string, len(includes))
	for _, include := range includes {
		absolute := filepath.Join(root, filepath.FromSlash(include))
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("restore include path must stay within the snapshot source: %q", include)
		}
		wanted[absolute] = include
	}
	found := make(map[string]bool, len(wanted))
	if err := s.streamSnapshotContents(ctx, id, snapshotID, root, true, func(node SnapshotNode) error {
		if _, ok := wanted[node.Path]; ok {
			found[node.Path] = true
		}
		return nil
	}); err != nil {
		return err
	}
	for absolute, include := range wanted {
		if !found[absolute] {
			return fmt.Errorf("restore include does not exist in selected snapshot: %q", include)
		}
	}
	return nil
}

func normalizeRestoreIncludes(includes []string) ([]string, error) {
	normalized := make([]string, 0, len(includes))
	for _, include := range includes {
		include = strings.TrimSpace(include)
		if include == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(include))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("restore include path must stay within the snapshot source: %q", include)
		}
		normalized = append(normalized, filepath.ToSlash(clean))
	}
	return normalized, nil
}
func (s *Service) Maintain(ctx context.Context, id string, policy domain.RetentionPolicy, dryRun bool) error {
	return s.locked(ctx, id, func() error { return s.maintainUnlocked(ctx, id, policy, dryRun) })
}

func (s *Service) PreviewMaintenance(ctx context.Context, id string, policy domain.RetentionPolicy) (summary MaintenanceSummary, err error) {
	err = s.locked(ctx, id, func() error {
		aggregate, _, loadErr := s.material(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if aggregate.Repository.Status != "ready" {
			return fmt.Errorf("repository maintenance is blocked while status is %s", aggregate.Repository.Status)
		}
		args := append(retentionArgs(policy), "--keep-tag", "rc:protected-partial", "--dry-run")
		result, executeErr := s.operation(ctx, id, restic.ForgetSnapshots, args)
		if executeErr != nil {
			return executeErr
		}
		summary, executeErr = parseMaintenanceSummary(result.Stdout)
		return executeErr
	})
	return summary, err
}

func parseMaintenanceSummary(output string) (MaintenanceSummary, error) {
	var groups []struct {
		Keep   []json.RawMessage `json:"keep"`
		Remove []json.RawMessage `json:"remove"`
	}
	if err := json.Unmarshal([]byte(output), &groups); err != nil {
		return MaintenanceSummary{}, fmt.Errorf("parse maintenance preview: %w", err)
	}
	var summary MaintenanceSummary
	for _, group := range groups {
		summary.KeepCount += len(group.Keep)
		summary.RemoveCount += len(group.Remove)
	}
	return summary, nil
}
func (s *Service) maintainUnlocked(ctx context.Context, id string, policy domain.RetentionPolicy, dryRun bool) error {
	aggregate, _, err := s.material(ctx, id)
	if err != nil {
		return err
	}
	if aggregate.Repository.Status != "ready" {
		return fmt.Errorf("repository maintenance is blocked while status is %s", aggregate.Repository.Status)
	}
	args := retentionArgs(policy)
	args = append(args, "--keep-tag", "rc:protected-partial")
	if dryRun {
		args = append(args, "--dry-run")
	}
	if _, err := s.operation(ctx, id, restic.ForgetSnapshots, args); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	if _, err := s.operation(ctx, id, restic.PruneRepository, nil); err != nil {
		return err
	}
	if _, err := s.operation(ctx, id, restic.CheckRepository, nil); err != nil {
		_ = s.store.UpdateRepositoryStatus(context.WithoutCancel(ctx), id, "abnormal")
		return err
	}
	return s.store.UpdateRepositoryStatus(ctx, id, "ready")
}
func retentionArgs(p domain.RetentionPolicy) []string {
	var args []string
	appendInt := func(flag string, value int) {
		if value > 0 {
			args = append(args, flag, strconv.Itoa(value))
		}
	}
	if p.KeepWithinDays > 0 {
		args = append(args, "--keep-within", fmt.Sprintf("%dd", p.KeepWithinDays))
	}
	appendInt("--keep-last", p.KeepLast)
	appendInt("--keep-hourly", p.KeepHourly)
	appendInt("--keep-daily", p.KeepDaily)
	appendInt("--keep-weekly", p.KeepWeekly)
	appendInt("--keep-monthly", p.KeepMonthly)
	appendInt("--keep-yearly", p.KeepYearly)
	return args
}
