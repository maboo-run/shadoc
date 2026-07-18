package installer

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const OfficialResticReleasesAPI = "https://api.github.com/repos/restic/restic/releases"

type Restic struct {
	client                    *http.Client
	api, target, goos, goarch string
	releaseMu                 sync.Mutex
	releaseCache              []release
	releaseCachedAt           time.Time
}

const releaseCatalogCacheTTL = 15 * time.Minute

// Artifact is an official Restic executable whose compressed release asset
// has been verified against the release SHA256SUMS file.
type Artifact struct {
	Version string
	GOOS    string
	GOARCH  string
	Content []byte
	SHA256  string
}
type release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

var stableResticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func NewRestic(client *http.Client, api, target, goos, goarch string) *Restic {
	if client == nil {
		client = http.DefaultClient
	}
	return &Restic{client: client, api: api, target: target, goos: goos, goarch: goarch}
}
func (r *Restic) Versions(ctx context.Context) ([]string, error) {
	items, err := r.releases(ctx)
	if err != nil {
		return nil, err
	}
	var versions []string
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.Draft || item.Prerelease {
			continue
		}
		version, ok := normalizeStableVersion(item.TagName)
		if ok {
			if _, exists := seen[version]; exists {
				continue
			}
			seen[version] = struct{}{}
			versions = append(versions, version)
		}
	}
	return versions, nil
}
func (r *Restic) Install(ctx context.Context, version string) (string, error) {
	artifact, err := r.Resolve(ctx, version, r.goos, r.goarch)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(r.target), 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(r.target), "restic-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o755); err != nil {
		temp.Close()
		return "", err
	}
	_, copyErr := io.Copy(temp, bytes.NewReader(artifact.Content))
	closeErr := temp.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(tempPath, r.target); err != nil {
		return "", err
	}
	return r.target, nil
}

// Resolve downloads and verifies an official stable release for a concrete
// platform without writing it to the local filesystem. Callers may transfer
// the verified bytes only to their own fixed installation target.
func (r *Restic) Resolve(ctx context.Context, version, goos, goarch string) (Artifact, error) {
	if !stableResticVersionPattern.MatchString(version) {
		return Artifact{}, errors.New("valid restic version is required")
	}
	if goos != "linux" && goos != "darwin" || goarch != "amd64" && goarch != "arm64" {
		return Artifact{}, errors.New("unsupported restic artifact platform")
	}
	items, err := r.releases(ctx)
	if err != nil {
		return Artifact{}, err
	}
	filename := "restic_" + version + "_" + goos + "_" + goarch + ".bz2"
	var binaryURL, sumsURL string
	for _, item := range items {
		candidate, ok := normalizeStableVersion(item.TagName)
		if !ok || candidate != version || item.Draft || item.Prerelease {
			continue
		}
		for _, asset := range item.Assets {
			if asset.Name == filename {
				binaryURL = asset.URL
			}
			if asset.Name == "SHA256SUMS" {
				sumsURL = asset.URL
			}
		}
	}
	if binaryURL == "" || sumsURL == "" {
		return Artifact{}, errors.New("official release asset is unavailable")
	}
	compressed, err := r.download(ctx, binaryURL, 128<<20)
	if err != nil {
		return Artifact{}, err
	}
	sums, err := r.download(ctx, sumsURL, 4<<20)
	if err != nil {
		return Artifact{}, err
	}
	want := ""
	scanner := bufio.NewScanner(strings.NewReader(string(sums)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == filename {
			want = fields[0]
			break
		}
	}
	digest := sha256.Sum256(compressed)
	if want == "" || !strings.EqualFold(want, hex.EncodeToString(digest[:])) {
		return Artifact{}, errors.New("restic release checksum mismatch")
	}
	content, err := io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(compressed)), 256<<20+1))
	if err != nil {
		return Artifact{}, err
	}
	if len(content) == 0 || len(content) > 256<<20 {
		return Artifact{}, errors.New("decompressed restic executable has an invalid size")
	}
	return Artifact{Version: version, GOOS: goos, GOARCH: goarch, Content: content, SHA256: hex.EncodeToString(digest[:])}, nil
}

func normalizeStableVersion(tag string) (string, bool) {
	version := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	return version, stableResticVersionPattern.MatchString(version)
}
func (r *Restic) releases(ctx context.Context) ([]release, error) {
	r.releaseMu.Lock()
	if len(r.releaseCache) != 0 && time.Since(r.releaseCachedAt) < releaseCatalogCacheTTL {
		items := append([]release(nil), r.releaseCache...)
		r.releaseMu.Unlock()
		return items, nil
	}
	r.releaseMu.Unlock()
	value, err := r.download(ctx, r.api, 8<<20)
	if err != nil {
		return nil, err
	}
	var items []release
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, err
	}
	r.releaseMu.Lock()
	r.releaseCache = append([]release(nil), items...)
	r.releaseCachedAt = time.Now()
	r.releaseMu.Unlock()
	return items, nil
}
func (r *Restic) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "shadoc")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download returned %d", response.StatusCode)
	}
	value, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if int64(len(value)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return value, err
}
