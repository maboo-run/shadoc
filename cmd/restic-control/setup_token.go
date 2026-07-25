package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	setupTokenFilename    = "setup-token"
	setupTokenRandomBytes = 24
)

func prepareSetupToken(dataDir, listen string, initialized bool, random io.Reader) (string, error) {
	required, err := listenRequiresSetupToken(listen)
	if err != nil {
		return "", err
	}
	if initialized || !required {
		return "", consumeSetupTokenFile(dataDir)
	}
	token, err := readSetupTokenFile(dataDir)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if random == nil {
		return "", errors.New("setup token randomness is required")
	}
	raw := make([]byte, setupTokenRandomBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", fmt.Errorf("generate setup token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	if err := writeSetupTokenFile(dataDir, token); err != nil {
		return "", err
	}
	return token, nil
}

func listenRequiresSetupToken(listen string) (bool, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("parse setup token listen address: %w", err)
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback(), nil
}

func readSetupTokenFile(dataDir string) (string, error) {
	path := filepath.Join(filepath.Clean(dataDir), setupTokenFilename)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("setup token file is not regular")
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("setup token permissions must be 0600, got %o", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(content))
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != setupTokenRandomBytes {
		return "", errors.New("setup token file is invalid")
	}
	return token, nil
}

func writeSetupTokenFile(dataDir, token string) error {
	dataDir = filepath.Clean(dataDir)
	if dataDir == "." || dataDir == string(filepath.Separator) {
		return errors.New("safe setup token data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create setup token directory: %w", err)
	}
	temporary, err := os.CreateTemp(dataDir, ".setup-token-*")
	if err != nil {
		return fmt.Errorf("create setup token file: %w", err)
	}
	name := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(name)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("protect setup token file: %w", err)
	}
	if _, err := io.WriteString(temporary, token+"\n"); err != nil {
		cleanup()
		return fmt.Errorf("write setup token file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync setup token file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close setup token file: %w", err)
	}
	path := filepath.Join(dataDir, setupTokenFilename)
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("publish setup token file: %w", err)
	}
	return nil
}

func consumeSetupTokenFile(dataDir string) error {
	path := filepath.Join(filepath.Clean(dataDir), setupTokenFilename)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove setup token file: %w", err)
	}
	return nil
}
