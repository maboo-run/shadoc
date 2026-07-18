package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const formatVersion byte = 1

type Vault struct {
	aead cipher.AEAD
}

func LoadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("vault key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create vault key directory: %w", err)
	}

	key, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("stat vault key: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("vault key permissions must be 0600, got %o", info.Mode().Perm())
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("vault key must be 32 bytes, got %d", len(key))
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vault key: %w", err)
	}

	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create vault key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write vault key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync vault key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close vault key: %w", err)
	}
	return key, nil
}

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("vault key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize vault gcm: %w", err)
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Seal(purpose string, plaintext []byte) ([]byte, error) {
	if purpose == "" {
		return nil, errors.New("secret purpose is required")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate secret nonce: %w", err)
	}
	result := make([]byte, 1+len(nonce))
	result[0] = formatVersion
	copy(result[1:], nonce)
	result = v.aead.Seal(result, nonce, plaintext, []byte(purpose))
	return result, nil
}

func (v *Vault) Open(purpose string, ciphertext []byte) ([]byte, error) {
	minimum := 1 + v.aead.NonceSize() + v.aead.Overhead()
	if len(ciphertext) < minimum {
		return nil, errors.New("invalid encrypted secret")
	}
	if ciphertext[0] != formatVersion {
		return nil, errors.New("unsupported encrypted secret format")
	}
	nonceEnd := 1 + v.aead.NonceSize()
	nonce := ciphertext[1:nonceEnd]
	plaintext, err := v.aead.Open(nil, nonce, ciphertext[nonceEnd:], []byte(purpose))
	if err != nil {
		return nil, errors.New("secret authentication failed")
	}
	return plaintext, nil
}
