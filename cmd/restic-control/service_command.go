package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

type backgroundService interface {
	Start(string, []string) error
	Stop() error
	Restart() error
	Status() (string, error)
}

type serviceLaunchConfig struct {
	DataDir string
	Listen  string
}

type serviceLaunchConfigLoader func() (serviceLaunchConfig, error)

type serviceScope string

const (
	userServiceScope   serviceScope = "user"
	systemServiceScope serviceScope = "system"
)

// handleVersionCommand stays before every command that loads configuration or
// opens SQLite. It gives release verification a side-effect-free way to read
// the build version from a controller binary.
func handleVersionCommand(args []string, stdout io.Writer, version string) (bool, error) {
	if len(args) == 0 || args[0] != "--version" && args[0] != "version" {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("version does not accept arguments")
	}
	_, err := fmt.Fprintln(stdout, version)
	return true, err
}

func handleServiceCommand(args []string, stdout io.Writer, executable string, service backgroundService, loadConfig serviceLaunchConfigLoader) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "start":
		flags := flag.NewFlagSet("start", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		port := flags.Int("port", 8585, "management port")
		system := flags.Bool("system", false, "manage the Linux root system service")
		if err := flags.Parse(args[1:]); err != nil {
			return true, err
		}
		if flags.NArg() != 0 {
			return true, errors.New("start does not accept positional arguments")
		}
		if *port < 1 || *port > 65535 {
			return true, errors.New("port must be between 1 and 65535")
		}
		portProvided := false
		flags.Visit(func(current *flag.Flag) {
			if current.Name == "port" {
				portProvided = true
			}
		})
		if loadConfig == nil {
			return true, errors.New("service launch configuration is unavailable")
		}
		launchConfig, err := loadConfig()
		if err != nil {
			return true, err
		}
		launchConfig.DataDir, err = filepath.Abs(launchConfig.DataDir)
		if err != nil {
			return true, fmt.Errorf("resolve Shadoc data directory: %w", err)
		}
		if portProvided {
			launchConfig.Listen, err = listenWithPort(launchConfig.Listen, *port)
			if err != nil {
				return true, fmt.Errorf("apply Shadoc management port: %w", err)
			}
		}
		serviceArguments := []string{"serve"}
		if *system {
			serviceArguments = append(serviceArguments, "--service-scope", "system")
		}
		serviceArguments = append(serviceArguments, "--listen", launchConfig.Listen, "--data-dir", launchConfig.DataDir)
		if err := service.Start(executable, serviceArguments); err != nil {
			return true, err
		}
		if portProvided {
			_, _ = fmt.Fprintf(stdout, "Shadoc started in the background on port %d\n", *port)
		} else {
			_, _ = fmt.Fprintln(stdout, "Shadoc started in the background")
		}
		token, err := readSetupTokenFile(launchConfig.DataDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, fmt.Errorf("read LAN initialization token: %w", err)
		}
		if token != "" {
			_, _ = fmt.Fprintf(stdout, "LAN initialization token: %s\n", token)
		}
		return true, nil
	case "stop":
		if _, err := parseSystemLifecycleFlags("stop", args[1:]); err != nil {
			return true, err
		}
		if err := service.Stop(); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc stopped")
		return true, nil
	case "restart":
		if _, err := parseSystemLifecycleFlags("restart", args[1:]); err != nil {
			return true, err
		}
		if err := service.Restart(); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc restarted")
		return true, nil
	case "status":
		if _, err := parseSystemLifecycleFlags("status", args[1:]); err != nil {
			return true, err
		}
		status, err := service.Status()
		if err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Shadoc is %s\n", status)
		return true, nil
	case "help", "--help", "-h":
		if len(args) != 1 {
			return true, errors.New("help does not accept arguments")
		}
		writeShadocHelp(stdout)
		return true, nil
	case "serve", "install-service", "uninstall-service":
		return false, nil
	default:
		return true, fmt.Errorf("unknown Shadoc command %q; run 'shadoc help'", args[0])
	}
}

func serviceCommandScope(args []string) (serviceScope, error) {
	if len(args) == 0 {
		return "", errors.New("service command is required")
	}
	switch args[0] {
	case "start":
		flags := flag.NewFlagSet("start", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		_ = flags.Int("port", 8585, "management port")
		system := flags.Bool("system", false, "manage the Linux root system service")
		if err := flags.Parse(args[1:]); err != nil {
			return "", err
		}
		if flags.NArg() != 0 {
			return "", errors.New("start does not accept positional arguments")
		}
		if *system {
			return systemServiceScope, nil
		}
		return userServiceScope, nil
	case "stop", "restart", "status":
		system, err := parseSystemLifecycleFlags(args[0], args[1:])
		if err != nil {
			return "", err
		}
		if system {
			return systemServiceScope, nil
		}
		return userServiceScope, nil
	default:
		return "", fmt.Errorf("%s is not a service lifecycle command", args[0])
	}
}

func backgroundCommandScope(args []string) (serviceScope, error) {
	scope, err := serviceCommandScope(args)
	if err == nil {
		return scope, nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "start", "stop", "restart", "status":
			return "", err
		}
	}
	return userServiceScope, nil
}

func parseSystemLifecycleFlags(command string, args []string) (bool, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	system := flags.Bool("system", false, "manage the Linux root system service")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		return false, fmt.Errorf("%s does not accept positional arguments", command)
	}
	return *system, nil
}

func writeShadocHelp(stdout io.Writer) {
	_, _ = fmt.Fprintln(stdout, `影刻 · Shadoc

Usage:
  shadoc start [--port PORT] [--system]
                              Start the control service in the background
  shadoc stop [--system]      Stop the background control service
  shadoc restart [--system]   Restart the background control service
  shadoc status [--system]    Show the background service status
  sudo shadoc migrate-to-root Move a Linux user install to the root system service,
                              verifying it offline, then delete the original service and data
  shadoc update-app [--version x.y.z] [--system]
                              Download, verify, and install an official release
  shadoc uninstall-app [--system]
                              Remove the service and application binary
  shadoc reset-admin-password [--system]
                              Reset the local administrator password
  shadoc --version            Print the controller build version
  shadoc help                 Show this help`)
}

type serveOptions struct {
	Listen       string
	DataDir      string
	Port         int
	PortProvided bool
	ServiceScope serviceScope
}

func parseServeCommand(args []string) (serveOptions, bool, error) {
	if len(args) == 0 || args[0] != "serve" {
		return serveOptions{}, false, nil
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	port := flags.Int("port", 8585, "management port")
	listen := flags.String("listen", "", "management listen address")
	dataDir := flags.String("data-dir", "", "application data directory")
	scope := flags.String("service-scope", string(userServiceScope), "native service scope")
	if err := flags.Parse(args[1:]); err != nil {
		return serveOptions{}, true, err
	}
	if flags.NArg() != 0 {
		return serveOptions{}, true, errors.New("serve does not accept positional arguments")
	}
	if *port < 1 || *port > 65535 {
		return serveOptions{}, true, errors.New("port must be between 1 and 65535")
	}
	if *listen != "" {
		if _, _, err := net.SplitHostPort(*listen); err != nil {
			return serveOptions{}, true, fmt.Errorf("invalid serve listen address: %w", err)
		}
	}
	if *dataDir != "" && !filepath.IsAbs(*dataDir) {
		return serveOptions{}, true, errors.New("serve data directory must be absolute")
	}
	if *scope != string(userServiceScope) && *scope != string(systemServiceScope) {
		return serveOptions{}, true, errors.New("serve service scope must be user or system")
	}
	options := serveOptions{Listen: *listen, DataDir: *dataDir, Port: *port, ServiceScope: serviceScope(*scope)}
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "port" {
			options.PortProvided = true
		}
	})
	return options, true, nil
}

func listenWithPort(listen string, port int) (string, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
