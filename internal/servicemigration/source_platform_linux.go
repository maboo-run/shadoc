//go:build linux

package servicemigration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const sourceUnitLimit = 1024 * 1024

func readSourceUnitFile(path string, expectedUID int) ([]byte, os.FileMode, bool, error) {
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	defer unix.Close(parentFD)
	var before unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil, 0, false, nil
	} else if err != nil {
		return nil, 0, false, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || int(before.Uid) != expectedUID || before.Mode&0o022 != 0 {
		return nil, 0, false, errors.New("source service definition is not a safe user-owned regular file")
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, false, err
	}
	file := os.NewFile(uintptr(fd), "source-service-definition")
	if file == nil {
		_ = unix.Close(fd)
		return nil, 0, false, errors.New("open source service definition")
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = file.Close()
		return nil, 0, false, err
	}
	if !sameStatIdentity(before, opened) {
		_ = file.Close()
		return nil, 0, false, errors.New("source service definition changed while opening")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, sourceUnitLimit+1))
	var after unix.Stat_t
	statErr := unix.Fstat(fd, &after)
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return nil, 0, false, errors.Join(readErr, statErr, closeErr)
	}
	if len(content) > sourceUnitLimit || !sameStableFile(opened, after) {
		return nil, 0, false, errors.New("source service definition changed while reading")
	}
	return content, os.FileMode(opened.Mode).Perm(), true, nil
}

func writeSourceUnitFileAtomic(path string, content []byte, mode os.FileMode, uid int) error {
	if uid <= 0 || mode.Perm() == 0 || mode.Perm()&0o022 != 0 || len(content) > sourceUnitLimit {
		return errors.New("unsafe source service restore request")
	}
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil ||
		parentStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		(int(parentStat.Uid) != uid && parentStat.Uid != 0) {
		return errors.New("source user service directory is unavailable")
	}
	tempName, err := sourceUnitTempName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(
		parentFD,
		tempName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return err
	}
	published := false
	var file *os.File
	defer func() {
		if file != nil {
			_ = file.Close()
		} else if fd >= 0 {
			_ = unix.Close(fd)
		}
		if !published {
			_ = unix.Unlinkat(parentFD, tempName, 0)
		}
	}()
	file = os.NewFile(uintptr(fd), "source-service-restore")
	if file == nil {
		return errors.New("create source service restore file")
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	var existing unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return errors.New("source service definition reappeared during restore")
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Renameat2(parentFD, tempName, parentFD, name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	published = true
	if uid != os.Geteuid() {
		if err := file.Chown(uid, -1); err != nil {
			return err
		}
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		file = nil
		fd = -1
		return err
	}
	file = nil
	fd = -1
	return unix.Fsync(parentFD)
}

func sourceUnitTempName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".shadoc-service-restore-" + hex.EncodeToString(random[:]), nil
}

func sameStatIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev &&
		left.Ino == right.Ino &&
		left.Mode&unix.S_IFMT == right.Mode&unix.S_IFMT &&
		left.Uid == right.Uid
}

func sameStableFile(left, right unix.Stat_t) bool {
	return sameStatIdentity(left, right) &&
		left.Size == right.Size &&
		left.Mode == right.Mode &&
		left.Mtim == right.Mtim &&
		left.Ctim == right.Ctim
}

func removeSourceUnitFile(path string, expectedUID int, expectedContent []byte) error {
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != expectedUID || stat.Mode&0o022 != 0 {
		return errors.New("source service definition changed before removal")
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "source-service-definition")
	content, readErr := io.ReadAll(io.LimitReader(file, sourceUnitLimit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if !bytes.Equal(content, expectedContent) {
		return errors.New("source service definition changed before removal")
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameStatIdentity(stat, current) {
		return errors.New("source service definition changed before removal")
	}
	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		return err
	}
	return unix.Fsync(parentFD)
}
