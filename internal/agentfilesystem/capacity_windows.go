//go:build windows

package agentfilesystem

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type filesystemCapacity struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

func probeCapacity(path string) (filesystemCapacity, error) {
	value, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return filesystemCapacity{}, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(value, &available, &total, &free); err != nil {
		return filesystemCapacity{}, fmt.Errorf("read local repository capacity: %w", err)
	}
	return filesystemCapacity{TotalBytes: total, AvailableBytes: available}, nil
}
