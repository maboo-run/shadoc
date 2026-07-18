package main

import (
	"errors"
	"os"
	"path/filepath"
)

func newManagedApplicationBinary(dataDir string) string {
	return filepath.Join(filepath.Clean(dataDir), "app", "shadoc")
}

func existingManagedApplicationBinary(dataDir string) string {
	current := newManagedApplicationBinary(dataDir)
	if info, err := os.Stat(current); err == nil && info.Mode().IsRegular() {
		return current
	}
	legacy := filepath.Join(filepath.Clean(dataDir), "app", "restic-control")
	if info, err := os.Stat(legacy); err == nil && info.Mode().IsRegular() {
		return legacy
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return current
	}
	return current
}
