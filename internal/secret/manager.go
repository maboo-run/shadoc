package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

type encryptedStore interface {
	SaveSecret(context.Context, string, string, []byte, time.Time) error
	LoadSecret(context.Context, string) (store.EncryptedSecret, error)
	DeleteSecret(context.Context, string) error
}

type Manager struct {
	store encryptedStore
	mu    sync.RWMutex
	vault *vault.Vault
	key   []byte
	now   func() time.Time
}

func New(s encryptedStore, v *vault.Vault, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{store: s, vault: v, now: now}
}

func (m *Manager) Put(ctx context.Context, purpose string, plaintext []byte) (string, error) {
	if purpose == "" || len(plaintext) == 0 {
		return "", errors.New("secret purpose and value are required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.vault == nil {
		return "", ErrLocked
	}
	ciphertext, err := m.vault.Seal(purpose, plaintext)
	if err != nil {
		return "", err
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	if err := m.store.SaveSecret(ctx, id, purpose, ciphertext, m.now()); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) Get(ctx context.Context, id, purpose string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.vault == nil {
		return nil, ErrLocked
	}
	record, err := m.store.LoadSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.Purpose != purpose {
		return nil, errors.New("secret purpose mismatch")
	}
	return m.vault.Open(purpose, record.Ciphertext)
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.vault == nil {
		return ErrLocked
	}
	return m.store.DeleteSecret(ctx, id)
}

func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate secret id: %w", err)
	}
	return "sec_" + hex.EncodeToString(raw), nil
}
