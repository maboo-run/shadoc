//go:build !linux

package servicemigration

import "errors"

type FileLock struct{}

func AcquireFileLock(string, int) (*FileLock, error) {
	return nil, errors.New("root migration lock is supported only on Linux")
}

func (*FileLock) Close() error {
	return nil
}
