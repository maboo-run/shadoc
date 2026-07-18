package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/serviceinstall"
	"github.com/maboo-run/shadoc/internal/store"
)

type managedApplicationInstaller interface {
	UpdateWithReporter(context.Context, string, appinstall.UpdateReporter) error
}

type managedUpdatePersistence interface {
	Operation(context.Context, string) (store.OperationRecord, error)
	UpdateOperationStage(context.Context, string, string, map[string]any) error
	FinishOperation(context.Context, string, string, string, time.Time, string, map[string]any) error
	AppendAudit(context.Context, store.AuditRecord) error
}

type managedUpdateReporter struct {
	store             managedUpdatePersistence
	id                string
	rollbackAttempted bool
	rollbackVerified  bool
}

func (r *managedUpdateReporter) Stage(stage string) error {
	if err := r.store.UpdateOperationStage(context.Background(), r.id, stage, nil); err != nil {
		return err
	}
	if stage == "rolling_back" || stage == "verifying_rollback" || stage == "rollback_verified" {
		r.rollbackAttempted = true
	}
	if stage == "rollback_verified" {
		r.rollbackVerified = true
	}
	return nil
}

func runManagedUpdateCommand() (bool, error) {
	if len(os.Args) < 2 || os.Args[1] != "managed-update" {
		return false, nil
	}
	flags := flag.NewFlagSet("managed-update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	operationID := flags.String("operation-id", "", "managed operation identity")
	version := flags.String("version", "", "verified release version")
	dataDir := flags.String("data-dir", "", "application data directory")
	listen := flags.String("listen", "", "control service listen address")
	if err := flags.Parse(os.Args[2:]); err != nil || flags.NArg() != 0 {
		return true, errors.New("invalid managed update arguments")
	}
	if !appinstall.ValidManagedOperationID(*operationID) || !appinstall.ValidReleaseVersion(*version) || !filepath.IsAbs(*dataDir) {
		return true, errors.New("invalid managed update identity, version, or data directory")
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return true, errors.New("invalid managed update listen address")
	}
	executable, err := os.Executable()
	if err != nil {
		return true, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return true, err
	}
	binary := existingManagedApplicationBinary(*dataDir)
	if !runningFromManagedPath(executable, binary) {
		return true, errors.New("managed updater is not running from the installed application path")
	}
	databasePath := filepath.Join(filepath.Clean(*dataDir), "shadoc.db")
	if _, statErr := os.Stat(databasePath); errors.Is(statErr, os.ErrNotExist) {
		legacyPath := filepath.Join(filepath.Clean(*dataDir), "restic-control.db")
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			databasePath = legacyPath
		}
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return true, err
	}
	defer database.Close()
	client := &http.Client{Timeout: 5 * time.Minute}
	releases := appinstall.NewGitHubRelease(client, appinstall.OfficialReleasesAPI, runtime.GOOS, runtime.GOARCH)
	health := appinstall.NewHTTPHealthChecker(client, 250*time.Millisecond)
	installer := appinstall.New(releases, serviceinstall.Manager{}, health, appinstall.Paths{
		Binary: binary, Previous: binary + ".previous", DataDir: filepath.Clean(*dataDir), HealthURL: lifecycleHealthURL(*listen), Companions: agentdeploy.ArtifactFilenames(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	return true, performManagedUpdate(ctx, database, installer, *operationID, *version, time.Now)
}

func performManagedUpdate(ctx context.Context, persistence managedUpdatePersistence, installer managedApplicationInstaller, operationID, version string, now func() time.Time) error {
	if persistence == nil || installer == nil || now == nil {
		return errors.New("managed update dependencies are unavailable")
	}
	record, err := persistence.Operation(ctx, operationID)
	if err != nil {
		return err
	}
	if record.Kind != "application_update" || record.Status != "running" || record.Target != version {
		return errors.New("managed update operation does not match the requested release")
	}
	reporter := &managedUpdateReporter{store: persistence, id: operationID}
	updateErr := installer.UpdateWithReporter(ctx, version, reporter)
	finishedAt := now().UTC()
	status, stage, summary := "success", "completed", ""
	detail := map[string]any{"targetVersion": version, "healthVerified": true, "rollbackAttempted": false, "rollbackVerified": false}
	if updateErr != nil {
		status, stage = "failed", "failed"
		rollbackAttempted := reporter.rollbackAttempted
		if reporter.rollbackVerified {
			stage = "rolled_back"
			summary = "应用升级未完成；系统已自动恢复并验证旧版本"
		} else if rollbackAttempted {
			stage = "rollback_failed"
			summary = "应用升级未完成；自动回滚未完成或未通过健康检查，需要人工检查"
		} else {
			summary = "应用升级未完成；旧版本仍被保留"
		}
		detail["healthVerified"] = false
		detail["rollbackAttempted"] = rollbackAttempted
		detail["rollbackVerified"] = reporter.rollbackVerified
	}
	finishErr := persistence.FinishOperation(context.Background(), operationID, status, stage, finishedAt, summary, detail)
	if finishErr == nil {
		finishErr = persistence.AppendAudit(context.Background(), store.AuditRecord{OccurredAt: finishedAt, Actor: record.Actor, Action: "application.update.finish", TargetType: "application", TargetID: version, Detail: map[string]any{"operationId": operationID, "status": status, "stage": stage}})
	}
	if updateErr != nil {
		return errors.Join(updateErr, finishErr)
	}
	return finishErr
}
