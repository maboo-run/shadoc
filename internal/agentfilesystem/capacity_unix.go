//go:build !windows

package agentfilesystem

import (
	"fmt"

	"golang.org/x/sys/unix"
)

type filesystemCapacity struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

func probeCapacity(path string) (filesystemCapacity, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return filesystemCapacity{}, fmt.Errorf("read local repository capacity: %w", err)
	}
	return filesystemCapacity{TotalBytes: stat.Blocks * uint64(stat.Bsize), AvailableBytes: stat.Bavail * uint64(stat.Bsize)}, nil
}
