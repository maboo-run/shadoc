//go:build !linux

package servicemigration

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func readSourceUnitFile(path string, expectedUID int) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if err := validateSourceUnitInfo(info, expectedUID); err != nil {
		return nil, 0, false, err
	}
	content, err := readLimitedFile(path, 1024*1024)
	if err != nil {
		return nil, 0, false, err
	}
	return content, info.Mode().Perm(), true, nil
}

func writeSourceUnitFileAtomic(path string, content []byte, mode os.FileMode, uid int) (retErr error) {
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return errors.New("source user service directory is unavailable")
	}
	temp, err := os.CreateTemp(directory, ".shadoc-service-restore-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() {
		_ = temp.Close()
		if retErr != nil {
			_ = os.Remove(name)
		}
	}()
	if err := temp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if uid != os.Geteuid() {
		if err := temp.Chown(uid, -1); err != nil {
			return err
		}
	}
	if _, err := io.Copy(temp, bytes.NewReader(content)); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func removeSourceUnitFile(path string, expectedUID int, expectedContent []byte) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateSourceUnitInfo(info, expectedUID); err != nil {
		return err
	}
	content, err := readLimitedFile(path, 1024*1024)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, expectedContent) {
		return errors.New("source service definition changed before removal")
	}
	return os.Remove(path)
}
