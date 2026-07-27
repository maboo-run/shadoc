//go:build linux

package servicemigration

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/maboo-run/shadoc/internal/appinstall"
	"golang.org/x/sys/unix"
)

func (m *FilesystemMover) Stage(source, staging, currentExecutable string, sourceUID int) (retErr error) {
	source = filepath.Clean(source)
	staging = filepath.Clean(staging)
	currentExecutable = filepath.Clean(currentExecutable)
	if staging != m.stagingDataDir || !filepath.IsAbs(source) || !filepath.IsAbs(currentExecutable) ||
		sourceUID <= 0 || unsafeSourceRoot(source) || source == m.targetDataDir || source == m.stagingDataDir {
		return errors.New("unsafe root migration staging request")
	}
	exists, err := m.Exists(staging)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("migration staging directory already exists: %s", staging)
	}
	sourceFD, err := openDirectoryNoSymlinks(source)
	if err != nil {
		return fmt.Errorf("open source data directory: %w", err)
	}
	defer unix.Close(sourceFD)
	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil {
		return err
	}
	if int(sourceStat.Uid) != sourceUID || sourceStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		sourceStat.Mode&0o022 != 0 {
		return errors.New("source data directory ownership or permissions changed before migration")
	}
	stagingFD, err := createDirectoryNoSymlinks(staging, 0o700)
	if err != nil {
		return fmt.Errorf("create root migration staging directory: %w", err)
	}
	defer func() {
		_ = unix.Close(stagingFD)
		if retErr != nil {
			retErr = errors.Join(retErr, m.Remove(staging, m.ownerUID))
		}
	}()
	if err := copyDirectoryContents(sourceFD, stagingFD, "", sourceUID, uint64(sourceStat.Dev)); err != nil {
		return fmt.Errorf("copy source data: %w", err)
	}
	if err := copyCurrentExecutable(currentExecutable, stagingFD, sourceUID); err != nil {
		return fmt.Errorf("install current executable into staged data: %w", err)
	}
	if err := unix.Fsync(stagingFD); err != nil {
		return fmt.Errorf("sync staged data directory: %w", err)
	}
	if err := unix.Close(stagingFD); err != nil {
		stagingFD = -1
		return fmt.Errorf("close staged data directory: %w", err)
	}
	stagingFD = -1
	if err := validateStagedData(staging, m.ownerUID); err != nil {
		return err
	}
	return nil
}

func (m *FilesystemMover) Commit(staging, target string) error {
	staging = filepath.Clean(staging)
	target = filepath.Clean(target)
	if staging != m.stagingDataDir || target != m.targetDataDir {
		return errors.New("filesystem migration commit does not match its fixed targets")
	}
	parentFD, stagingName, err := openParentNoSymlinks(staging)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if filepath.Dir(staging) != filepath.Dir(target) {
		return errors.New("migration staging and target directories must share a parent")
	}
	var stagingStat unix.Stat_t
	if err := unix.Fstatat(parentFD, stagingName, &stagingStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stagingStat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stagingStat.Uid) != m.ownerUID {
		return errors.New("migration staging directory is not owned by the root migration process")
	}
	targetName := filepath.Base(target)
	var targetStat unix.Stat_t
	if err := unix.Fstatat(parentFD, targetName, &targetStat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return errors.New("root migration target appeared before commit")
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Renameat(parentFD, stagingName, parentFD, targetName); err != nil {
		return err
	}
	return unix.Fsync(parentFD)
}

func (m *FilesystemMover) Remove(path string, expectedUID int) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return errors.New("refusing to remove an unsafe migration path")
	}
	switch path {
	case m.targetDataDir, m.stagingDataDir:
		if expectedUID != m.ownerUID {
			return errors.New("root migration target owner does not match the migration process")
		}
	default:
		if expectedUID <= 0 || unsafeSourceRoot(path) {
			return errors.New("refusing to remove an unsafe source data directory")
		}
	}
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	var rootStat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &rootStat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || int(rootStat.Uid) != expectedUID {
		return errors.New("migration cleanup path is not the expected owned directory")
	}
	rootFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var openedRoot unix.Stat_t
	if err := unix.Fstat(rootFD, &openedRoot); err != nil || !sameOpenedEntry(rootStat, openedRoot) {
		_ = unix.Close(rootFD)
		return errors.New("migration cleanup directory changed while opening")
	}
	if err := removeDirectoryContents(rootFD, uint64(rootStat.Dev)); err != nil {
		_ = unix.Close(rootFD)
		return err
	}
	var emptiedRoot unix.Stat_t
	if err := unix.Fstat(rootFD, &emptiedRoot); err != nil || !sameOpenedEntry(rootStat, emptiedRoot) {
		_ = unix.Close(rootFD)
		return errors.New("migration cleanup directory changed while deleting")
	}
	if err := unix.Close(rootFD); err != nil {
		return err
	}
	var currentRoot unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &currentRoot, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameOpenedEntry(rootStat, currentRoot) {
		return errors.New("migration cleanup directory changed before final removal")
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(parentFD)
}

func (m *FilesystemMover) Exists(path string) (bool, error) {
	path = filepath.Clean(path)
	if path != m.targetDataDir && path != m.stagingDataDir {
		return false, errors.New("migration existence check is limited to fixed target directories")
	}
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return false, err
	}
	defer unix.Close(parentFD)
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func copyDirectoryContents(sourceFD, targetFD int, relative string, sourceUID int, sourceDevice uint64) error {
	readFD, err := unix.Dup(sourceFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(readFD), "source-directory")
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
			return errors.New("source data contains an unsafe filename")
		}
		var before unix.Stat_t
		if err := unix.Fstatat(sourceFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if uint64(before.Dev) != sourceDevice || (int(before.Uid) != sourceUID && before.Uid != 0) {
			return fmt.Errorf("source entry %s has an unexpected filesystem or owner", filepath.Join(relative, name))
		}
		childRelative := filepath.Join(relative, name)
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			childSourceFD, err := unix.Openat(sourceFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			if err := unix.Fstat(childSourceFD, &opened); err != nil || !sameOpenedEntry(before, opened) {
				_ = unix.Close(childSourceFD)
				return fmt.Errorf("source directory %s changed while opening", childRelative)
			}
			if err := unix.Mkdirat(targetFD, name, 0o700); err != nil {
				_ = unix.Close(childSourceFD)
				return err
			}
			childTargetFD, err := unix.Openat(targetFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				_ = unix.Close(childSourceFD)
				return err
			}
			if err := sanitizeMigratedFD(childTargetFD, 0o700, os.Geteuid(), true); err != nil {
				_ = unix.Close(childTargetFD)
				_ = unix.Close(childSourceFD)
				return err
			}
			copyErr := copyDirectoryContents(childSourceFD, childTargetFD, childRelative, sourceUID, sourceDevice)
			copyErr = errors.Join(copyErr, unix.Fsync(childTargetFD), unix.Close(childTargetFD), unix.Close(childSourceFD))
			if copyErr != nil {
				return copyErr
			}
		case unix.S_IFREG:
			if slash := filepath.ToSlash(childRelative); slash == "app/shadoc" ||
				slash == "app/restic-control" ||
				slash == "app/shadoc.sha256" ||
				slash == "app/restic-control.sha256" ||
				slash == "bin/restic" {
				continue
			}
			childSourceFD, err := unix.Openat(sourceFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			if err := unix.Fstat(childSourceFD, &opened); err != nil {
				_ = unix.Close(childSourceFD)
				return err
			}
			if !sameOpenedEntry(before, opened) {
				_ = unix.Close(childSourceFD)
				return fmt.Errorf("source file %s changed while opening", childRelative)
			}
			// Source files are migrated as data only. The verified Shadoc
			// program is installed separately below; no other user-owned file
			// becomes executable by root.
			mode := uint32(0o600)
			childTargetFD, err := unix.Openat(targetFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
			if err != nil {
				_ = unix.Close(childSourceFD)
				return err
			}
			if err := sanitizeMigratedFD(childTargetFD, mode, os.Geteuid(), false); err != nil {
				_ = unix.Close(childTargetFD)
				_ = unix.Close(childSourceFD)
				return err
			}
			if err := copyRegularFile(childSourceFD, childTargetFD, opened); err != nil {
				return fmt.Errorf("copy %s: %w", childRelative, err)
			}
		default:
			return fmt.Errorf("source entry %s is not a regular file or directory", childRelative)
		}
	}
	return nil
}

func copyCurrentExecutable(path string, targetRootFD, sourceUID int) error {
	expectedChecksum, err := appinstall.ReadInstalledChecksum(path)
	if err != nil {
		return err
	}
	sourceFD, err := openRegularFileNoSymlinks(path)
	if err != nil {
		return err
	}
	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil {
		_ = unix.Close(sourceFD)
		return err
	}
	if sourceStat.Mode&unix.S_IFMT != unix.S_IFREG || sourceStat.Mode&0o022 != 0 ||
		sourceStat.Mode&0o111 == 0 || (int(sourceStat.Uid) != sourceUID && sourceStat.Uid != 0) {
		_ = unix.Close(sourceFD)
		return errors.New("current Shadoc executable is unsafe to install as root")
	}
	actualChecksum, err := checksumFileDescriptor(sourceFD)
	if err != nil {
		_ = unix.Close(sourceFD)
		return err
	}
	var verifiedStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &verifiedStat); err != nil ||
		!sameStableFile(sourceStat, verifiedStat) ||
		subtle.ConstantTimeCompare(actualChecksum[:], expectedChecksum[:]) != 1 {
		_ = unix.Close(sourceFD)
		return errors.New("current Shadoc executable failed its local integrity verification")
	}
	if _, err := unix.Seek(sourceFD, 0, io.SeekStart); err != nil {
		_ = unix.Close(sourceFD)
		return err
	}
	appFD, err := openOrCreateDirectoryAt(targetRootFD, "app")
	if err != nil {
		_ = unix.Close(sourceFD)
		return err
	}
	targetFD, err := unix.Openat(appFD, "shadoc", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o755)
	if err != nil {
		_ = unix.Close(appFD)
		_ = unix.Close(sourceFD)
		return err
	}
	if err := sanitizeMigratedFD(targetFD, 0o755, os.Geteuid(), false); err != nil {
		_ = unix.Close(targetFD)
		_ = unix.Close(appFD)
		_ = unix.Close(sourceFD)
		return err
	}
	copyErr := copyRegularFile(sourceFD, targetFD, sourceStat)
	if copyErr == nil {
		copyErr = writeStagedChecksum(appFD, expectedChecksum)
	}
	copyErr = errors.Join(copyErr, unix.Fsync(appFD), unix.Close(appFD))
	return copyErr
}

func writeStagedChecksum(appFD int, checksum [sha256.Size]byte) error {
	fd, err := unix.Openat(
		appFD,
		"shadoc.sha256",
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return err
	}
	if err := sanitizeMigratedFD(fd, 0o600, os.Geteuid(), false); err != nil {
		_ = unix.Close(fd)
		return err
	}
	file := os.NewFile(uintptr(fd), "staged-shadoc-checksum")
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create staged Shadoc integrity record")
	}
	_, writeErr := file.Write([]byte(hex.EncodeToString(checksum[:]) + "\n"))
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func checksumFileDescriptor(fd int) ([sha256.Size]byte, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	file := os.NewFile(uintptr(duplicate), "migration-checksum-source")
	if file == nil {
		_ = unix.Close(duplicate)
		return [sha256.Size]byte{}, errors.New("open migration checksum source")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return [sha256.Size]byte{}, errors.Join(copyErr, closeErr)
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	return checksum, nil
}

func copyRegularFile(sourceFD, targetFD int, before unix.Stat_t) error {
	source := os.NewFile(uintptr(sourceFD), "migration-source")
	target := os.NewFile(uintptr(targetFD), "migration-target")
	if source == nil || target == nil {
		if source != nil {
			_ = source.Close()
		} else {
			_ = unix.Close(sourceFD)
		}
		if target != nil {
			_ = target.Close()
		} else {
			_ = unix.Close(targetFD)
		}
		return errors.New("open migration file descriptor")
	}
	_, copyErr := io.Copy(target, source)
	syncErr := target.Sync()
	var after unix.Stat_t
	statErr := unix.Fstat(int(source.Fd()), &after)
	targetCloseErr := target.Close()
	sourceCloseErr := source.Close()
	if statErr == nil && (after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim) {
		statErr = errors.New("source file changed during migration copy")
	}
	return errors.Join(copyErr, syncErr, statErr, targetCloseErr, sourceCloseErr)
}

func openOrCreateDirectoryAt(parentFD int, name string) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		if err := sanitizeMigratedFD(fd, 0o700, os.Geteuid(), true); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, err
	}
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		return -1, err
	}
	fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := sanitizeMigratedFD(fd, 0o700, os.Geteuid(), true); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func sameOpenedEntry(before, opened unix.Stat_t) bool {
	return before.Dev == opened.Dev &&
		before.Ino == opened.Ino &&
		before.Mode == opened.Mode &&
		before.Uid == opened.Uid
}

func createDirectoryNoSymlinks(path string, mode uint32) (int, error) {
	parentFD, name, err := openParentNoSymlinks(path)
	if err != nil {
		return -1, err
	}
	defer unix.Close(parentFD)
	var existing unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return -1, os.ErrExist
	} else if !errors.Is(err, unix.ENOENT) {
		return -1, err
	}
	if err := unix.Mkdirat(parentFD, name, mode); err != nil {
		return -1, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		return -1, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		return -1, err
	}
	if err := sanitizeMigratedFD(fd, mode, os.Geteuid(), true); err != nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		return -1, err
	}
	return fd, nil
}

func sanitizeMigratedFD(fd int, mode uint32, expectedUID int, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if int(stat.Uid) != expectedUID {
		if err := unix.Fchown(fd, expectedUID, -1); err != nil {
			return err
		}
	}
	attributes := []string{"system.posix_acl_access", "security.capability"}
	if directory {
		attributes = append(attributes, "system.posix_acl_default")
	}
	for _, attribute := range attributes {
		size, err := unix.Fgetxattr(fd, attribute, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			continue
		}
		if err != nil {
			return err
		}
		if size > 0 {
			if err := unix.Fremovexattr(fd, attribute); err != nil {
				return err
			}
		}
	}
	return unix.Fchmod(fd, mode)
}

func openDirectoryNoSymlinks(path string) (int, error) {
	return openAbsoluteNoSymlinks(path, unix.O_RDONLY|unix.O_DIRECTORY)
}

func openRegularFileNoSymlinks(path string) (int, error) {
	return openAbsoluteNoSymlinks(path, unix.O_RDONLY)
}

func openAbsoluteNoSymlinks(path string, finalFlags int) (int, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return -1, errors.New("path must be absolute")
	}
	currentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		flags := unix.O_RDONLY | unix.O_DIRECTORY
		if index == len(components)-1 {
			flags = finalFlags
		}
		nextFD, openErr := unix.Openat(currentFD, component, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func openParentNoSymlinks(path string) (int, string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return -1, "", errors.New("path requires a safe absolute parent")
	}
	fd, err := openDirectoryNoSymlinks(filepath.Dir(path))
	if err != nil {
		return -1, "", err
	}
	name := filepath.Base(path)
	if name == "." || name == ".." || name == "" {
		_ = unix.Close(fd)
		return -1, "", errors.New("path has an unsafe final component")
	}
	return fd, name, nil
}

func removeDirectoryContents(directoryFD int, device uint64) error {
	readFD, err := unix.Dup(directoryFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(readFD), "migration-cleanup-directory")
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		name := entry.Name()
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			if uint64(stat.Dev) != device {
				return fmt.Errorf("refusing to cross a mounted directory at %s", name)
			}
			childFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			if err := unix.Fstat(childFD, &opened); err != nil || !sameOpenedEntry(stat, opened) {
				_ = unix.Close(childFD)
				return fmt.Errorf("cleanup directory %s changed while opening", name)
			}
			removeErr := removeDirectoryContents(childFD, device)
			closeErr := unix.Close(childFD)
			if removeErr != nil || closeErr != nil {
				return errors.Join(removeErr, closeErr)
			}
			var current unix.Stat_t
			if err := unix.Fstatat(directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
				!sameOpenedEntry(stat, current) {
				return fmt.Errorf("cleanup directory %s changed before removal", name)
			}
			if err := unix.Unlinkat(directoryFD, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		entryFD, err := unix.Openat(directoryFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		var opened unix.Stat_t
		statErr := unix.Fstat(entryFD, &opened)
		closeErr := unix.Close(entryFD)
		if statErr != nil || closeErr != nil || !sameOpenedEntry(stat, opened) {
			return errors.Join(fmt.Errorf("cleanup entry %s changed while opening", name), statErr, closeErr)
		}
		var current unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			!sameOpenedEntry(stat, current) {
			return fmt.Errorf("cleanup entry %s changed before removal", name)
		}
		if err := unix.Unlinkat(directoryFD, name, 0); err != nil {
			return err
		}
	}
	return unix.Fsync(directoryFD)
}
