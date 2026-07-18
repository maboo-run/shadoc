package secret

import (
	"errors"
	"time"

	"github.com/maboo-run/shadoc/internal/vault"
)

var ErrLocked = errors.New("secret vault is locked")

// Gate is the restart-lockable secret manager. It aliases Manager so existing
// backup services consume the same narrow Get/Put/Delete surface.
type Gate = Manager

func NewGate(s encryptedStore, now func() time.Time) *Gate {
	if now == nil {
		now = time.Now
	}
	return &Gate{store: s, now: now}
}

func (m *Manager) Unlock(master []byte) error {
	v, err := vault.New(master)
	if err != nil {
		return err
	}
	key := append([]byte(nil), master...)
	m.mu.Lock()
	clear(m.key)
	m.key = key
	m.vault = v
	m.mu.Unlock()
	return nil
}

func (m *Manager) Lock() {
	m.mu.Lock()
	m.vault = nil
	clear(m.key)
	m.key = nil
	m.mu.Unlock()
}

func (m *Manager) IsLocked() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vault == nil
}

func (m *Manager) MasterKey() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.vault == nil || len(m.key) != 32 {
		return nil, ErrLocked
	}
	return append([]byte(nil), m.key...), nil
}
