package servicemigration

import (
	"errors"
	"os"
	"path/filepath"
)

type FilesystemMover struct {
	targetDataDir  string
	stagingDataDir string
	ownerUID       int
}

func NewFilesystemMover(targetDataDir, stagingDataDir string) (*FilesystemMover, error) {
	target := filepath.Clean(targetDataDir)
	staging := filepath.Clean(stagingDataDir)
	if !filepath.IsAbs(target) || !filepath.IsAbs(staging) || target == string(filepath.Separator) ||
		staging == string(filepath.Separator) || target == staging || filepath.Dir(target) != filepath.Dir(staging) {
		return nil, errors.New("filesystem migration requires distinct safe targets in one parent directory")
	}
	return &FilesystemMover{targetDataDir: target, stagingDataDir: staging, ownerUID: os.Geteuid()}, nil
}
