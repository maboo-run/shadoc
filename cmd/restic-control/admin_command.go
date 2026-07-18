package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

type passwordReader interface {
	ReadPassword(string) ([]byte, error)
}

type passwordResetter interface {
	ResetPassword(context.Context, string) error
}

type terminalPasswordReader struct{}

func (terminalPasswordReader) ReadPassword(prompt string) ([]byte, error) {
	_, _ = fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	return password, err
}

func handleAdminCommand(ctx context.Context, args []string, reader passwordReader, resetter passwordResetter) (bool, error) {
	if len(args) == 0 || args[0] != "reset-admin-password" {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("reset-admin-password does not accept arguments")
	}
	first, err := reader.ReadPassword("New administrator password: ")
	if err != nil {
		return true, err
	}
	defer clear(first)
	second, err := reader.ReadPassword("Repeat administrator password: ")
	if err != nil {
		return true, err
	}
	defer clear(second)
	if !bytes.Equal(first, second) {
		return true, errors.New("passwords do not match")
	}
	if err := resetter.ResetPassword(ctx, string(first)); err != nil {
		return true, err
	}
	return true, nil
}
