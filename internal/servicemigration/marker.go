package servicemigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type FileMarkerStore struct {
	path        string
	expectedUID int
}

func NewFileMarkerStore(path string, expectedUID int) (*FileMarkerStore, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) || expectedUID < 0 {
		return nil, errors.New("migration marker requires a safe absolute path and owner")
	}
	return &FileMarkerStore{path: filepath.Clean(path), expectedUID: expectedUID}, nil
}

func (s *FileMarkerStore) Load() (Marker, bool, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, fmt.Errorf("inspect migration marker: %w", err)
	}
	if err := validatePrivateMarker(info, s.expectedUID); err != nil {
		return Marker{}, false, err
	}
	file, err := os.Open(s.path)
	if err != nil {
		return Marker{}, false, fmt.Errorf("open migration marker: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	decoder.DisallowUnknownFields()
	var marker Marker
	if err := decoder.Decode(&marker); err != nil {
		return Marker{}, false, fmt.Errorf("decode migration marker: %w", err)
	}
	if err := ensureMarkerJSONEOF(decoder); err != nil {
		return Marker{}, false, err
	}
	if err := validateMarker(marker); err != nil {
		return Marker{}, false, err
	}
	return marker, true, nil
}

func (s *FileMarkerStore) Save(marker Marker) (retErr error) {
	if err := validateMarker(marker); err != nil {
		return err
	}
	if info, err := os.Lstat(s.path); err == nil {
		if err := validatePrivateMarker(info, s.expectedUID); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect migration marker: %w", err)
	}
	directory := filepath.Dir(s.path)
	temp, err := os.CreateTemp(directory, ".shadoc-root-migration-*")
	if err != nil {
		return fmt.Errorf("create migration marker: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		if retErr != nil {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if s.expectedUID != os.Geteuid() {
		if err := temp.Chown(s.expectedUID, -1); err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(temp)
	if err := encoder.Encode(marker); err != nil {
		return fmt.Errorf("encode migration marker: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync migration marker: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close migration marker: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("publish migration marker: %w", err)
	}
	return syncDirectory(directory)
}

func (s *FileMarkerStore) Remove() error {
	if info, err := os.Lstat(s.path); err == nil {
		if err := validatePrivateMarker(info, s.expectedUID); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove migration marker: %w", err)
	}
	return syncDirectory(filepath.Dir(s.path))
}

func validatePrivateMarker(info os.FileInfo, expectedUID int) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("migration marker must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("migration marker permissions must be 0600, got %o", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID {
		return errors.New("migration marker has an unexpected owner")
	}
	return nil
}

func validateMarker(marker Marker) error {
	switch marker.Stage {
	case stagePrepared, stageSourceDisabled, stageDataStaged, stageTargetCommitted, stageSystemStarting, stageHealthVerified:
	default:
		return fmt.Errorf("migration marker has unsupported stage %q", marker.Stage)
	}
	if err := validateInstallation(marker.Source, marker.TargetDataDir); err != nil {
		return fmt.Errorf("migration marker source: %w", err)
	}
	return validateMigrationPaths(marker.CurrentExecutable, marker.TargetDataDir, marker.StagingDataDir)
}

func ensureMarkerJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("migration marker contains trailing data")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
