package servicemigration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// NativeRunner invokes fixed native service-manager commands without a shell.
type NativeRunner interface {
	CombinedOutput(context.Context, string, ...string) ([]byte, error)
}

type OSNativeRunner struct{}

func (OSNativeRunner) CombinedOutput(ctx context.Context, program string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, program, arguments...).CombinedOutput()
}

type LinuxUserSource struct {
	username string
	uid      int
	home     string
	runner   NativeRunner
	health   func(context.Context, string) error
}

func NewLinuxUserSource(username string, uid int, home string, runner NativeRunner) (*LinuxUserSource, error) {
	if username == "" || username == "root" || uid <= 0 || !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
		return nil, errors.New("source Linux user identity is unsafe")
	}
	if runner == nil {
		runner = OSNativeRunner{}
	}
	return &LinuxUserSource{
		username: username, uid: uid, home: filepath.Clean(home), runner: runner,
		health: waitForSourceHTTPHealth,
	}, nil
}

func (s *LinuxUserSource) Inspect(ctx context.Context) (Installation, error) {
	identity, path, definition, mode, err := s.readDefinition()
	if err != nil {
		return Installation{}, err
	}
	invocation, err := parseGeneratedUserUnit(string(definition), s.home)
	if err != nil {
		return Installation{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if identity == "restic-control" && !invocation.dataDirExplicit {
		invocation.dataDir = filepath.Join(s.home, ".config", "restic-control")
	}
	if err := validateSourceDataDirectory(invocation.dataDir, s.uid); err != nil {
		return Installation{}, err
	}
	if err := validateSourceExecutable(invocation.executable, s.uid); err != nil {
		return Installation{}, err
	}
	enabled, err := s.queryState(ctx, "is-enabled", identity)
	if err != nil {
		return Installation{}, err
	}
	active, err := s.queryState(ctx, "is-active", identity)
	if err != nil {
		return Installation{}, err
	}
	if active {
		if err := s.health(ctx, invocation.listen); err != nil {
			return Installation{}, fmt.Errorf("source user service is not healthy before migration: %w", err)
		}
	}
	return Installation{
		Username: s.username, UID: s.uid, Home: s.home, Identity: identity,
		DataDir: invocation.dataDir, Listen: invocation.listen, Executable: invocation.executable,
		UnitDefinition: string(definition), UnitMode: uint32(mode.Perm()), WasEnabled: enabled, WasActive: active,
	}, nil
}

func (s *LinuxUserSource) Disable(ctx context.Context, installation Installation) error {
	if err := s.validateInstallationIdentity(installation); err != nil {
		return err
	}
	present, err := s.definitionMatches(installation)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("source user service definition disappeared before it could be disabled")
	}
	if err := s.runSystemctl(ctx, "disable", "--now", installation.Identity+".service"); err != nil {
		return fmt.Errorf("disable source user service: %w", err)
	}
	active, err := s.queryState(ctx, "is-active", installation.Identity)
	if err != nil {
		return err
	}
	if active {
		return errors.New("source user service remains active after disable")
	}
	return nil
}

func (s *LinuxUserSource) Restore(ctx context.Context, installation Installation) error {
	if err := s.validateInstallationIdentity(installation); err != nil {
		return err
	}
	present, err := s.definitionMatches(installation)
	if err != nil {
		return err
	}
	if !present {
		if err := writeSourceUnitFileAtomic(s.definitionPath(installation.Identity), []byte(installation.UnitDefinition), os.FileMode(installation.UnitMode), installation.UID); err != nil {
			return fmt.Errorf("restore source user service definition: %w", err)
		}
	}
	if err := s.runSystemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("reload source user services: %w", err)
	}
	if installation.WasEnabled {
		if err := s.runSystemctl(ctx, "enable", installation.Identity+".service"); err != nil {
			return fmt.Errorf("re-enable source user service: %w", err)
		}
	} else if err := s.runSystemctl(ctx, "disable", installation.Identity+".service"); err != nil {
		return fmt.Errorf("restore disabled source user service: %w", err)
	}
	if installation.WasActive {
		if err := s.runSystemctl(ctx, "start", installation.Identity+".service"); err != nil {
			return fmt.Errorf("restart source user service: %w", err)
		}
		active, err := s.queryState(ctx, "is-active", installation.Identity)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("restored source user service is not active")
		}
		if err := s.health(ctx, installation.Listen); err != nil {
			return fmt.Errorf("restored source user service is not healthy: %w", err)
		}
	} else if err := s.runSystemctl(ctx, "stop", installation.Identity+".service"); err != nil {
		return fmt.Errorf("restore stopped source user service: %w", err)
	}
	return nil
}

func waitForSourceHTTPHealth(ctx context.Context, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	healthURL := "http://" + net.JoinHostPort(host, port) + "/api/health"
	healthCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(healthCtx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned %s", response.Status)
		} else {
			lastErr = err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-healthCtx.Done():
			timer.Stop()
			return errors.Join(healthCtx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func (s *LinuxUserSource) Remove(ctx context.Context, installation Installation) error {
	if err := s.validateInstallationIdentity(installation); err != nil {
		return err
	}
	present, err := s.definitionMatches(installation)
	if err != nil {
		return err
	}
	if present {
		if err := s.runSystemctl(ctx, "disable", "--now", installation.Identity+".service"); err != nil {
			return fmt.Errorf("disable source user service for removal: %w", err)
		}
		if err := removeSourceUnitFile(s.definitionPath(installation.Identity), installation.UID, []byte(installation.UnitDefinition)); err != nil {
			return fmt.Errorf("remove source user service definition: %w", err)
		}
	}
	if err := s.runSystemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("reload source user services after removal: %w", err)
	}
	return nil
}

func (s *LinuxUserSource) readDefinition() (string, string, []byte, os.FileMode, error) {
	type candidate struct {
		identity string
		path     string
		content  []byte
		mode     os.FileMode
	}
	var found []candidate
	for _, identity := range []string{"shadoc", "restic-control"} {
		if err := s.rejectUnitDropIns(identity); err != nil {
			return "", "", nil, 0, err
		}
		path := s.definitionPath(identity)
		content, mode, present, err := readSourceUnitFile(path, s.uid)
		if err != nil {
			return "", "", nil, 0, fmt.Errorf("%s: %w", path, err)
		}
		if !present {
			continue
		}
		found = append(found, candidate{identity: identity, path: path, content: content, mode: mode})
	}
	if len(found) == 0 {
		return "", "", nil, 0, errors.New("no Shadoc user service was found for the sudo source user")
	}
	if len(found) != 1 {
		return "", "", nil, 0, errors.New("both current and legacy user services exist; resolve the duplicate before migration")
	}
	item := found[0]
	return item.identity, item.path, item.content, item.mode, nil
}

func (s *LinuxUserSource) definitionMatches(installation Installation) (bool, error) {
	path := s.definitionPath(installation.Identity)
	content, _, present, err := readSourceUnitFile(path, installation.UID)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	if string(content) != installation.UnitDefinition {
		return false, errors.New("source user service changed during migration; refusing to overwrite or delete it")
	}
	return true, nil
}

func (s *LinuxUserSource) validateInstallationIdentity(installation Installation) error {
	if installation.Username != s.username || installation.UID != s.uid || filepath.Clean(installation.Home) != s.home ||
		(installation.Identity != "shadoc" && installation.Identity != "restic-control") ||
		installation.UnitDefinition == "" || os.FileMode(installation.UnitMode).Perm()&0o022 != 0 {
		return errors.New("source user service identity does not match the migration marker")
	}
	return nil
}

func (s *LinuxUserSource) definitionPath(identity string) string {
	return filepath.Join(s.home, ".config", "systemd", "user", identity+".service")
}

func (s *LinuxUserSource) rejectUnitDropIns(identity string) error {
	unitDropIn := identity + ".service.d"
	paths := []string{
		filepath.Join(s.home, ".config", "systemd", "user", unitDropIn),
		filepath.Join(s.home, ".local", "share", "systemd", "user", unitDropIn),
		filepath.Join("/etc/systemd/user", unitDropIn),
		filepath.Join("/run/systemd/user", unitDropIn),
		filepath.Join("/usr/local/lib/systemd/user", unitDropIn),
		filepath.Join("/usr/lib/systemd/user", unitDropIn),
		filepath.Join("/lib/systemd/user", unitDropIn),
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("source user service has an unsupported drop-in directory at %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect source user service drop-ins: %w", err)
		}
	}
	return nil
}

func (s *LinuxUserSource) runSystemctl(ctx context.Context, arguments ...string) error {
	base := []string{"--user", "--machine=" + s.username + "@.host"}
	output, err := s.runner.CombinedOutput(ctx, "systemctl", append(base, arguments...)...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *LinuxUserSource) queryState(ctx context.Context, action, identity string) (bool, error) {
	base := []string{"--user", "--machine=" + s.username + "@.host", action, identity + ".service"}
	output, err := s.runner.CombinedOutput(ctx, "systemctl", base...)
	state := strings.ToLower(strings.TrimSpace(string(output)))
	switch action {
	case "is-enabled":
		switch state {
		case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias":
			return true, nil
		case "disabled", "static", "indirect", "generated", "transient":
			return false, nil
		}
	case "is-active":
		switch state {
		case "active", "activating", "reloading":
			return true, nil
		case "inactive", "failed", "deactivating", "unknown":
			return false, nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("query source user service %s: %w: %s", action, err, state)
	}
	return false, fmt.Errorf("query source user service %s returned unexpected state %q", action, state)
}

type userServiceInvocation struct {
	executable      string
	dataDir         string
	listen          string
	dataDirExplicit bool
}

func parseGeneratedUserUnit(definition, home string) (userServiceInvocation, error) {
	execStart, err := generatedExecStart(definition)
	if err != nil {
		return userServiceInvocation{}, err
	}
	tokens, err := parseSystemdQuotedTokens(execStart)
	if err != nil {
		return userServiceInvocation{}, err
	}
	if len(tokens) == 0 || !filepath.IsAbs(tokens[0]) {
		return userServiceInvocation{}, errors.New("service executable must be an absolute path")
	}
	for _, token := range tokens {
		if strings.ContainsAny(token, "\x00\n\r") {
			return userServiceInvocation{}, errors.New("service argument contains an unsafe control character")
		}
	}
	invocation := userServiceInvocation{
		executable: filepath.Clean(tokens[0]),
		dataDir:    defaultSourceDataDirectory(home),
		listen:     "127.0.0.1:8585",
	}
	if len(tokens) == 1 {
		return invocation, nil
	}
	if tokens[1] != "serve" {
		return userServiceInvocation{}, errors.New("service does not run the Shadoc serve command")
	}
	seen := map[string]bool{}
	listenProvided, portProvided := false, false
	for index := 2; index < len(tokens); {
		name := tokens[index]
		if index+1 >= len(tokens) || seen[name] {
			return userServiceInvocation{}, fmt.Errorf("service argument %q is missing or duplicated", name)
		}
		value := tokens[index+1]
		seen[name] = true
		index += 2
		switch name {
		case "--listen":
			if _, _, err := net.SplitHostPort(value); err != nil {
				return userServiceInvocation{}, fmt.Errorf("invalid service listen address: %w", err)
			}
			invocation.listen, listenProvided = value, true
		case "--data-dir":
			if !filepath.IsAbs(value) {
				return userServiceInvocation{}, errors.New("service data directory must be absolute")
			}
			invocation.dataDir, invocation.dataDirExplicit = filepath.Clean(value), true
		case "--port":
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return userServiceInvocation{}, errors.New("service port must be between 1 and 65535")
			}
			invocation.listen, portProvided = net.JoinHostPort("127.0.0.1", value), true
		case "--service-scope":
			if value != "user" {
				return userServiceInvocation{}, errors.New("source service must use user scope")
			}
		default:
			return userServiceInvocation{}, fmt.Errorf("unsupported generated service argument %q", name)
		}
	}
	if listenProvided && portProvided {
		return userServiceInvocation{}, errors.New("service cannot specify both listen address and port")
	}
	return invocation, nil
}

func generatedExecStart(definition string) (string, error) {
	lines := strings.Split(definition, "\n")
	if len(lines) != 14 || lines[13] != "" ||
		lines[0] != "[Unit]" ||
		(lines[1] != "Description=Shadoc backup service" && lines[1] != "Description=restic-control backup service") ||
		lines[2] != "After=network-online.target" ||
		lines[3] != "" ||
		lines[4] != "[Service]" ||
		!strings.HasPrefix(lines[5], "ExecStart=") ||
		lines[6] != "Restart=on-failure" ||
		lines[7] != "RestartSec=5" ||
		lines[8] != "NoNewPrivileges=true" ||
		lines[9] != "PrivateTmp=true" ||
		lines[10] != "" ||
		lines[11] != "[Install]" ||
		lines[12] != "WantedBy=default.target" {
		return "", errors.New("source service is not an exact Shadoc-generated user unit")
	}
	execStart := strings.TrimPrefix(lines[5], "ExecStart=")
	if execStart == "" {
		return "", errors.New("service has no generated ExecStart directive")
	}
	return execStart, nil
}

func parseSystemdQuotedTokens(value string) ([]string, error) {
	var tokens []string
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
			index++
		}
		if index == len(value) {
			break
		}
		if value[index] != '"' {
			return nil, errors.New("ExecStart must contain only generated quoted arguments")
		}
		index++
		var token strings.Builder
		closed := false
		for index < len(value) {
			current := value[index]
			index++
			switch current {
			case '"':
				closed = true
			case '\\':
				if index == len(value) {
					return nil, errors.New("ExecStart contains an incomplete escape")
				}
				escaped := value[index]
				index++
				if escaped != '\\' && escaped != '"' {
					return nil, fmt.Errorf("ExecStart contains unsupported escape \\%c", escaped)
				}
				token.WriteByte(escaped)
			case '%':
				if index == len(value) || value[index] != '%' {
					return nil, errors.New("ExecStart contains an unescaped systemd specifier")
				}
				index++
				token.WriteByte('%')
			default:
				token.WriteByte(current)
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, errors.New("ExecStart contains an unterminated argument")
		}
		if index < len(value) && value[index] != ' ' && value[index] != '\t' {
			return nil, errors.New("ExecStart contains unexpected text after an argument")
		}
		tokens = append(tokens, token.String())
	}
	return tokens, nil
}

func defaultSourceDataDirectory(home string) string {
	current := filepath.Join(home, ".config", "shadoc")
	legacy := filepath.Join(home, ".config", "restic-control")
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			return legacy
		}
	}
	return current
}

func validateSourceDataDirectory(path string, expectedUID int) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || unsafeSourceRoot(clean) {
		return fmt.Errorf("source data directory %q is unsafe to migrate", path)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("inspect source data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("source data directory must be a private real directory")
	}
	if uid, ok := fileOwnerUID(info); !ok || uid != expectedUID {
		return errors.New("source data directory is not owned by the source user")
	}
	if runtime.GOOS == "linux" {
		if err := rejectSymlinkComponents(clean); err != nil {
			return fmt.Errorf("source data directory: %w", err)
		}
	}
	return nil
}

func validateSourceExecutable(path string, expectedUID int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect source service executable: %w", err)
	}
	uid, ownerOK := fileOwnerUID(info)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 ||
		info.Mode().Perm()&0o111 == 0 || !ownerOK || (uid != expectedUID && uid != 0) {
		return errors.New("source service executable is not a safe regular executable")
	}
	if runtime.GOOS == "linux" {
		if err := rejectSymlinkComponents(path); err != nil {
			return fmt.Errorf("source service executable: %w", err)
		}
	}
	return nil
}

func unsafeSourceRoot(path string) bool {
	switch path {
	case "/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/opt", "/proc", "/root",
		"/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/var", "/var/lib", "/var/lib/shadoc",
		"/var/lib/.shadoc-root-staging":
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	return len(parts) < 2
}

func rejectSymlinkComponents(path string) error {
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", current)
		}
	}
	return nil
}

func validateSourceUnitInfo(info os.FileInfo, expectedUID int) error {
	uid, ownerOK := fileOwnerUID(info)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 ||
		!ownerOK || uid != expectedUID {
		return errors.New("source service definition is not a safe user-owned regular file")
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

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("source service definition is too large")
	}
	return content, nil
}
