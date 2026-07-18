package serviceinstall

import (
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func Install() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return InstallExecutable(executable)
}

// Manager adapts native user services to the application lifecycle manager.
type Manager struct{}

var operationIDPattern = regexp.MustCompile(`^op_[0-9a-f]{24}$`)

func (Manager) Install(executable string) error { return InstallExecutable(executable) }
func (Manager) Start(executable string, arguments []string) error {
	return StartExecutable(executable, arguments)
}
func (Manager) Stop() error             { return Stop() }
func (Manager) Restart() error          { return Restart() }
func (Manager) Status() (string, error) { return Status() }
func (Manager) Uninstall() error        { return Uninstall() }

// LaunchUpdater starts the fixed self-update helper in a separate native
// service so restarting restic-control cannot terminate its own rollback and
// health-check supervisor.
func LaunchUpdater(operationID, executable string, arguments []string) error {
	program, args, err := updaterCommand(runtime.GOOS, os.Getuid(), operationID, executable, arguments)
	if err != nil {
		return err
	}
	return exec.Command(program, args...).Run()
}

func updaterCommand(goos string, uid int, operationID, executable string, arguments []string) (string, []string, error) {
	if !operationIDPattern.MatchString(operationID) || !filepath.IsAbs(executable) || len(arguments) == 0 {
		return "", nil, errors.New("safe updater identity, executable, and arguments are required")
	}
	for _, argument := range arguments {
		if strings.ContainsRune(argument, '\x00') {
			return "", nil, errors.New("updater argument contains a null byte")
		}
	}
	suffix := strings.TrimPrefix(operationID, "op_")
	switch goos {
	case "linux":
		args := []string{"--user", "--unit", "shadoc-update-" + suffix, "--collect", "--property=Type=exec", executable}
		return "systemd-run", append(args, arguments...), nil
	case "darwin":
		args := []string{"submit", "-l", "io.shadoc.update." + suffix, "--", executable}
		return "launchctl", append(args, arguments...), nil
	default:
		return "", nil, fmt.Errorf("managed application update is unsupported on %s", goos)
	}
}

func InstallExecutable(executable string) error {
	return installExecutable(executable, nil)
}

func StartExecutable(executable string, arguments []string) error {
	return installExecutable(executable, arguments)
}

func installExecutable(executable string, arguments []string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("service executable path must be absolute")
	}
	for _, argument := range arguments {
		if strings.ContainsRune(argument, '\x00') || strings.ContainsAny(argument, "\n\r") {
			return errors.New("service argument contains an unsafe control character")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		restoreCurrent, err := snapshotCurrentService("linux", os.Getuid(), home)
		if err != nil {
			return err
		}
		restoreLegacy, err := suspendLegacyService("linux", os.Getuid(), home)
		if err != nil {
			return err
		}
		path := definitionPath("linux", home)
		if err := os.WriteFile(path, []byte(systemdUnit(executable, arguments)), 0o600); err != nil {
			return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if err := exec.Command("systemctl", "--user", "enable", "shadoc.service").Run(); err != nil {
			return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if err := exec.Command("systemctl", "--user", "restart", "shadoc.service").Run(); err != nil {
			return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if err := verifyStartedService("linux", os.Getuid(), home, arguments); err != nil {
			return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if restoreLegacy != nil {
			if err := finalizeLegacyMigration("linux", home); err != nil {
				return rollbackNewService("linux", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
			}
		}
		return nil
	case "darwin":
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		restoreCurrent, err := snapshotCurrentService("darwin", os.Getuid(), home)
		if err != nil {
			return err
		}
		restoreLegacy, err := suspendLegacyService("darwin", os.Getuid(), home)
		if err != nil {
			return err
		}
		path := definitionPath("darwin", home)
		if err := os.WriteFile(path, []byte(launchdPlist(executable, arguments)), 0o600); err != nil {
			return rollbackNewService("darwin", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), path).Run()
		if err := exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path).Run(); err != nil {
			return rollbackNewService("darwin", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if err := verifyStartedService("darwin", os.Getuid(), home, arguments); err != nil {
			return rollbackNewService("darwin", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
		}
		if restoreLegacy != nil {
			if err := finalizeLegacyMigration("darwin", home); err != nil {
				return rollbackNewService("darwin", os.Getuid(), home, err, restoreCurrent, restoreLegacy)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported service OS %s", runtime.GOOS)
	}
}

func Stop() error {
	return runServiceAction("stop")
}

func Restart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	commands, err := restartActionCommandsFor(runtime.GOOS, os.Getuid(), home, installedServiceIdentity(runtime.GOOS, home))
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := exec.Command(command.program, command.arguments...).Run(); err != nil && !command.ignoreError {
			return err
		}
	}
	return nil
}

func Status() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	program, arguments, err := serviceActionCommandFor(runtime.GOOS, os.Getuid(), home, "status", installedServiceIdentity(runtime.GOOS, home))
	if err != nil {
		return "", err
	}
	output, commandErr := exec.Command(program, arguments...).CombinedOutput()
	return statusFromCommand(runtime.GOOS, output, commandErr)
}

func statusFromCommand(goos string, output []byte, commandErr error) (string, error) {
	if commandErr == nil {
		return "running", nil
	}
	var exitErr *exec.ExitError
	if errors.As(commandErr, &exitErr) {
		state := strings.TrimSpace(string(output))
		lowerState := strings.ToLower(state)
		if strings.Contains(lowerState, "could not be found") || strings.Contains(lowerState, "not-found") {
			return "not installed", nil
		}
		if goos == "darwin" || state == "inactive" || state == "failed" || state == "unknown" {
			return "stopped", nil
		}
	}
	return "", commandErr
}

func verifyStartedService(goos string, uid int, home string, arguments []string) error {
	program, nativeArguments, err := serviceActionCommandFor(goos, uid, home, "status", "shadoc")
	if err != nil {
		return err
	}
	output, commandErr := exec.Command(program, nativeArguments...).CombinedOutput()
	status, err := statusFromCommand(goos, output, commandErr)
	if err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("Shadoc native service is %s after start", status)
	}
	healthURL, ok := serviceHealthURL(arguments)
	if !ok {
		return nil
	}
	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		response, requestErr := client.Get(healthURL)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("Shadoc health check did not become ready after start")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func serviceHealthURL(arguments []string) (string, bool) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] != "--listen" {
			continue
		}
		host, port, err := net.SplitHostPort(arguments[index+1])
		if err != nil {
			return "", false
		}
		if host == "" || net.ParseIP(host) != nil && net.ParseIP(host).IsUnspecified() {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port) + "/api/health", true
	}
	return "", false
}

func runServiceAction(action string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	program, arguments, err := serviceActionCommandFor(runtime.GOOS, os.Getuid(), home, action, installedServiceIdentity(runtime.GOOS, home))
	if err != nil {
		return err
	}
	return exec.Command(program, arguments...).Run()
}

type nativeCommand struct {
	program     string
	arguments   []string
	ignoreError bool
}

func legacyServiceTransitionCommands(goos string, uid int, home string) (nativeCommand, nativeCommand, error) {
	legacyPath := definitionPathFor(goos, home, "restic-control")
	switch goos {
	case "linux":
		return nativeCommand{program: "systemctl", arguments: []string{"--user", "disable", "--now", "restic-control.service"}},
			nativeCommand{program: "systemctl", arguments: []string{"--user", "enable", "--now", "restic-control.service"}}, nil
	case "darwin":
		domain := "gui/" + strconv.Itoa(uid)
		return nativeCommand{program: "launchctl", arguments: []string{"bootout", domain, legacyPath}},
			nativeCommand{program: "launchctl", arguments: []string{"bootstrap", domain, legacyPath}}, nil
	default:
		return nativeCommand{}, nativeCommand{}, fmt.Errorf("unsupported service OS %s", goos)
	}
}

func suspendLegacyService(goos string, uid int, home string) (func() error, error) {
	legacyPath := definitionPathFor(goos, home, "restic-control")
	parkedPath := legacyPath + ".shadoc-migration"
	definitionPath := legacyPath
	info, err := os.Stat(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		definitionPath = parkedPath
		info, err = os.Stat(parkedPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("legacy service definition is not a regular file")
	}
	definition, err := os.ReadFile(definitionPath)
	if err != nil {
		return nil, err
	}
	stop, restore, err := legacyServiceTransitionCommands(goos, uid, home)
	if err != nil {
		return nil, err
	}
	if definitionPath == parkedPath {
		// A previous migration was interrupted after safely parking the old
		// definition. Keep it parked and use it only for rollback.
	} else if goos == "darwin" {
		output, stopErr := exec.Command(stop.program, stop.arguments...).CombinedOutput()
		if stopErr != nil {
			statusProgram, statusArguments, statusErr := serviceActionCommandFor(goos, uid, home, "status", "restic-control")
			if statusErr != nil {
				return nil, errors.Join(stopErr, statusErr)
			}
			if statusOutput, printErr := exec.Command(statusProgram, statusArguments...).CombinedOutput(); printErr == nil {
				return nil, fmt.Errorf("stop legacy service: %w: %s; service remains loaded: %s", stopErr, strings.TrimSpace(string(output)), strings.TrimSpace(string(statusOutput)))
			}
		}
	} else if err := exec.Command(stop.program, stop.arguments...).Run(); err != nil {
		return nil, fmt.Errorf("stop legacy service: %w", err)
	}
	if definitionPath == legacyPath {
		if err := os.Rename(legacyPath, parkedPath); err != nil {
			return nil, errors.Join(err, exec.Command(restore.program, restore.arguments...).Run())
		}
	}
	return func() error {
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(legacyPath, definition, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Remove(parkedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if goos == "linux" {
			if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
				return err
			}
		}
		return exec.Command(restore.program, restore.arguments...).Run()
	}, nil
}

func finalizeLegacyMigration(goos, home string) error {
	legacyPath := definitionPathFor(goos, home, "restic-control")
	if err := os.Remove(legacyPath + ".shadoc-migration"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy service definition: %w", err)
	}
	if err := removeDefinitionFor(goos, home, "restic-control"); err != nil {
		return fmt.Errorf("remove unexpected legacy service definition: %w", err)
	}
	if goos == "linux" {
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return fmt.Errorf("reload services after legacy migration: %w", err)
		}
	}
	return nil
}

func snapshotCurrentService(goos string, uid int, home string) (func() error, error) {
	path := definitionPathFor(goos, home, "shadoc")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("current service definition is not a regular file")
	}
	definition, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return func() error {
		if err := os.WriteFile(path, definition, info.Mode().Perm()); err != nil {
			return err
		}
		switch goos {
		case "linux":
			if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
				return err
			}
			if err := exec.Command("systemctl", "--user", "enable", "shadoc.service").Run(); err != nil {
				return err
			}
			return exec.Command("systemctl", "--user", "restart", "shadoc.service").Run()
		case "darwin":
			_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(uid), path).Run()
			return exec.Command("launchctl", "bootstrap", "gui/"+strconv.Itoa(uid), path).Run()
		default:
			return fmt.Errorf("unsupported service OS %s", goos)
		}
	}, nil
}

func rollbackNewService(goos string, uid int, home string, cause error, restoreCurrent, restoreLegacy func() error) error {
	var cleanupErrors []error
	switch goos {
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "shadoc.service").Run()
		if err := removeDefinitionFor("linux", home, "shadoc"); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	case "darwin":
		path := definitionPathFor("darwin", home, "shadoc")
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(uid), path).Run()
		if err := removeDefinitionFor("darwin", home, "shadoc"); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	default:
		cleanupErrors = append(cleanupErrors, fmt.Errorf("unsupported service OS %s", goos))
	}
	if restoreCurrent != nil {
		if err := restoreCurrent(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("restore current service: %w", err))
		}
	}
	if restoreLegacy != nil {
		if err := restoreLegacy(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("restore legacy service: %w", err))
		}
	}
	return errors.Join(append([]error{cause}, cleanupErrors...)...)
}

func restartActionCommands(goos string, uid int, home string) ([]nativeCommand, error) {
	return restartActionCommandsFor(goos, uid, home, "shadoc")
}

func restartActionCommandsFor(goos string, uid int, home, identity string) ([]nativeCommand, error) {
	if goos == "darwin" {
		stopProgram, stopArguments, err := serviceActionCommandFor(goos, uid, home, "stop", identity)
		if err != nil {
			return nil, err
		}
		startProgram, startArguments, err := serviceActionCommandFor(goos, uid, home, "start", identity)
		if err != nil {
			return nil, err
		}
		return []nativeCommand{
			{program: stopProgram, arguments: stopArguments, ignoreError: true},
			{program: startProgram, arguments: startArguments},
		}, nil
	}
	program, arguments, err := serviceActionCommandFor(goos, uid, home, "restart", identity)
	if err != nil {
		return nil, err
	}
	return []nativeCommand{{program: program, arguments: arguments}}, nil
}

func serviceActionCommand(goos string, uid int, home, action string) (string, []string, error) {
	return serviceActionCommandFor(goos, uid, home, action, "shadoc")
}

func serviceActionCommandFor(goos string, uid int, home, action, identity string) (string, []string, error) {
	if identity != "shadoc" && identity != "restic-control" {
		return "", nil, errors.New("unsupported native service identity")
	}
	switch goos {
	case "linux":
		unit := identity + ".service"
		switch action {
		case "stop":
			return "systemctl", []string{"--user", "stop", unit}, nil
		case "restart":
			return "systemctl", []string{"--user", "restart", unit}, nil
		case "status":
			return "systemctl", []string{"--user", "is-active", unit}, nil
		}
	case "darwin":
		domain := "gui/" + strconv.Itoa(uid)
		path := definitionPathFor("darwin", home, identity)
		switch action {
		case "stop":
			return "launchctl", []string{"bootout", domain, path}, nil
		case "start":
			return "launchctl", []string{"bootstrap", domain, path}, nil
		case "status":
			return "launchctl", []string{"print", domain + "/io." + identity}, nil
		}
	}
	return "", nil, fmt.Errorf("unsupported service action %q on %s", action, goos)
}

func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		identity := installedServiceIdentity("linux", home)
		_ = exec.Command("systemctl", "--user", "disable", "--now", identity+".service").Run()
		if err := removeDefinitionFor("linux", home, identity); err != nil {
			return err
		}
		return exec.Command("systemctl", "--user", "daemon-reload").Run()
	case "darwin":
		identity := installedServiceIdentity("darwin", home)
		path := definitionPathFor("darwin", home, identity)
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), path).Run()
		return removeDefinitionFor("darwin", home, identity)
	default:
		return fmt.Errorf("unsupported service OS %s", runtime.GOOS)
	}
}
func definitionPath(goos, home string) string {
	return definitionPathFor(goos, home, "shadoc")
}

func definitionPathFor(goos, home, identity string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", "io."+identity+".plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", identity+".service")
}
func removeDefinition(goos, home string) error {
	return removeDefinitionFor(goos, home, "shadoc")
}

func removeDefinitionFor(goos, home, identity string) error {
	err := os.Remove(definitionPathFor(goos, home, identity))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func installedServiceIdentity(goos, home string) string {
	if info, err := os.Stat(definitionPathFor(goos, home, "shadoc")); err == nil && info.Mode().IsRegular() {
		return "shadoc"
	}
	if info, err := os.Stat(definitionPathFor(goos, home, "restic-control")); err == nil && info.Mode().IsRegular() {
		return "restic-control"
	}
	return "shadoc"
}
func systemdUnit(executable string, arguments []string) string {
	command := []string{systemdArgument(executable)}
	for _, argument := range arguments {
		command = append(command, systemdArgument(argument))
	}
	return "[Unit]\nDescription=Shadoc backup service\nAfter=network-online.target\n\n[Service]\nExecStart=" + strings.Join(command, " ") + "\nRestart=on-failure\nRestartSec=5\nNoNewPrivileges=true\nPrivateTmp=true\n\n[Install]\nWantedBy=default.target\n"
}

func systemdArgument(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "%", "%%").Replace(value)
	return `"` + escaped + `"`
}

func launchdPlist(executable string, arguments []string) string {
	values := append([]string{executable}, arguments...)
	var programArguments strings.Builder
	for _, value := range values {
		programArguments.WriteString("<string>")
		programArguments.WriteString(html.EscapeString(value))
		programArguments.WriteString("</string>")
	}
	return `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>io.shadoc</string><key>ProgramArguments</key><array>` + programArguments.String() + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict></dict></plist>`
}
