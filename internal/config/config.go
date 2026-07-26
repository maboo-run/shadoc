package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir      string
	DatabasePath string
	VaultKeyPath string
	Listen       string
}

const LinuxSystemDataDir = "/var/lib/shadoc"

func Load(getenv func(string) string) (Config, error) {
	return load(getenv, os.UserConfigDir)
}

func LoadSystem(getenv func(string) string, goos string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}
	if goos != "linux" {
		return Config{}, fmt.Errorf("system service configuration is unsupported on %s", goos)
	}
	if configured := compatibleValue(getenv, "SHADOC_DATA_DIR", "RESTIC_CONTROL_DATA_DIR"); configured != "" && filepath.Clean(configured) != LinuxSystemDataDir {
		return Config{}, fmt.Errorf("system service data directory must be %s", LinuxSystemDataDir)
	}
	return configForDataDir(getenv, LinuxSystemDataDir)
}

func load(getenv func(string) string, userConfigDir func() (string, error)) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}

	dataDir := compatibleValue(getenv, "SHADOC_DATA_DIR", "RESTIC_CONTROL_DATA_DIR")
	if dataDir == "" {
		base, err := userConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		dataDir = defaultDataDirectory(base)
	}
	return configForDataDir(getenv, dataDir)
}

func configForDataDir(getenv func(string) string, dataDir string) (Config, error) {
	dataDir = filepath.Clean(dataDir)
	listen := compatibleValue(getenv, "SHADOC_LISTEN", "RESTIC_CONTROL_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8585"
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return Config{}, fmt.Errorf("invalid listen address: %w", err)
	}
	databasePath := filepath.Join(dataDir, "shadoc.db")
	legacyDatabasePath := filepath.Join(dataDir, "restic-control.db")
	if _, err := os.Stat(databasePath); errors.Is(err, os.ErrNotExist) {
		if _, legacyErr := os.Stat(legacyDatabasePath); legacyErr == nil {
			databasePath = legacyDatabasePath
		}
	}
	return Config{
		DataDir:      dataDir,
		DatabasePath: databasePath,
		VaultKeyPath: filepath.Join(dataDir, "vault.key"),
		Listen:       listen,
	}, nil
}

func compatibleValue(getenv func(string) string, primary, legacy string) string {
	if value := getenv(primary); value != "" {
		return value
	}
	return getenv(legacy)
}

func defaultDataDirectory(base string) string {
	current := filepath.Join(base, "shadoc")
	legacy := filepath.Join(base, "restic-control")
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			return legacy
		}
	}
	return current
}
