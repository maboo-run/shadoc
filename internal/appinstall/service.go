package appinstall

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Artifact is a release binary and the checksum published for it.
type Artifact struct {
	Binary []byte
	SHA256 [sha256.Size]byte
}

type ReleaseSource interface {
	Fetch(context.Context, string) (Artifact, error)
}

type ServiceManager interface {
	Install(string) error
	Restart() error
	Uninstall() error
}

type HealthChecker interface {
	Wait(context.Context, string) error
}

type UpdateReporter interface {
	Stage(string) error
}

type Paths struct {
	Binary     string
	Previous   string
	DataDir    string
	HealthURL  string
	Companions []string
}

type Installer struct {
	releases ReleaseSource
	services ServiceManager
	health   HealthChecker
	paths    Paths
}

func New(releases ReleaseSource, services ServiceManager, health HealthChecker, paths Paths) *Installer {
	return &Installer{releases: releases, services: services, health: health, paths: paths}
}

func (i *Installer) InstallCurrent(ctx context.Context, current string) error {
	if i.services == nil {
		return errors.New("service manager is required")
	}
	if i.paths.Binary == "" {
		return errors.New("installed binary path is required")
	}
	if i.paths.DataDir != "" {
		if err := os.MkdirAll(i.paths.DataDir, 0o700); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
	}
	knownGood, readErr := os.ReadFile(i.paths.Binary)
	hadKnownGood := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read installed binary: %w", readErr)
	}
	if err := copyAtomic(current, i.paths.Binary); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	companions, err := installCompanions(current, i.paths.Binary, i.paths.Companions)
	if err != nil {
		return errors.Join(fmt.Errorf("install companion files: %w", err), i.restoreInstallBinary(knownGood, hadKnownGood))
	}
	if err := i.services.Install(i.paths.Binary); err != nil {
		restoreErr := errors.Join(i.restoreInstallBinary(knownGood, hadKnownGood), restoreInstalledFiles(companions))
		if hadKnownGood {
			restoreErr = errors.Join(restoreErr, i.services.Restart())
		} else {
			restoreErr = errors.Join(restoreErr, i.services.Uninstall())
		}
		return errors.Join(fmt.Errorf("register service: %w", err), restoreErr)
	}
	if i.health != nil && i.paths.HealthURL != "" {
		if err := i.health.Wait(ctx, i.paths.HealthURL); err != nil {
			installErr := fmt.Errorf("wait for installed service: %w", err)
			restoreErr := errors.Join(i.restoreInstallBinary(knownGood, hadKnownGood), restoreInstalledFiles(companions))
			if hadKnownGood {
				restoreErr = errors.Join(restoreErr, i.services.Restart())
			} else {
				restoreErr = errors.Join(restoreErr, i.services.Uninstall())
			}
			return errors.Join(installErr, restoreErr)
		}
	}
	return nil
}

func (i *Installer) restoreInstallBinary(knownGood []byte, existed bool) error {
	if existed {
		if err := writeAtomic(i.paths.Binary, knownGood); err != nil {
			return fmt.Errorf("restore installed binary: %w", err)
		}
		return nil
	}
	if err := os.Remove(i.paths.Binary); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove failed installation: %w", err)
	}
	return nil
}

func (i *Installer) Update(ctx context.Context, version string) error {
	return i.UpdateWithReporter(ctx, version, nil)
}

func (i *Installer) UpdateWithReporter(ctx context.Context, version string, reporter UpdateReporter) error {
	if i.releases == nil {
		return errors.New("release source is required")
	}
	if i.services == nil {
		return errors.New("service manager is required")
	}
	if err := reportUpdateStage(reporter, "downloading_release"); err != nil {
		return err
	}
	artifact, err := i.releases.Fetch(ctx, version)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	if got := sha256.Sum256(artifact.Binary); got != artifact.SHA256 {
		return errors.New("release checksum mismatch")
	}
	if err := reportUpdateStage(reporter, "release_verified"); err != nil {
		return err
	}
	knownGood, err := os.ReadFile(i.paths.Binary)
	if err != nil {
		return fmt.Errorf("read current binary: %w", err)
	}
	if i.paths.Previous == "" {
		i.paths.Previous = i.paths.Binary + ".previous"
	}
	if err := reportUpdateStage(reporter, "saving_rollback"); err != nil {
		return err
	}
	if err := writeAtomic(i.paths.Previous, knownGood); err != nil {
		return fmt.Errorf("save rollback binary: %w", err)
	}
	if err := reportUpdateStage(reporter, "replacing_binary"); err != nil {
		return err
	}
	if err := writeAtomic(i.paths.Binary, artifact.Binary); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	if err := reportUpdateStage(reporter, "restarting_service"); err != nil {
		return i.rollback(ctx, err, reporter)
	}
	if err := i.services.Restart(); err != nil {
		return i.rollback(ctx, fmt.Errorf("restart updated service: %w", err), reporter)
	}
	if i.health != nil && i.paths.HealthURL != "" {
		if err := reportUpdateStage(reporter, "verifying_health"); err != nil {
			return i.rollback(ctx, err, reporter)
		}
		if err := i.health.Wait(ctx, i.paths.HealthURL); err != nil {
			return i.rollback(ctx, fmt.Errorf("updated service health check: %w", err), reporter)
		}
	}
	return reportUpdateStage(reporter, "health_verified")
}

func (i *Installer) Uninstall(removeData bool) error {
	if i.services == nil {
		return errors.New("service manager is required")
	}
	if i.paths.Binary == "" {
		return errors.New("installed binary path is required")
	}
	if err := i.services.Uninstall(); err != nil {
		return fmt.Errorf("unregister service: %w", err)
	}
	var cleanupErr error
	paths := []string{i.paths.Binary, i.paths.Previous}
	for _, name := range i.paths.Companions {
		if name == "" || filepath.Base(name) != name {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unsafe companion filename %q", name))
			continue
		}
		paths = append(paths, filepath.Join(filepath.Dir(i.paths.Binary), name))
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if removeData {
		dataDir := filepath.Clean(i.paths.DataDir)
		if i.paths.DataDir == "" || dataDir == "." || dataDir == string(filepath.Separator) {
			return errors.Join(cleanupErr, errors.New("refusing to remove unsafe data directory"))
		}
		if err := os.RemoveAll(dataDir); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

type installedFile struct {
	path    string
	content []byte
	existed bool
}

func installCompanions(currentBinary, installedBinary string, names []string) ([]installedFile, error) {
	var installed []installedFile
	for _, name := range names {
		if name == "" || filepath.Base(name) != name {
			return installed, errors.Join(fmt.Errorf("unsafe companion filename %q", name), restoreInstalledFiles(installed))
		}
		source := filepath.Join(filepath.Dir(currentBinary), name)
		info, err := os.Stat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return installed, errors.Join(err, restoreInstalledFiles(installed))
		}
		if !info.Mode().IsRegular() {
			return installed, errors.Join(fmt.Errorf("companion file %s is not regular", name), restoreInstalledFiles(installed))
		}
		target := filepath.Join(filepath.Dir(installedBinary), name)
		previous, readErr := os.ReadFile(target)
		state := installedFile{path: target, content: previous, existed: readErr == nil}
		if readErr != nil && !os.IsNotExist(readErr) {
			return installed, errors.Join(readErr, restoreInstalledFiles(installed))
		}
		installed = append(installed, state)
		if err := copyAtomic(source, target); err != nil {
			return installed, errors.Join(err, restoreInstalledFiles(installed))
		}
	}
	return installed, nil
}

func restoreInstalledFiles(files []installedFile) error {
	var restoreErr error
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		if file.existed {
			restoreErr = errors.Join(restoreErr, writeAtomic(file.path, file.content))
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return restoreErr
}

func (i *Installer) rollback(ctx context.Context, updateErr error, reporter UpdateReporter) error {
	_ = reportUpdateStage(reporter, "rolling_back")
	previous, err := os.ReadFile(i.paths.Previous)
	if err != nil {
		return errors.Join(updateErr, fmt.Errorf("read rollback binary: %w", err))
	}
	if err := writeAtomic(i.paths.Binary, previous); err != nil {
		return errors.Join(updateErr, fmt.Errorf("restore rollback binary: %w", err))
	}
	if err := i.services.Restart(); err != nil {
		return errors.Join(updateErr, fmt.Errorf("restart rolled back service: %w", err))
	}
	if i.health != nil && i.paths.HealthURL != "" {
		_ = reportUpdateStage(reporter, "verifying_rollback")
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := i.health.Wait(rollbackCtx, i.paths.HealthURL); err != nil {
			return errors.Join(updateErr, fmt.Errorf("rolled back service health check: %w", err))
		}
	}
	if err := reportUpdateStage(reporter, "rollback_verified"); err != nil {
		return errors.Join(updateErr, err)
	}
	return updateErr
}

func reportUpdateStage(reporter UpdateReporter, stage string) error {
	if reporter == nil {
		return nil
	}
	if err := reporter.Stage(stage); err != nil {
		return fmt.Errorf("record application update stage: %w", err)
	}
	return nil
}

func copyAtomic(source, target string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	return writeFromAtomic(target, src)
}

func writeAtomic(target string, content []byte) error {
	return writeFromAtomic(target, &byteReader{content: content})
}

type byteReader struct {
	content []byte
	offset  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.offset == len(r.content) {
		return 0, io.EOF
	}
	n := copy(p, r.content[r.offset:])
	r.offset += n
	return n, nil
}

func writeFromAtomic(target string, source io.Reader) (retErr error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".shadoc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o755); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, source); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
