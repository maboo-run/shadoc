//go:build !linux

package servicemigration

func validateMigratedMetadata(string, bool) error {
	return nil
}
