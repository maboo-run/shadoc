//go:build !windows

package repositorycapacity

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func probeLocalCapacity(path string) (Capacity, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Capacity{}, fmt.Errorf("read local repository capacity: %w", err)
	}
	return Capacity{TotalBytes: stat.Blocks * uint64(stat.Bsize), AvailableBytes: stat.Bavail * uint64(stat.Bsize)}, nil
}
