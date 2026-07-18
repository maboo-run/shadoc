package appinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubReleaseDownloadsSelectedPlatformAndVerifiesPublishedChecksum(t *testing.T) {
	binary := []byte("release-binary")
	sum := sha256.Sum256(binary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/tags/v1.2.3":
			fmt.Fprintf(w, `{"assets":[{"name":"shadoc_linux_amd64","browser_download_url":%q},{"name":"SHA256SUMS","browser_download_url":%q}]}`,
				serverURL(r)+"/asset", serverURL(r)+"/checksums")
		case "/asset":
			_, _ = w.Write(binary)
		case "/checksums":
			fmt.Fprintf(w, "%s  shadoc_linux_amd64\n", hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := NewGitHubRelease(server.Client(), server.URL+"/releases", "linux", "amd64")
	artifact, err := source.Fetch(context.Background(), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if string(artifact.Binary) != string(binary) || artifact.SHA256 != sum {
		t.Fatalf("artifact=%q checksum=%x", artifact.Binary, artifact.SHA256)
	}
}

func TestGitHubReleaseReportsLatestStableCompatibleReleaseWithoutAssetURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v1.2.3","name":"Stable 1.2.3","body":"Fixes\u0000 and compatibility notes %s","published_at":"2026-07-15T08:00:00Z","draft":false,"prerelease":false,"assets":[{"name":"shadoc_linux_amd64","browser_download_url":"https://private.example/binary"},{"name":"SHA256SUMS","browser_download_url":"https://private.example/sums"}]}`, strings.Repeat("x", 5000))
	}))
	defer server.Close()

	source := NewGitHubRelease(server.Client(), server.URL+"/releases", "linux", "amd64")
	info, err := source.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "v1.2.3" || !info.Compatible || info.Platform != "linux_amd64" || len(info.Summary) > 4000 || strings.ContainsRune(info.Summary, '\x00') || strings.Contains(info.Summary, "private.example") {
		t.Fatalf("info=%+v", info)
	}
}

func TestGitHubReleaseRejectsPrereleaseShapedTagEvenWhenMetadataFlagIsWrong(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3-rc.1","published_at":"2026-07-15T08:00:00Z","draft":false,"prerelease":false,"assets":[]}`)
	}))
	defer server.Close()
	if _, err := NewGitHubRelease(server.Client(), server.URL, "linux", "amd64").Latest(t.Context()); err == nil {
		t.Fatal("prerelease-shaped tag was accepted as the latest stable release")
	}
}

func TestIsUpgradeAvailableRequiresANewerStableSemanticVersion(t *testing.T) {
	tests := []struct {
		current   string
		candidate string
		want      bool
	}{
		{current: "v1.2.3", candidate: "v1.2.4", want: true},
		{current: "1.2.9", candidate: "v1.3.0", want: true},
		{current: "v1.9.9", candidate: "v2.0.0", want: true},
		{current: "v1.2.3", candidate: "v1.2.3", want: false},
		{current: "v2.0.0", candidate: "v1.9.9", want: false},
		{current: "development", candidate: "v1.2.3", want: false},
		{current: "v1.2.3", candidate: "v1.3.0-rc.1", want: false},
		{current: "v1.2.3", candidate: "v01.3.0", want: false},
		{current: "v18446744073709551616.0.0", candidate: "v2.0.0", want: false},
	}
	for _, test := range tests {
		if got := IsUpgradeAvailable(test.current, test.candidate); got != test.want {
			t.Errorf("IsUpgradeAvailable(%q, %q)=%t want %t", test.current, test.candidate, got, test.want)
		}
	}
}

func TestGitHubReleaseRejectsMissingExactChecksumEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			fmt.Fprintf(w, `{"assets":[{"name":"shadoc_darwin_arm64","browser_download_url":%q},{"name":"SHA256SUMS","browser_download_url":%q}]}`,
				serverURL(r)+"/asset", serverURL(r)+"/checksums")
		case "/asset":
			_, _ = w.Write([]byte("release-binary"))
		case "/checksums":
			_, _ = w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  some-other-file\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := NewGitHubRelease(server.Client(), server.URL+"/releases", "darwin", "arm64")
	if _, err := source.Fetch(context.Background(), ""); err == nil {
		t.Fatal("missing exact checksum accepted")
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
