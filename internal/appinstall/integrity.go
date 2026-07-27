package appinstall

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
)

const checksumSuffix = ".sha256"

// ChecksumPath returns the local integrity record kept beside a managed binary.
func ChecksumPath(binary string) string {
	return filepath.Clean(binary) + checksumSuffix
}

// VerifyInstalledBinary verifies a managed binary using only its local integrity
// record. The installer creates that record after the release checksum has
// already been verified.
func VerifyInstalledBinary(binary string) error {
	expected, err := ReadInstalledChecksum(binary)
	if err != nil {
		return err
	}
	actual, err := checksumRegularFile(binary)
	if err != nil {
		return fmt.Errorf("calculate installed Shadoc checksum: %w", err)
	}
	if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return errors.New("installed Shadoc checksum does not match its local integrity record")
	}
	return nil
}

// ReadInstalledChecksum loads the strict SHA-256 record for a managed binary.
func ReadInstalledChecksum(binary string) ([sha256.Size]byte, error) {
	path := ChecksumPath(binary)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return [sha256.Size]byte{}, errors.New("installed Shadoc integrity record is missing; reinstall or update Shadoc before migrating")
		}
		return [sha256.Size]byte{}, fmt.Errorf("inspect installed Shadoc integrity record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return [sha256.Size]byte{}, errors.New("installed Shadoc integrity record is not a protected regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("read installed Shadoc integrity record: %w", err)
	}
	value := strings.TrimSuffix(string(content), "\n")
	if len(content) != sha256.Size*2+1 || len(value) != sha256.Size*2 {
		return [sha256.Size]byte{}, errors.New("installed Shadoc integrity record is malformed")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || strings.ToLower(value) != value {
		return [sha256.Size]byte{}, errors.New("installed Shadoc integrity record is malformed")
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], decoded)
	return checksum, nil
}

func writeInstalledChecksum(binary string, checksum [sha256.Size]byte) error {
	content := []byte(hex.EncodeToString(checksum[:]) + "\n")
	if err := writeAtomicMode(ChecksumPath(binary), content, 0o600); err != nil {
		return fmt.Errorf("write installed Shadoc integrity record: %w", err)
	}
	return nil
}

func writeInstalledChecksumForFile(binary string) error {
	checksum, err := checksumRegularFile(binary)
	if err != nil {
		return err
	}
	return writeInstalledChecksum(binary, checksum)
}

func checksumRegularFile(path string) ([sha256.Size]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return [sha256.Size]byte{}, errors.New("installed Shadoc program is not a protected regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o022 != 0 {
		return [sha256.Size]byte{}, errors.New("installed Shadoc program changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		opened.Mode() != after.Mode() || !opened.ModTime().Equal(after.ModTime()) {
		return [sha256.Size]byte{}, errors.New("installed Shadoc program changed while calculating its checksum")
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	return checksum, nil
}
