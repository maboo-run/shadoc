package main

import (
	"path/filepath"
	"strings"
)

const (
	windowsServiceName       = "shadoc-agent"
	legacyWindowsServiceName = "restic-control-agent"
)

func windowsServiceNameForExecutable(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
	if base == legacyWindowsServiceName || parent == legacyWindowsServiceName {
		return legacyWindowsServiceName
	}
	return windowsServiceName
}
