package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type artifact struct {
	Name  string         `json:"name"`
	Path  string         `json:"path"`
	Type  string         `json:"type"`
	Extra map[string]any `json:"extra"`
}

var requiredBinaries = map[string]bool{
	"shadoc_linux_amd64":             false,
	"shadoc_linux_arm64":             false,
	"shadoc_darwin_amd64":            false,
	"shadoc_darwin_arm64":            false,
	"shadoc-agent-linux-amd64":       false,
	"shadoc-agent-linux-arm64":       false,
	"shadoc-agent-darwin-amd64":      false,
	"shadoc-agent-darwin-arm64":      false,
	"shadoc-agent-windows-amd64.exe": false,
	"shadoc-agent-windows-arm64.exe": false,
}

func main() {
	if err := prepareReleaseArtifacts(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepareReleaseArtifacts() error {
	repositoryRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	distDirectory := filepath.Join(repositoryRoot, "dist")

	contents, err := os.ReadFile(filepath.Join(distDirectory, "artifacts.json"))
	if err != nil {
		return fmt.Errorf("read GoReleaser artifacts: %w", err)
	}
	var artifacts []artifact
	if err := json.Unmarshal(contents, &artifacts); err != nil {
		return fmt.Errorf("decode GoReleaser artifacts: %w", err)
	}

	seen := make(map[string]bool, len(requiredBinaries))
	for _, candidate := range artifacts {
		if candidate.Type != "Binary" || !isReleaseBinary(candidate) {
			continue
		}
		if _, required := requiredBinaries[candidate.Name]; !required {
			continue
		}
		if seen[candidate.Name] {
			return fmt.Errorf("duplicate GoReleaser artifact: %s", candidate.Name)
		}
		sourcePath, err := safeArtifactPath(repositoryRoot, distDirectory, candidate.Path)
		if err != nil {
			return fmt.Errorf("invalid path for %s: %w", candidate.Name, err)
		}
		if err := copyRegularFile(sourcePath, filepath.Join(distDirectory, candidate.Name)); err != nil {
			return fmt.Errorf("prepare %s: %w", candidate.Name, err)
		}
		seen[candidate.Name] = true
	}

	var missing []string
	for name := range requiredBinaries {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("GoReleaser output is missing required artifacts: %s", strings.Join(missing, ", "))
	}

	for source, destination := range map[string]string{
		filepath.Join(repositoryRoot, "scripts", "install.sh"): filepath.Join(distDirectory, "install.sh"),
		filepath.Join(repositoryRoot, "LICENSE"):               filepath.Join(distDirectory, "LICENSE"),
	} {
		if err := copyRegularFile(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func isReleaseBinary(candidate artifact) bool {
	id, _ := candidate.Extra["ID"].(string)
	return id == "shadoc-binaries" || id == "shadoc-agent-binaries"
}

func safeArtifactPath(repositoryRoot, distDirectory, artifactPath string) (string, error) {
	if artifactPath == "" || filepath.IsAbs(artifactPath) {
		return "", errors.New("path must be relative")
	}
	path := filepath.Clean(filepath.Join(repositoryRoot, artifactPath))
	relative, err := filepath.Rel(distDirectory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes dist directory")
	}
	return path, nil
}

func copyRegularFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", source)
	}

	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}
