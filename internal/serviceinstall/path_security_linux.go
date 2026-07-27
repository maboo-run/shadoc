//go:build linux

package serviceinstall

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func validateAdditionalPathSecurity(path string, directory bool) error {
	attributes := []string{"system.posix_acl_access", "security.capability"}
	if directory {
		attributes = append(attributes, "system.posix_acl_default")
	}
	for _, attribute := range attributes {
		size, err := unix.Lgetxattr(path, attribute, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s metadata on %s: %w", attribute, path, err)
		}
		if size > 0 {
			return fmt.Errorf("%s has extended ACL or capability metadata", path)
		}
	}
	return nil
}

func sanitizeAdditionalFileSecurity(fd int) error {
	for _, attribute := range []string{"system.posix_acl_access", "security.capability"} {
		size, err := unix.Fgetxattr(fd, attribute, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect temporary service definition metadata: %w", err)
		}
		if size > 0 {
			if err := unix.Fremovexattr(fd, attribute); err != nil {
				return fmt.Errorf("remove temporary service definition metadata: %w", err)
			}
		}
	}
	return nil
}
