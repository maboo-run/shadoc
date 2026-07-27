package servicemigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	LinuxStagingDataDir = "/var/lib/.shadoc-root-staging"
	LinuxMarkerPath     = "/var/lib/.shadoc-root-migration.json"
	LinuxLockPath       = "/var/lib/.shadoc-root-migration.lock"
)

type Installation struct {
	Username       string `json:"username"`
	UID            int    `json:"uid"`
	Home           string `json:"home"`
	Identity       string `json:"identity"`
	DataDir        string `json:"dataDir"`
	Listen         string `json:"listen"`
	Executable     string `json:"executable"`
	UnitDefinition string `json:"unitDefinition"`
	UnitMode       uint32 `json:"unitMode"`
	WasEnabled     bool   `json:"wasEnabled"`
	WasActive      bool   `json:"wasActive"`
}

type stage string

const (
	stagePrepared        stage = "prepared"
	stageSourceDisabled  stage = "source_disabled"
	stageDataStaged      stage = "data_staged"
	stageTargetCommitted stage = "target_committed"
	stageSystemStarting  stage = "system_starting"
	stageHealthVerified  stage = "system_health_verified"
)

type Marker struct {
	Stage             stage        `json:"stage"`
	Source            Installation `json:"source"`
	CurrentExecutable string       `json:"currentExecutable"`
	TargetDataDir     string       `json:"targetDataDir"`
	StagingDataDir    string       `json:"stagingDataDir"`
}

type Source interface {
	Inspect(context.Context) (Installation, error)
	Disable(context.Context, Installation) error
	Restore(context.Context, Installation) error
	Remove(context.Context, Installation) error
}

type Target interface {
	Prepare() error
	Install(string, []string) error
	Healthy(context.Context, string) error
	Uninstall() error
}

type Mover interface {
	Stage(source, staging, currentExecutable string, sourceUID int) error
	Commit(staging, target string) error
	Remove(string, int) error
	Exists(string) (bool, error)
}

type MarkerStore interface {
	Load() (Marker, bool, error)
	Save(Marker) error
	Remove() error
}

type Result struct {
	TargetDataDir string
	SourceRemoved bool
}

type Service struct {
	source            Source
	target            Target
	mover             Mover
	markers           MarkerStore
	currentExecutable string
	targetDataDir     string
	stagingDataDir    string
}

func New(source Source, target Target, mover Mover, markers MarkerStore, currentExecutable, targetDataDir, stagingDataDir string) *Service {
	return &Service{
		source: source, target: target, mover: mover, markers: markers,
		currentExecutable: currentExecutable, targetDataDir: targetDataDir, stagingDataDir: stagingDataDir,
	}
}

func (s *Service) Migrate(ctx context.Context) (Result, error) {
	if s == nil || s.source == nil || s.target == nil || s.mover == nil || s.markers == nil {
		return Result{}, errors.New("service migration dependencies are unavailable")
	}
	if err := validateMigrationPaths(s.currentExecutable, s.targetDataDir, s.stagingDataDir); err != nil {
		return Result{}, err
	}
	marker, present, err := s.markers.Load()
	if err != nil {
		return Result{}, err
	}
	if !present {
		marker, err = s.prepare(ctx)
		if err != nil {
			return Result{}, err
		}
	}
	return s.resume(ctx, marker)
}

func (s *Service) prepare(ctx context.Context) (Marker, error) {
	installation, err := s.source.Inspect(ctx)
	if err != nil {
		return Marker{}, err
	}
	if err := validateInstallation(installation, s.targetDataDir); err != nil {
		return Marker{}, err
	}
	if filepath.Clean(installation.Executable) != filepath.Clean(s.currentExecutable) {
		return Marker{}, errors.New("run migrate-to-root with the managed Shadoc command used by the user service")
	}
	if err := s.target.Prepare(); err != nil {
		return Marker{}, fmt.Errorf("prepare root service target: %w", err)
	}
	for _, path := range []string{s.targetDataDir, s.stagingDataDir} {
		exists, err := s.mover.Exists(path)
		if err != nil {
			return Marker{}, err
		}
		if exists {
			return Marker{}, fmt.Errorf("migration target already exists: %s", path)
		}
	}
	marker := Marker{
		Stage: stagePrepared, Source: installation, CurrentExecutable: s.currentExecutable,
		TargetDataDir: s.targetDataDir, StagingDataDir: s.stagingDataDir,
	}
	if err := s.markers.Save(marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func (s *Service) resume(ctx context.Context, marker Marker) (Result, error) {
	if marker.TargetDataDir != s.targetDataDir || marker.StagingDataDir != s.stagingDataDir || marker.CurrentExecutable == "" {
		return Result{}, errors.New("migration marker does not match the fixed root target")
	}
	if marker.Stage == stageHealthVerified {
		if err := s.target.Healthy(ctx, marker.Source.Listen); err != nil {
			return Result{}, fmt.Errorf("root service is not healthy; source cleanup was not attempted: %w", err)
		}
		return s.cleanupSource(ctx, marker)
	}
	if marker.Stage == stageSystemStarting {
		if err := s.target.Healthy(ctx, marker.Source.Listen); err != nil {
			return Result{}, s.rollback(ctx, marker, fmt.Errorf("root service did not survive migration interruption: %w", err))
		}
		marker.Stage = stageHealthVerified
		if err := s.markers.Save(marker); err != nil {
			return Result{}, err
		}
		return s.cleanupSource(ctx, marker)
	}
	if marker.Stage == stagePrepared {
		if err := s.source.Disable(ctx, marker.Source); err != nil {
			return Result{}, s.rollback(ctx, marker, fmt.Errorf("disable user service: %w", err))
		}
		marker.Stage = stageSourceDisabled
		if err := s.markers.Save(marker); err != nil {
			return Result{}, s.rollback(ctx, marker, err)
		}
	}
	if marker.Stage == stageSourceDisabled {
		if err := s.mover.Stage(marker.Source.DataDir, marker.StagingDataDir, marker.CurrentExecutable, marker.Source.UID); err != nil {
			return Result{}, s.rollback(ctx, marker, fmt.Errorf("stage root data: %w", err))
		}
		marker.Stage = stageDataStaged
		if err := s.markers.Save(marker); err != nil {
			return Result{}, s.rollback(ctx, marker, err)
		}
	}
	if marker.Stage == stageDataStaged {
		if err := s.mover.Commit(marker.StagingDataDir, marker.TargetDataDir); err != nil {
			return Result{}, s.rollback(ctx, marker, fmt.Errorf("commit root data: %w", err))
		}
		marker.Stage = stageTargetCommitted
		if err := s.markers.Save(marker); err != nil {
			return Result{}, s.rollback(ctx, marker, err)
		}
	}
	if marker.Stage != stageTargetCommitted {
		return Result{}, fmt.Errorf("unsupported migration stage %q", marker.Stage)
	}
	marker.Stage = stageSystemStarting
	if err := s.markers.Save(marker); err != nil {
		return Result{}, s.rollback(ctx, marker, err)
	}
	targetExecutable := filepath.Join(marker.TargetDataDir, "app", "shadoc")
	arguments := []string{
		"serve", "--service-scope", "system",
		"--listen", marker.Source.Listen,
		"--data-dir", marker.TargetDataDir,
	}
	if err := s.target.Install(targetExecutable, arguments); err != nil {
		return Result{}, s.rollback(ctx, marker, fmt.Errorf("start root service: %w", err))
	}
	if err := s.target.Healthy(ctx, marker.Source.Listen); err != nil {
		return Result{}, s.rollback(ctx, marker, fmt.Errorf("verify root service: %w", err))
	}
	marker.Stage = stageHealthVerified
	if err := s.markers.Save(marker); err != nil {
		return Result{}, err
	}
	return s.cleanupSource(ctx, marker)
}

func (s *Service) cleanupSource(ctx context.Context, marker Marker) (Result, error) {
	if err := s.source.Remove(ctx, marker.Source); err != nil {
		return Result{}, fmt.Errorf("root service is healthy but user service cleanup is incomplete: %w", err)
	}
	if err := s.mover.Remove(marker.Source.DataDir, marker.Source.UID); err != nil {
		return Result{}, fmt.Errorf("root service is healthy but source data cleanup is incomplete: %w", err)
	}
	if err := s.markers.Remove(); err != nil {
		return Result{}, fmt.Errorf("root migration completed but its marker could not be removed: %w", err)
	}
	return Result{TargetDataDir: marker.TargetDataDir, SourceRemoved: true}, nil
}

func (s *Service) rollback(ctx context.Context, marker Marker, cause error) error {
	var rollbackErr error
	if marker.Stage == stageSystemStarting || marker.Stage == stageHealthVerified {
		rollbackErr = errors.Join(rollbackErr, s.target.Uninstall())
	}
	rollbackErr = errors.Join(rollbackErr, s.mover.Remove(marker.TargetDataDir, 0))
	rollbackErr = errors.Join(rollbackErr, s.mover.Remove(marker.StagingDataDir, 0))
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if err := s.source.Restore(rollbackCtx, marker.Source); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else {
		rollbackErr = errors.Join(rollbackErr, s.markers.Remove())
	}
	return errors.Join(cause, rollbackErr)
}

func validateMigrationPaths(currentExecutable, target, staging string) error {
	for name, value := range map[string]string{
		"current executable": currentExecutable,
		"target data":        target,
		"staging data":       staging,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) == string(filepath.Separator) {
			return fmt.Errorf("%s path must be a safe absolute path", name)
		}
	}
	if filepath.Clean(target) != "/var/lib/shadoc" || filepath.Clean(staging) != LinuxStagingDataDir {
		return errors.New("root migration paths must use the fixed /var/lib locations")
	}
	return nil
}

func validateInstallation(installation Installation, target string) error {
	if installation.Username == "" || installation.UID <= 0 || !filepath.IsAbs(installation.Home) ||
		(installation.Identity != "shadoc" && installation.Identity != "restic-control") ||
		!filepath.IsAbs(installation.DataDir) || !filepath.IsAbs(installation.Executable) ||
		installation.UnitDefinition == "" || os.FileMode(installation.UnitMode).Perm() == 0 ||
		os.FileMode(installation.UnitMode).Perm()&0o022 != 0 {
		return errors.New("source user installation is invalid")
	}
	source := filepath.Clean(installation.DataDir)
	if source == string(filepath.Separator) || source == filepath.Clean(target) ||
		installation.Listen == "" || installation.Username == "root" {
		return errors.New("source user installation is unsafe to migrate")
	}
	return nil
}
