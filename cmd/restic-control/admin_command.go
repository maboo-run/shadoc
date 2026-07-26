package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	flags := flag.NewFlagSet("reset-admin-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	_ = flags.Bool("system", false, "use the Linux root system service data")
	if err := flags.Parse(args[1:]); err != nil {
		return true, err
	}
	if flags.NArg() != 0 {
		return true, errors.New("reset-admin-password does not accept positional arguments")
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

func adminCommandScope(args []string) (serviceScope, error) {
	if len(args) == 0 || args[0] != "reset-admin-password" {
		return "", errors.New("administrator command is required")
	}
	flags := flag.NewFlagSet("reset-admin-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	system := flags.Bool("system", false, "use the Linux root system service data")
	if err := flags.Parse(args[1:]); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", errors.New("reset-admin-password does not accept positional arguments")
	}
	if *system {
		return systemServiceScope, nil
	}
	return userServiceScope, nil
}
