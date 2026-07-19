package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/agentrestore"
	"github.com/maboo-run/shadoc/internal/agentruntime"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/resticagent"
	"github.com/maboo-run/shadoc/internal/rsync"
)

var applicationVersion = "0.1.0-dev"

var agentToolVersionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

func main() {
	if err := runEntry(run); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Shadoc Agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context) error {
	serviceURL := flag.String("service", compatibleEnv("SHADOC_AGENT_SERVICE", "RESTIC_CONTROL_AGENT_SERVICE", ""), "Agent service HTTPS URL")
	agentID := flag.String("id", compatibleEnv("SHADOC_AGENT_ID", "RESTIC_CONTROL_AGENT_ID", ""), "unique Agent ID")
	dataDir := flag.String("data-dir", compatibleEnv("SHADOC_AGENT_DATA_DIR", "RESTIC_CONTROL_AGENT_DATA_DIR", "./agent-data"), "Agent credential and runtime directory")
	token := flag.String("enrollment-token", compatibleEnv("SHADOC_AGENT_ENROLLMENT_TOKEN", "RESTIC_CONTROL_AGENT_ENROLLMENT_TOKEN", ""), "one-time enrollment token")
	tokenFile := flag.String("enrollment-token-file", compatibleEnv("SHADOC_AGENT_ENROLLMENT_TOKEN_FILE", "RESTIC_CONTROL_AGENT_ENROLLMENT_TOKEN_FILE", ""), "one-time enrollment token file; removed after successful enrollment")
	caFile := flag.String("ca-file", compatibleEnv("SHADOC_AGENT_CA_FILE", "RESTIC_CONTROL_AGENT_CA_FILE", ""), "service CA PEM used for first enrollment")
	resticProgram := flag.String("restic", compatibleEnv("SHADOC_AGENT_RESTIC", "RESTIC_CONTROL_AGENT_RESTIC", "restic"), "restic executable")
	rsyncProgram := flag.String("rsync", compatibleEnv("SHADOC_AGENT_RSYNC", "RESTIC_CONTROL_AGENT_RSYNC", "rsync"), "rsync executable")
	interval := flag.Duration("poll-interval", time.Second, "heartbeat and assignment polling interval")
	allowedRoots := flag.String("allowed-roots", compatibleEnv("SHADOC_AGENT_ALLOWED_ROOTS", "RESTIC_CONTROL_AGENT_ALLOWED_ROOTS", ""), "comma-separated absolute roots available to directory browsing and creation")
	showVersion := flag.Bool("version", false, "print Agent version")
	flag.Parse()
	if *showVersion {
		_, err := fmt.Fprintln(os.Stdout, applicationVersion)
		return err
	}
	if *serviceURL == "" || *agentID == "" {
		return errors.New("--service and --id are required")
	}
	if _, err := os.Stat(filepath.Join(*dataDir, "agent.crt")); errors.Is(err, os.ErrNotExist) {
		enrollmentSecret, cleanupToken, tokenErr := enrollmentToken(*token, *tokenFile)
		if tokenErr != nil || *caFile == "" {
			return errors.Join(errors.New("unenrolled Agent requires an enrollment token and --ca-file"), tokenErr)
		}
		caPEM, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = agentruntime.Enroll(ctx, *serviceURL, *agentID, enrollmentSecret, caPEM, *dataDir)
		cancel()
		cleanupToken(err == nil)
		if err != nil {
			return fmt.Errorf("enroll agent: %w", err)
		}
	}
	client, err := agentruntime.MTLSHTTPClient(*dataDir)
	if err != nil {
		return err
	}
	control, err := agentruntime.NewHTTPControl(*serviceURL, client)
	if err != nil {
		return err
	}
	if err := control.EnableCertificateRenewal(*dataDir); err != nil {
		return fmt.Errorf("configure Agent certificate renewal: %w", err)
	}
	tempRoot := filepath.Join(*dataDir, "run")
	executor := command.OSExecutor{OutputLimit: 64 << 10}
	var engines []execution.Engine
	resticVersion, rsyncVersion := "", ""
	pathStyle := "posix"
	if runtime.GOOS == "windows" {
		pathStyle = "windows"
	}
	roots := splitRoots(*allowedRoots)
	if len(roots) == 0 {
		roots = agentfilesystem.DefaultRoots(pathStyle)
	}
	engines = append(engines, agentfilesystem.New(pathStyle, roots))
	capabilities := append(agentCapabilities(pathStyle, runtime.GOOS), "os:"+runtime.GOOS, "arch:"+runtime.GOARCH)
	if program, found := resolveResticProgram(*resticProgram, *dataDir, runtime.GOOS, exec.LookPath, os.Stat); found {
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resticVersion = probeAgentToolVersion(probeCtx, executor, program, []string{"version"})
		cancel()
		if resticVersion != "" {
			resticRunner := restic.New(program, executor, tempRoot)
			engines = append(engines, resticagent.New(resticRunner), agentrestore.New(pathStyle, roots, resticRunner))
			capabilities = append(capabilities, "restic", string(agentrestore.Kind))
		} else {
			slog.Warn("restic capability unavailable", "error", "version probe failed")
		}
	}
	if program, runtimeName, found := resolveRsyncProgram(*rsyncProgram, runtime.GOOS, os.Getenv, exec.LookPath); found {
		rsyncEngine := rsync.New(program, executor, tempRoot)
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		probeErr := rsyncEngine.Probe(probeCtx)
		cancel()
		if probeErr == nil {
			versionCtx, versionCancel := context.WithTimeout(context.Background(), 10*time.Second)
			rsyncVersion = probeAgentToolVersion(versionCtx, executor, program, []string{"--version"})
			versionCancel()
			if rsyncVersion != "" {
				engines = append(engines, rsyncEngine)
				capabilities = append(capabilities, "rsync")
				if runtimeName != "" {
					capabilities = append(capabilities, "rsync-runtime:"+runtimeName)
				}
			} else {
				slog.Warn("rsync capability unavailable", "error", "version probe failed")
			}
		} else {
			slog.Warn("rsync capability unavailable", "error", probeErr)
		}
	}
	engines = append(engines, repositorycapacity.NewEngine(repositorycapacity.SystemProbe{}))
	capabilities = append(capabilities, string(repositorycapacity.Kind))
	agentRuntime := agentruntime.New(*agentID, execution.NewRegistry(engines...), time.Now)
	agentRuntime.SetRuntimeInfo(agentprotocol.RuntimeInfo{
		BuildVersion: applicationVersion, ProtocolMin: agentprotocol.Version, ProtocolMax: agentprotocol.Version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, ResticVersion: resticVersion, RsyncVersion: rsyncVersion,
		ServiceURL: strings.TrimRight(*serviceURL, "/"), RenewalStatus: "healthy",
	})
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	slog.Info("Shadoc Agent started", "id", *agentID, "service", *serviceURL)
	return agentRuntime.Run(ctx, control, capabilities, *interval)
}

func probeAgentToolVersion(ctx context.Context, executor command.Executor, program string, arguments []string) string {
	if executor == nil || strings.TrimSpace(program) == "" || len(arguments) != 1 || arguments[0] != "version" && arguments[0] != "--version" {
		return ""
	}
	result, err := executor.Run(ctx, command.Spec{Program: program, Args: append([]string(nil), arguments...)})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return agentToolVersionPattern.FindString(result.Stdout + "\n" + result.Stderr)
}

func filesystemCapabilities(pathStyle string) []string {
	return []string{"filesystem-browse", "filesystem-create-directory", "filesystem-scope-preview", "filesystem-restore-target", "path-style:" + pathStyle}
}

func agentCapabilities(pathStyle, goos string) []string {
	capabilities := filesystemCapabilities(pathStyle)
	if goos == "linux" {
		capabilities = append(capabilities, agentprotocol.ManagedResticInstallCapability)
	}
	return capabilities
}

func splitRoots(value string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func enrollmentToken(explicit, path string) (string, func(bool), error) {
	if explicit != "" {
		return explicit, func(bool) {}, nil
	}
	if path == "" {
		return "", func(bool) {}, errors.New("enrollment token or token file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", func(bool) {}, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", func(bool) {}, errors.Join(readErr, closeErr)
	}
	if len(content) > 4096 {
		return "", func(bool) {}, errors.New("enrollment token file is too large")
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", func(bool) {}, errors.New("enrollment token file is empty")
	}
	return value, func(success bool) {
		if success {
			_ = os.Remove(path)
		}
	}, nil
}

func resolveResticProgram(program, dataDir, goos string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) (string, bool) {
	base := strings.ToLower(filepath.Base(program))
	defaultName := base == "restic" || base == "restic.exe"
	if defaultName && program == filepath.Base(program) {
		name := "restic"
		if goos == "windows" {
			name = "restic.exe"
		}
		managed := filepath.Join(dataDir, "tools", name)
		if info, err := stat(managed); err == nil && info.Mode().IsRegular() && (goos == "windows" || info.Mode().Perm()&0o111 != 0) {
			return managed, true
		}
		if filepath.Base(filepath.Clean(dataDir)) == "shadoc-agent" {
			legacy := filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "restic-control-agent", "tools", name)
			if info, err := stat(legacy); err == nil && info.Mode().IsRegular() && (goos == "windows" || info.Mode().Perm()&0o111 != 0) {
				return legacy, true
			}
		}
	}
	resolved, err := lookPath(program)
	return resolved, err == nil
}

func resolveRsyncProgram(program, goos string, getenv func(string) string, lookPath func(string) (string, error)) (string, string, bool) {
	if resolved, err := lookPath(program); err == nil {
		name := "rsync"
		if goos == "windows" {
			name = "cwrsync"
		}
		return resolved, name, true
	}
	if goos != "windows" || !strings.EqualFold(filepath.Base(program), "rsync") && !strings.EqualFold(filepath.Base(program), "rsync.exe") {
		return "", "", false
	}
	for _, variable := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if root := strings.TrimSpace(getenv(variable)); root != "" {
			candidate := strings.TrimRight(root, `\\/`) + `\cwRsync\bin\rsync.exe`
			if resolved, err := lookPath(candidate); err == nil {
				return resolved, "cwrsync", true
			}
		}
	}
	return "", "", false
}

func compatibleEnv(primary, legacy, fallback string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	if value := os.Getenv(legacy); value != "" {
		return value
	}
	return fallback
}
