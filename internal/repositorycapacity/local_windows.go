//go:build windows

package repositorycapacity

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func probeLocalCapacity(path string) (Capacity, error) {
	value, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Capacity{}, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(value, &available, &total, &free); err != nil {
		return Capacity{}, fmt.Errorf("read local repository capacity: %w", err)
	}
	return Capacity{TotalBytes: total, AvailableBytes: available}, nil
}
