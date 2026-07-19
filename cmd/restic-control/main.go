package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/agentrestore"
	"github.com/maboo-run/shadoc/internal/agentservice"
	"github.com/maboo-run/shadoc/internal/agenttask"
	"github.com/maboo-run/shadoc/internal/agenttool"
	"github.com/maboo-run/shadoc/internal/alerting"
	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/backup"
	"github.com/maboo-run/shadoc/internal/capacitymonitor"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/config"
	"github.com/maboo-run/shadoc/internal/controlplane"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/dbrestore"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/httpapi"
	"github.com/maboo-run/shadoc/internal/installer"
	"github.com/maboo-run/shadoc/internal/lifecycle"
	"github.com/maboo-run/shadoc/internal/localfilesystem"
	"github.com/maboo-run/shadoc/internal/maintenance"
	"github.com/maboo-run/shadoc/internal/notification"
	"github.com/maboo-run/shadoc/internal/ntfy"
	"github.com/maboo-run/shadoc/internal/protectionsetup"
	"github.com/maboo-run/shadoc/internal/repolock"
	"github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/restoreverification"
	"github.com/maboo-run/shadoc/internal/rsync"
	"github.com/maboo-run/shadoc/internal/scheduler"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/serviceinstall"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/taskpreview"
	"github.com/maboo-run/shadoc/internal/taskrun"
	"github.com/maboo-run/shadoc/internal/vault"
	"github.com/maboo-run/shadoc/internal/webhook"
)

var applicationVersion = "0.1.0-dev"

func main() {
	if handled, err := runManagedUpdateCommand(); handled {
		if err != nil {
			slog.Error("managed application update", "error", err)
			os.Exit(1)
		}
		return
	}
	if handled, err := runLifecycleCommand(); handled {
		if err != nil {
			slog.Error("application lifecycle command", "error", err)
			os.Exit(1)
		}
		return
	}
	if handled, err := runAdminPasswordCommand(); handled {
		if err != nil {
			slog.Error("administrator reset command", "error", err)
			os.Exit(1)
		}
		fmt.Println("administrator password reset; all sessions revoked")
		return
	}
	if handled, err := runBackgroundServiceCommand(); handled {
		if err != nil {
			slog.Error("Shadoc background service command", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-service" {
		if err := serviceinstall.Install(); err != nil {
			slog.Error("install service", "error", err)
			os.Exit(1)
		}
		fmt.Println("Shadoc service installed")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "uninstall-service" {
		if err := serviceinstall.Uninstall(); err != nil {
			slog.Error("uninstall service", "error", err)
			os.Exit(1)
		}
		fmt.Println("Shadoc service uninstalled; application data preserved")
		return
	}
	serve, handled, err := parseServeCommand(os.Args[1:])
	if err != nil {
		slog.Error("Shadoc serve command", "error", err)
		os.Exit(1)
	}
	if err := run(serve, handled); err != nil {
		slog.Error("Shadoc stopped", "error", err)
		os.Exit(1)
	}
}

func runBackgroundServiceCommand() (bool, error) {
	if len(os.Args) < 2 {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return true, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return true, err
	}
	return handleServiceCommand(os.Args[1:], os.Stdout, executable, serviceinstall.Manager{}, func() (serviceLaunchConfig, error) {
		cfg, err := config.Load(os.Getenv)
		if err != nil {
			return serviceLaunchConfig{}, err
		}
		return serviceLaunchConfig{DataDir: cfg.DataDir, Listen: cfg.Listen}, nil
	})
}

func runAdminPasswordCommand() (bool, error) {
	if len(os.Args) < 2 || os.Args[1] != "reset-admin-password" {
		return false, nil
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return true, err
	}
	s, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return true, err
	}
	defer s.Close()
	manager := auth.New(s, time.Now)
	return handleAdminCommand(context.Background(), os.Args[1:], terminalPasswordReader{}, manager)
}

func runLifecycleCommand() (bool, error) {
	if len(os.Args) < 2 || (os.Args[1] != "install-app" && os.Args[1] != "update-app" && os.Args[1] != "uninstall-app") {
		return false, nil
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return true, err
	}
	current, err := os.Executable()
	if err != nil {
		return true, err
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return true, err
	}
	releasesAPI := os.Getenv("SHADOC_RELEASES_API")
	if releasesAPI == "" {
		releasesAPI = os.Getenv("RESTIC_CONTROL_RELEASES_API")
	}
	if releasesAPI == "" {
		releasesAPI = appinstall.OfficialReleasesAPI
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	releases := appinstall.NewGitHubRelease(client, releasesAPI, runtime.GOOS, runtime.GOARCH)
	health := appinstall.NewHTTPHealthChecker(client, 250*time.Millisecond)
	binary := existingManagedApplicationBinary(cfg.DataDir)
	if os.Args[1] == "install-app" {
		binary = newManagedApplicationBinary(cfg.DataDir)
	}
	lifecycle := appinstall.New(releases, serviceinstall.Manager{}, health, appinstall.Paths{
		Binary:     binary,
		Previous:   binary + ".previous",
		DataDir:    cfg.DataDir,
		HealthURL:  lifecycleHealthURL(cfg.Listen),
		Companions: agentdeploy.ArtifactFilenames(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return handleLifecycleCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, lifecycle, current)
}

func lifecycleHealthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:8585/api/health"
	}
	ip := net.ParseIP(host)
	if host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/health"
}

func run(serve serveOptions, overrideConfig bool) error {
	getenv := os.Getenv
	if overrideConfig {
		getenv = func(key string) string {
			switch key {
			case "SHADOC_DATA_DIR":
				if serve.DataDir != "" {
					return serve.DataDir
				}
			case "SHADOC_LISTEN":
				if serve.Listen != "" {
					return serve.Listen
				}
			}
			return os.Getenv(key)
		}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if overrideConfig && serve.PortProvided {
		cfg.Listen, err = listenWithPort(cfg.Listen, serve.Port)
		if err != nil {
			return fmt.Errorf("apply Shadoc management port: %w", err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve control service executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve control service executable path: %w", err)
	}
	agentArtifactDir, err := agentArtifactDirectory(func() (string, error) { return executable, nil })
	if err != nil {
		return err
	}
	s, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := recoverInterruptedState(context.Background(), s, time.Now().UTC()); err != nil {
		return err
	}
	keyFile := vault.NewKeyFile(cfg.VaultKeyPath)
	if _, statErr := os.Stat(cfg.VaultKeyPath); errors.Is(statErr, os.ErrNotExist) {
		if _, err := vault.LoadOrCreateKey(cfg.VaultKeyPath); err != nil {
			return err
		}
	} else if statErr != nil {
		return statErr
	}
	key, keyState, err := keyFile.Load("")
	if err != nil {
		return err
	}
	defer clear(key)
	authManager := auth.New(s, time.Now)
	secretManager := secret.NewGate(s, time.Now)
	if len(key) != 0 {
		if err := secretManager.Unlock(key); err != nil {
			return err
		}
	}
	vaultController := secret.NewVaultController(keyFile, secretManager, keyState)
	lifecycleService := lifecycle.New(s)
	executor := command.OSExecutor{}
	managedResticPath := filepath.Join(cfg.DataDir, "bin", "restic")
	resticPath := selectLocalResticProgram(managedResticPath, runtime.GOOS, exec.LookPath, os.Stat)
	tempRoot := filepath.Join(cfg.DataDir, "run")
	resticEngine := restic.New(resticPath, executor, tempRoot)
	rsyncProbeContext, cancelRsyncProbe := context.WithTimeout(context.Background(), 10*time.Second)
	rsyncPath, rsyncProbeErr := selectLocalRsyncProgram(rsyncProbeContext, executor, runtime.GOOS, exec.LookPath)
	cancelRsyncProbe()
	if rsyncProbeErr != nil {
		rsyncPath = "rsync"
		slog.Warn("compatible local rsync unavailable; local rsync tasks will fail until rsync 3+ is installed", "error", rsyncProbeErr)
	} else {
		slog.Info("local rsync selected", "program", rsyncPath)
	}
	rsyncEngine := rsync.New(rsyncPath, executor, tempRoot)
	pathStyle := "posix"
	if runtime.GOOS == "windows" {
		pathStyle = "windows"
	}
	localFilesystemService, err := localfilesystem.New(context.Background(), s, pathStyle)
	if err != nil {
		return fmt.Errorf("initialize local filesystem browser: %w", err)
	}
	taskPreviewService := taskpreview.New(s, secretManager, localFilesystemService, rsyncEngine, time.Now)
	rsyncService := rsync.NewService(s, secretManager, rsyncEngine, time.Now)
	backupService := backup.New(s, secretManager, resticEngine, database.NewMySQL(tempRoot), database.NewPostgres(tempRoot), time.Now)
	backupService.SetMetadataExecutor(executor)
	repositoryService := repository.New(s, secretManager, resticEngine)
	protectionSetupService := protectionsetup.New(s, secretManager, repositoryService, time.Now, nil)
	repositoryCapacityService := repositorycapacity.NewService(s, secretManager, repositorycapacity.SystemProbe{}, time.Now)
	repositoryCapacityDispatcher := capacitymonitor.New(s, repositoryCapacityService, 2)
	repositoryLocks := repolock.New()
	backupService.SetRepositoryLocker(repositoryLocks)
	repositoryService.SetLocker(repositoryLocks)
	restoreVerificationService := restoreverification.New(s, repositoryService, filepath.Join(tempRoot, "restore-verifications"), time.Now)
	resticInstaller := installer.NewRestic(http.DefaultClient, installer.OfficialResticReleasesAPI, managedResticPath, runtime.GOOS, runtime.GOARCH)
	agentResticInstaller := agenttool.New(s, secretManager, resticInstaller, agenttool.SSHDialer{}, time.Now)
	databaseRestoreService := dbrestore.New(s, secretManager, repositoryService, executor, tempRoot)
	ntfyClient := ntfy.New(&http.Client{Timeout: 15 * time.Second})
	webhookClient := webhook.New(&http.Client{Timeout: 15 * time.Second})
	notificationService := notification.New(s, secretManager, ntfyClient)
	notificationService.SetWebhook(webhookClient)
	alertService := alerting.New(s, time.Now)
	alertService.SetNotifier(notificationService)
	taskRunner := taskrun.New(s, map[execution.EngineKind]taskrun.Runner{
		execution.EngineKind(domain.ResticEngine): backupService,
		execution.EngineKind(domain.RsyncEngine):  rsyncService,
	})
	taskRunner.SetAgentRunner(agenttask.New(s, time.Now))
	taskRunner.SetObserver(alertService)
	taskRunner.AddObserver(repositorycapacity.NewRunObserver(s, repositoryCapacityService, time.Now))
	toolPaths := compat.ToolPaths{
		Restic:          resticPath,
		Rsync:           rsyncPath,
		MySQLDump:       localToolPath("mysqldump"),
		MySQLRestore:    localToolPath("mysql"),
		PostgresDump:    localToolPath("pg_dump"),
		PostgresRestore: localToolPath("pg_restore"),
	}
	compatibilityContext, cancelCompatibilityProbe := context.WithTimeout(context.Background(), 10*time.Second)
	initialCompatibility := compat.Merge(
		compat.System(cfg.DataDir),
		compat.NewProbe(executor).Tools(compatibilityContext, toolPaths),
	)
	cancelCompatibilityProbe()

	setupToken := ""
	if host, _, splitErr := net.SplitHostPort(cfg.Listen); splitErr == nil {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			raw := make([]byte, 24)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			setupToken = base64.RawURLEncoding.EncodeToString(raw)
			slog.Warn("LAN initialization token; required once before the administrator exists", "token", setupToken)
		}
	}
	initialAgentSettings := agentservice.Settings{ListenHost: "0.0.0.0", Port: agentservice.DefaultPort}
	agentService := agentservice.New(s, secretManager, cfg.DataDir, agentArtifactDir, time.Now)
	agentRestoreService := agentrestore.NewService(s, time.Now)
	agentRestoreService.SetLocker(repositoryLocks)
	if err := agentService.Start(context.Background(), initialAgentSettings); err != nil {
		slog.Warn("Agent HTTPS service is not running; update its settings from the administration page", "error", err)
	}
	controlPlaneService := controlplane.NewService(
		s,
		secretManager,
		controlplane.AgentCAFileSource{Directory: filepath.Join(cfg.DataDir, "agent-pki")},
		applicationVersion,
		time.Now,
	)
	controlPlaneService.SetImportToolChecker(controlplane.SystemToolChecker{ResticPath: resticPath})
	releaseCatalog := appinstall.NewGitHubRelease(&http.Client{Timeout: 30 * time.Second}, appinstall.OfficialReleasesAPI, runtime.GOOS, runtime.GOARCH)
	managedBinary := existingManagedApplicationBinary(cfg.DataDir)
	applicationUpdater := appinstall.NewWebUpdater(executable, cfg.DataDir, cfg.Listen, runningFromManagedPath(executable, managedBinary), serviceinstall.LaunchUpdater)
	apiServer := httpapi.NewWithRuntime(s, authManager, secretManager, httpapi.Runtime{
		Runner: taskRunner, Repositories: repositoryService, Paths: toolPaths, Compatibility: initialCompatibility, Installer: resticInstaller, DatabaseBackupPreflighter: backupService,
		SelectRestic: resticEngine.SetProgram, DatabaseRestore: databaseRestoreService, DumpFileRestore: dbrestore.NewDumpFileService(repositoryService), Ntfy: ntfyClient, Webhook: webhookClient,
		DataDir: cfg.DataDir, SetupToken: setupToken, Vault: vaultController, Lifecycle: lifecycleService,
		ApplicationVersion: applicationVersion, ApplicationReleases: releaseCatalog, ApplicationUpdater: applicationUpdater,
		AgentService: agentService, AgentUninstaller: agentService, AgentUpgrader: agentService, AgentToolProber: agentService, AgentHeartbeatProber: agentService, AgentResticInstaller: agentResticInstaller,
		AgentRestore:       agentRestoreService,
		RepositoryCapacity: repositoryCapacityService, TaskPreviewer: taskPreviewService, Alerts: alertService,
		LocalFilesystem: localFilesystemService,
		ProtectionSetup: protectionSetupService,
		ControlPlane:    controlPlaneService, RestoreVerification: restoreVerificationService,
	})
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           apiServer,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Repository maintenance and restore operations can legitimately run for
		// hours. Their managed contexts and graceful shutdown provide the bound.
		WriteTimeout: 0,
		IdleTimeout:  2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dispatcher := scheduler.New(s, taskRunner)
	var backgroundWG sync.WaitGroup
	backgroundWG.Add(1)
	go func() { defer backgroundWG.Done(); dispatcher.Run(ctx, 30*time.Second) }()
	startCapacityMonitor(&backgroundWG, ctx, repositoryCapacityDispatcher, 30*time.Second)
	maintenanceDispatcher := maintenance.New(s, repositoryService)
	backgroundWG.Add(1)
	go func() { defer backgroundWG.Done(); maintenanceDispatcher.Run(ctx, 30*time.Second) }()
	backgroundWG.Add(1)
	go func() { defer backgroundWG.Done(); alertService.Run(ctx, 30*time.Second) }()
	lifecycleDispatcher := lifecycle.NewDispatcher(lifecycleService)
	backgroundWG.Add(1)
	go func() { defer backgroundWG.Done(); lifecycleDispatcher.Run(ctx, 24*time.Hour) }()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("Shadoc listening", "address", cfg.Listen)
		errCh <- server.ListenAndServe()
	}()
	var serveErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpErr := server.Shutdown(shutdownCtx)
	agentHTTPErr := agentService.Close(shutdownCtx)
	jobsErr := apiServer.Shutdown(shutdownCtx)
	backgroundWG.Wait()
	return errors.Join(serveErr, httpErr, agentHTTPErr, jobsErr)
}

func selectLocalRsyncProgram(ctx context.Context, executor command.Executor, goos string, lookPath func(string) (string, error)) (string, error) {
	candidates := make([]string, 0, 3)
	if lookPath != nil {
		if program, err := lookPath("rsync"); err == nil {
			candidates = append(candidates, program)
		}
	}
	if goos == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync")
	}
	seen := make(map[string]bool, len(candidates))
	var probeErrors []error
	for _, program := range candidates {
		program = strings.TrimSpace(program)
		if program == "" || seen[program] {
			continue
		}
		seen[program] = true
		if err := rsync.New(program, executor, "").Probe(ctx); err == nil {
			return program, nil
		} else {
			probeErrors = append(probeErrors, fmt.Errorf("%s: %w", program, err))
		}
	}
	if len(probeErrors) == 0 {
		return "", errors.New("rsync executable was not found")
	}
	return "", fmt.Errorf("rsync 3 or newer is unavailable: %w", errors.Join(probeErrors...))
}

func localToolPath(name string) string {
	program, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return program
}

func selectLocalResticProgram(managedPath, goos string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) string {
	if stat == nil {
		stat = os.Stat
	}
	candidates := []string{managedPath}
	if lookPath != nil {
		if program, err := lookPath("restic"); err == nil {
			candidates = append(candidates, program)
		}
	}
	if goos == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/restic", "/usr/local/bin/restic")
	}
	if goos == "linux" {
		candidates = append(candidates, "/usr/local/bin/restic", "/usr/bin/restic", "/snap/bin/restic")
	}
	seen := make(map[string]bool, len(candidates))
	for _, program := range candidates {
		program = strings.TrimSpace(program)
		if program == "" || seen[program] {
			continue
		}
		seen[program] = true
		info, err := stat(program)
		if err == nil && info.Mode().IsRegular() && (goos == "windows" || info.Mode().Perm()&0o111 != 0) {
			return program
		}
	}
	return managedPath
}

type capacityMonitorRunner interface {
	Run(context.Context, time.Duration)
}

func startCapacityMonitor(wg *sync.WaitGroup, ctx context.Context, monitor capacityMonitorRunner, interval time.Duration) {
	if wg == nil || monitor == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		monitor.Run(ctx, interval)
	}()
}

type interruptedStateStore interface {
	RecoverInterruptedScheduleOccurrences(context.Context, time.Time) (int, error)
	RecoverInterruptedRuns(context.Context, time.Time) (int, error)
	RecoverInterruptedRestoreVerifications(context.Context, time.Time) (int, error)
	RecoverInterruptedProtectionDrafts(context.Context, time.Time) (int, error)
	RecoverInterruptedAgentDrains(context.Context) (int, error)
}

func recoverInterruptedState(ctx context.Context, storage interruptedStateStore, at time.Time) error {
	if _, err := storage.RecoverInterruptedScheduleOccurrences(ctx, at); err != nil {
		return fmt.Errorf("recover interrupted schedule occurrences: %w", err)
	}
	if _, err := storage.RecoverInterruptedRuns(ctx, at); err != nil {
		return fmt.Errorf("recover interrupted task runs: %w", err)
	}
	if _, err := storage.RecoverInterruptedRestoreVerifications(ctx, at); err != nil {
		return fmt.Errorf("recover interrupted restore verifications: %w", err)
	}
	if _, err := storage.RecoverInterruptedProtectionDrafts(ctx, at); err != nil {
		return fmt.Errorf("recover interrupted protection drafts: %w", err)
	}
	if _, err := storage.RecoverInterruptedAgentDrains(ctx); err != nil {
		return fmt.Errorf("recover interrupted Agent drains: %w", err)
	}
	return nil
}

func agentArtifactDirectory(executable func() (string, error)) (string, error) {
	if executable == nil {
		return "", errors.New("resolve control service executable: resolver is required")
	}
	path, err := executable()
	if err != nil {
		return "", fmt.Errorf("resolve control service executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve control service executable directory: %w", err)
	}
	return filepath.Dir(path), nil
}

func runningFromManagedPath(executable, managedBinary string) bool {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(managedBinary) {
		return false
	}
	runningInfo, err := os.Stat(filepath.Clean(executable))
	if err != nil {
		return false
	}
	managedInfo, err := os.Stat(filepath.Clean(managedBinary))
	if err != nil {
		return false
	}
	return os.SameFile(runningInfo, managedInfo)
}
