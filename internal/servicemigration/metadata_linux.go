//go:build linux

package servicemigration

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func validateMigratedMetadata(path string, directory bool) error {
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
			return err
		}
		if size > 0 {
			return fmt.Errorf("%s retained extended ACL or capability metadata", path)
		}
	}
	return nil
}
