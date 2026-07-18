package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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

func handleServiceCommand(args []string, stdout io.Writer, executable string, service backgroundService, loadConfig serviceLaunchConfigLoader) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "start":
		flags := flag.NewFlagSet("start", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		port := flags.Int("port", 8585, "management port")
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
		serviceArguments := []string{"serve", "--listen", launchConfig.Listen, "--data-dir", launchConfig.DataDir}
		if err := service.Start(executable, serviceArguments); err != nil {
			return true, err
		}
		if portProvided {
			_, _ = fmt.Fprintf(stdout, "Shadoc started in the background on port %d\n", *port)
		} else {
			_, _ = fmt.Fprintln(stdout, "Shadoc started in the background")
		}
		return true, nil
	case "stop":
		if len(args) != 1 {
			return true, errors.New("stop does not accept arguments")
		}
		if err := service.Stop(); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc stopped")
		return true, nil
	case "restart":
		if len(args) != 1 {
			return true, errors.New("restart does not accept arguments")
		}
		if err := service.Restart(); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "Shadoc restarted")
		return true, nil
	case "status":
		if len(args) != 1 {
			return true, errors.New("status does not accept arguments")
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

func writeShadocHelp(stdout io.Writer) {
	_, _ = fmt.Fprintln(stdout, `影刻 · Shadoc

Usage:
  shadoc start [--port PORT]  Start the control service in the background
  shadoc stop                 Stop the background control service
  shadoc restart              Restart the background control service
  shadoc status               Show the background service status
  shadoc update-app [--version x.y.z]
                              Download, verify, and install an official release
  shadoc uninstall-app        Remove the service and application binary
  shadoc help                 Show this help`)
}

type serveOptions struct {
	Listen       string
	DataDir      string
	Port         int
	PortProvided bool
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
	options := serveOptions{Listen: *listen, DataDir: *dataDir, Port: *port}
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
