package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/argon2"
)

type KeyState string

const (
	Automatic KeyState = "automatic"
	Locked    KeyState = "locked"
	Unlocked  KeyState = "unlocked"
)

var ErrInvalidPassphrase = errors.New("invalid vault passphrase")

const (
	wrappedKeyVersion = 1
	wrapMemoryKiB     = 64 * 1024
	wrapIterations    = 3
)

type wrappedKey struct {
	Version     int    `json:"version"`
	MemoryKiB   uint32 `json:"memoryKiB"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	Salt        []byte `json:"salt"`
	Nonce       []byte `json:"nonce"`
	Ciphertext  []byte `json:"ciphertext"`
}

type KeyFile struct {
	path string
}

func NewKeyFile(path string) *KeyFile {
	return &KeyFile{path: path}
}

func (f *KeyFile) Load(passphrase string) ([]byte, KeyState, error) {
	content, err := os.ReadFile(f.path)
	if err != nil {
		return nil, Locked, err
	}
	info, err := os.Stat(f.path)
	if err != nil {
		return nil, Locked, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, Locked, fmt.Errorf("vault key permissions must be 0600, got %o", info.Mode().Perm())
	}
	if len(content) == 32 {
		return append([]byte(nil), content...), Automatic, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var wrapped wrappedKey
	if err := decoder.Decode(&wrapped); err != nil {
		return nil, Locked, errors.New("invalid wrapped vault key")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, Locked, err
	}
	if err := validateWrappedKey(wrapped); err != nil {
		return nil, Locked, err
	}
	if passphrase == "" {
		return nil, Locked, nil
	}
	derived := argon2.IDKey([]byte(passphrase), wrapped.Salt, wrapped.Iterations, wrapped.MemoryKiB, wrapped.Parallelism, 32)
	defer clear(derived)
	aead, err := keyAEAD(derived)
	if err != nil {
		return nil, Locked, err
	}
	master, err := aead.Open(nil, wrapped.Nonce, wrapped.Ciphertext, []byte("restic-control:vault-key:v1"))
	if err != nil || len(master) != 32 {
		clear(master)
		return nil, Locked, ErrInvalidPassphrase
	}
	return master, Unlocked, nil
}

func (f *KeyFile) SaveLocked(master []byte, passphrase string) error {
	if len(master) != 32 {
		return fmt.Errorf("vault key must be 32 bytes, got %d", len(master))
	}
	if len(passphrase) < 12 {
		return errors.New("vault passphrase must contain at least 12 characters")
	}
	parallelism := uint8(min(runtime.NumCPU(), 4))
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate key salt: %w", err)
	}
	derived := argon2.IDKey([]byte(passphrase), salt, wrapIterations, wrapMemoryKiB, parallelism, 32)
	defer clear(derived)
	aead, err := keyAEAD(derived)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate key nonce: %w", err)
	}
	record := wrappedKey{
		Version: wrappedKeyVersion, MemoryKiB: wrapMemoryKiB, Iterations: wrapIterations,
		Parallelism: parallelism, Salt: salt, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, master, []byte("restic-control:vault-key:v1")),
	}
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return f.write(content)
}

func (f *KeyFile) SaveAutomatic(master []byte) error {
	if len(master) != 32 {
		return fmt.Errorf("vault key must be 32 bytes, got %d", len(master))
	}
	return f.write(master)
}

func (f *KeyFile) write(content []byte) (retErr error) {
	if f.path == "" {
		return errors.New("vault key path is required")
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".vault-key-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, f.path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func keyAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func validateWrappedKey(w wrappedKey) error {
	if w.Version != wrappedKeyVersion {
		return errors.New("unsupported wrapped vault key version")
	}
	if w.MemoryKiB < 8*1024 || w.MemoryKiB > 1024*1024 || w.Iterations == 0 || w.Iterations > 10 || w.Parallelism == 0 || w.Parallelism > 16 {
		return errors.New("invalid wrapped vault key parameters")
	}
	if len(w.Salt) < 16 || len(w.Salt) > 64 || len(w.Nonce) != 12 || len(w.Ciphertext) != 48 {
		return errors.New("invalid wrapped vault key payload")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid trailing wrapped vault key data")
	}
	return nil
}
