//go:build !linux

package serviceinstall

func validateAdditionalPathSecurity(string, bool) error {
	return nil
}

func sanitizeAdditionalFileSecurity(int) error {
	return nil
}
