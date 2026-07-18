//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/appinstall"
)

func TestOfficialApplicationReleaseMetadataAndArtifactAreVerifiable(t *testing.T) {
	if os.Getenv("SHADOC_RELEASE_VERIFY") != "1" && os.Getenv("RESTIC_CONTROL_RELEASE_VERIFY") != "1" {
		t.Skip("official application release download runs only in the release gate")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	source := appinstall.NewGitHubRelease(&http.Client{Timeout: 2 * time.Minute}, appinstall.OfficialReleasesAPI, runtime.GOOS, runtime.GOARCH)
	info, err := source.Latest(ctx)
	if err != nil {
		t.Fatalf("load latest official application release: %v", err)
	}
	if !appinstall.ValidReleaseVersion(info.Version) || !info.Compatible || info.Platform != runtime.GOOS+"_"+runtime.GOARCH {
		t.Fatalf("latest release is not usable on this platform: %+v", info)
	}
	artifact, err := source.Fetch(ctx, info.Version)
	if err != nil {
		t.Fatalf("download and verify latest official application release: %v", err)
	}
	if len(artifact.Binary) == 0 || sha256.Sum256(artifact.Binary) != artifact.SHA256 {
		t.Fatal("official application release artifact did not match its published checksum")
	}
	recordCheck("official-application-release", "passed", info.Version+" "+info.Platform)
}
