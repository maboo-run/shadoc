package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/config"
	"github.com/maboo-run/shadoc/internal/serviceinstall"
	"github.com/maboo-run/shadoc/internal/servicemigration"
)

type rootMigrator interface {
	Migrate(context.Context) (servicemigration.Result, error)
}

func handleMigrateToRootCommand(ctx context.Context, args []string, stdout io.Writer, migrator rootMigrator) (bool, error) {
	if len(args) == 0 || args[0] != "migrate-to-root" {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("migrate-to-root does not accept arguments")
	}
	if migrator == nil {
		return true, errors.New("root service migrator is unavailable")
	}
	result, err := migrator.Migrate(ctx)
	if err != nil {
		return true, err
	}
	if !result.SourceRemoved {
		return true, errors.New("root migration finished without removing the original user instance")
	}
	_, _ = fmt.Fprintf(stdout, "Shadoc is running as a root system service from %s; the original user service and data were deleted\n", result.TargetDataDir)
	return true, nil
}

func runMigrateToRootCommand() (bool, error) {
	if len(os.Args) < 2 || os.Args[1] != "migrate-to-root" {
		return false, nil
	}
	if err := validateMigrationRuntime(runtime.GOOS, os.Geteuid()); err != nil {
		return true, err
	}
	sourceUser, err := resolveSudoSourceUser(os.Getenv, user.LookupId)
	if err != nil {
		return true, err
	}
	currentExecutable, err := os.Executable()
	if err != nil {
		return true, err
	}
	currentExecutable, err = filepath.Abs(currentExecutable)
	if err != nil {
		return true, err
	}
	currentExecutable, err = filepath.EvalSymlinks(currentExecutable)
	if err != nil {
		return true, fmt.Errorf("resolve current Shadoc executable: %w", err)
	}
	if err := validateMigrationExecutable(currentExecutable); err != nil {
		return true, err
	}
	lock, err := servicemigration.AcquireFileLock(servicemigration.LinuxLockPath, 0)
	if err != nil {
		return true, err
	}
	defer lock.Close()
	source, err := servicemigration.NewLinuxUserSource(sourceUser.username, sourceUser.uid, sourceUser.home, servicemigration.OSNativeRunner{})
	if err != nil {
		return true, err
	}
	targetManager, err := serviceinstall.NewManager(serviceinstall.SystemScope, nil)
	if err != nil {
		return true, err
	}
	health := appinstall.NewHTTPHealthChecker(&http.Client{Timeout: 2 * time.Second}, 250*time.Millisecond)
	target := &rootMigrationTarget{
		services: targetManager, health: health,
		loadState: systemServiceLoadState, unitExists: systemUnitExists,
	}
	mover, err := servicemigration.NewFilesystemMover(config.LinuxSystemDataDir, servicemigration.LinuxStagingDataDir)
	if err != nil {
		return true, err
	}
	markers, err := servicemigration.NewFileMarkerStore(servicemigration.LinuxMarkerPath, 0)
	if err != nil {
		return true, err
	}
	migrator := servicemigration.New(
		source, target, mover, markers, currentExecutable,
		config.LinuxSystemDataDir, servicemigration.LinuxStagingDataDir,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return handleMigrateToRootCommand(ctx, os.Args[1:], os.Stdout, migrator)
}

func validateMigrationExecutable(path string) error {
	if err := appinstall.VerifyInstalledBinary(path); err != nil {
		return fmt.Errorf("verify installed Shadoc before root migration: %w", err)
	}
	return nil
}

func validateMigrationRuntime(goos string, euid int) error {
	if goos != "linux" {
		return fmt.Errorf("migrate-to-root is unsupported on %s", goos)
	}
	if euid != 0 {
		return errors.New("migrate-to-root must be run with sudo")
	}
	return nil
}

type sudoSourceUser struct {
	username string
	uid      int
	home     string
}

type lookupUserByID func(string) (*user.User, error)

var linuxUsernamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*[$]?$`)

func resolveSudoSourceUser(getenv func(string) string, lookup lookupUserByID) (sudoSourceUser, error) {
	if getenv == nil || lookup == nil {
		return sudoSourceUser{}, errors.New("sudo source user lookup is unavailable")
	}
	username := getenv("SUDO_USER")
	uidText := getenv("SUDO_UID")
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid <= 0 || username == "" || username == "root" || !linuxUsernamePattern.MatchString(username) {
		return sudoSourceUser{}, errors.New("migrate-to-root requires sudo from a non-root user")
	}
	account, err := lookup(uidText)
	if err != nil {
		return sudoSourceUser{}, fmt.Errorf("look up sudo source user: %w", err)
	}
	accountUID, err := strconv.Atoi(account.Uid)
	if err != nil || accountUID != uid || account.Username != username || !filepath.IsAbs(account.HomeDir) ||
		filepath.Clean(account.HomeDir) == string(filepath.Separator) {
		return sudoSourceUser{}, errors.New("SUDO_USER and SUDO_UID do not identify one safe local account")
	}
	return sudoSourceUser{username: username, uid: uid, home: filepath.Clean(account.HomeDir)}, nil
}

type migrationServiceManager interface {
	Start(string, []string) error
	Status() (string, error)
	Uninstall() error
}

type migrationHealthChecker interface {
	Wait(context.Context, string) error
}

type rootMigrationTarget struct {
	services   migrationServiceManager
	health     migrationHealthChecker
	loadState  func() (string, error)
	unitExists func(string) (bool, error)
}

func (t *rootMigrationTarget) Prepare() error {
	if t == nil || t.services == nil || t.loadState == nil || t.unitExists == nil {
		return errors.New("root system service manager is unavailable")
	}
	for _, path := range []string{
		"/etc/systemd/system/shadoc.service",
		"/run/systemd/system/shadoc.service",
		"/usr/local/lib/systemd/system/shadoc.service",
		"/usr/lib/systemd/system/shadoc.service",
		"/lib/systemd/system/shadoc.service",
	} {
		exists, err := t.unitExists(path)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("root system service already exists at %s", path)
		}
	}
	state, err := t.loadState()
	if err != nil {
		return err
	}
	if state != "not-found" {
		return fmt.Errorf("root system service already exists with load state %s", state)
	}
	return nil
}

func systemUnitExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

func systemServiceLoadState() (string, error) {
	output, err := exec.Command("systemctl", "show", "--property=LoadState", "--value", "shadoc.service").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect root system service: %w: %s", err, strings.TrimSpace(string(output)))
	}
	state := strings.TrimSpace(string(output))
	if state == "" {
		return "", errors.New("inspect root system service returned an empty load state")
	}
	return state, nil
}

func (t *rootMigrationTarget) Install(executable string, arguments []string) error {
	if t == nil || t.services == nil {
		return errors.New("root system service manager is unavailable")
	}
	return t.services.Start(executable, arguments)
}

func (t *rootMigrationTarget) Healthy(ctx context.Context, listen string) error {
	if t == nil || t.services == nil || t.health == nil {
		return errors.New("root system service health checker is unavailable")
	}
	status, err := t.services.Status()
	if err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("root system service is %s", status)
	}
	return t.health.Wait(ctx, lifecycleHealthURL(listen))
}

func (t *rootMigrationTarget) Uninstall() error {
	if t == nil || t.services == nil {
		return nil
	}
	return t.services.Uninstall()
}
