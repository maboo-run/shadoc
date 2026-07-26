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
	"syscall"
	"time"
)

type Scope string

const (
	UserScope   Scope = "user"
	SystemScope Scope = "system"
)

func normalizeScope(scope Scope) (Scope, error) {
	if scope == "" {
		return UserScope, nil
	}
	if scope != UserScope && scope != SystemScope {
		return "", fmt.Errorf("unsupported service scope %q", scope)
	}
	return scope, nil
}

func validateScopeRuntime(goos string, scope Scope, euid int) error {
	scope, err := normalizeScope(scope)
	if err != nil {
		return err
	}
	if scope == SystemScope {
		if goos != "linux" {
			return fmt.Errorf("system service scope is unsupported on %s", goos)
		}
		if euid != 0 {
			return errors.New("system service scope requires root")
		}
		return nil
	}
	if euid == 0 {
		return errors.New("root cannot manage a user service; use --system")
	}
	return nil
}

func Install() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return InstallExecutable(executable)
}

// Manager adapts one fixed native service scope to the application lifecycle manager.
type Manager struct {
	scope            Scope
	installArguments []string
}

func NewManager(scope Scope, installArguments []string) (Manager, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Manager{}, err
	}
	if err := validateServiceArguments(installArguments); err != nil {
		return Manager{}, err
	}
	return Manager{scope: scope, installArguments: append([]string(nil), installArguments...)}, nil
}

func (m Manager) Scope() Scope {
	scope, err := normalizeScope(m.scope)
	if err != nil {
		return UserScope
	}
	return scope
}

var operationIDPattern = regexp.MustCompile(`^op_[0-9a-f]{24}$`)

func (m Manager) Install(executable string) error {
	return installExecutableForScope(m.Scope(), executable, m.installArguments)
}
func (m Manager) Start(executable string, arguments []string) error {
	return installExecutableForScope(m.Scope(), executable, arguments)
}
func (m Manager) Stop() error             { return stopForScope(m.Scope()) }
func (m Manager) Restart() error          { return restartForScope(m.Scope()) }
func (m Manager) Status() (string, error) { return statusForScope(m.Scope()) }
func (m Manager) Uninstall() error        { return uninstallForScope(m.Scope()) }

// LaunchUpdater starts the fixed self-update helper in a separate native
// service so restarting restic-control cannot terminate its own rollback and
// health-check supervisor.
func LaunchUpdater(operationID, executable string, arguments []string) error {
	return LaunchUpdaterForScope(UserScope, operationID, executable, arguments)
}

func LaunchUpdaterForScope(scope Scope, operationID, executable string, arguments []string) error {
	program, args, err := updaterCommand(runtime.GOOS, scope, os.Getuid(), operationID, executable, arguments)
	if err != nil {
		return err
	}
	return exec.Command(program, args...).Run()
}

func updaterCommand(goos string, scope Scope, uid int, operationID, executable string, arguments []string) (string, []string, error) {
	if !operationIDPattern.MatchString(operationID) || !filepath.IsAbs(executable) || len(arguments) == 0 {
		return "", nil, errors.New("safe updater identity, executable, and arguments are required")
	}
	scope, err := normalizeScope(scope)
	if err != nil {
		return "", nil, err
	}
	for _, argument := range arguments {
		if strings.ContainsRune(argument, '\x00') {
			return "", nil, errors.New("updater argument contains a null byte")
		}
	}
	suffix := strings.TrimPrefix(operationID, "op_")
	switch goos {
	case "linux":
		args := []string{"--unit", "shadoc-update-" + suffix, "--collect", "--property=Type=exec", executable}
		if scope == UserScope {
			args = append([]string{"--user"}, args...)
		}
		return "systemd-run", append(args, arguments...), nil
	case "darwin":
		if scope == SystemScope {
			return "", nil, errors.New("system updater is unsupported on darwin")
		}
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
	return installExecutableForScope(UserScope, executable, arguments)
}

func validateServiceArguments(arguments []string) error {
	for _, argument := range arguments {
		if strings.ContainsRune(argument, '\x00') || strings.ContainsAny(argument, "\n\r") {
			return errors.New("service argument contains an unsafe control character")
		}
	}
	return nil
}

func installExecutableForScope(scope Scope, executable string, arguments []string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("service executable path must be absolute")
	}
	if err := validateServiceArguments(arguments); err != nil {
		return err
	}
	if err := validateScopeRuntime(runtime.GOOS, scope, os.Geteuid()); err != nil {
		return err
	}
	if scope == SystemScope {
		return installSystemExecutable(executable, arguments)
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

func installSystemExecutable(executable string, arguments []string) error {
	if filepath.Clean(executable) != "/var/lib/shadoc/app/shadoc" {
		return errors.New("system service executable must be /var/lib/shadoc/app/shadoc; use the root installer or migrate-to-root first")
	}
	if err := validateOwnedExecutablePath(executable, 0); err != nil {
		return fmt.Errorf("validate system service executable: %w", err)
	}
	if err := validateSystemServiceArguments(arguments); err != nil {
		return err
	}
	path := definitionPathForScope("linux", SystemScope, "", "shadoc")
	var previous []byte
	var previousMode os.FileMode
	hadPrevious := false
	if info, err := os.Lstat(path); err == nil {
		if err := validateOwnedRegularFile(info, 0); err != nil {
			return fmt.Errorf("validate existing system service definition: %w", err)
		}
		if err := validateAdditionalPathSecurity(path, false); err != nil {
			return fmt.Errorf("validate existing system service definition: %w", err)
		}
		previous, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		previousMode, hadPrevious = info.Mode().Perm(), true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeOwnedFileAtomic(path, []byte(systemdUnitForScope(SystemScope, executable, arguments)), 0o644, 0); err != nil {
		return err
	}
	rollback := func(cause error) error {
		var cleanup error
		_ = exec.Command("systemctl", "disable", "--now", "shadoc.service").Run()
		if hadPrevious {
			cleanup = errors.Join(cleanup, writeOwnedFileAtomic(path, previous, previousMode, 0))
			cleanup = errors.Join(cleanup, exec.Command("systemctl", "daemon-reload").Run())
			cleanup = errors.Join(cleanup, exec.Command("systemctl", "enable", "--now", "shadoc.service").Run())
		} else {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanup = errors.Join(cleanup, err)
			}
			cleanup = errors.Join(cleanup, exec.Command("systemctl", "daemon-reload").Run())
		}
		return errors.Join(cause, cleanup)
	}
	for _, command := range [][]string{
		{"daemon-reload"},
		{"enable", "shadoc.service"},
		{"restart", "shadoc.service"},
	} {
		if err := exec.Command("systemctl", command...).Run(); err != nil {
			return rollback(err)
		}
	}
	if err := verifyStartedServiceForScope("linux", SystemScope, 0, "", arguments); err != nil {
		return rollback(err)
	}
	return nil
}

func validateSystemServiceArguments(arguments []string) error {
	if len(arguments) != 7 ||
		arguments[0] != "serve" ||
		arguments[1] != "--service-scope" || arguments[2] != "system" ||
		arguments[3] != "--listen" ||
		arguments[5] != "--data-dir" || filepath.Clean(arguments[6]) != "/var/lib/shadoc" {
		return errors.New("system service must use the fixed Shadoc root serve command")
	}
	if _, _, err := net.SplitHostPort(arguments[4]); err != nil {
		return fmt.Errorf("system service listen address is invalid: %w", err)
	}
	return nil
}

func validateOwnedExecutablePath(path string, expectedUID int) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || expectedUID < 0 {
		return errors.New("owned executable path must be a safe absolute path")
	}
	current := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", current)
		}
		if err := validateAdditionalPathSecurity(current, index < len(components)-1); err != nil {
			return err
		}
		uid, ok := fileOwnerUID(info)
		if !ok || uid != 0 && uid != expectedUID {
			return fmt.Errorf("%s has an unexpected owner", current)
		}
		if index < len(components)-1 {
			if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("%s is not a protected directory", current)
			}
			continue
		}
		if !info.Mode().IsRegular() || uid != expectedUID || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
			return errors.New("system service executable must be an owned, non-writable regular executable")
		}
	}
	return nil
}

func validateOwnedRegularFile(info os.FileInfo, expectedUID int) error {
	uid, ok := fileOwnerUID(info)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 ||
		!ok || uid != expectedUID {
		return errors.New("file must be an owned, non-writable regular file")
	}
	return nil
}

func fileOwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

func writeOwnedFileAtomic(path string, content []byte, mode os.FileMode, expectedUID int) (retErr error) {
	if existing, err := os.Lstat(path); err == nil {
		if err := validateOwnedRegularFile(existing, expectedUID); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".shadoc-service-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() {
		_ = temp.Close()
		if retErr != nil {
			_ = os.Remove(name)
		}
	}()
	if err := temp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if expectedUID != os.Geteuid() {
		if err := temp.Chown(expectedUID, -1); err != nil {
			return err
		}
	}
	if err := sanitizeAdditionalFileSecurity(int(temp.Fd())); err != nil {
		return err
	}
	if _, err := temp.Write(content); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := validateAdditionalPathSecurity(path, false); err != nil {
		return err
	}
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func Stop() error {
	return stopForScope(UserScope)
}

func Restart() error {
	return restartForScope(UserScope)
}

func stopForScope(scope Scope) error {
	if err := validateScopeRuntime(runtime.GOOS, scope, os.Geteuid()); err != nil {
		return err
	}
	if scope == SystemScope {
		program, arguments, err := serviceActionCommandForScope(runtime.GOOS, scope, 0, "", "stop", "shadoc")
		if err != nil {
			return err
		}
		return exec.Command(program, arguments...).Run()
	}
	return runServiceAction("stop")
}

func restartForScope(scope Scope) error {
	if err := validateScopeRuntime(runtime.GOOS, scope, os.Geteuid()); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if scope == SystemScope {
		program, arguments, err := serviceActionCommandForScope(runtime.GOOS, scope, 0, "", "restart", "shadoc")
		if err != nil {
			return err
		}
		return exec.Command(program, arguments...).Run()
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
	return statusForScope(UserScope)
}

func statusForScope(scope Scope) (string, error) {
	if err := validateScopeRuntime(runtime.GOOS, scope, os.Geteuid()); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	identity := installedServiceIdentity(runtime.GOOS, home)
	if scope == SystemScope {
		identity = "shadoc"
	}
	program, arguments, err := serviceActionCommandForScope(runtime.GOOS, scope, os.Getuid(), home, "status", identity)
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
	return verifyStartedServiceForScope(goos, UserScope, uid, home, arguments)
}

func verifyStartedServiceForScope(goos string, scope Scope, uid int, home string, arguments []string) error {
	program, nativeArguments, err := serviceActionCommandForScope(goos, scope, uid, home, "status", "shadoc")
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
	return serviceActionCommandForScope(goos, UserScope, uid, home, action, identity)
}

func serviceActionCommandForScope(goos string, scope Scope, uid int, home, action, identity string) (string, []string, error) {
	if identity != "shadoc" && identity != "restic-control" {
		return "", nil, errors.New("unsupported native service identity")
	}
	scope, err := normalizeScope(scope)
	if err != nil {
		return "", nil, err
	}
	if scope == SystemScope {
		if goos != "linux" || identity != "shadoc" {
			return "", nil, fmt.Errorf("system service action is unsupported for %s on %s", identity, goos)
		}
		unit := identity + ".service"
		switch action {
		case "stop":
			return "systemctl", []string{"stop", unit}, nil
		case "restart":
			return "systemctl", []string{"restart", unit}, nil
		case "status":
			return "systemctl", []string{"is-active", unit}, nil
		}
		return "", nil, fmt.Errorf("unsupported service action %q on %s", action, goos)
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
	return uninstallForScope(UserScope)
}

func uninstallForScope(scope Scope) error {
	if err := validateScopeRuntime(runtime.GOOS, scope, os.Geteuid()); err != nil {
		return err
	}
	if scope == SystemScope {
		_ = exec.Command("systemctl", "disable", "--now", "shadoc.service").Run()
		path := definitionPathForScope("linux", scope, "", "shadoc")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return exec.Command("systemctl", "daemon-reload").Run()
	}
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
	return definitionPathForScope(goos, UserScope, home, identity)
}

func definitionPathForScope(goos string, scope Scope, home, identity string) string {
	if scope == SystemScope {
		if goos == "linux" {
			return filepath.Join("/etc/systemd/system", identity+".service")
		}
		return ""
	}
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
	return systemdUnitForScope(UserScope, executable, arguments)
}

func systemdUnitForScope(scope Scope, executable string, arguments []string) string {
	command := []string{systemdArgument(executable)}
	for _, argument := range arguments {
		command = append(command, systemdArgument(argument))
	}
	if scope == SystemScope {
		return "[Unit]\nDescription=Shadoc backup service\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nUser=root\nGroup=root\nExecStart=" + strings.Join(command, " ") + "\nRestart=on-failure\nRestartSec=5\nNoNewPrivileges=true\nPrivateTmp=true\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n"
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
