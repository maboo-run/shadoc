package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/restic"
	runcontrol "github.com/maboo-run/shadoc/internal/run"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type Store interface {
	LoadTaskExecution(context.Context, string) (store.TaskExecution, error)
	StartRun(context.Context, store.RunRecord) error
	FinishRun(context.Context, string, string, time.Time, int, string, map[string]any, string) error
	AppendAudit(context.Context, store.AuditRecord) error
	UpdateRepositoryStatus(context.Context, string, string) error
	SaveSnapshotMetadata(context.Context, string, string, database.SnapshotMetadata, time.Time) error
}
type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}
type Runner interface {
	Execute(context.Context, restic.Operation) (restic.Result, error)
}
type RepositoryLocker interface {
	With(context.Context, string, func() error) error
}

type Service struct {
	store            Store
	secrets          Secrets
	restic           Runner
	mysql            database.Connector
	postgres         database.Connector
	controller       *runcontrol.Controller
	now              func() time.Time
	repositoryLocker RepositoryLocker
	metadataExecutor command.Executor
	databaseExecutor command.Executor
}

func (s *Service) SetRepositoryLocker(locker RepositoryLocker)   { s.repositoryLocker = locker }
func (s *Service) SetMetadataExecutor(executor command.Executor) { s.metadataExecutor = executor }
func (s *Service) SetDatabaseExecutor(executor command.Executor) { s.databaseExecutor = executor }

func New(s Store, secrets Secrets, runner Runner, mysql, postgres database.Connector, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, secrets: secrets, restic: runner, mysql: mysql, postgres: postgres, controller: runcontrol.NewController(2), now: now}
}

func (s *Service) Run(ctx context.Context, taskID, planID, trigger string) (store.RunRecord, error) {
	aggregate, err := s.store.LoadTaskExecution(ctx, taskID)
	if err != nil {
		return store.RunRecord{}, err
	}
	if !aggregate.Task.Enabled {
		return store.RunRecord{}, errors.New("backup task is disabled")
	}
	started := s.now().UTC()
	record := store.RunRecord{ID: newRunID(started), TaskID: taskID, PlanID: planID, Trigger: trigger, Status: "running", StartedAt: started}
	if err := s.store.StartRun(ctx, record); err != nil {
		return store.RunRecord{}, err
	}
	_ = s.store.AppendAudit(ctx, store.AuditRecord{OccurredAt: started, Action: "run.start", TargetType: "task", TargetID: taskID, Detail: map[string]any{"trigger": trigger}})
	var sensitiveValues []string
	controlled := s.controller.Execute(ctx, runcontrol.Request{TaskID: taskID, RepositoryID: aggregate.Repository.ID, MaxAttempts: 3, RetryDelay: time.Second}, func(ctx context.Context) (runcontrol.Status, error) {
		var result restic.Result
		var sensitive []string
		var runErr error
		operation := func() error { result, sensitive, runErr = s.execute(ctx, aggregate); return runErr }
		if s.repositoryLocker != nil {
			runErr = s.repositoryLocker.With(ctx, aggregate.Repository.ID, operation)
		} else {
			runErr = operation()
		}
		record.SnapshotID = result.SnapshotID
		record.Summary = result.Summary
		sensitiveValues = append(sensitiveValues, sensitive...)
		record.RawLog = redact(result.Stdout+"\n"+result.Stderr, sensitive...)
		if runErr != nil {
			return runcontrol.Failed, runErr
		}
		if result.Outcome == restic.Partial {
			return runcontrol.Partial, nil
		}
		return runcontrol.Succeeded, nil
	})
	record.Status = string(controlled.Status)
	record.AttemptCount = controlled.Attempts
	finished := s.now().UTC()
	record.FinishedAt = &finished
	summary := make(map[string]any, len(record.Summary)+2)
	for key, value := range record.Summary {
		summary[key] = value
	}
	record.Summary = summary
	record.Summary["error"] = safeError(controlled.Error, sensitiveValues...)
	if aggregate.Task.ScopeConfirmation.Present() {
		record.Summary["scopeConfirmation"] = aggregate.Task.ScopeConfirmation
	}
	_ = s.store.FinishRun(context.WithoutCancel(ctx), record.ID, record.Status, finished, record.AttemptCount, record.SnapshotID, record.Summary, record.RawLog)
	_ = s.store.AppendAudit(context.WithoutCancel(ctx), store.AuditRecord{OccurredAt: finished, Action: "run.finish", TargetType: "task", TargetID: taskID, Detail: map[string]any{"status": record.Status, "runId": record.ID}})
	return record, controlled.Error
}

func (s *Service) execute(ctx context.Context, a store.TaskExecution) (restic.Result, []string, error) {
	pendingPartial := ""
	if strings.HasPrefix(a.Repository.Status, "unprotected-partial:") {
		pendingPartial = strings.TrimPrefix(a.Repository.Status, "unprotected-partial:")
	}
	if a.Repository.Status != "ready" && pendingPartial == "" {
		return restic.Result{}, nil, fmt.Errorf("repository is not writable: %s", a.Repository.Status)
	}
	repository, sensitive, err := s.repositoryMaterial(ctx, a)
	if err != nil {
		return restic.Result{}, sensitive, err
	}
	if pendingPartial != "" {
		if _, err := s.restic.Execute(ctx, restic.Operation{Kind: restic.TagSnapshot, Repository: repository, Arguments: []string{"--add", "rc:protected-partial", pendingPartial}}); err != nil {
			return restic.Result{}, sensitive, fmt.Errorf("protect pending partial snapshot: %v", err)
		}
		if err := s.store.UpdateRepositoryStatus(ctx, a.Repository.ID, "ready"); err != nil {
			return restic.Result{}, sensitive, err
		}
	}
	if a.Task.Kind == domain.DirectoryTask {
		limits := backupResourceArguments(a.Task.Resources, false)
		result, err := s.restic.Execute(ctx, restic.Operation{Kind: restic.BackupDirectory, Repository: repository, Directory: &restic.DirectoryBackup{Path: a.Task.Directory.Path, Exclusions: a.Task.Directory.Exclusions, SkipIfUnchanged: a.Task.Directory.SkipIfUnchanged, Compression: a.Task.Resources.Compression}, Arguments: limits})
		if err == nil && result.SnapshotID != "" {
			if result.Outcome == restic.Partial {
				if _, tagErr := s.restic.Execute(ctx, restic.Operation{Kind: restic.TagSnapshot, Repository: repository, Arguments: []string{"--add", "rc:protected-partial", result.SnapshotID}}); tagErr != nil {
					_ = s.store.UpdateRepositoryStatus(context.WithoutCancel(ctx), a.Repository.ID, "unprotected-partial:"+result.SnapshotID)
					// Do not expose Temporary() through wrapping: the next run must first tag
					// this exact snapshot instead of creating another backup on retry.
					return result, sensitive, fmt.Errorf("protect partial snapshot: %v", tagErr)
				}
			} else {
				_, _ = s.restic.Execute(ctx, restic.Operation{Kind: restic.TagSnapshot, Repository: repository, Arguments: []string{"--remove", "rc:protected-partial", "--tag", "rc:protected-partial"}})
			}
		}
		return result, sensitive, err
	}
	return s.executeDatabase(ctx, a, repository, sensitive, nil, true)
}

func (s *Service) repositoryMaterial(ctx context.Context, a store.TaskExecution) (restic.Repository, []string, error) {
	password, err := s.secrets.Get(ctx, a.RepositoryPasswordSecretID, "repository-password")
	if err != nil {
		return restic.Repository{}, nil, err
	}
	sensitive := []string{string(password)}
	repository := restic.Repository{Location: a.Repository.Path, Password: string(password)}
	if a.Repository.EffectiveKind() == domain.S3Repository {
		encoded, err := s.secrets.Get(ctx, a.Repository.BackendSecretID, s3backend.CredentialPurpose)
		if err != nil {
			return restic.Repository{}, sensitive, err
		}
		credentials, err := s3backend.DecodeCredentials(encoded)
		clear(encoded)
		if err != nil {
			return restic.Repository{}, sensitive, err
		}
		sensitive = append(sensitive, credentials.AccessKey, credentials.SecretKey)
		repository, err = s3backend.Material(a.Repository, string(password), credentials)
		if err != nil {
			return restic.Repository{}, sensitive, err
		}
	}
	if a.Repository.EffectiveKind() == domain.SFTPRepository {
		if strings.TrimSpace(a.Host.HostFingerprint) == "" {
			return restic.Repository{}, sensitive, errors.New("SSH host key is not pinned")
		}
		key, err := s.secrets.Get(ctx, a.PrivateKeySecretID, "ssh-private-key")
		if err != nil {
			return restic.Repository{}, sensitive, err
		}
		sensitive = append(sensitive, string(key))
		repository = restic.Repository{Location: sftpLocation(a.Host, a.Repository.Path), Password: string(password), SSHPrivateKey: key, SSHPort: a.Host.Port, KnownHosts: []byte(a.Host.HostFingerprint)}
	}
	return repository, sensitive, nil
}

func (s *Service) executeDatabase(ctx context.Context, a store.TaskExecution, repository restic.Repository, sensitive []string, extraArguments []string, indexMetadata bool) (restic.Result, []string, error) {
	if a.DatabaseConnection == nil || a.Task.Database == nil {
		return restic.Result{}, sensitive, errors.New("database connection missing")
	}
	if a.DatabaseConnection.Status != "ready" || a.DatabaseConnection.Preflight.CheckedAt.IsZero() || a.DatabaseConnection.Preflight.Error != "" {
		return restic.Result{}, sensitive, errors.New("database connection preflight is missing or failed")
	}
	dbPassword, err := s.secrets.Get(ctx, a.DatabasePasswordSecretID, "database-backup-password")
	if err != nil {
		return restic.Result{}, sensitive, err
	}
	sensitive = append(sensitive, string(dbPassword))
	connection := databaseConnection(a, string(dbPassword))
	connector := s.mysql
	if connection.Engine == database.PostgreSQL {
		connector = s.postgres
	}
	if connector == nil {
		return restic.Result{}, sensitive, errors.New("database connector is unavailable")
	}
	prepared, metadata, err := connector.PrepareExport(ctx, connection, a.Task.Database.Database)
	if err != nil {
		return restic.Result{}, sensitive, err
	}
	metadataConnector, ok := connector.(database.MetadataConnector)
	if !ok || s.metadataExecutor == nil {
		prepared.Cleanup()
		return restic.Result{}, sensitive, errors.New("database metadata probe is unavailable")
	}
	probe, err := metadataConnector.PrepareMetadata(ctx, connection, a.Task.Database.Database, prepared.CredentialPath)
	if err != nil {
		prepared.Cleanup()
		return restic.Result{}, sensitive, err
	}
	serverFacts, serverErr := s.metadataExecutor.Run(ctx, probe.Server)
	if serverErr != nil || serverFacts.ExitCode != 0 {
		prepared.Cleanup()
		return restic.Result{}, sensitive, fmt.Errorf("query database snapshot metadata: %w", firstExecutionError(serverErr, serverFacts.Stderr))
	}
	clientFacts, clientErr := s.metadataExecutor.Run(ctx, probe.Client)
	if clientErr != nil || clientFacts.ExitCode != 0 {
		prepared.Cleanup()
		return restic.Result{}, sensitive, fmt.Errorf("query database client version: %w", firstExecutionError(clientErr, clientFacts.Stderr))
	}
	facts, err := probe.Parse(serverFacts.Stdout, clientFacts.Stdout)
	if err != nil {
		prepared.Cleanup()
		return restic.Result{}, sensitive, err
	}
	metadata.ServerVersion, metadata.ClientVersion, metadata.Encoding, metadata.Collation = facts.ServerVersion, facts.ClientVersion, facts.Encoding, facts.Collation
	encodedTags, err := database.EncodeMetadataTags(metadata)
	if err != nil {
		prepared.Cleanup()
		return restic.Result{}, sensitive, err
	}
	tags := append([]string{}, extraArguments...)
	for _, tag := range encodedTags {
		tags = append(tags, "--tag", tag)
	}
	tags = append(tags, backupResourceArguments(a.Task.Resources, true)...)
	result, err := s.restic.Execute(ctx, restic.Operation{Kind: restic.BackupCommand, Repository: repository, Command: &prepared.Spec, Filename: metadata.Filename, CommandCleanup: prepared.Cleanup, Arguments: tags})
	if indexMetadata && err == nil && result.Outcome == restic.Success && result.SnapshotID != "" {
		if indexErr := s.store.SaveSnapshotMetadata(ctx, a.Repository.ID, result.SnapshotID, metadata, s.now().UTC()); indexErr != nil {
			return result, sensitive, fmt.Errorf("index database snapshot metadata: %w", indexErr)
		}
	}
	return result, sensitive, err
}

// PreflightDatabaseBackup validates the logical export command without
// copying table data or writing a Restic snapshot. It checks the repository
// read path, then runs the official dump client in schema-only/no-data mode.
func (s *Service) PreflightDatabaseBackup(ctx context.Context, taskID string) error {
	a, err := s.store.LoadTaskExecution(ctx, taskID)
	if err != nil {
		return err
	}
	if a.Task.EffectiveEngine() != domain.ResticEngine || a.Task.Kind != domain.DatabaseTask || a.Task.Database == nil {
		return errors.New("only Restic database tasks support a database backup preflight")
	}
	if a.Repository.Status != "ready" {
		return fmt.Errorf("repository is not writable: %s", a.Repository.Status)
	}
	repository, sensitive, err := s.repositoryMaterial(ctx, a)
	if err != nil {
		return errors.New(safeError(err, sensitive...))
	}
	work := func() error {
		if s.restic == nil {
			return errors.New("Restic verifier is unavailable")
		}
		if _, err := s.restic.Execute(ctx, restic.Operation{Kind: restic.VerifyRepository, Repository: repository}); err != nil {
			return fmt.Errorf("verify Restic repository: %w", err)
		}
		if a.DatabaseConnection == nil || a.DatabaseConnection.Status != "ready" || a.DatabaseConnection.Preflight.CheckedAt.IsZero() || a.DatabaseConnection.Preflight.Error != "" {
			return errors.New("database connection preflight is missing or failed")
		}
		dbPassword, err := s.secrets.Get(ctx, a.DatabasePasswordSecretID, "database-backup-password")
		if err != nil {
			return err
		}
		sensitive = append(sensitive, string(dbPassword))
		connection := databaseConnection(a, string(dbPassword))
		connector := s.mysql
		if connection.Engine == database.PostgreSQL {
			connector = s.postgres
		}
		if connector == nil {
			return errors.New("database connector is unavailable")
		}
		prepared, _, err := connector.PrepareExport(ctx, connection, a.Task.Database.Database)
		if err != nil {
			return err
		}
		defer prepared.Cleanup()
		metadataConnector, ok := connector.(database.MetadataConnector)
		if !ok {
			return errors.New("database metadata preflight is unavailable")
		}
		probe, err := metadataConnector.PrepareMetadata(ctx, connection, a.Task.Database.Database, prepared.CredentialPath)
		if err != nil {
			return err
		}
		metadataExecutor := s.metadataExecutor
		if metadataExecutor == nil {
			metadataExecutor = command.OSExecutor{}
		}
		serverFacts, serverErr := metadataExecutor.Run(ctx, probe.Server)
		if serverErr != nil || serverFacts.ExitCode != 0 {
			return fmt.Errorf("query database snapshot metadata: %w", firstExecutionError(serverErr, serverFacts.Stderr))
		}
		clientFacts, clientErr := metadataExecutor.Run(ctx, probe.Client)
		if clientErr != nil || clientFacts.ExitCode != 0 {
			return fmt.Errorf("query database client version: %w", firstExecutionError(clientErr, clientFacts.Stderr))
		}
		if _, err := probe.Parse(serverFacts.Stdout, clientFacts.Stdout); err != nil {
			return err
		}
		prepared.Spec.Args, err = database.PreflightDumpArguments(connection.Engine, prepared.Spec.Args)
		if err != nil {
			return err
		}
		executor := s.databaseExecutor
		if executor == nil {
			executor = s.metadataExecutor
		}
		if executor == nil {
			executor = command.OSExecutor{}
		}
		prepared.Spec.Stdout = io.Discard
		result, runErr := executor.Run(ctx, prepared.Spec)
		if runErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("database export preflight failed: %w", firstExecutionError(runErr, result.Stderr))
		}
		return nil
	}
	if s.repositoryLocker != nil {
		err = s.repositoryLocker.With(ctx, a.Repository.ID, work)
	} else {
		err = work()
	}
	if err != nil {
		return errors.New(safeError(err, sensitive...))
	}
	return nil
}

func databaseConnection(a store.TaskExecution, password string) database.Connection {
	toolPaths := database.ResolveToolPaths(*a.DatabaseConnection)
	return database.Connection{Engine: database.Engine(a.DatabaseConnection.Engine), Purpose: database.Backup, Network: database.Network(a.DatabaseConnection.Network), Host: a.DatabaseConnection.Host, Port: a.DatabaseConnection.Port, SocketPath: a.DatabaseConnection.SocketPath, Username: a.DatabaseConnection.Username, Password: password, DumpProgram: toolPaths["dump"], AdminProgram: toolPaths["admin"], TLSMode: a.DatabaseConnection.TLS.Mode, TLSCA: a.DatabaseConnection.TLS.CA, TLSClientCert: a.DatabaseConnection.TLS.ClientCert, TLSClientKey: a.DatabaseConnection.TLS.ClientKey, TLSServerName: a.DatabaseConnection.TLS.ServerName}
}

func backupResourceArguments(policy domain.ResourcePolicy, includeCompression bool) []string {
	var arguments []string
	if policy.UploadKiBPerSecond > 0 {
		arguments = append(arguments, "--limit-upload", fmt.Sprint(policy.UploadKiBPerSecond))
	}
	if policy.ReadConcurrency > 0 {
		arguments = append(arguments, "--read-concurrency", fmt.Sprint(policy.ReadConcurrency))
	}
	if includeCompression && policy.Compression != "" {
		arguments = append(arguments, "--compression", policy.Compression)
	}
	return arguments
}

func firstExecutionError(err error, stderr string) error {
	diagnostic := strings.TrimSpace(stderr)
	if len(diagnostic) > 8<<10 {
		diagnostic = diagnostic[:8<<10] + "…"
	}
	if err != nil {
		if diagnostic != "" {
			return fmt.Errorf("%w: %s", err, diagnostic)
		}
		return err
	}
	if diagnostic != "" {
		return errors.New(diagnostic)
	}
	return errors.New("database metadata command failed")
}

func sftpLocation(host domain.RemoteHost, path string) string {
	address := host.Host
	if strings.Contains(address, ":") {
		address = "[" + strings.Trim(address, "[]") + "]"
	}
	return "sftp:" + host.Username + "@" + address + ":" + path
}
func newRunID(now time.Time) string { return fmt.Sprintf("run_%d", now.UnixNano()) }
func redact(value string, sensitive ...string) string {
	value = strings.ReplaceAll(value, "RESTIC_PASSWORD", "[redacted]")
	for _, secret := range sensitive {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	if len(value) > 4<<20 {
		return value[:4<<20]
	}
	return value
}
func safeError(err error, sensitive ...string) string {
	if err == nil {
		return ""
	}
	return redact(err.Error(), sensitive...)
}
