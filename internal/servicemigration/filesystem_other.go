//go:build !linux

package servicemigration

import "errors"

func (m *FilesystemMover) Stage(string, string, string, int) error {
	return errors.New("root service data migration is supported only on Linux")
}

func (m *FilesystemMover) Commit(string, string) error {
	return errors.New("root service data migration is supported only on Linux")
}

func (m *FilesystemMover) Remove(string, int) error {
	return errors.New("root service data migration is supported only on Linux")
}

func (m *FilesystemMover) Exists(string) (bool, error) {
	return false, errors.New("root service data migration is supported only on Linux")
}
