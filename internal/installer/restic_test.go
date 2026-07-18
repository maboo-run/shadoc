package installer

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResticInstallerListsStableVersionsAndVerifiesOfficialAsset(t *testing.T) {
	binary := []byte("restic-test-binary")
	compressed := bzip2EncodeForTest(t, binary)
	digest := sha256.Sum256(compressed)
	checksum := hex.EncodeToString(digest[:])
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v0.18.0","draft":false,"prerelease":false,"assets":[{"name":"restic_0.18.0_linux_amd64.bz2","browser_download_url":"%s/asset"},{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]},{"tag_name":"v0.18.0","draft":false,"prerelease":false},{"tag_name":"v0.19.0-rc1","prerelease":true},{"tag_name":"latest","draft":false,"prerelease":false}]`, server.URL, server.URL)
		case "/asset":
			_, _ = w.Write(compressed)
		case "/sums":
			fmt.Fprintf(w, "%s  restic_0.18.0_linux_amd64.bz2\n", checksum)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "bin", "restic")
	service := NewRestic(server.Client(), server.URL+"/releases", target, "linux", "amd64")
	versions, err := service.Versions(context.Background())
	if err != nil || len(versions) != 1 || versions[0] != "0.18.0" {
		t.Fatalf("versions=%v err=%v", versions, err)
	}
	path, err := service.Install(context.Background(), "0.18.0")
	if err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(value, binary) {
		t.Fatalf("binary=%q err=%v", value, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	artifact, err := service.Resolve(context.Background(), "0.18.0", "linux", "amd64")
	if err != nil || artifact.Version != "0.18.0" || artifact.GOOS != "linux" || artifact.GOARCH != "amd64" || !bytes.Equal(artifact.Content, binary) || artifact.SHA256 != checksum {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestResticReleaseCatalogReusesSuccessfulResult(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		fmt.Fprint(w, `[{"tag_name":"v0.19.1","draft":false,"prerelease":false}]`)
	}))
	defer server.Close()
	service := NewRestic(server.Client(), server.URL, filepath.Join(t.TempDir(), "restic"), "linux", "amd64")
	for range 2 {
		versions, err := service.Versions(t.Context())
		if err != nil || len(versions) != 1 || versions[0] != "0.19.1" {
			t.Fatalf("versions=%v err=%v", versions, err)
		}
	}
	if requests != 1 {
		t.Fatalf("release catalog requests=%d want=1", requests)
	}
}

func TestResticArtifactRejectsUnsupportedPlatformBeforeDownloading(t *testing.T) {
	service := NewRestic(nil, "https://example.invalid/releases", filepath.Join(t.TempDir(), "restic"), "linux", "amd64")
	if _, err := service.Resolve(context.Background(), "0.18.0", "windows", "amd64"); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}

func TestResticArtifactRejectsNonStableVersion(t *testing.T) {
	service := NewRestic(nil, "https://example.invalid/releases", filepath.Join(t.TempDir(), "restic"), "linux", "amd64")
	for _, version := range []string{"latest", "0.18.0-rc1", "0.18.0/escape", "v0.18.0"} {
		if _, err := service.Resolve(context.Background(), version, "linux", "amd64"); err == nil {
			t.Fatalf("version %q accepted", version)
		}
	}
}

func TestChecksumFailureLeavesExistingManagedBinaryUntouched(t *testing.T) {
	compressed := bzip2EncodeForTest(t, []byte("restic-test-binary"))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v0.18.0","assets":[{"name":"restic_0.18.0_linux_amd64.bz2","browser_download_url":"%s/asset"},{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}]`, server.URL, server.URL)
		case "/asset":
			_, _ = w.Write(compressed)
		case "/sums":
			fmt.Fprintln(w, "0000  restic_0.18.0_linux_amd64.bz2")
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "bin", "restic")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewRestic(server.Client(), server.URL+"/releases", target, "linux", "amd64")
	if _, err := service.Install(context.Background(), "0.18.0"); err == nil {
		t.Fatal("checksum failure accepted")
	}
	value, _ := os.ReadFile(target)
	if string(value) != "known-good" {
		t.Fatalf("existing binary changed: %q", value)
	}
}

// Go's standard library only decodes bzip2. This fixture is a checked-in encoding
// of "restic-test-binary" produced by bzip2 and is decoded here to guard fixture drift.
func bzip2EncodeForTest(t *testing.T, want []byte) []byte {
	t.Helper()
	encoded := []byte{0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26, 0x53, 0x59, 0xc0, 0xdb, 0xee, 0x5e, 0x00, 0x00, 0x05, 0x11, 0x80, 0x00, 0x02, 0x3a, 0x21, 0x1c, 0x20, 0x20, 0x00, 0x31, 0x00, 0xd3, 0x4d, 0x04, 0x34, 0x0c, 0x86, 0xd1, 0x93, 0x48, 0x19, 0xe5, 0xad, 0xc8, 0x2f, 0x17, 0x72, 0x45, 0x38, 0x50, 0x90, 0xc0, 0xdb, 0xee, 0x5e}
	decoded, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(encoded)))
	if err != nil || !bytes.Equal(decoded, want) {
		t.Fatalf("bad fixture decoded=%q err=%v", decoded, err)
	}
	return encoded
}
