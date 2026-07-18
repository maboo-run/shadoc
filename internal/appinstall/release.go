package appinstall

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxReleaseAssetSize     = 256 << 20
	maxReleaseMetadataSize  = 1 << 20
	maxChecksumManifestSize = 8 << 20
	OfficialReleasesAPI     = "https://api.github.com/repos/maboo-run/shadoc/releases"
)

var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
var stableReleaseVersionPattern = regexp.MustCompile(`^v?(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

func ValidReleaseVersion(version string) bool { return releaseVersionPattern.MatchString(version) }

// IsUpgradeAvailable only permits a forward move between stable semantic
// versions. Unknown, development, prerelease, equal, and older candidates are
// deliberately not treated as page-upgrade targets.
func IsUpgradeAvailable(current, candidate string) bool {
	currentVersion, currentOK := stableVersionParts(current)
	candidateVersion, candidateOK := stableVersionParts(candidate)
	if !currentOK || !candidateOK {
		return false
	}
	for index := range currentVersion {
		if candidateVersion[index] != currentVersion[index] {
			return candidateVersion[index] > currentVersion[index]
		}
	}
	return false
}

func stableVersionParts(version string) ([3]uint64, bool) {
	if !stableReleaseVersionPattern.MatchString(version) {
		return [3]uint64{}, false
	}
	fields := strings.Split(strings.TrimPrefix(version, "v"), ".")
	var parts [3]uint64
	for index, field := range fields {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return [3]uint64{}, false
		}
		parts[index] = value
	}
	return parts, true
}

type GitHubRelease struct {
	client *http.Client
	apiURL string
	asset  string
}

type ReleaseInfo struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	Summary     string    `json:"summary"`
	Compatible  bool      `json:"compatible"`
	Platform    string    `json:"platform"`
}

type releaseMetadata struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func NewGitHubRelease(client *http.Client, apiURL, goos, goarch string) *GitHubRelease {
	if client == nil {
		client = http.DefaultClient
	}
	return &GitHubRelease{
		client: client,
		apiURL: strings.TrimRight(apiURL, "/"),
		asset:  "shadoc_" + goos + "_" + goarch,
	}
}

func (g *GitHubRelease) Fetch(ctx context.Context, version string) (Artifact, error) {
	release, err := g.release(ctx, version)
	if err != nil {
		return Artifact{}, err
	}
	var binaryURL, checksumsURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case g.asset:
			binaryURL = asset.URL
		case "SHA256SUMS":
			checksumsURL = asset.URL
		}
	}
	if binaryURL == "" || checksumsURL == "" {
		return Artifact{}, fmt.Errorf("release does not contain %s and SHA256SUMS", g.asset)
	}
	checksums, err := g.download(ctx, checksumsURL, maxChecksumManifestSize)
	if err != nil {
		return Artifact{}, fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksums, g.asset)
	if err != nil {
		return Artifact{}, err
	}
	binary, err := g.download(ctx, binaryURL, maxReleaseAssetSize)
	if err != nil {
		return Artifact{}, fmt.Errorf("download binary: %w", err)
	}
	if actual := sha256.Sum256(binary); actual != expected {
		return Artifact{}, errors.New("downloaded release checksum mismatch")
	}
	return Artifact{Binary: binary, SHA256: expected}, nil
}

func (g *GitHubRelease) Latest(ctx context.Context) (ReleaseInfo, error) {
	release, err := g.release(ctx, "latest")
	if err != nil {
		return ReleaseInfo{}, err
	}
	if release.Draft || release.Prerelease || !stableReleaseVersionPattern.MatchString(release.TagName) || release.PublishedAt.IsZero() {
		return ReleaseInfo{}, errors.New("latest release metadata is not a stable semantic version")
	}
	assets := make(map[string]bool, len(release.Assets))
	for _, asset := range release.Assets {
		assets[asset.Name] = asset.URL != ""
	}
	return ReleaseInfo{
		Version: release.TagName, PublishedAt: release.PublishedAt.UTC(), Summary: boundedReleaseSummary(release.Name, release.Body),
		Compatible: assets[g.asset] && assets["SHA256SUMS"], Platform: strings.TrimPrefix(g.asset, "shadoc_"),
	}, nil
}

func (g *GitHubRelease) release(ctx context.Context, version string) (releaseMetadata, error) {
	endpoint := g.apiURL + "/latest"
	if version != "" && version != "latest" {
		if !releaseVersionPattern.MatchString(version) {
			return releaseMetadata{}, errors.New("invalid release version")
		}
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		endpoint = g.apiURL + "/tags/" + version
	}
	encoded, err := g.download(ctx, endpoint, maxReleaseMetadataSize)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("load release metadata: %w", err)
	}
	var release releaseMetadata
	if err := json.Unmarshal(encoded, &release); err != nil {
		return releaseMetadata{}, fmt.Errorf("decode release metadata: %w", err)
	}
	return release, nil
}

func boundedReleaseSummary(name, body string) string {
	value := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(name), strings.TrimSpace(body)}, "\n\n"))
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= ' ' && character != 0x7f {
			return character
		}
		return -1
	}, value)
	const maximumBytes = 4000
	if len(value) > maximumBytes {
		value = value[:maximumBytes]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (g *GitHubRelease) download(ctx context.Context, url string, maximum int64) ([]byte, error) {
	if maximum < 1 || maximum > maxReleaseAssetSize {
		return nil, errors.New("release download size limit is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("release response exceeds size limit")
	}
	return content, nil
}

func checksumFor(manifest []byte, asset string) ([sha256.Size]byte, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return [sha256.Size]byte{}, fmt.Errorf("invalid checksum for %s", asset)
		}
		var sum [sha256.Size]byte
		copy(sum[:], decoded)
		return sum, nil
	}
	if err := scanner.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	return [sha256.Size]byte{}, fmt.Errorf("checksum for %s not found", asset)
}
