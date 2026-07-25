package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareSetupTokenPersistsUntilConsumed(t *testing.T) {
	dataDir := t.TempDir()
	firstRandom := bytes.NewReader(bytes.Repeat([]byte{0x31}, setupTokenRandomBytes))
	token, err := prepareSetupToken(dataDir, "0.0.0.0:8585", false, firstRandom)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("LAN setup token was not created")
	}
	path := filepath.Join(dataDir, setupTokenFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("setup token permissions=%o", info.Mode().Perm())
	}

	reused, err := prepareSetupToken(dataDir, "0.0.0.0:8585", false, bytes.NewReader(bytes.Repeat([]byte{0x72}, setupTokenRandomBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if reused != token {
		t.Fatalf("setup token changed across restart: first=%q second=%q", token, reused)
	}

	if err := consumeSetupTokenFile(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup token still exists: %v", err)
	}
}

func TestPrepareSetupTokenRemovesStaleFileWhenNotRequired(t *testing.T) {
	for _, test := range []struct {
		name        string
		listen      string
		initialized bool
	}{
		{name: "loopback", listen: "127.0.0.1:8585"},
		{name: "initialized", listen: "0.0.0.0:8585", initialized: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, setupTokenFilename)
			if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
				t.Fatal(err)
			}
			token, err := prepareSetupToken(dataDir, test.listen, test.initialized, bytes.NewReader(bytes.Repeat([]byte{0x31}, setupTokenRandomBytes)))
			if err != nil {
				t.Fatal(err)
			}
			if token != "" {
				t.Fatalf("unexpected token %q", token)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale setup token still exists: %v", err)
			}
		})
	}
}
