//go:build !windows

package main

import "context"

func runEntry(run func(context.Context) error) error {
	return run(context.Background())
}
