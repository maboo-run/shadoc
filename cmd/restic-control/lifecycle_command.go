package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type appLifecycle interface {
	InstallCurrent(context.Context, string) error
	Update(context.Context, string) error
	Uninstall(bool) error
}

func handleLifecycleCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, lifecycle appLifecycle, current string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "install-app":
		flags := flag.NewFlagSet("install-app", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		if err := flags.Parse(args[1:]); err != nil {
			return true, err
		}
		if flags.NArg() != 0 {
			return true, errors.New("install-app does not accept positional arguments")
		}
		if err := lifecycle.InstallCurrent(ctx, current); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc installed and healthy")
		return true, nil
	case "update-app":
		flags := flag.NewFlagSet("update-app", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		version := flags.String("version", "latest", "release version")
		if err := flags.Parse(args[1:]); err != nil {
			return true, err
		}
		if flags.NArg() != 0 {
			return true, errors.New("update-app does not accept positional arguments")
		}
		if err := lifecycle.Update(ctx, *version); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Shadoc updated to %s and is healthy\n", *version)
		return true, nil
	case "uninstall-app":
		flags := flag.NewFlagSet("uninstall-app", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		removeData := flags.Bool("remove-data", false, "also permanently remove application data")
		if err := flags.Parse(args[1:]); err != nil {
			return true, err
		}
		if flags.NArg() != 0 {
			return true, errors.New("uninstall-app does not accept positional arguments")
		}
		if *removeData {
			_, _ = fmt.Fprintln(stdout, "This permanently deletes all application settings, encrypted secrets, logs, and task history. Type REMOVE to continue:")
			line, err := bufio.NewReader(stdin).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return true, err
			}
			if strings.TrimSpace(line) != "REMOVE" {
				return true, errors.New("data removal cancelled")
			}
		}
		if err := lifecycle.Uninstall(*removeData); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc uninstalled")
		return true, nil
	default:
		return false, nil
	}
}
