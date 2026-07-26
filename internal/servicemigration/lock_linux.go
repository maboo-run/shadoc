//go:build linux

package servicemigration

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type FileLock struct {
	fd int
}

func AcquireFileLock(path string, expectedUID int) (*FileLock, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || expectedUID < 0 {
		return nil, errors.New("migration lock requires a safe absolute path and owner")
	}
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("open migration lock directory: %w", err)
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(
		parentFD,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open migration lock: %w", err)
	}
	closeOnError := func(cause error) (*FileLock, error) {
		_ = unix.Close(fd)
		return nil, cause
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeOnError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != expectedUID ||
		stat.Mode&0o077 != 0 {
		return closeOnError(errors.New("migration lock is not the expected private owned file"))
	}
	if err := sanitizeMigratedFD(fd, 0o600, expectedUID, false); err != nil {
		return closeOnError(fmt.Errorf("secure migration lock: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeOnError(errors.New("another root migration is already running"))
		}
		return closeOnError(fmt.Errorf("lock root migration: %w", err))
	}
	if err := unix.Fsync(parentFD); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeOnError(err)
	}
	return &FileLock{fd: fd}, nil
}

func (l *FileLock) Close() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	fd := l.fd
	l.fd = -1
	return errors.Join(unix.Flock(fd, unix.LOCK_UN), unix.Close(fd))
}
