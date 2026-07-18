package secret

import (
	"errors"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/vault"
)

type VaultStatus struct {
	Mode   string `json:"mode"`
	Locked bool   `json:"locked"`
}

type VaultController struct {
	file       *vault.KeyFile
	gate       *Gate
	mu         sync.RWMutex
	mode       string
	unlockMu   sync.Mutex
	failures   int
	retryAfter time.Time
}

var ErrUnlockRateLimited = errors.New("vault unlock temporarily rate limited")

func NewVaultController(file *vault.KeyFile, gate *Gate, state vault.KeyState) *VaultController {
	mode := "lock-on-restart"
	if state == vault.Automatic {
		mode = "automatic"
	}
	return &VaultController{file: file, gate: gate, mode: mode}
}

func (c *VaultController) Status() VaultStatus {
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	return VaultStatus{Mode: mode, Locked: c.gate.IsLocked()}
}

func (c *VaultController) LockOnRestart(passphrase string) error {
	master, err := c.gate.MasterKey()
	if err != nil {
		return err
	}
	defer clear(master)
	if err := c.file.SaveLocked(master, passphrase); err != nil {
		return err
	}
	c.mu.Lock()
	c.mode = "lock-on-restart"
	c.mu.Unlock()
	return nil
}

func (c *VaultController) Unlock(passphrase string) error {
	c.unlockMu.Lock()
	defer c.unlockMu.Unlock()
	if time.Now().Before(c.retryAfter) {
		return ErrUnlockRateLimited
	}
	master, state, err := c.file.Load(passphrase)
	if err != nil {
		if errors.Is(err, vault.ErrInvalidPassphrase) {
			c.failures++
			delay := time.Duration(1<<min(c.failures-1, 5)) * time.Second
			c.retryAfter = time.Now().Add(delay)
		}
		return err
	}
	defer clear(master)
	if err := c.gate.Unlock(master); err != nil {
		return err
	}
	c.mu.Lock()
	if state == vault.Automatic {
		c.mode = "automatic"
	} else {
		c.mode = "lock-on-restart"
	}
	c.mu.Unlock()
	c.failures = 0
	c.retryAfter = time.Time{}
	return nil
}

func (c *VaultController) Automatic() error {
	master, err := c.gate.MasterKey()
	if err != nil {
		return err
	}
	defer clear(master)
	if err := c.file.SaveAutomatic(master); err != nil {
		return err
	}
	c.mu.Lock()
	c.mode = "automatic"
	c.mu.Unlock()
	return nil
}
