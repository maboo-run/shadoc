package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/maboo-run/shadoc/internal/agentcontrol"
	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/agentrestore"
	"github.com/maboo-run/shadoc/internal/agentservice"
	"github.com/maboo-run/shadoc/internal/agenttool"
	"github.com/maboo-run/shadoc/internal/alerting"
	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/controlplane"
	databaseverify "github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/dbrestore"
	diagnosticservice "github.com/maboo-run/shadoc/internal/diagnostics"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/lifecycle"
	"github.com/maboo-run/shadoc/internal/localfilesystem"
	"github.com/maboo-run/shadoc/internal/ntfy"
	operationruntime "github.com/maboo-run/shadoc/internal/operation"
	"github.com/maboo-run/shadoc/internal/protectionsetup"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/schedule"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/sshhost"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/taskpreview"
	"github.com/maboo-run/shadoc/internal/vault"
	"github.com/maboo-run/shadoc/internal/webhook"
	"github.com/maboo-run/shadoc/internal/webui"
)

type initializationStore interface {
	IsInitialized(context.Context) (bool, error)
}

// Server is the complete public HTTP interface. Dependencies are injected so
// HTTP behavior can be exercised without starting a process or opening ports.
type Server struct {
	mux                  *http.ServeMux
	store                initializationStore
	auth                 *auth.Manager
	secrets              *secret.Manager
	log                  *slog.Logger
	runner               taskRunner
	repositories         repositoryManager
	probe                *compat.Probe
	paths                compat.ToolPaths
	installer            resticInstaller
	selectRestic         func(string)
	databaseRestore      databaseRestoreManager
	databaseVerifier     databaseverify.Verifier
	databaseEnumerator   databaseverify.Enumerator
	localFilesystem      localFilesystemManager
	protectionSetup      protectionSetupManager
	ntfy                 *ntfy.Client
	webhook              webhookPublisher
	dataDir              string
	background           context.Context
	cancel               context.CancelFunc
	jobs                 sync.WaitGroup
	jobsMu               sync.Mutex
	manualMu             sync.Mutex
	manualRuns           map[string]string
	closing              bool
	setupToken           string
	vault                vaultManager
	lifecycle            lifecycleManager
	operations           *operationruntime.Manager
	applicationVersion   string
	applicationReleases  applicationReleaseCatalog
	applicationUpdater   applicationUpdateLauncher
	applicationOps       applicationUpdateOperations
	agents               *agentcontrol.Service
	agentDeployer        agentDeployer
	agentUninstaller     agentUninstaller
	agentUpgrader        agentUpgrader
	agentToolProber      agentToolProber
	agentResticInstaller agentResticInstaller
	agentService         agentServiceManager
	agentRestore         agentDirectoryRestoreManager
	repositoryCapacity   repositoryCapacityProber
	taskPreviewer        taskScopePreviewer
	alerts               *alerting.Service
	controlPlane         controlPlaneRecoveryManager
	restoreVerification  restoreVerificationManager
	diagnostics          *diagnosticservice.Service
}

type taskRunner interface {
	Run(context.Context, string, string, string) (store.RunRecord, error)
}
type agentDeployer interface {
	Deploy(context.Context, agentdeploy.DeployRequest, agentdeploy.StageReporter) (agentdeploy.DeployResult, error)
}
type agentUninstaller interface {
	Uninstall(context.Context, string, agentdeploy.StageReporter) (agentdeploy.RemovalResult, error)
}
type agentUpgrader interface {
	Upgrade(context.Context, agentdeploy.UpgradeRequest, agentdeploy.StageReporter) (agentdeploy.UpgradeResult, error)
}
type agentToolProber interface {
	ReprobeTools(context.Context, string, agentdeploy.StageReporter) (agentdeploy.ToolProbeResult, error)
}
type agentResticInstaller interface {
	InstallRestic(context.Context, agenttool.InstallRequest, agenttool.StageReporter) (agenttool.InstallResult, error)
}
type agentServiceManager interface {
	Status() agentservice.Status
	Configure(context.Context, agentservice.Settings) (agentservice.Status, error)
	CreateEnrollmentToken(context.Context, time.Duration) (string, string, error)
	Deploy(context.Context, agentdeploy.DeployRequest, agentdeploy.StageReporter) (agentdeploy.DeployResult, error)
}
type repositoryCapacityProber interface {
	Probe(context.Context, string, repositorycapacity.StageReporter) (domain.RepositoryCapacity, error)
}
type taskScopePreviewer interface {
	Preview(context.Context, string) (store.TaskScopePreview, error)
}
type localFilesystemManager interface {
	Settings() localfilesystem.Settings
	SaveSettings(context.Context, []string) (localfilesystem.Settings, error)
	Browse(context.Context, string) (localfilesystem.BrowseResult, error)
	CreateDirectory(context.Context, string) error
}
type protectionSetupManager interface {
	CreateTemplate(context.Context, protectionsetup.TemplateInput) (protectionsetup.Template, error)
	ListTemplates(context.Context) ([]protectionsetup.Template, error)
	CreateDraft(context.Context, protectionsetup.CreateDraftInput) (protectionsetup.Draft, error)
	Draft(context.Context, string) (protectionsetup.Draft, error)
	ListDrafts(context.Context) ([]protectionsetup.Draft, error)
	Apply(context.Context, string, protectionsetup.StageReporter) (protectionsetup.Draft, error)
	Cancel(context.Context, string) (protectionsetup.Draft, error)
}
type repositoryManager interface {
	Initialize(context.Context, string) error
	Snapshots(context.Context, string) ([]repositoryservice.Snapshot, error)
	Maintain(context.Context, string, domain.RetentionPolicy, bool) error
	RotatePassword(context.Context, string, string) error
	PasswordRotationStatus(context.Context, string) (repositoryservice.PasswordRotationStatus, error)
	RevokeOldPassword(context.Context, string) error
	RestoreDirectory(context.Context, string, string, string, []string, int) error
}
type repositoryConnector interface {
	ConnectExisting(context.Context, domain.Repository, string, *s3backend.Credentials) ([]repositoryservice.Snapshot, error)
	VerifyExisting(context.Context, string) ([]repositoryservice.Snapshot, error)
}
type repositoryMaintenancePreviewer interface {
	PreviewMaintenance(context.Context, string, domain.RetentionPolicy) (repositoryservice.MaintenanceSummary, error)
}
type directoryRestorePreflighter interface {
	PreflightDirectoryRestore(context.Context, string, string, string, []string) (repositoryservice.DirectoryRestorePreflight, error)
}
type directoryRestoreSelectionPreflighter interface {
	PreflightDirectoryRestoreSelection(context.Context, string, string, []string) (repositoryservice.DirectoryRestorePreflight, error)
}
type agentDirectoryRestoreManager interface {
	PreflightTarget(context.Context, string, string, string) error
	Restore(context.Context, agentrestore.Request) error
}
type snapshotContentBrowser interface {
	BrowseSnapshotContents(context.Context, string, string, repositoryservice.SnapshotContentsQuery) (repositoryservice.SnapshotContentsPage, error)
	CompareSnapshots(context.Context, string, string, string, repositoryservice.SnapshotDiffQuery) (repositoryservice.SnapshotDiff, error)
}
type databaseRestoreManager interface {
	Restore(context.Context, dbrestore.Request) error
}
type databaseRestorePreflighter interface {
	Preflight(context.Context, dbrestore.Request) (dbrestore.PreflightResult, error)
}
type resticInstaller interface {
	Versions(context.Context) ([]string, error)
	Install(context.Context, string) (string, error)
}
type vaultManager interface {
	Status() secret.VaultStatus
	LockOnRestart(string) error
	Unlock(string) error
	Automatic() error
}
type lifecycleManager interface {
	Policy(context.Context) (lifecycle.Policy, error)
	SavePolicy(context.Context, lifecycle.Policy, time.Time) error
	CleanupConfigured(context.Context, time.Time) (lifecycle.Report, error)
	PreviewConfigured(context.Context, time.Time) (lifecycle.Report, error)
}
type controlPlaneRecoveryManager interface {
	Export(context.Context, string) ([]byte, error)
	PreflightImport(context.Context, []byte, string) (controlplane.ImportPreview, error)
	Import(context.Context, []byte, string, string) (controlplane.ImportResult, error)
}
type restoreVerificationManager interface {
	Run(context.Context, string, string) (store.RestoreVerificationRecord, error)
	Cleanup(context.Context, string) (store.RestoreVerificationRecord, error)
}
type webhookPublisher interface {
	Publish(context.Context, webhook.Config, webhook.Event) error
}
type applicationReleaseCatalog interface {
	Latest(context.Context) (appinstall.ReleaseInfo, error)
}
type applicationUpdateLauncher interface {
	Managed() bool
	Launch(context.Context, string, string) error
}
type applicationUpdateOperations interface {
	CreateOperation(context.Context, store.OperationRecord) error
	StartOperation(context.Context, string, string, time.Time) error
	FinishOperation(context.Context, string, string, string, time.Time, string, map[string]any) error
	ActiveApplicationUpdate(context.Context) (store.OperationRecord, error)
}
type Runtime struct {
	Runner               taskRunner
	Repositories         repositoryManager
	Paths                compat.ToolPaths
	Installer            resticInstaller
	SelectRestic         func(string)
	DatabaseRestore      databaseRestoreManager
	DatabaseVerifier     databaseverify.Verifier
	DatabaseEnumerator   databaseverify.Enumerator
	LocalFilesystem      localFilesystemManager
	ProtectionSetup      protectionSetupManager
	Ntfy                 *ntfy.Client
	Webhook              webhookPublisher
	DataDir              string
	SetupToken           string
	Vault                vaultManager
	Lifecycle            lifecycleManager
	ApplicationVersion   string
	ApplicationReleases  applicationReleaseCatalog
	ApplicationUpdater   applicationUpdateLauncher
	Agents               *agentcontrol.Service
	AgentDeployer        agentDeployer
	AgentUninstaller     agentUninstaller
	AgentUpgrader        agentUpgrader
	AgentToolProber      agentToolProber
	AgentResticInstaller agentResticInstaller
	AgentService         agentServiceManager
	AgentRestore         agentDirectoryRestoreManager
	RepositoryCapacity   repositoryCapacityProber
	TaskPreviewer        taskScopePreviewer
	Alerts               *alerting.Service
	ControlPlane         controlPlaneRecoveryManager
	RestoreVerification  restoreVerificationManager
}

func New(s *store.Store) *Server {
	return NewWithAuth(s, auth.New(s, time.Now))
}

func NewWithAuth(s *store.Store, manager *auth.Manager) *Server {
	return NewWithDependencies(s, manager, nil)
}

func NewWithDependencies(s *store.Store, manager *auth.Manager, secrets *secret.Manager) *Server {
	return NewWithRuntime(s, manager, secrets, Runtime{})
}

func NewWithRuntime(s *store.Store, manager *auth.Manager, secrets *secret.Manager, runtime Runtime) *Server {
	background, cancel := context.WithCancel(context.Background())
	operations, err := operationruntime.New(s, background, time.Now, nil)
	if err != nil {
		cancel()
		panic("initialize operation runtime: " + err.Error())
	}
	verifier := runtime.DatabaseVerifier
	tempRoot := os.TempDir()
	if runtime.DataDir != "" {
		tempRoot = filepath.Join(runtime.DataDir, "tmp")
	}
	if verifier == nil {
		verifier = databaseverify.SystemVerifier{Executor: command.OSExecutor{}, TempRoot: tempRoot}
	}
	enumerator := runtime.DatabaseEnumerator
	if enumerator == nil {
		enumerator = databaseverify.SystemEnumerator{Executor: command.OSExecutor{OutputLimit: 256 << 10}, TempRoot: tempRoot}
	}
	alertService := runtime.Alerts
	if alertService == nil {
		alertService = alerting.New(s, time.Now)
	}
	srv := &Server{
		mux:                http.NewServeMux(),
		store:              s,
		auth:               manager,
		secrets:            secrets,
		log:                slog.Default(),
		runner:             runtime.Runner,
		repositories:       runtime.Repositories,
		probe:              compat.NewProbe(command.OSExecutor{}),
		paths:              runtime.Paths,
		installer:          runtime.Installer,
		selectRestic:       runtime.SelectRestic,
		databaseRestore:    runtime.DatabaseRestore,
		databaseVerifier:   verifier,
		databaseEnumerator: enumerator,
		localFilesystem:    runtime.LocalFilesystem,
		protectionSetup:    runtime.ProtectionSetup,
		ntfy:               runtime.Ntfy,
		webhook:            runtime.Webhook,
		dataDir:            runtime.DataDir,
		background:         background, cancel: cancel,
		manualRuns:           make(map[string]string),
		setupToken:           runtime.SetupToken,
		vault:                runtime.Vault,
		lifecycle:            runtime.Lifecycle,
		operations:           operations,
		applicationVersion:   runtime.ApplicationVersion,
		applicationReleases:  runtime.ApplicationReleases,
		applicationUpdater:   runtime.ApplicationUpdater,
		applicationOps:       s,
		agents:               runtime.Agents,
		agentDeployer:        runtime.AgentDeployer,
		agentUninstaller:     runtime.AgentUninstaller,
		agentUpgrader:        runtime.AgentUpgrader,
		agentToolProber:      runtime.AgentToolProber,
		agentResticInstaller: runtime.AgentResticInstaller,
		agentService:         runtime.AgentService,
		agentRestore:         runtime.AgentRestore,
		repositoryCapacity:   runtime.RepositoryCapacity,
		taskPreviewer:        runtime.TaskPreviewer,
		alerts:               alertService,
		controlPlane:         runtime.ControlPlane,
		restoreVerification:  runtime.RestoreVerification,
		diagnostics:          diagnosticservice.New(s, time.Now),
	}
	srv.routes()
	return srv
}
func (s *Server) Shutdown(ctx context.Context) error {
	s.jobsMu.Lock()
	s.closing = true
	s.cancel()
	s.jobsMu.Unlock()
	s.operations.Close()
	done := make(chan struct{})
	go func() { s.jobs.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) beginManagedJob(w http.ResponseWriter) (context.Context, func(), bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.closing {
		writeError(w, http.StatusServiceUnavailable, "服务正在停止")
		return nil, func() {}, false
	}
	s.jobs.Add(1)
	return s.background, s.jobs.Done, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	auditActor := ""
	if cookie, err := r.Cookie("rc_session"); err == nil {
		auditActor, _ = s.auth.Authenticate(r.Context(), cookie.Value)
	}
	if s.vault != nil && s.vault.Status().Locked && vaultProtectedPath(r.URL.Path) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if _, ok := s.requireMutationSession(recorder, r); !ok {
				return
			}
		} else if _, ok := s.requireSession(recorder, r); !ok {
			return
		}
		writeError(recorder, http.StatusLocked, "秘密库已锁定，请先解锁")
		return
	}
	s.mux.ServeHTTP(recorder, r)
	if (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete) && recorder.status < 400 && r.URL.Path != "/api/setup" && r.URL.Path != "/api/login" {
		if resources, ok := s.store.(*store.Store); ok {
			_ = resources.AppendAudit(context.WithoutCancel(r.Context()), store.AuditRecord{OccurredAt: time.Now().UTC(), Actor: auditActor, Action: r.Method, TargetType: "http", TargetID: r.URL.Path, Detail: map[string]any{"status": recorder.status}})
		}
	}
}

func (s *Server) appendSemanticAudit(ctx context.Context, actor, action, targetType, targetID string, detail map[string]any) {
	if resources, ok := s.store.(*store.Store); ok {
		_ = resources.AppendAudit(context.WithoutCancel(ctx), store.AuditRecord{OccurredAt: time.Now().UTC(), Actor: actor, Action: action, TargetType: targetType, TargetID: targetID, Detail: detail})
	}
}

func vaultProtectedPath(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	switch path {
	case "/api/health", "/api/setup/status", "/api/setup", "/api/login", "/api/session", "/api/logout", "/api/compatibility", "/api/diagnostics/export", "/api/vault/status", "/api/vault/unlock":
		return false
	default:
		return true
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /api/application/version", s.applicationVersionInfo)
	s.mux.HandleFunc("GET /api/application/releases", s.applicationReleaseInfo)
	s.mux.HandleFunc("POST /api/application/update", s.startApplicationUpdate)
	s.mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	s.mux.HandleFunc("POST /api/setup", s.setup)
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("GET /api/session", s.currentSession)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET /api/vault/status", s.vaultStatus)
	s.mux.HandleFunc("POST /api/vault/lock-on-restart", s.vaultLockOnRestart)
	s.mux.HandleFunc("POST /api/vault/unlock", s.vaultUnlock)
	s.mux.HandleFunc("POST /api/vault/automatic", s.vaultAutomatic)
	s.mux.HandleFunc("POST /api/control-plane/export", s.exportControlPlane)
	s.mux.HandleFunc("POST /api/control-plane/import/preflight", s.preflightControlPlaneImport)
	s.mux.HandleFunc("POST /api/control-plane/import", s.importControlPlane)
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("GET /api/agent-service", s.agentServiceStatus)
	s.mux.HandleFunc("PUT /api/agent-service", s.configureAgentService)
	s.mux.HandleFunc("POST /api/agents/enrollment-token", s.createAgentEnrollmentToken)
	s.mux.HandleFunc("POST /api/agents/deploy", s.deployAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/revoke", s.revokeAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/uninstall", s.uninstallAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/upgrade", s.upgradeAgent)
	s.mux.HandleFunc("POST /api/agents/{id}/tools/reprobe", s.reprobeAgentTools)
	s.mux.HandleFunc("POST /api/agents/{id}/restic/install", s.installAgentRestic)
	s.mux.HandleFunc("POST /api/agents/{id}/filesystem/browse", s.browseAgentFilesystem)
	s.mux.HandleFunc("POST /api/agents/{id}/filesystem/directories", s.createAgentDirectory)
	s.mux.HandleFunc("GET /api/local-filesystem/settings", s.localFilesystemSettings)
	s.mux.HandleFunc("PUT /api/local-filesystem/settings", s.saveLocalFilesystemSettings)
	s.mux.HandleFunc("POST /api/local-filesystem/browse", s.browseLocalFilesystem)
	s.mux.HandleFunc("POST /api/local-filesystem/directories", s.createLocalDirectory)
	s.mux.HandleFunc("GET /api/protection-templates", s.listProtectionTemplates)
	s.mux.HandleFunc("POST /api/protection-templates", s.createProtectionTemplate)
	s.mux.HandleFunc("DELETE /api/protection-templates/{id}", s.deleteProtectionTemplate)
	s.mux.HandleFunc("GET /api/protection-drafts", s.listProtectionDrafts)
	s.mux.HandleFunc("POST /api/protection-drafts", s.createProtectionDraft)
	s.mux.HandleFunc("GET /api/protection-drafts/{id}", s.getProtectionDraft)
	s.mux.HandleFunc("GET /api/protection-drafts/{id}/checklist", s.protectionChecklist)
	s.mux.HandleFunc("POST /api/protection-drafts/{id}/apply", s.applyProtectionDraft)
	s.mux.HandleFunc("POST /api/protection-drafts/{id}/cancel", s.cancelProtectionDraft)
	s.mux.HandleFunc("GET /api/lifecycle-policy", s.lifecyclePolicy)
	s.mux.HandleFunc("PUT /api/lifecycle-policy", s.saveLifecyclePolicy)
	s.mux.HandleFunc("POST /api/lifecycle/cleanup", s.cleanupLifecycle)
	s.mux.HandleFunc("POST /api/lifecycle/cleanup/preview", s.previewLifecycleCleanup)
	s.mux.HandleFunc("GET /api/remote-hosts", s.listRemoteHosts)
	s.mux.HandleFunc("POST /api/remote-hosts", s.createRemoteHost)
	s.mux.HandleFunc("GET /api/remote-hosts/{id}/ssh-public-key", s.remoteHostPublicKey)
	s.mux.HandleFunc("POST /api/remote-hosts/{id}/connection-test", s.testRemoteHostConnection)
	s.mux.HandleFunc("POST /api/ssh/host-key", s.probeSSHHostKey)
	s.mux.HandleFunc("GET /api/remote-hosts/{id}", s.getRemoteHost)
	s.mux.HandleFunc("PUT /api/remote-hosts/{id}", s.updateRemoteHost)
	s.mux.HandleFunc("DELETE /api/remote-hosts/{id}", s.deleteRemoteHost)
	s.mux.HandleFunc("GET /api/repositories", s.listRepositories)
	s.mux.HandleFunc("POST /api/repositories", s.createRepository)
	s.mux.HandleFunc("POST /api/repositories/connect", s.connectExistingRepository)
	s.mux.HandleFunc("GET /api/repositories/{id}", s.getRepository)
	s.mux.HandleFunc("PUT /api/repositories/{id}", s.updateRepository)
	s.mux.HandleFunc("DELETE /api/repositories/{id}", s.deleteRepository)
	s.mux.HandleFunc("GET /api/database-connections", s.listDatabaseConnections)
	s.mux.HandleFunc("POST /api/database-connections", s.createDatabaseConnection)
	s.mux.HandleFunc("POST /api/database-connections/temporary", s.createTemporaryDatabaseConnection)
	s.mux.HandleFunc("GET /api/database-connections/{id}", s.getDatabaseConnection)
	s.mux.HandleFunc("PUT /api/database-connections/{id}", s.updateDatabaseConnection)
	s.mux.HandleFunc("DELETE /api/database-connections/{id}", s.deleteDatabaseConnection)
	s.mux.HandleFunc("POST /api/database-connections/{id}/databases", s.listLogicalDatabases)
	s.mux.HandleFunc("GET /api/tasks", s.listTasks)
	s.mux.HandleFunc("POST /api/tasks", s.createTask)
	s.mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	s.mux.HandleFunc("PUT /api/tasks/{id}", s.updateTask)
	s.mux.HandleFunc("POST /api/tasks/{id}/preview", s.previewTaskScope)
	s.mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	s.mux.HandleFunc("GET /api/tasks/{id}/restore-verification-policy", s.getRestoreVerificationPolicy)
	s.mux.HandleFunc("DELETE /api/tasks/{id}/restore-verification-policy", s.deleteRestoreVerificationPolicy)
	s.mux.HandleFunc("GET /api/plans", s.listPlans)
	s.mux.HandleFunc("POST /api/plans", s.createPlan)
	s.mux.HandleFunc("GET /api/plans/{id}", s.getPlan)
	s.mux.HandleFunc("PUT /api/plans/{id}", s.updatePlan)
	s.mux.HandleFunc("DELETE /api/plans/{id}", s.deletePlan)
	s.mux.HandleFunc("GET /api/compatibility", s.compatibility)
	s.mux.HandleFunc("GET /api/diagnostics/export", s.exportDiagnostics)
	s.mux.HandleFunc("POST /api/tasks/{id}/run", s.runTask)
	s.mux.HandleFunc("GET /api/activity", s.listActivity)
	s.mux.HandleFunc("GET /api/activity/export", s.exportActivity)
	s.mux.HandleFunc("GET /api/task-trends", s.taskTrends)
	s.mux.HandleFunc("GET /api/runs", s.listRuns)
	s.mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	s.mux.HandleFunc("GET /api/runs/{id}/log", s.getRunLog)
	s.mux.HandleFunc("GET /api/operations", s.listOperations)
	s.mux.HandleFunc("GET /api/operations/{id}", s.getOperation)
	s.mux.HandleFunc("GET /api/restore-verifications", s.listRestoreVerifications)
	s.mux.HandleFunc("GET /api/restore-verifications/{id}", s.getRestoreVerification)
	s.mux.HandleFunc("POST /api/restore-verifications/{id}/cleanup", s.cleanupRestoreVerification)
	s.mux.HandleFunc("POST /api/operations/{id}/cancel", s.cancelOperation)
	s.mux.HandleFunc("POST /api/operations/{id}/cleanup/preflight", s.preflightOperationCleanup)
	s.mux.HandleFunc("POST /api/operations/{id}/cleanup", s.cleanupOperation)
	s.mux.HandleFunc("GET /api/delete-previews/{resource}/{id}", s.previewResourceDelete)
	s.mux.HandleFunc("POST /api/delete-previews/{resource}/{id}/confirm", s.confirmResourceDelete)
	s.mux.HandleFunc("GET /api/audits", s.listAudits)
	s.mux.HandleFunc("GET /api/audits/export", s.exportAudits)
	s.mux.HandleFunc("GET /api/dashboard", s.dashboard)
	s.mux.HandleFunc("GET /api/alerts", s.listAlerts)
	s.mux.HandleFunc("POST /api/repositories/{id}/initialize", s.initializeRepository)
	s.mux.HandleFunc("POST /api/repositories/{id}/verify-existing", s.verifyExistingRepository)
	s.mux.HandleFunc("POST /api/repositories/{id}/capacity", s.probeRepositoryCapacity)
	s.mux.HandleFunc("GET /api/repositories/{id}/capacity-policy", s.getRepositoryCapacityPolicy)
	s.mux.HandleFunc("PUT /api/repositories/{id}/capacity-policy", s.saveRepositoryCapacityPolicy)
	s.mux.HandleFunc("GET /api/repositories/{id}/capacity-samples", s.listRepositoryCapacitySamples)
	s.mux.HandleFunc("GET /api/repositories/{id}/capacity-forecast", s.getRepositoryCapacityForecast)
	s.mux.HandleFunc("GET /api/repositories/{id}/snapshots", s.repositorySnapshots)
	s.mux.HandleFunc("GET /api/repositories/{id}/snapshots/{snapshot}/contents", s.repositorySnapshotContents)
	s.mux.HandleFunc("GET /api/repositories/{id}/snapshot-diff", s.repositorySnapshotDiff)
	s.mux.HandleFunc("POST /api/repositories/{id}/maintenance", s.maintainRepository)
	s.mux.HandleFunc("GET /api/repositories/{id}/maintenance-policy", s.getMaintenancePolicy)
	s.mux.HandleFunc("PUT /api/repositories/{id}/maintenance-policy", s.saveMaintenancePolicy)
	s.mux.HandleFunc("POST /api/repositories/{id}/rotate-password", s.rotateRepositoryPassword)
	s.mux.HandleFunc("GET /api/repositories/{id}/password-rotation", s.repositoryPasswordRotationStatus)
	s.mux.HandleFunc("POST /api/repositories/{id}/revoke-old-password", s.revokeOldRepositoryPassword)
	s.mux.HandleFunc("GET /api/restic/versions", s.resticVersions)
	s.mux.HandleFunc("POST /api/restic/install", s.installRestic)
	s.mux.HandleFunc("POST /api/repositories/{id}/restore-directory", s.restoreDirectory)
	s.mux.HandleFunc("POST /api/repositories/{id}/restore-directory/preflight", s.preflightDirectoryRestore)
	s.mux.HandleFunc("POST /api/repositories/{id}/restore-database", s.restoreDatabase)
	s.mux.HandleFunc("POST /api/repositories/{id}/restore-database/preflight", s.preflightDatabaseRestore)
	s.mux.HandleFunc("POST /api/restores/{id}/authorize", s.authorizeRestore)
	s.mux.HandleFunc("GET /api/ntfy", s.getNtfy)
	s.mux.HandleFunc("POST /api/ntfy", s.saveNtfy)
	s.mux.HandleFunc("POST /api/ntfy/test", s.testNtfy)
	s.mux.HandleFunc("GET /api/webhook", s.getWebhook)
	s.mux.HandleFunc("POST /api/webhook", s.saveWebhook)
	s.mux.HandleFunc("POST /api/webhook/test", s.testWebhook)
	s.mux.HandleFunc("GET /api/email", s.emailNotificationRemoved)
	s.mux.HandleFunc("POST /api/email", s.emailNotificationRemoved)
	s.mux.HandleFunc("POST /api/email/test", s.emailNotificationRemoved)
	s.mux.Handle("/", webui.Handler())
}

const (
	maximumControlPlaneBundleBytes = controlplane.MaximumBundleBytes
	maximumControlPlaneFormBytes   = maximumControlPlaneBundleBytes + (1 << 20)
	maximumRecoveryPassphraseBytes = 1024
)

func (s *Server) exportControlPlane(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.controlPlane == nil {
		writeError(w, http.StatusServiceUnavailable, "控制面恢复服务尚未配置")
		return
	}
	var input struct {
		AdministratorPassword          string `json:"administratorPassword"`
		RecoveryPassphrase             string `json:"recoveryPassphrase"`
		RecoveryPassphraseConfirmation string `json:"recoveryPassphraseConfirmation"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if input.AdministratorPassword == "" || len(input.RecoveryPassphrase) < controlplane.MinimumRecoveryPassphraseBytes || len(input.RecoveryPassphrase) > maximumRecoveryPassphraseBytes || input.RecoveryPassphrase != input.RecoveryPassphraseConfirmation {
		writeError(w, http.StatusUnprocessableEntity, "恢复口令至少需要 12 个字节并且两次输入必须一致")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.AdministratorPassword); err != nil {
		s.appendSemanticAudit(r.Context(), username, "control_plane.export_authorize.failure", "control_plane", "local", nil)
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	bundle, err := s.controlPlane.Export(r.Context(), input.RecoveryPassphrase)
	if err != nil {
		s.appendSemanticAudit(r.Context(), username, "control_plane.export.failure", "control_plane", "local", nil)
		writeError(w, http.StatusInternalServerError, "无法生成控制面恢复包")
		return
	}
	defer clearSensitiveBytes(bundle)
	if len(bundle) == 0 || len(bundle) > maximumControlPlaneBundleBytes {
		writeError(w, http.StatusInternalServerError, "生成的控制面恢复包大小无效")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "control_plane.export", "control_plane", "local", map[string]any{"formatVersion": controlplane.BundleFormatVersion, "bundleBytes": len(bundle)})
	filename := "shadoc-recovery-" + time.Now().UTC().Format("20060102T150405Z") + ".rcbundle"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(bundle)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bundle)
}

func (s *Server) preflightControlPlaneImport(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.controlPlane == nil {
		writeError(w, http.StatusServiceUnavailable, "控制面恢复服务尚未配置")
		return
	}
	bundle, values, ok := readControlPlaneMultipart(w, r, map[string]bool{"recoveryPassphrase": true})
	if !ok {
		return
	}
	defer clearSensitiveBytes(bundle)
	passphrase := []byte(values["recoveryPassphrase"])
	defer clearSensitiveBytes(passphrase)
	if len(passphrase) == 0 || len(passphrase) > maximumRecoveryPassphraseBytes {
		writeError(w, http.StatusUnprocessableEntity, "必须提供有效的恢复口令")
		return
	}
	preview, err := s.controlPlane.PreflightImport(r.Context(), bundle, string(passphrase))
	if err != nil {
		s.appendSemanticAudit(r.Context(), username, "control_plane.import_preflight.failure", "control_plane", "local", nil)
		writeError(w, http.StatusUnprocessableEntity, "恢复包、恢复口令或包完整性验证失败")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "control_plane.import_preflight", "control_plane", "local", map[string]any{
		"canImport": preview.CanImport, "conflictCount": len(preview.Conflicts), "missingToolCount": len(preview.MissingTools), "restartRequired": preview.RestartRequired,
	})
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) importControlPlane(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.controlPlane == nil {
		writeError(w, http.StatusServiceUnavailable, "控制面恢复服务尚未配置")
		return
	}
	bundle, values, ok := readControlPlaneMultipart(w, r, map[string]bool{
		"recoveryPassphrase": true, "previewId": true, "administratorPassword": true, "impactConfirmed": true,
	})
	if !ok {
		return
	}
	passphrase := []byte(values["recoveryPassphrase"])
	transferred := false
	defer func() {
		if !transferred {
			clearSensitiveBytes(bundle)
			clearSensitiveBytes(passphrase)
		}
	}()
	previewID := strings.TrimSpace(values["previewId"])
	administratorPassword := values["administratorPassword"]
	if values["impactConfirmed"] != "true" || previewID == "" || len(previewID) > 256 || len(passphrase) == 0 || len(passphrase) > maximumRecoveryPassphraseBytes || administratorPassword == "" || len(administratorPassword) > 4096 {
		writeError(w, http.StatusUnprocessableEntity, "必须确认导入影响并提供有效的预检、恢复口令和管理员密码")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, administratorPassword); err != nil {
		s.appendSemanticAudit(r.Context(), username, "control_plane.import_authorize.failure", "control_plane", "local", nil)
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	record, reused, err := s.operations.StartUnique("control-plane:import", operationruntime.StartRequest{
		Kind: "control_plane_import", Actor: username, Detail: map[string]any{"mode": "recovery_bundle"},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		defer clearSensitiveBytes(bundle)
		defer clearSensitiveBytes(passphrase)
		_ = reporter.Stage("validating_bundle", nil)
		result, importErr := s.controlPlane.Import(ctx, bundle, string(passphrase), previewID)
		if importErr != nil {
			s.appendSemanticAudit(context.WithoutCancel(ctx), username, "control_plane.import.failure", "control_plane", "local", nil)
			return errors.New("控制面恢复包导入失败")
		}
		_ = reporter.Stage("imported", map[string]any{
			"importedCounts": result.ImportedCounts, "revalidationCount": len(result.Revalidation), "restartRequired": result.RestartRequired,
		})
		s.appendSemanticAudit(context.WithoutCancel(ctx), username, "control_plane.import", "control_plane", "local", map[string]any{
			"importedCounts": result.ImportedCounts, "revalidationCount": len(result.Revalidation), "restartRequired": result.RestartRequired,
		})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动控制面恢复导入")
		return
	}
	if !reused {
		transferred = true
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func readControlPlaneMultipart(w http.ResponseWriter, r *http.Request, allowedValues map[string]bool) ([]byte, map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maximumControlPlaneFormBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeError(w, http.StatusRequestEntityTooLarge, "控制面恢复包超过 32 MiB 限制")
		} else {
			writeError(w, http.StatusBadRequest, "必须使用 multipart/form-data 上传恢复包")
		}
		return nil, nil, false
	}
	if r.MultipartForm == nil {
		writeError(w, http.StatusBadRequest, "恢复包上传格式无效")
		return nil, nil, false
	}
	defer r.MultipartForm.RemoveAll()
	for name, items := range r.MultipartForm.File {
		if name != "bundle" || len(items) != 1 {
			writeError(w, http.StatusBadRequest, "恢复包上传字段无效")
			return nil, nil, false
		}
	}
	files := r.MultipartForm.File["bundle"]
	if len(files) != 1 || files[0].Size < 1 || files[0].Size > maximumControlPlaneBundleBytes {
		if len(files) == 1 && files[0].Size > maximumControlPlaneBundleBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "控制面恢复包超过 32 MiB 限制")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "必须选择一个非空的控制面恢复包")
		}
		return nil, nil, false
	}
	values := make(map[string]string, len(r.MultipartForm.Value))
	for name, items := range r.MultipartForm.Value {
		if !allowedValues[name] || len(items) != 1 {
			writeError(w, http.StatusBadRequest, "恢复包表单字段无效")
			return nil, nil, false
		}
		values[name] = items[0]
	}
	file, err := files[0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取控制面恢复包")
		return nil, nil, false
	}
	defer file.Close()
	bundle, err := io.ReadAll(io.LimitReader(file, maximumControlPlaneBundleBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取控制面恢复包")
		return nil, nil, false
	}
	if len(bundle) == 0 || len(bundle) > maximumControlPlaneBundleBytes {
		clearSensitiveBytes(bundle)
		writeError(w, http.StatusRequestEntityTooLarge, "控制面恢复包超过 32 MiB 限制")
		return nil, nil, false
	}
	return bundle, values, true
}

func clearSensitiveBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.operations.List(r.Context(), limit, r.URL.Query().Get("kind"), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取操作记录")
		return
	}
	taskNames, repositoryNames, taskRepositories := s.resourceNames(r.Context())
	type namedOperation struct {
		store.OperationRecord
		TaskName       string `json:"taskName,omitempty"`
		RepositoryName string `json:"repositoryName,omitempty"`
	}
	result := make([]namedOperation, 0, len(items))
	for _, item := range items {
		repositoryID := item.RepositoryID
		if repositoryID == "" {
			repositoryID = taskRepositories[item.TaskID]
		}
		result = append(result, namedOperation{OperationRecord: item, TaskName: taskNames[item.TaskID], RepositoryName: repositoryNames[repositoryID]})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resourceNames(ctx context.Context) (map[string]string, map[string]string, map[string]string) {
	taskNames, repositoryNames, taskRepositories := map[string]string{}, map[string]string{}, map[string]string{}
	resources, ok := s.store.(*store.Store)
	if !ok {
		return taskNames, repositoryNames, taskRepositories
	}
	tasks, _ := resources.ListTasks(ctx)
	for _, item := range tasks {
		taskNames[item.ID] = item.Name
		taskRepositories[item.ID] = item.RepositoryID
	}
	repositories, _ := resources.ListRepositories(ctx)
	for _, item := range repositories {
		repositoryNames[item.ID] = item.Name
	}
	return taskNames, repositoryNames, taskRepositories
}

func (s *Server) getOperation(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	record, err := s.operations.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "操作记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取操作记录")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) cancelOperation(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	err := s.operations.Cancel(r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "操作记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "操作无法取消："+err.Error())
		return
	}
	s.appendSemanticAudit(r.Context(), username, "operation.cancel", "operation", r.PathValue("id"), nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": r.PathValue("id"), "status": "cancelling"})
}

func (s *Server) previewResourceDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	preview, err := resources.ResourceDeletePreview(r.Context(), r.PathValue("resource"), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法生成删除影响预览")
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) confirmResourceDelete(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
	}
	if decodeJSON(r, &input) != nil || input.ExpectedUpdatedAt == "" {
		writeError(w, http.StatusBadRequest, "删除确认缺少资源版本，请重新预览")
		return
	}
	resourceType, id := r.PathValue("resource"), r.PathValue("id")
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	secretIDs, err := resources.DeleteResourceVersioned(r.Context(), resourceType, id, input.ExpectedUpdatedAt)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "资源或依赖已变化，请刷新并重新确认")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	if s.secrets != nil {
		cleanupCtx := context.WithoutCancel(r.Context())
		for _, secretID := range secretIDs {
			_ = s.secrets.Delete(cleanupCtx, secretID)
		}
	}
	actionType := strings.TrimSuffix(resourceType, "s")
	s.appendSemanticAudit(r.Context(), username, actionType+".delete", actionType, id, map[string]any{"expectedUpdatedAt": input.ExpectedUpdatedAt})
	w.WriteHeader(http.StatusNoContent)
}

type operationCleanupStore interface {
	Operation(context.Context, string) (store.OperationRecord, error)
	ResolveOperationCleanup(context.Context, string, time.Time, map[string]any) error
}

type operationCleanupPreflight struct {
	Safe         bool   `json:"safe"`
	Kind         string `json:"kind"`
	ResidualPath string `json:"residualPath,omitempty"`
	Resolution   string `json:"resolution"`
}

func (s *Server) preflightOperationCleanup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	preflight, err := s.operationCleanupPreflight(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "操作记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preflight)
}

func (s *Server) cleanupOperation(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil || input.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入当前管理员密码")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "管理员密码验证失败")
		return
	}
	preflight, err := s.operationCleanupPreflight(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	resolution := preflight.Resolution
	if preflight.Kind == "directory_restore" {
		if err := os.RemoveAll(preflight.ResidualPath); err != nil {
			writeError(w, http.StatusInternalServerError, "清理恢复残留失败")
			return
		}
	}
	cleanupStore := s.store.(operationCleanupStore)
	if err := cleanupStore.ResolveOperationCleanup(r.Context(), r.PathValue("id"), time.Now().UTC(), map[string]any{"cleanupResolution": resolution}); err != nil {
		writeError(w, http.StatusConflict, "清理状态已发生变化")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "operation.cleanup", "operation", r.PathValue("id"), map[string]any{"kind": preflight.Kind, "resolution": resolution})
	record, _ := cleanupStore.Operation(r.Context(), r.PathValue("id"))
	if preflight.Kind == "database_restore" {
		connectionID, _ := record.Detail["connectionId"].(string)
		s.deleteTemporaryDatabaseConnection(context.WithoutCancel(r.Context()), username, connectionID, "cleanup_resolved")
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) operationCleanupPreflight(ctx context.Context, id string) (operationCleanupPreflight, error) {
	cleanupStore, ok := s.store.(operationCleanupStore)
	if !ok {
		return operationCleanupPreflight{}, errors.New("操作清理服务不可用")
	}
	record, err := cleanupStore.Operation(ctx, id)
	if err != nil {
		return operationCleanupPreflight{}, err
	}
	if record.Status != "cleanup_required" {
		return operationCleanupPreflight{}, errors.New("该操作当前不允许自动清理")
	}
	if record.Kind == "database_restore" {
		preflighter, ok := s.databaseRestore.(databaseRestorePreflighter)
		if !ok {
			return operationCleanupPreflight{}, errors.New("数据库恢复预检不可用")
		}
		connectionID, _ := record.Detail["connectionId"].(string)
		databaseName, _ := record.Detail["database"].(string)
		if record.RepositoryID == "" || record.SnapshotID == "" || connectionID == "" || databaseName == "" {
			return operationCleanupPreflight{}, errors.New("数据库恢复记录缺少重新预检所需信息")
		}
		_, err := preflighter.Preflight(ctx, dbrestore.Request{RepositoryID: record.RepositoryID, SnapshotID: record.SnapshotID, ConnectionID: connectionID, Database: databaseName})
		if err != nil {
			return operationCleanupPreflight{}, errors.New("数据库目标仍未清理或无法通过恢复预检：" + err.Error())
		}
		return operationCleanupPreflight{Safe: true, Kind: record.Kind, Resolution: "external_cleanup_verified"}, nil
	}
	if record.Kind != "directory_restore" {
		return operationCleanupPreflight{}, errors.New("该操作当前不支持清理确认")
	}
	residual, _ := record.Detail["residualPath"].(string)
	residual = filepath.Clean(strings.TrimSpace(residual))
	base := filepath.Base(residual)
	if !filepath.IsAbs(residual) || residual == string(filepath.Separator) || !strings.Contains(base, ".restic-control-restore-") {
		return operationCleanupPreflight{}, errors.New("残留路径未通过操作归属校验")
	}
	info, err := os.Lstat(residual)
	if err != nil {
		if os.IsNotExist(err) {
			return operationCleanupPreflight{}, errors.New("残留路径已不存在，请刷新状态")
		}
		return operationCleanupPreflight{}, errors.New("无法检查恢复残留")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return operationCleanupPreflight{}, errors.New("残留目标不是可安全清理的目录")
	}
	return operationCleanupPreflight{Safe: true, Kind: record.Kind, ResidualPath: residual, Resolution: "removed"}, nil
}

func redactFilesystemTarget(target string) string {
	cleaned := filepath.Clean(strings.TrimSpace(target))
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "…"
	}
	return filepath.Join("…", filepath.Base(cleaned))
}

func (s *Server) lifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.lifecycle == nil {
		writeError(w, http.StatusServiceUnavailable, "数据生命周期服务不可用")
		return
	}
	policy, err := s.lifecycle.Policy(r.Context())
	if err != nil {
		s.log.Error("load lifecycle policy", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取数据生命周期策略")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) saveLifecyclePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.lifecycle == nil {
		writeError(w, http.StatusServiceUnavailable, "数据生命周期服务不可用")
		return
	}
	var policy lifecycle.Policy
	if decodeJSON(r, &policy) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if err := s.lifecycle.SavePolicy(r.Context(), policy, time.Now()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "数据生命周期策略超出安全范围")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cleanupLifecycle(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.lifecycle == nil {
		writeError(w, http.StatusServiceUnavailable, "数据生命周期服务不可用")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	report, err := s.lifecycle.CleanupConfigured(r.Context(), time.Now())
	if err != nil {
		s.log.Error("manual lifecycle cleanup", "error", err)
		writeError(w, http.StatusInternalServerError, "数据清理失败")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "lifecycle.cleanup.complete", "application", "execution-data", map[string]any{"logsCleared": report.LogsCleared, "runsDeleted": report.RunsDeleted, "auditsDeleted": report.AuditsDeleted})
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) previewLifecycleCleanup(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.lifecycle == nil {
		writeError(w, http.StatusServiceUnavailable, "数据生命周期服务不可用")
		return
	}
	report, err := s.lifecycle.PreviewConfigured(r.Context(), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法预览数据清理影响")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "lifecycle.cleanup.preview", "application", "execution-data", map[string]any{"logsCleared": report.LogsCleared, "runsDeleted": report.RunsDeleted, "auditsDeleted": report.AuditsDeleted})
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) vaultStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库控制器不可用")
		return
	}
	writeJSON(w, http.StatusOK, s.vault.Status())
}

func (s *Server) vaultLockOnRestart(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库控制器不可用")
		return
	}
	var input struct {
		Passphrase string `json:"passphrase"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if err := s.vault.LockOnRestart(input.Passphrase); err != nil {
		if errors.Is(err, secret.ErrLocked) {
			writeError(w, http.StatusLocked, "秘密库已锁定")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "秘密库口令至少需要 12 个字符")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "vault.protection.enable", "vault", "primary", map[string]any{"mode": "lock-on-restart"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) vaultUnlock(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库控制器不可用")
		return
	}
	var input struct {
		Passphrase string `json:"passphrase"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if err := s.vault.Unlock(input.Passphrase); err != nil {
		if errors.Is(err, secret.ErrUnlockRateLimited) {
			s.appendSemanticAudit(r.Context(), username, "vault.unlock.failure", "vault", "primary", map[string]any{"reason": "rate_limited"})
			writeError(w, http.StatusTooManyRequests, "解锁尝试过于频繁，请稍后重试")
			return
		}
		if errors.Is(err, vault.ErrInvalidPassphrase) {
			s.appendSemanticAudit(r.Context(), username, "vault.unlock.failure", "vault", "primary", map[string]any{"reason": "invalid_passphrase"})
			writeError(w, http.StatusUnprocessableEntity, "秘密库口令错误")
			return
		}
		s.log.Error("unlock vault", "error", err)
		s.appendSemanticAudit(r.Context(), username, "vault.unlock.failure", "vault", "primary", map[string]any{"reason": "internal_error"})
		writeError(w, http.StatusInternalServerError, "无法解锁秘密库")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "vault.unlock.success", "vault", "primary", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) vaultAutomatic(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库控制器不可用")
		return
	}
	var input struct {
		Password  string `json:"password"`
		Confirmed bool   `json:"confirmed"`
	}
	if decodeJSON(r, &input) != nil || !input.Confirmed || input.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "必须确认自动解锁风险并输入当前管理员密码")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.Password); err != nil {
		s.appendSemanticAudit(r.Context(), username, "vault.protection.disable.failure", "vault", "primary", map[string]any{"reason": "invalid_admin_password"})
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	if err := s.vault.Automatic(); err != nil {
		if errors.Is(err, secret.ErrLocked) {
			writeError(w, http.StatusLocked, "秘密库已锁定")
			return
		}
		s.log.Error("enable automatic vault unlock", "error", err)
		writeError(w, http.StatusInternalServerError, "无法启用自动解锁")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "vault.protection.disable", "vault", "primary", map[string]any{"mode": "automatic"})
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) probeSSHHostKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	var input struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "SSH 地址无效")
		return
	}
	result, err := sshhost.Probe(r.Context(), input.Host, input.Port)
	if err != nil {
		writeError(w, 502, "无法获取 SSH 主机密钥："+err.Error())
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) exportAudits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.filteredAudits(r)
	if err != nil {
		if errors.Is(err, errInvalidAuditFilter) {
			writeError(w, http.StatusBadRequest, "审计筛选时间无效")
			return
		}
		writeError(w, 500, "无法导出审计")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="shadoc-audit.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"time", "actor", "action", "target_type", "target_id", "detail"})
	for _, item := range items {
		detail, _ := json.Marshal(item.Detail)
		_ = writer.Write([]string{item.OccurredAt.Format(time.RFC3339), item.Actor, item.Action, item.TargetType, item.TargetID, string(detail)})
	}
	writer.Flush()
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	tasks, err := resources.ListTasks(r.Context())
	if err != nil {
		writeError(w, 500, "无法读取仪表盘")
		return
	}
	repos, _ := resources.ListRepositories(r.Context())
	runs, _ := resources.ListRuns(r.Context(), 200)
	plans, _ := resources.ListPlans(r.Context())
	repoNames := map[string]string{}
	alerts, err := s.alerts.Active(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取当前告警")
		return
	}
	repositoryStatus := "healthy"
	for _, repo := range repos {
		repoNames[repo.ID] = repo.Name
		if repo.Status == "abnormal" {
			repositoryStatus = "abnormal"
		}
	}
	latest := map[string]store.RunRecord{}
	succeeded, failed, partial := 0, 0, 0
	for _, run := range runs {
		if _, ok := latest[run.TaskID]; !ok {
			latest[run.TaskID] = run
		}
		switch run.Status {
		case "success":
			succeeded++
		case "failed":
			failed++
		case "partial":
			partial++
		}
	}
	next := map[string]time.Time{}
	lastScheduled := map[string]time.Time{}
	var nextOverall time.Time
	scheduleCoverage := make([]map[string]any, 0)
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		anchor := plan.ScheduleAnchorAt
		if anchor.IsZero() {
			anchor = plan.CreatedAt
		}
		cursor := anchor
		latestOccurrence, latestErr := resources.LatestScheduleOccurrence(r.Context(), "plan", plan.ID, anchor)
		if latestErr == nil {
			cursor = latestOccurrence.ScheduledAt
			for _, taskID := range plan.TaskIDs {
				if previous, ok := lastScheduled[taskID]; !ok || latestOccurrence.ScheduledAt.After(previous) {
					lastScheduled[taskID] = latestOccurrence.ScheduledAt
				}
			}
		} else if !errors.Is(latestErr, sql.ErrNoRows) {
			continue
		}
		stats, statsErr := resources.ScheduleOccurrenceStats(r.Context(), "plan", plan.ID, anchor)
		if statsErr == nil {
			coveragePercent := 0
			if stats.Total > 0 {
				coveragePercent = (stats.Success + stats.Partial) * 100 / stats.Total
			}
			scheduleCoverage = append(scheduleCoverage, map[string]any{"planId": plan.ID, "planName": plan.Name, "total": stats.Total, "success": stats.Success, "partial": stats.Partial, "missed": stats.Missed, "failed": stats.Failed, "cancelled": stats.Cancelled, "skipped": stats.Skipped, "interrupted": stats.Interrupted, "coveragePercent": coveragePercent})
		}
		when, calcErr := schedule.NextAnchored(plan.Schedule, plan.Timezone, anchor, cursor)
		if calcErr != nil {
			continue
		}
		if nextOverall.IsZero() || when.Before(nextOverall) {
			nextOverall = when
		}
		for _, id := range plan.TaskIDs {
			if old, ok := next[id]; !ok || when.Before(old) {
				next[id] = when
			}
		}
	}
	items := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		last := latest[task.ID]
		lastRun := "尚未运行"
		status := "idle"
		if !last.StartedAt.IsZero() {
			lastRun = last.StartedAt.Format(time.RFC3339)
			status = last.Status
		}
		if !task.Enabled {
			status = "disabled"
		}
		nextRun := "未加入计划"
		if value, ok := next[task.ID]; ok {
			nextRun = value.Format(time.RFC3339)
		}
		lastScheduledAt := "尚无计划发生记录"
		if value, ok := lastScheduled[task.ID]; ok {
			lastScheduledAt = value.Format(time.RFC3339)
		}
		item := map[string]any{"id": task.ID, "name": task.Name, "kind": task.Kind, "status": status, "enabled": task.Enabled, "repository": repoNames[task.RepositoryID], "lastScheduledAt": lastScheduledAt, "lastRun": lastRun, "nextRun": nextRun}
		if task.EffectiveEngine() == domain.ResticEngine && task.Kind == domain.DirectoryTask {
			complete, completeErr := resources.LatestSuccessfulRun(r.Context(), task.ID)
			if completeErr == nil {
				item["lastCompleteBackup"] = map[string]any{"snapshotId": complete.SnapshotID, "startedAt": complete.StartedAt, "finishedAt": complete.FinishedAt}
			} else if !errors.Is(completeErr, sql.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "无法读取最近完整备份")
				return
			}
		}
		items = append(items, item)
	}
	nextOverallValue := "暂无计划"
	if !nextOverall.IsZero() {
		nextOverallValue = nextOverall.Format(time.RFC3339)
	}
	total := succeeded + failed + partial
	successRate := 0
	if total > 0 {
		successRate = succeeded * 100 / total
	}
	writeJSON(w, 200, map[string]any{"tasks": items, "alerts": alerts, "repositoryStatus": repositoryStatus, "nextRun": nextOverallValue, "scheduleCoverage": scheduleCoverage, "runOverview": map[string]int{"total": total, "succeeded": succeeded, "failed": failed, "partial": partial, "successRate": successRate}})
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	active, err := s.alerts.Active(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取当前告警")
		return
	}
	events, err := s.alerts.History(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取告警历史")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	deliveries, err := resources.ListNotificationDeliveries(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取通知投递记录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "events": events, "deliveries": deliveries})
}

type restoreVerificationPolicyRequest struct {
	Schedule               domain.Schedule `json:"schedule"`
	Timezone               string          `json:"timezone"`
	SelectionPath          string          `json:"selectionPath"`
	MaximumBytes           int64           `json:"maximumBytes"`
	MaximumSuccessAgeHours int             `json:"maximumSuccessAgeHours"`
	Enabled                bool            `json:"enabled"`
	CatchUpWindowMinutes   *int            `json:"catchUpWindowMinutes"`
	TaskID                 json.RawMessage `json:"taskId,omitempty"`
	RepositoryID           json.RawMessage `json:"repositoryId,omitempty"`
	SnapshotID             json.RawMessage `json:"snapshotId,omitempty"`
	Status                 json.RawMessage `json:"status,omitempty"`
	Evidence               json.RawMessage `json:"evidence,omitempty"`
}

type restoreVerificationPolicyView struct {
	domain.RestoreVerificationPolicy
	NextRun            *time.Time                     `json:"nextRun,omitempty"`
	LastScheduledAt    *time.Time                     `json:"lastScheduledAt,omitempty"`
	LastActualAt       *time.Time                     `json:"lastActualAt,omitempty"`
	LastScheduleStatus string                         `json:"lastScheduleStatus,omitempty"`
	ScheduleCoverage   *store.ScheduleOccurrenceStats `json:"scheduleCoverage,omitempty"`
}

func (s *Server) listRestoreVerifications(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 1000 {
		limit = 100
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	policies, err := resources.ListRestoreVerificationPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证策略")
		return
	}
	views := make([]restoreVerificationPolicyView, 0, len(policies))
	for _, policy := range policies {
		if taskID != "" && policy.TaskID != taskID {
			continue
		}
		view, viewErr := buildRestoreVerificationPolicyView(r.Context(), resources, policy)
		if viewErr != nil {
			writeError(w, http.StatusInternalServerError, "无法读取恢复验证计划状态")
			return
		}
		views = append(views, view)
	}
	records, err := resources.ListRestoreVerifications(r.Context(), taskID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证证据")
		return
	}
	cleanupByTask, err := resources.RestoreVerificationCleanupRequired(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证清理状态")
		return
	}
	cleanup := make([]store.RestoreVerificationRecord, 0, len(cleanupByTask))
	for cleanupTaskID, record := range cleanupByTask {
		if taskID == "" || cleanupTaskID == taskID {
			cleanup = append(cleanup, record)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": views, "records": records, "cleanupRequired": cleanup})
}

func (s *Server) getRestoreVerificationPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	policy, err := resources.RestoreVerificationPolicy(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "恢复验证策略不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证策略")
		return
	}
	view, err := buildRestoreVerificationPolicyView(r.Context(), resources, policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证计划状态")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func buildRestoreVerificationPolicyView(ctx context.Context, resources *store.Store, policy domain.RestoreVerificationPolicy) (restoreVerificationPolicyView, error) {
	view := restoreVerificationPolicyView{RestoreVerificationPolicy: policy}
	if policy.ScheduleAnchorAt.IsZero() {
		return view, nil
	}
	cursor := policy.ScheduleAnchorAt
	latest, err := resources.LatestScheduleOccurrence(ctx, "restore_verification", policy.TaskID, policy.ScheduleAnchorAt)
	if err == nil {
		cursor = latest.ScheduledAt
		view.LastScheduledAt = &latest.ScheduledAt
		view.LastActualAt = latest.StartedAt
		view.LastScheduleStatus = latest.Status
	} else if !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	stats, err := resources.ScheduleOccurrenceStats(ctx, "restore_verification", policy.TaskID, policy.ScheduleAnchorAt)
	if err != nil {
		return view, err
	}
	view.ScheduleCoverage = &stats
	if policy.Enabled {
		next, err := schedule.NextAnchored(policy.Schedule, policy.Timezone, policy.ScheduleAnchorAt, cursor)
		if err != nil {
			return view, err
		}
		view.NextRun = &next
	}
	return view, nil
}

func (s *Server) saveRestoreVerificationPolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input restoreVerificationPolicyRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "恢复验证策略格式无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	catchUp := defaultCatchUpWindowMinutes
	if existing, err := resources.RestoreVerificationPolicy(r.Context(), r.PathValue("id")); err == nil {
		catchUp = existing.CatchUpWindowMinutes
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "无法读取现有恢复验证策略")
		return
	}
	if input.CatchUpWindowMinutes != nil {
		catchUp = *input.CatchUpWindowMinutes
	}
	policy := domain.RestoreVerificationPolicy{
		TaskID: r.PathValue("id"), Schedule: input.Schedule, Timezone: input.Timezone, SelectionPath: input.SelectionPath,
		MaximumBytes: input.MaximumBytes, MaximumSuccessAgeHours: input.MaximumSuccessAgeHours, Enabled: input.Enabled,
		CatchUpWindowMinutes: catchUp, UpdatedAt: time.Now().UTC(),
	}
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "恢复验证策略无效："+err.Error())
		return
	}
	if err := resources.SaveRestoreVerificationPolicy(r.Context(), policy); err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	s.appendSemanticAudit(r.Context(), username, "restore-verification.policy.update", "task", policy.TaskID, map[string]any{"enabled": policy.Enabled, "maximumBytes": policy.MaximumBytes, "maximumSuccessAgeHours": policy.MaximumSuccessAgeHours})
	saved, err := resources.RestoreVerificationPolicy(r.Context(), policy.TaskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取已保存的恢复验证策略")
		return
	}
	view, err := buildRestoreVerificationPolicyView(r.Context(), resources, saved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证计划状态")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) deleteRestoreVerificationPolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	taskID := r.PathValue("id")
	if err := resources.DeleteRestoreVerificationPolicy(r.Context(), taskID); err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	s.appendSemanticAudit(r.Context(), username, "restore-verification.policy.delete", "task", taskID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getRestoreVerification(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	record, err := resources.RestoreVerification(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "恢复验证证据不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证证据")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) runRestoreVerification(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "恢复验证请求格式无效")
		return
	}
	if s.restoreVerification == nil {
		writeError(w, http.StatusServiceUnavailable, "恢复验证服务尚未配置")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	taskID := r.PathValue("id")
	policy, err := resources.RestoreVerificationPolicy(r.Context(), taskID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "请先保存恢复验证策略")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证策略")
		return
	}
	tasks, err := resources.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务")
		return
	}
	var task domain.Task
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	record, reused, err := s.operations.StartUnique("restore-verification:"+taskID, operationruntime.StartRequest{
		Kind: "restore_verification", Actor: username, RepositoryID: task.RepositoryID, TaskID: taskID, Target: policy.SelectionPath,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("restoring_for_verification", nil)
		evidence, runErr := s.restoreVerification.Run(ctx, taskID, "manual")
		detail := map[string]any{"verificationId": evidence.ID, "status": evidence.Status, "fileCount": evidence.FileCount, "byteCount": evidence.ByteCount, "manifestSha256": evidence.ManifestSHA256, "cleanupStatus": evidence.CleanupStatus}
		_ = reporter.Stage("verification_evidence_persisted", detail)
		s.appendSemanticAudit(context.WithoutCancel(ctx), username, "restore-verification.run.complete", "restore-verification", evidence.ID, detail)
		if runErr == nil {
			return nil
		}
		if evidence.Status == "cancelled" || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return context.Canceled
		}
		return errors.New("恢复验证失败；请查看持久化验证证据")
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动恢复验证")
		return
	}
	if !reused {
		s.appendSemanticAudit(r.Context(), username, "restore-verification.run.start", "task", taskID, map[string]any{"operationId": record.ID})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"operationId": record.ID, "status": record.Status, "reused": reused})
}

func (s *Server) cleanupRestoreVerification(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "恢复验证清理请求格式无效")
		return
	}
	if s.restoreVerification == nil {
		writeError(w, http.StatusServiceUnavailable, "恢复验证服务尚未配置")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	verificationID := r.PathValue("id")
	evidence, err := resources.RestoreVerification(r.Context(), verificationID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "恢复验证证据不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复验证证据")
		return
	}
	if evidence.CleanupStatus != "required" {
		writeError(w, http.StatusConflict, "该恢复验证没有待清理内容")
		return
	}
	record, reused, err := s.operations.StartUnique("restore-verification-cleanup:"+verificationID, operationruntime.StartRequest{
		Kind: "restore_verification_cleanup", Actor: username, RepositoryID: evidence.RepositoryID, TaskID: evidence.TaskID, SnapshotID: evidence.SnapshotID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("cleaning_verification_content", map[string]any{"verificationId": verificationID})
		cleaned, cleanupErr := s.restoreVerification.Cleanup(ctx, verificationID)
		detail := map[string]any{"verificationId": verificationID, "cleanupStatus": cleaned.CleanupStatus}
		s.appendSemanticAudit(context.WithoutCancel(ctx), username, "restore-verification.cleanup.complete", "restore-verification", verificationID, detail)
		if cleanupErr != nil {
			if errors.Is(cleanupErr, context.Canceled) || errors.Is(cleanupErr, context.DeadlineExceeded) {
				return context.Canceled
			}
			return errors.New("恢复验证临时内容清理失败")
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动恢复验证清理")
		return
	}
	if !reused {
		s.appendSemanticAudit(r.Context(), username, "restore-verification.cleanup.start", "restore-verification", verificationID, map[string]any{"operationId": record.ID})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"operationId": record.ID, "status": record.Status, "reused": reused})
}

func (s *Server) getMaintenancePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	repositoryID := r.PathValue("id")
	item, err := resources.MaintenancePolicy(r.Context(), repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		item = domain.MaintenancePolicy{RepositoryID: repositoryID, Enabled: false, CatchUpWindowMinutes: defaultCatchUpWindowMinutes}
		effective, _, retentionErr := resources.EffectiveMaintenanceRetention(r.Context(), repositoryID, item.Retention)
		if retentionErr != nil {
			writeError(w, http.StatusInternalServerError, "无法读取权威保留策略")
			return
		}
		item.Retention = effective
		item.RetentionSource = domain.RepositoryRetentionSource
		item.PolicyFingerprint = effective.Fingerprint()
	} else if err != nil {
		writeError(w, 500, "无法读取维护计划")
		return
	}
	response := map[string]any{"repositoryId": item.RepositoryID, "schedule": item.Schedule, "timezone": item.Timezone, "retention": item.Retention, "retentionSource": item.RetentionSource, "retentionConflict": item.RetentionConflict, "reviewedRetention": item.ReviewedRetention, "policyFingerprint": item.PolicyFingerprint, "enabled": item.Enabled, "catchUpWindowMinutes": item.CatchUpWindowMinutes, "scheduleAnchorAt": item.ScheduleAnchorAt, "updatedAt": item.UpdatedAt}
	tasks, taskErr := resources.ListTasks(r.Context())
	if taskErr != nil {
		writeError(w, 500, "无法读取仓库绑定任务")
		return
	}
	for _, task := range tasks {
		if task.RepositoryID == repositoryID && task.EffectiveEngine() == domain.ResticEngine {
			response["boundTask"] = map[string]any{"id": task.ID, "name": task.Name}
			break
		}
	}
	if !item.ScheduleAnchorAt.IsZero() {
		cursor := item.ScheduleAnchorAt
		latest, latestErr := resources.LatestScheduleOccurrence(r.Context(), "maintenance", item.RepositoryID, item.ScheduleAnchorAt)
		if latestErr == nil {
			cursor = latest.ScheduledAt
			response["lastScheduledAt"] = latest.ScheduledAt.Format(time.RFC3339)
			response["lastScheduleStatus"] = latest.Status
			if latest.StartedAt != nil {
				response["lastActualAt"] = latest.StartedAt.Format(time.RFC3339)
			}
		} else if !errors.Is(latestErr, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "无法读取维护计划发生记录")
			return
		}
		stats, statsErr := resources.ScheduleOccurrenceStats(r.Context(), "maintenance", item.RepositoryID, item.ScheduleAnchorAt)
		if statsErr != nil {
			writeError(w, http.StatusInternalServerError, "无法读取维护计划覆盖率")
			return
		}
		response["scheduleCoverage"] = stats
		if item.Enabled {
			if next, nextErr := schedule.NextAnchored(item.Schedule, item.Timezone, item.ScheduleAnchorAt, cursor); nextErr == nil {
				response["nextRun"] = next.Format(time.RFC3339)
			}
		}
	}
	writeJSON(w, 200, response)
}
func (s *Server) saveMaintenancePolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input maintenancePolicyRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "维护计划格式无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	defaultCatchUp := defaultCatchUpWindowMinutes
	existing, existingErr := resources.MaintenancePolicy(r.Context(), r.PathValue("id"))
	if existingErr == nil {
		defaultCatchUp = existing.CatchUpWindowMinutes
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "无法读取现有维护计划")
		return
	}
	item := input.policy(r.PathValue("id"), defaultCatchUp, time.Now().UTC())
	if input.PreviewID == "" {
		writeError(w, http.StatusConflict, "保存维护计划前必须完成 dry-run 预览")
		return
	}
	if err := item.Validate(); err != nil {
		writeError(w, 422, "维护计划无效："+err.Error())
		return
	}
	if _, err := resources.ConsumeMaintenancePreview(r.Context(), input.PreviewID, item.RepositoryID, item.Retention, time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, "维护预览已过期、已使用或策略已变化，请重新预览")
		return
	}
	if err := resources.SaveMaintenancePolicy(r.Context(), item); err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	s.appendSemanticAudit(r.Context(), username, "maintenance.policy.update", "repository", item.RepositoryID, map[string]any{"enabled": item.Enabled, "previewId": input.PreviewID, "policyFingerprint": item.Retention.Fingerprint()})
	item, readErr := resources.MaintenancePolicy(r.Context(), item.RepositoryID)
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, "无法读取已保存维护计划")
		return
	}
	writeJSON(w, 200, item)
}

type ntfyStored struct {
	BaseURL       string `json:"baseUrl"`
	Topic         string `json:"topic"`
	TokenSecretID string `json:"tokenSecretId,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

func (c ntfyStored) enabled() bool { return c.Enabled != nil && *c.Enabled }

func (s *Server) getNtfy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	value, err := resources.Metadata(r.Context(), "ntfy.config")
	if err != nil {
		writeJSON(w, 200, map[string]any{"configured": false})
		return
	}
	var config ntfyStored
	_ = json.Unmarshal([]byte(value), &config)
	writeJSON(w, 200, map[string]any{"configured": true, "enabled": config.enabled(), "baseUrl": config.BaseURL, "topic": config.Topic, "hasToken": config.TokenSecretID != ""})
}
func (s *Server) saveNtfy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, 503, "秘密库尚未配置")
		return
	}
	var input struct {
		BaseURL    string `json:"baseUrl"`
		Topic      string `json:"topic"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clearToken"`
		Enabled    *bool  `json:"enabled"`
	}
	if decodeJSON(r, &input) != nil || input.BaseURL == "" || input.Topic == "" || (input.ClearToken && input.Token != "") {
		writeError(w, 400, "ntfy 配置无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	var previous ntfyStored
	if value, err := resources.Metadata(r.Context(), "ntfy.config"); err == nil {
		_ = json.Unmarshal([]byte(value), &previous)
	}
	enabled := false
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	config := ntfyStored{BaseURL: input.BaseURL, Topic: input.Topic, TokenSecretID: previous.TokenSecretID, Enabled: &enabled}
	newTokenSecretID := ""
	if input.ClearToken {
		config.TokenSecretID = ""
	} else if input.Token != "" {
		id, err := s.secrets.Put(r.Context(), "ntfy-token", []byte(input.Token))
		if err != nil {
			writeError(w, 500, "无法保存 ntfy 令牌")
			return
		}
		config.TokenSecretID = id
		newTokenSecretID = id
	}
	value, _ := json.Marshal(config)
	if err := resources.SetMetadata(r.Context(), "ntfy.config", string(value)); err != nil {
		if newTokenSecretID != "" {
			_ = s.secrets.Delete(context.WithoutCancel(r.Context()), newTokenSecretID)
		}
		writeError(w, 500, "无法保存 ntfy 配置")
		return
	}
	if previous.TokenSecretID != "" && previous.TokenSecretID != config.TokenSecretID {
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), previous.TokenSecretID)
	}
	s.appendSemanticAudit(r.Context(), username, "ntfy.config.update", "notification", "ntfy", map[string]any{"enabled": enabled, "tokenAction": map[bool]string{true: "cleared", false: "retained_or_replaced"}[input.ClearToken]})
	writeJSON(w, 200, map[string]bool{"configured": true, "enabled": enabled})
}
func (s *Server) testNtfy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.ntfy == nil || s.secrets == nil {
		writeError(w, 503, "ntfy 尚未配置")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	value, err := resources.Metadata(r.Context(), "ntfy.config")
	if err != nil {
		writeError(w, 404, "尚未保存 ntfy 配置")
		return
	}
	var stored ntfyStored
	_ = json.Unmarshal([]byte(value), &stored)
	if !stored.enabled() {
		writeError(w, http.StatusConflict, "ntfy 通知已停用，请先启用后再发送测试")
		return
	}
	token := ""
	if stored.TokenSecretID != "" {
		secretValue, secretErr := s.secrets.Get(r.Context(), stored.TokenSecretID, "ntfy-token")
		if secretErr != nil {
			writeError(w, 500, "无法读取 ntfy 令牌")
			return
		}
		defer clear(secretValue)
		token = string(secretValue)
	}
	if err := s.ntfy.Publish(r.Context(), ntfy.Config{BaseURL: stored.BaseURL, Topic: stored.Topic, Token: token}, ntfy.Event{Title: "Shadoc 测试", Message: "通知通道连接成功", Severity: "success"}); err != nil {
		writeError(w, 502, "ntfy 测试失败")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "delivered"})
}

type directoryRestoreRequest struct {
	SnapshotID     string   `json:"snapshotId"`
	Target         string   `json:"target"`
	Includes       []string `json:"includes"`
	TargetKind     string   `json:"targetKind"`
	AgentID        string   `json:"agentId"`
	ConfirmationID string   `json:"confirmationId"`
}

func (s *Server) preflightDirectoryRestore(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	preflighter, ok := s.repositories.(directoryRestorePreflighter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "目录恢复预检不可用")
		return
	}
	var input directoryRestoreRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "恢复参数无效")
		return
	}
	repositoryID := r.PathValue("id")
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	resourcePolicy, resourceBound, resourceErr := resources.EffectiveRepositoryResources(r.Context(), repositoryID)
	if resourceErr != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复资源策略")
		return
	}
	input.TargetKind = normalizedRestoreTargetKind(input.TargetKind)
	var result repositoryservice.DirectoryRestorePreflight
	var err error
	if input.TargetKind == "agent" {
		selection, selectionOK := s.repositories.(directoryRestoreSelectionPreflighter)
		if !selectionOK || s.agentRestore == nil || input.AgentID == "" {
			writeError(w, http.StatusServiceUnavailable, "Agent 目录恢复预检不可用")
			return
		}
		result, err = selection.PreflightDirectoryRestoreSelection(r.Context(), repositoryID, input.SnapshotID, input.Includes)
		if err == nil {
			err = s.agentRestore.PreflightTarget(r.Context(), input.AgentID, repositoryID, input.Target)
		}
	} else {
		result, err = preflighter.PreflightDirectoryRestore(r.Context(), repositoryID, input.SnapshotID, input.Target, input.Includes)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "目录恢复预检失败："+err.Error())
		return
	}
	now := time.Now().UTC()
	record := store.RestoreConfirmation{ID: newID("restore_confirmation"), Actor: username, Kind: "directory", Fingerprint: directoryRestoreFingerprint(repositoryID, input, resourcePolicy.DownloadKiBPerSecond), Summary: map[string]any{"repositoryId": repositoryID, "snapshotId": input.SnapshotID, "sourcePath": result.SourcePath, "target": redactFilesystemTarget(input.Target), "targetKind": input.TargetKind, "agentId": input.AgentID, "includes": result.Includes, "behavior": "create_directory", "downloadKiBPerSecond": resourcePolicy.DownloadKiBPerSecond, "resourcePolicySource": restoreResourcePolicySource(resourceBound)}, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := resources.CreateRestoreConfirmation(r.Context(), record); err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存恢复预检")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "restore.preflight", "repository", repositoryID, map[string]any{"confirmationId": record.ID, "kind": "directory", "snapshotId": input.SnapshotID, "target": redactFilesystemTarget(input.Target), "downloadKiBPerSecond": resourcePolicy.DownloadKiBPerSecond})
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) authorizeRestore(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil || input.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入当前管理员密码")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "管理员密码验证失败")
		return
	}
	if err := s.resourceStore(w).AuthorizeRestoreConfirmation(r.Context(), r.PathValue("id"), username, time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, "恢复预检已过期或不属于当前会话")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "restore.authorize", "restore_confirmation", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func directoryRestoreFingerprint(repositoryID string, input directoryRestoreRequest, downloadKiBPerSecond int) string {
	input.TargetKind = normalizedRestoreTargetKind(input.TargetKind)
	encoded, _ := json.Marshal(struct {
		RepositoryID         string   `json:"repositoryId"`
		SnapshotID           string   `json:"snapshotId"`
		Target               string   `json:"target"`
		Includes             []string `json:"includes"`
		TargetKind           string   `json:"targetKind"`
		AgentID              string   `json:"agentId"`
		DownloadKiBPerSecond int      `json:"downloadKiBPerSecond"`
	}{repositoryID, input.SnapshotID, filepath.Clean(input.Target), input.Includes, input.TargetKind, input.AgentID, downloadKiBPerSecond})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func normalizedRestoreTargetKind(value string) string {
	if value == "agent" {
		return "agent"
	}
	return "local"
}

func (s *Server) restoreDirectory(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, 503, "仓库执行器尚未配置")
		return
	}
	var input directoryRestoreRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "恢复参数无效")
		return
	}
	repositoryID := r.PathValue("id")
	input.TargetKind = normalizedRestoreTargetKind(input.TargetKind)
	if input.ConfirmationID == "" {
		writeError(w, http.StatusConflict, "请先完成恢复预检和管理员密码确认")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	resourcePolicy, _, resourceErr := resources.EffectiveRepositoryResources(r.Context(), repositoryID)
	if resourceErr != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复资源策略")
		return
	}
	confirmation, err := resources.ConsumeRestoreConfirmation(r.Context(), input.ConfirmationID, username, directoryRestoreFingerprint(repositoryID, input, resourcePolicy.DownloadKiBPerSecond), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, "恢复预检已过期、已使用或请求内容已变化")
		return
	}
	record, err := s.operations.Start(operationruntime.StartRequest{
		Kind: "directory_restore", Actor: username, RepositoryID: repositoryID,
		SnapshotID: input.SnapshotID, Target: redactFilesystemTarget(input.Target), Detail: map[string]any{"includeCount": len(input.Includes), "targetKind": input.TargetKind, "agentId": input.AgentID, "downloadKiBPerSecond": resourcePolicy.DownloadKiBPerSecond},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("restoring", map[string]any{"downloadKiBPerSecond": resourcePolicy.DownloadKiBPerSecond})
		if input.TargetKind == "agent" {
			if s.agentRestore == nil {
				return errors.New("Agent directory restore is unavailable")
			}
			sourcePath, _ := confirmation.Summary["sourcePath"].(string)
			return s.agentRestore.Restore(ctx, agentrestore.Request{AgentID: input.AgentID, RepositoryID: repositoryID, SnapshotID: input.SnapshotID, SourcePath: sourcePath, Target: input.Target, Includes: input.Includes, DownloadKiBPerSecond: resourcePolicy.DownloadKiBPerSecond})
		}
		return s.repositories.RestoreDirectory(ctx, repositoryID, input.SnapshotID, input.Target, input.Includes, resourcePolicy.DownloadKiBPerSecond)
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动目录恢复："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}
func (s *Server) restoreDatabase(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.databaseRestore == nil {
		writeError(w, 503, "数据库恢复器尚未配置")
		return
	}
	var input dbrestore.Request
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "恢复参数无效")
		return
	}
	input.RepositoryID = r.PathValue("id")
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	resourcePolicy, _, resourceErr := resources.EffectiveRepositoryResources(r.Context(), input.RepositoryID)
	if resourceErr != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复资源策略")
		return
	}
	input.DownloadKiBPerSecond = resourcePolicy.DownloadKiBPerSecond
	if input.ConfirmationID == "" {
		writeError(w, http.StatusConflict, "请先完成恢复预检和管理员密码确认")
		return
	}
	if _, err := resources.ConsumeRestoreConfirmation(r.Context(), input.ConfirmationID, username, databaseRestoreFingerprint(input), time.Now().UTC()); err != nil {
		writeError(w, http.StatusConflict, "恢复预检已过期、已使用或请求内容已变化")
		return
	}
	record, err := s.operations.Start(operationruntime.StartRequest{
		Kind: "database_restore", Actor: username, RepositoryID: input.RepositoryID,
		SnapshotID: input.SnapshotID, Detail: map[string]any{"connectionId": input.ConnectionID, "database": input.Database, "downloadKiBPerSecond": input.DownloadKiBPerSecond},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("restoring", map[string]any{"preflight": "inline", "downloadKiBPerSecond": input.DownloadKiBPerSecond})
		restoreErr := s.databaseRestore.Restore(ctx, input)
		cleanupRequired := false
		var cleanup interface{ CleanupIsRequired() bool }
		if errors.As(restoreErr, &cleanup) {
			cleanupRequired = cleanup.CleanupIsRequired()
		}
		if strings.HasPrefix(input.ConnectionID, "temporary-dbconn_") && !cleanupRequired {
			s.deleteTemporaryDatabaseConnection(context.WithoutCancel(ctx), username, input.ConnectionID, "restore_finished")
		}
		return restoreErr
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动数据库恢复："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) deleteTemporaryDatabaseConnection(ctx context.Context, actor, id, reason string) {
	if !strings.HasPrefix(id, "temporary-dbconn_") || s.secrets == nil {
		return
	}
	resources, ok := s.store.(*store.Store)
	if !ok {
		return
	}
	secretID, err := resources.DeleteDatabaseConnection(ctx, id)
	if err != nil {
		s.log.Error("delete temporary database connection", "connection_id", id, "error", err)
		return
	}
	if err := s.secrets.Delete(ctx, secretID); err != nil {
		s.log.Error("delete temporary database password", "connection_id", id, "error", err)
	}
	s.appendSemanticAudit(ctx, actor, "database_connection.temporary.delete", "database_connection", id, map[string]any{"reason": reason})
}

func (s *Server) preflightDatabaseRestore(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	preflighter, ok := s.databaseRestore.(databaseRestorePreflighter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "数据库恢复预检不可用")
		return
	}
	var input dbrestore.Request
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "恢复参数无效")
		return
	}
	input.RepositoryID = r.PathValue("id")
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	resourcePolicy, resourceBound, resourceErr := resources.EffectiveRepositoryResources(r.Context(), input.RepositoryID)
	if resourceErr != nil {
		writeError(w, http.StatusInternalServerError, "无法读取恢复资源策略")
		return
	}
	input.DownloadKiBPerSecond = resourcePolicy.DownloadKiBPerSecond
	result, err := preflighter.Preflight(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "数据库恢复预检失败："+err.Error())
		return
	}
	now := time.Now().UTC()
	record := store.RestoreConfirmation{ID: newID("restore_confirmation"), Actor: username, Kind: "database", Fingerprint: databaseRestoreFingerprint(input), Summary: map[string]any{"repositoryId": input.RepositoryID, "snapshotId": input.SnapshotID, "connectionId": input.ConnectionID, "database": input.Database, "metadata": result.Metadata, "target": result.Target, "downloadKiBPerSecond": input.DownloadKiBPerSecond, "resourcePolicySource": restoreResourcePolicySource(resourceBound)}, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := resources.CreateRestoreConfirmation(r.Context(), record); err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存恢复预检")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "restore.preflight", "repository", input.RepositoryID, map[string]any{"confirmationId": record.ID, "kind": "database", "snapshotId": input.SnapshotID, "connectionId": input.ConnectionID, "database": input.Database, "downloadKiBPerSecond": input.DownloadKiBPerSecond})
	writeJSON(w, http.StatusOK, record)
}

func databaseRestoreFingerprint(input dbrestore.Request) string {
	input.ConfirmationID = ""
	encoded, _ := json.Marshal(struct {
		Request              dbrestore.Request `json:"request"`
		DownloadKiBPerSecond int               `json:"downloadKiBPerSecond"`
	}{Request: input, DownloadKiBPerSecond: input.DownloadKiBPerSecond})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func restoreResourcePolicySource(bound bool) string {
	if bound {
		return "task"
	}
	return "unbound_repository"
}

func (s *Server) rotateRepositoryPassword(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, 503, "仓库执行器尚未配置")
		return
	}
	var input struct {
		Password              string `json:"password"`
		PasswordConfirmed     bool   `json:"passwordConfirmed"`
		AdministratorPassword string `json:"administratorPassword"`
	}
	if decodeJSON(r, &input) != nil || !input.PasswordConfirmed || input.AdministratorPassword == "" {
		writeError(w, http.StatusUnprocessableEntity, "必须先确认新仓库密码已安全保存")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.AdministratorPassword); err != nil {
		s.appendSemanticAudit(r.Context(), username, "repository.password.rotation_authorize.failure", "repository", r.PathValue("id"), nil)
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "repository.password.rotation_authorize", "repository", r.PathValue("id"), nil)
	status, err := s.repositories.PasswordRotationStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "仓库不存在或无法读取轮换状态")
		return
	}
	if status.Pending {
		writeError(w, http.StatusConflict, "旧 key 仍等待撤销，请先完成当前轮换流程")
		return
	}
	repositoryID := r.PathValue("id")
	record, _, err := s.operations.StartUnique("repository:"+repositoryID+":password-rotation", operationruntime.StartRequest{Kind: "repository_password_rotation", Actor: username, RepositoryID: repositoryID}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("rotating_password", nil)
		return s.repositories.RotatePassword(ctx, repositoryID, input.Password)
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动密码轮换："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) repositoryPasswordRotationStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, http.StatusServiceUnavailable, "仓库执行器尚未配置")
		return
	}
	status, err := s.repositories.PasswordRotationStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取仓库密码轮换状态")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) revokeOldRepositoryPassword(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, http.StatusServiceUnavailable, "仓库执行器尚未配置")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	ctx, done, ok := s.beginManagedJob(w)
	if !ok {
		return
	}
	defer done()
	if err := s.repositories.RevokeOldPassword(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, "撤销旧仓库 key 失败："+err.Error())
		return
	}
	s.appendSemanticAudit(r.Context(), username, "repository.password.old_key_revoke", "repository", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "old-key-revoked"})
}

func (s *Server) resticVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.installer == nil {
		writeError(w, 503, "Restic 安装器尚未配置")
		return
	}
	items, err := s.installer.Versions(r.Context())
	if err != nil {
		writeError(w, 502, "无法读取官方版本："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"versions": items})
}
func (s *Server) installRestic(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.installer == nil {
		writeError(w, 503, "Restic 安装器尚未配置")
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "版本无效")
		return
	}
	versions, err := s.installer.Versions(r.Context())
	if err != nil || len(versions) == 0 {
		writeError(w, http.StatusBadGateway, "无法验证可安装的 Restic 版本")
		return
	}
	if input.Version == "" {
		input.Version = versions[0]
	}
	allowed := false
	for _, version := range versions {
		allowed = allowed || version == input.Version
	}
	if !allowed {
		writeError(w, http.StatusUnprocessableEntity, "所选 Restic 版本不在当前官方稳定版本列表中")
		return
	}
	version := input.Version
	record, _, err := s.operations.StartUnique("restic:install", operationruntime.StartRequest{Kind: "restic_install", Actor: username, Detail: map[string]any{"version": version}}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("downloading", map[string]any{"version": version})
		path, err := s.installer.Install(ctx, version)
		if err != nil {
			return err
		}
		_ = reporter.Stage("activating", nil)
		s.paths.Restic = path
		if s.selectRestic != nil {
			s.selectRestic(path)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Restic 安装："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) initializeRepository(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, 503, "仓库执行器尚未配置")
		return
	}
	repositoryID := r.PathValue("id")
	record, _, err := s.operations.StartUnique("repository:"+repositoryID+":initialize", operationruntime.StartRequest{
		Kind: "repository_initialize", Actor: username, RepositoryID: repositoryID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("initializing", nil)
		return s.repositories.Initialize(ctx, repositoryID)
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动仓库初始化："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) verifyExistingRepository(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	connector, ok := s.repositories.(repositoryConnector)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "已有仓库连接服务尚未配置")
		return
	}
	repositoryID := r.PathValue("id")
	if repositoryID == "" {
		writeError(w, http.StatusBadRequest, "仓库 ID 无效")
		return
	}
	record, _, err := s.operations.StartUnique("repository:"+repositoryID+":verify-existing", operationruntime.StartRequest{
		Kind: "repository_verify_existing", Actor: username, RepositoryID: repositoryID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("verifying_read_only", nil)
		snapshots, verifyErr := connector.VerifyExisting(ctx, repositoryID)
		if verifyErr != nil {
			return verifyErr
		}
		_ = reporter.Stage("connected", map[string]any{"snapshotCount": len(snapshots)})
		s.appendSemanticAudit(context.WithoutCancel(ctx), username, "repository.reconnect", "repository", repositoryID, map[string]any{"snapshotCount": len(snapshots)})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动仓库重新验证："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) probeRepositoryCapacity(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositoryCapacity == nil {
		writeError(w, http.StatusServiceUnavailable, "仓库容量探测服务尚未配置")
		return
	}
	repositoryID := r.PathValue("id")
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	record, _, err := s.operations.StartUnique("repository:"+repositoryID+":capacity", operationruntime.StartRequest{
		Kind: "repository_capacity_probe", Actor: username, RepositoryID: repositoryID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, err := s.repositoryCapacity.Probe(ctx, repositoryID, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		if err != nil {
			if persistErr := resources.RecordRepositoryCapacityFailure(context.WithoutCancel(ctx), repositoryID, time.Now().UTC(), err.Error()); persistErr != nil {
				s.log.Error("persist manual repository capacity probe failure", "repository_id", repositoryID, "error", persistErr)
			}
		}
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动仓库容量探测："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) getRepositoryCapacityPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	policy, err := resources.RepositoryCapacityPolicy(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "备份仓库不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取容量监控策略")
		return
	}
	status, err := repositoryStatusForCapacity(r.Context(), resources, policy.RepositoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取仓库状态")
		return
	}
	writeJSON(w, http.StatusOK, repositoryCapacityPolicyViewFor(policy, status, time.Now().UTC()))
}

type repositoryCapacityPolicyView struct {
	domain.RepositoryCapacityPolicy
	Stale bool `json:"stale"`
}

func repositoryCapacityPolicyViewFor(policy domain.RepositoryCapacityPolicy, repositoryStatus string, now time.Time) repositoryCapacityPolicyView {
	view := repositoryCapacityPolicyView{RepositoryCapacityPolicy: policy}
	if !policy.Enabled || repositoryStatus == "uninitialized" || repositoryStatus == "disconnected" || policy.ProbeIntervalMinutes <= 0 {
		return view
	}
	baseline := policy.UpdatedAt
	if policy.LastSuccessAt != nil {
		baseline = *policy.LastSuccessAt
	}
	view.Stale = !baseline.IsZero() && !now.Before(baseline.Add(2*time.Duration(policy.ProbeIntervalMinutes)*time.Minute))
	return view
}

func repositoryStatusForCapacity(ctx context.Context, resources *store.Store, repositoryID string) (string, error) {
	repositories, err := resources.ListRepositories(ctx)
	if err != nil {
		return "", err
	}
	for _, repository := range repositories {
		if repository.ID == repositoryID {
			return repository.Status, nil
		}
	}
	return "", sql.ErrNoRows
}

type repositoryCapacityPolicyRequest struct {
	Enabled                 bool    `json:"enabled"`
	ProbeIntervalMinutes    int     `json:"probeIntervalMinutes"`
	MinimumAvailableBytes   uint64  `json:"minimumAvailableBytes"`
	MinimumAvailablePercent float64 `json:"minimumAvailablePercent"`
	ExhaustionWarningDays   int     `json:"exhaustionWarningDays"`
}

func (s *Server) saveRepositoryCapacityPolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input repositoryCapacityPolicyRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "容量监控策略格式无效")
		return
	}
	policy := domain.RepositoryCapacityPolicy{
		RepositoryID: r.PathValue("id"), Enabled: input.Enabled, ProbeIntervalMinutes: input.ProbeIntervalMinutes,
		MinimumAvailableBytes: input.MinimumAvailableBytes, MinimumAvailablePercent: input.MinimumAvailablePercent,
		ExhaustionWarningDays: input.ExhaustionWarningDays,
	}
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "容量监控策略无效："+err.Error())
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if err := resources.SaveRepositoryCapacityPolicy(r.Context(), policy, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "备份仓库不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法保存容量监控策略")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "repository.capacity-policy.update", "repository", policy.RepositoryID, map[string]any{
		"enabled": policy.Enabled, "probeIntervalMinutes": policy.ProbeIntervalMinutes,
		"minimumAvailableBytes": policy.MinimumAvailableBytes, "minimumAvailablePercent": policy.MinimumAvailablePercent,
		"exhaustionWarningDays": policy.ExhaustionWarningDays,
	})
	saved, err := resources.RepositoryCapacityPolicy(r.Context(), policy.RepositoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取已保存的容量监控策略")
		return
	}
	status, err := repositoryStatusForCapacity(r.Context(), resources, policy.RepositoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取仓库状态")
		return
	}
	writeJSON(w, http.StatusOK, repositoryCapacityPolicyViewFor(saved, status, time.Now().UTC()))
}

func (s *Server) listRepositoryCapacitySamples(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "容量历史条数必须在 1 到 500 之间")
			return
		}
		limit = parsed
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if _, err := resources.RepositoryCapacityPolicy(r.Context(), r.PathValue("id")); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "备份仓库不存在")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取容量监控策略")
		return
	}
	items, err := resources.ListRepositoryCapacitySamples(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取容量历史")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getRepositoryCapacityForecast(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if _, err := resources.RepositoryCapacityPolicy(r.Context(), r.PathValue("id")); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "备份仓库不存在")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取容量监控策略")
		return
	}
	forecast, err := resources.RepositoryCapacityForecast(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法计算容量预测")
		return
	}
	writeJSON(w, http.StatusOK, forecast)
}

func (s *Server) repositorySnapshots(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, 503, "仓库执行器尚未配置")
		return
	}
	ctx, done, ok := s.beginManagedJob(w)
	if !ok {
		return
	}
	defer done()
	items, err := s.repositories.Snapshots(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, 502, "无法读取快照："+err.Error())
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) repositorySnapshotContents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	browser, ok := s.repositories.(snapshotContentBrowser)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "快照内容浏览不可用")
		return
	}
	ctx, done, ok := s.beginManagedJob(w)
	if !ok {
		return
	}
	defer done()
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var parseErr error
		limit, parseErr = strconv.Atoi(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "快照分页数量无效")
			return
		}
	}
	query := repositoryservice.SnapshotContentsQuery{
		Path:      r.URL.Query().Get("path"),
		Search:    r.URL.Query().Get("search"),
		Cursor:    r.URL.Query().Get("cursor"),
		Limit:     limit,
		Recursive: r.URL.Query().Get("recursive") == "true",
	}
	items, err := browser.BrowseSnapshotContents(ctx, r.PathValue("id"), r.PathValue("snapshot"), query)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "无法读取快照内容："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) repositorySnapshotDiff(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	browser, ok := s.repositories.(snapshotContentBrowser)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "快照差异比较不可用")
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var parseErr error
		limit, parseErr = strconv.Atoi(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "快照差异样例数量无效")
			return
		}
	}
	ctx, done, ok := s.beginManagedJob(w)
	if !ok {
		return
	}
	defer done()
	result, err := browser.CompareSnapshots(ctx, r.PathValue("id"), r.URL.Query().Get("from"), r.URL.Query().Get("to"), repositoryservice.SnapshotDiffQuery{Path: r.URL.Query().Get("path"), Limit: limit})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "无法比较快照："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) maintainRepository(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.repositories == nil {
		writeError(w, 503, "仓库执行器尚未配置")
		return
	}
	var input struct {
		Retention domain.RetentionPolicy `json:"retention"`
		DryRun    bool                   `json:"dryRun"`
		PreviewID string                 `json:"previewId"`
		Confirmed bool                   `json:"confirmed"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "维护策略无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	repositoryID := r.PathValue("id")
	if input.DryRun {
		if input.Retention.Validate() != nil {
			writeError(w, http.StatusBadRequest, "维护策略无效")
			return
		}
		ctx, done, started := s.beginManagedJob(w)
		if !started {
			return
		}
		defer done()
		summary := repositoryservice.MaintenanceSummary{}
		var err error
		if previewer, supported := s.repositories.(repositoryMaintenancePreviewer); supported {
			summary, err = previewer.PreviewMaintenance(ctx, repositoryID, input.Retention)
		} else {
			err = s.repositories.Maintain(ctx, repositoryID, input.Retention, true)
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "仓库维护预览失败："+err.Error())
			return
		}
		now := time.Now().UTC()
		preview := store.MaintenancePreview{ID: newID("maintenance_preview"), RepositoryID: repositoryID, Retention: input.Retention, PolicyFingerprint: input.Retention.Fingerprint(), KeepCount: summary.KeepCount, RemoveCount: summary.RemoveCount, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		if err := resources.CreateMaintenancePreview(r.Context(), preview); err != nil {
			writeError(w, http.StatusInternalServerError, "无法保存维护预览")
			return
		}
		s.appendSemanticAudit(r.Context(), username, "maintenance.preview", "repository", repositoryID, map[string]any{"previewId": preview.ID, "keepCount": preview.KeepCount, "removeCount": preview.RemoveCount, "policyFingerprint": preview.PolicyFingerprint})
		writeJSON(w, http.StatusOK, preview)
		return
	}
	if !input.Confirmed || input.PreviewID == "" {
		writeError(w, http.StatusConflict, "真实维护必须先完成 dry-run 预览并明确确认")
		return
	}
	policy, err := resources.MaintenancePolicy(r.Context(), repositoryID)
	if err != nil {
		writeError(w, http.StatusConflict, "维护计划不存在或已变化，请重新保存并预览")
		return
	}
	preview, err := resources.ConsumeMaintenancePreview(r.Context(), input.PreviewID, repositoryID, policy.Retention, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusConflict, "维护预览已过期、已使用或策略已变化，请重新预览")
		return
	}
	record, err := s.operations.Start(operationruntime.StartRequest{Kind: "repository_maintenance", Actor: username, RepositoryID: repositoryID, Detail: map[string]any{"previewId": preview.ID, "keepCount": preview.KeepCount, "removeCount": preview.RemoveCount, "policyFingerprint": preview.PolicyFingerprint}}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("maintaining", map[string]any{"previewId": preview.ID, "policyFingerprint": preview.PolicyFingerprint})
		return s.repositories.Maintain(ctx, repositoryID, preview.Retention, false)
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动仓库维护："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) compatibility(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.compatibilityReport(r.Context()))
}

func (s *Server) compatibilityReport(ctx context.Context) compat.Report {
	paths := s.paths
	if paths.Restic == "" {
		paths.Restic, _ = exec.LookPath("restic")
	}
	if paths.MySQLDump == "" {
		paths.MySQLDump, _ = exec.LookPath("mysqldump")
	}
	if paths.MySQLRestore == "" {
		paths.MySQLRestore, _ = exec.LookPath("mysql")
	}
	if paths.PostgresDump == "" {
		paths.PostgresDump, _ = exec.LookPath("pg_dump")
	}
	if paths.PostgresRestore == "" {
		paths.PostgresRestore, _ = exec.LookPath("pg_restore")
	}
	return compat.Merge(compat.System(s.dataDir), s.probe.Tools(ctx, paths))
}

func (s *Server) exportDiagnostics(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	version := s.applicationVersion
	if version == "" {
		version = "development"
	}
	result, err := s.diagnostics.Generate(r.Context(), diagnosticservice.Request{
		ApplicationVersion: version,
		Compatibility:      s.compatibilityReport(r.Context()),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成脱敏诊断包")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "diagnostics.export", "application", "diagnostics", map[string]any{
		"resources": result.Counts.Resources, "compatibilityFindings": result.Counts.CompatibilityFindings,
		"recentFailures": result.Counts.RecentFailures, "activeAlerts": result.Counts.ActiveAlerts,
		"notificationChannels": result.Counts.NotificationChannels, "capacityRepositories": result.Counts.CapacityRepositories,
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="shadoc-diagnostics.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Bytes)
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "任务执行器尚未配置")
		return
	}
	taskID := r.PathValue("id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "任务 ID 无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	tasks, err := resources.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法验证任务状态")
		return
	}
	var task *domain.Task
	for index := range tasks {
		if tasks[index].ID == taskID {
			task = &tasks[index]
			break
		}
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	if !task.Enabled {
		writeError(w, http.StatusConflict, "任务已停用，不能手动运行")
		return
	}
	s.manualMu.Lock()
	if existing := s.manualRuns[taskID]; existing != "" {
		s.manualMu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]string{"taskId": taskID, "operationId": existing, "status": "already_running"})
		return
	}
	s.manualRuns[taskID] = "starting"
	registered := make(chan struct{})
	s.manualMu.Unlock()
	operationKind, operationStage := "backup", "backing_up"
	if task.EffectiveEngine() == domain.RsyncEngine {
		operationKind, operationStage = "sync", "syncing"
	}
	record, err := s.operations.Start(operationruntime.StartRequest{Kind: operationKind, Actor: username, RepositoryID: task.RepositoryID, TaskID: taskID}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		<-registered
		defer func() { s.manualMu.Lock(); delete(s.manualRuns, taskID); s.manualMu.Unlock() }()
		_ = reporter.Stage(operationStage, nil)
		_, runErr := s.runner.Run(ctx, taskID, "", "manual")
		return runErr
	})
	if err != nil {
		s.manualMu.Lock()
		delete(s.manualRuns, taskID)
		s.manualMu.Unlock()
		close(registered)
		writeError(w, http.StatusServiceUnavailable, "无法启动任务："+err.Error())
		return
	}
	s.manualMu.Lock()
	s.manualRuns[taskID] = record.ID
	s.manualMu.Unlock()
	close(registered)
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": taskID, "operationId": record.ID, "status": record.Status})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 100
	}
	items, err := resources.ListRuns(r.Context(), 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取运行记录")
		return
	}
	status := r.URL.Query().Get("status")
	taskNames, repositoryNames, taskRepositories := s.resourceNames(r.Context())
	type namedRun struct {
		store.RunRecord
		TaskName       string `json:"taskName,omitempty"`
		RepositoryName string `json:"repositoryName,omitempty"`
	}
	filtered := make([]namedRun, 0, min(limit, len(items)))
	for _, item := range items {
		if status != "" && item.Status != status {
			continue
		}
		item.RawLog = ""
		filtered = append(filtered, namedRun{RunRecord: item, TaskName: taskNames[item.TaskID], RepositoryName: repositoryNames[taskRepositories[item.TaskID]]})
		if len(filtered) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	filter, err := activityFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "运行记录筛选无效："+err.Error())
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	page, err := resources.ListActivity(r.Context(), filter)
	if err != nil {
		if errors.Is(err, store.ErrInvalidActivityCursor) || errors.Is(err, store.ErrInvalidActivityFilter) {
			writeError(w, http.StatusBadRequest, "运行记录筛选或游标无效")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法读取运行记录")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) taskTrends(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	report, err := resources.TaskTrends(r.Context(), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务健康趋势")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func activityFilterFromRequest(r *http.Request) (store.ActivityFilter, error) {
	query := r.URL.Query()
	filter := store.ActivityFilter{
		RecordType: query.Get("recordType"), ObjectID: query.Get("objectId"), Engine: query.Get("engine"),
		Status: query.Get("status"), Trigger: query.Get("trigger"), Kind: query.Get("kind"), Cursor: query.Get("cursor"),
	}
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return store.ActivityFilter{}, errors.New("limit must be an integer")
		}
		filter.Limit = value
	}
	if raw := query.Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return store.ActivityFilter{}, errors.New("page must be a positive integer")
		}
		filter.Page = value
	}
	if raw := query.Get("from"); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return store.ActivityFilter{}, errors.New("from must be RFC3339 time")
		}
		filter.From = &value
	}
	if raw := query.Get("to"); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return store.ActivityFilter{}, errors.New("to must be RFC3339 time")
		}
		filter.To = &value
	}
	return filter, nil
}

func (s *Server) exportActivity(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	filter, err := activityFilterFromRequest(r)
	if err != nil || filter.Cursor != "" || filter.Page != 0 {
		writeError(w, http.StatusBadRequest, "运行记录导出筛选无效")
		return
	}
	filter.Limit = 200
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	page, err := resources.ListActivity(r.Context(), filter)
	if err != nil {
		if errors.Is(err, store.ErrInvalidActivityCursor) || errors.Is(err, store.ErrInvalidActivityFilter) {
			writeError(w, http.StatusBadRequest, "运行记录导出筛选无效")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法导出运行记录")
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="shadoc-activity.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"record_type", "id", "kind", "engine", "status", "trigger", "object_type", "object_id", "object_name", "task_id", "repository_id", "plan_id", "occurred_at", "started_at", "finished_at", "attempts", "error_summary", "duration_ms", "files_processed", "files_changed", "bytes_processed", "bytes_changed"})
	rowCount := 0
	for {
		for _, item := range page.Items {
			if err := r.Context().Err(); err != nil {
				return
			}
			_ = writer.Write(activityCSVRow(item))
			rowCount++
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return
		}
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
		page, err = resources.ListActivity(r.Context(), filter)
		if err != nil {
			s.log.Error("continue activity export", "error", err)
			return
		}
	}
	s.appendSemanticAudit(r.Context(), username, "activity.export", "application", "activity", map[string]any{"rowCount": rowCount, "recordType": filter.RecordType, "engine": filter.Engine, "status": filter.Status})
}

func activityCSVRow(item store.ActivityItem) []string {
	return []string{
		safeActivityCSVCell(item.RecordType), safeActivityCSVCell(item.ID), safeActivityCSVCell(item.Kind), safeActivityCSVCell(item.Engine),
		safeActivityCSVCell(item.Status), safeActivityCSVCell(item.Trigger), safeActivityCSVCell(item.ObjectType), safeActivityCSVCell(item.ObjectID),
		safeActivityCSVCell(item.ObjectName), safeActivityCSVCell(item.TaskID), safeActivityCSVCell(item.RepositoryID), safeActivityCSVCell(item.PlanID),
		item.OccurredAt.UTC().Format(time.RFC3339Nano), activityCSVTime(item.StartedAt), activityCSVTime(item.FinishedAt),
		strconv.Itoa(item.AttemptCount), safeActivityCSVCell(item.ErrorSummary), activityCSVMetric(item.Metrics, func(metrics *store.RunMetrics) *int64 { return metrics.DurationMilliseconds }),
		activityCSVMetric(item.Metrics, func(metrics *store.RunMetrics) *int64 { return metrics.FilesProcessed }), activityCSVMetric(item.Metrics, func(metrics *store.RunMetrics) *int64 { return metrics.FilesChanged }),
		activityCSVMetric(item.Metrics, func(metrics *store.RunMetrics) *int64 { return metrics.BytesProcessed }), activityCSVMetric(item.Metrics, func(metrics *store.RunMetrics) *int64 { return metrics.BytesChanged }),
	}
}

func activityCSVMetric(metrics *store.RunMetrics, value func(*store.RunMetrics) *int64) string {
	if metrics == nil || value(metrics) == nil {
		return ""
	}
	return strconv.FormatInt(*value(metrics), 10)
}

func activityCSVTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func safeActivityCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) || value != "" && strings.ContainsRune("\t\r", rune(value[0])) {
		value = "'" + value
	}
	const maximumBytes = 512
	if len(value) <= maximumBytes {
		return value
	}
	cut := maximumBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	run, err := s.resourceStore(w).Run(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "运行记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取运行详情")
		return
	}
	logBytes := len([]byte(run.RawLog))
	run.RawLog = ""
	writeJSON(w, http.StatusOK, struct {
		store.RunRecord
		LogBytes int `json:"logBytes"`
	}{RunRecord: run, LogBytes: logBytes})
}

func (s *Server) getRunLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	run, err := s.resourceStore(w).Run(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "运行记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取运行日志")
		return
	}
	if run.RawLogExpired {
		writeError(w, http.StatusGone, "运行日志已按生命周期策略过期")
		return
	}
	const maxLogBytes = 4 << 20
	log := []byte(run.RawLog)
	if len(log) > maxLogBytes {
		log = log[:maxLogBytes]
		w.Header().Set("X-Log-Truncated", "true")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(run.ID)+`.log"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(log)
}

func safeFilename(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
}

func (s *Server) listAudits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	filter, err := auditFilterFromRequest(r)
	page, pageSize := 1, 25
	if value := r.URL.Query().Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 {
			err = errInvalidAuditFilter
		}
	}
	if value := r.URL.Query().Get("pageSize"); value != "" && err == nil {
		pageSize, err = strconv.Atoi(value)
		if err != nil || pageSize < 1 || pageSize > 100 {
			err = errInvalidAuditFilter
		}
	}
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize
	resources, ok := s.store.(*store.Store)
	if !ok && err == nil {
		err = errors.New("resource store unavailable")
	}
	var result store.AuditPage
	if err == nil {
		result, err = resources.FilterAudits(r.Context(), filter)
	}
	if err != nil {
		if errors.Is(err, errInvalidAuditFilter) {
			writeError(w, http.StatusBadRequest, "审计筛选时间无效")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法读取审计记录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result.Items, "page": page, "pageSize": pageSize, "total": result.Total})
}

var errInvalidAuditFilter = errors.New("invalid audit filter")

func auditFilterFromRequest(r *http.Request) (store.AuditFilter, error) {
	var filter store.AuditFilter
	parse := func(name string) (*time.Time, error) {
		value := strings.TrimSpace(r.URL.Query().Get(name))
		if value == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, errInvalidAuditFilter
		}
		return &parsed, nil
	}
	from, err := parse("from")
	if err != nil {
		return filter, err
	}
	to, err := parse("to")
	if err != nil {
		return filter, err
	}
	filter.Action = strings.TrimSpace(r.URL.Query().Get("action"))
	filter.From, filter.To = from, to
	return filter, nil
}

func (s *Server) filteredAudits(r *http.Request) ([]store.AuditRecord, error) {
	resources, ok := s.store.(*store.Store)
	if !ok {
		return nil, errors.New("resource store unavailable")
	}
	filter, err := auditFilterFromRequest(r)
	if err != nil {
		return nil, err
	}
	page, err := resources.FilterAudits(r.Context(), filter)
	return page.Items, err
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) applicationVersionInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	version := s.applicationVersion
	if version == "" {
		version = "development"
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

func (s *Server) applicationReleaseInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.applicationReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "应用发布信息服务尚未配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	latest, err := s.applicationReleases.Latest(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法读取官方稳定版发布信息")
		return
	}
	current := s.currentApplicationVersion()
	managed := s.applicationUpdater != nil && s.applicationUpdater.Managed()
	writeJSON(w, http.StatusOK, map[string]any{
		"currentVersion":  current,
		"latest":          latest,
		"updateAvailable": appinstall.IsUpgradeAvailable(current, latest.Version),
		"managed":         managed,
	})
}

func (s *Server) startApplicationUpdate(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct {
		Version               string `json:"version"`
		AdministratorPassword string `json:"administratorPassword"`
		ImpactConfirmed       bool   `json:"impactConfirmed"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if !input.ImpactConfirmed || input.AdministratorPassword == "" || len(input.AdministratorPassword) > 4096 || !appinstall.ValidReleaseVersion(input.Version) {
		writeError(w, http.StatusUnprocessableEntity, "必须确认升级影响并提供有效版本和管理员密码")
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), username, input.AdministratorPassword); err != nil {
		s.appendSemanticAudit(r.Context(), username, "application.update_authorize.failure", "application", s.currentApplicationVersion(), nil)
		writeError(w, http.StatusUnauthorized, "管理员密码错误")
		return
	}
	if s.applicationReleases == nil || s.applicationUpdater == nil || !s.applicationUpdater.Managed() || s.applicationOps == nil {
		writeError(w, http.StatusConflict, "当前运行方式不支持从管理页面升级，请使用原生命令")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	latest, err := s.applicationReleases.Latest(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法验证官方稳定版发布信息")
		return
	}
	if !latest.Compatible || input.Version != latest.Version {
		writeError(w, http.StatusConflict, "所选版本不是当前平台可用的官方最新稳定版")
		return
	}
	if !appinstall.IsUpgradeAvailable(s.currentApplicationVersion(), latest.Version) {
		writeError(w, http.StatusConflict, "当前版本不低于官方最新稳定版，不能执行页面升级")
		return
	}
	if _, err := s.applicationOps.ActiveApplicationUpdate(r.Context()); err == nil {
		writeError(w, http.StatusConflict, "已有应用升级正在执行")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "无法检查应用升级状态")
		return
	}
	operationID, err := newApplicationUpdateID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建应用升级操作")
		return
	}
	now := time.Now().UTC()
	record := store.OperationRecord{
		ID: operationID, Kind: "application_update", Actor: username, Target: latest.Version,
		Status: "queued", Stage: "queued", CreatedAt: now,
		Detail: map[string]any{"currentVersion": s.currentApplicationVersion(), "targetVersion": latest.Version, "expectedDisconnect": true},
	}
	if err := s.applicationOps.CreateOperation(r.Context(), record); err != nil {
		if _, activeErr := s.applicationOps.ActiveApplicationUpdate(r.Context()); activeErr == nil {
			writeError(w, http.StatusConflict, "已有应用升级正在执行")
			return
		}
		writeError(w, http.StatusInternalServerError, "无法创建应用升级操作")
		return
	}
	if err := s.applicationOps.StartOperation(r.Context(), operationID, "launching_updater", now); err != nil {
		_ = s.applicationOps.FinishOperation(context.WithoutCancel(r.Context()), operationID, "failed", "failed", time.Now().UTC(), "无法启动应用升级操作", nil)
		writeError(w, http.StatusInternalServerError, "无法创建应用升级操作")
		return
	}
	if err := s.applicationUpdater.Launch(r.Context(), operationID, latest.Version); err != nil {
		_ = s.applicationOps.FinishOperation(context.WithoutCancel(r.Context()), operationID, "failed", "failed", time.Now().UTC(), "无法启动受保护的应用升级助手", map[string]any{"healthVerified": false, "rollbackAttempted": false, "rollbackVerified": false})
		writeError(w, http.StatusServiceUnavailable, "无法启动受保护的应用升级助手")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "application.update.authorized", "application", latest.Version, map[string]any{"operationId": operationID, "fromVersion": s.currentApplicationVersion(), "targetVersion": latest.Version})
	writeJSON(w, http.StatusAccepted, map[string]any{"operationId": operationID, "status": "running", "stage": "launching_updater", "kind": "application_update", "expectedDisconnect": true})
}

func (s *Server) currentApplicationVersion() string {
	if s.applicationVersion == "" {
		return "development"
	}
	return s.applicationVersion
}

func newApplicationUpdateID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "op_" + hex.EncodeToString(raw), nil
}

type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token,omitempty"`
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var input credentialsRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	local := splitErr == nil && ip != nil && ip.IsLoopback()
	tokenOK := s.setupToken != "" && subtle.ConstantTimeCompare([]byte(input.Token), []byte(s.setupToken)) == 1
	if !local && !tokenOK {
		writeError(w, http.StatusForbidden, "首次初始化只允许从本机访问")
		return
	}
	session, err := s.auth.Setup(r.Context(), input.Username, input.Password)
	if errors.Is(err, auth.ErrAlreadyInitialized) {
		writeError(w, http.StatusConflict, "应用已经初始化")
		return
	}
	if errors.Is(err, auth.ErrInvalidInput) {
		writeError(w, http.StatusUnprocessableEntity, "管理员名称至少 3 个字符，密码至少 12 个字符")
		return
	}
	if err != nil {
		s.log.Error("setup administrator", "error", err)
		writeError(w, http.StatusInternalServerError, "初始化失败")
		return
	}
	setSessionCookie(w, session)
	writeJSON(w, http.StatusCreated, map[string]string{"username": session.Username})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input credentialsRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	session, err := s.auth.Login(r.Context(), input.Username, input.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.appendSemanticAudit(r.Context(), strings.TrimSpace(input.Username), "auth.login.failure", "administrator", strings.TrimSpace(input.Username), map[string]any{"remoteAddress": r.RemoteAddr})
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if err != nil {
		s.log.Error("login administrator", "error", err)
		writeError(w, http.StatusInternalServerError, "登录失败")
		return
	}
	s.appendSemanticAudit(r.Context(), session.Username, "auth.login.success", "administrator", session.Username, map[string]any{"remoteAddress": r.RemoteAddr})
	setSessionCookie(w, session)
	writeJSON(w, http.StatusOK, map[string]string{"username": session.Username})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("rc_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	username, err := s.auth.Authenticate(r.Context(), cookie.Value)
	if errors.Is(err, auth.ErrUnauthenticated) {
		writeError(w, http.StatusUnauthorized, "会话无效或已过期")
		return
	}
	if err != nil {
		s.log.Error("authenticate session", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取会话")
		return
	}
	if csrf, csrfErr := r.Cookie("rc_csrf"); csrfErr == nil && s.auth.ValidateCSRF(r.Context(), cookie.Value, csrf.Value) {
		w.Header().Set("X-CSRF-Token", csrf.Value)
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if cookie, err := r.Cookie("rc_session"); err == nil {
		if err := s.auth.Logout(r.Context(), cookie.Value); err != nil {
			s.log.Error("logout administrator", "error", err)
			writeError(w, http.StatusInternalServerError, "退出失败")
			return
		}
	}
	s.appendSemanticAudit(r.Context(), username, "auth.logout", "administrator", username, nil)
	http.SetCookie(w, &http.Cookie{
		Name:     "rc_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{Name: "rc_csrf", Value: "", Path: "/", SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func setSessionCookie(w http.ResponseWriter, session auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     "rc_session",
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  session.ExpiresAt,
		MaxAge:   24 * 60 * 60,
	})
	http.SetCookie(w, &http.Cookie{
		Name: "rc_csrf", Value: session.CSRFToken, Path: "/",
		SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt, MaxAge: 24 * 60 * 60,
	})
	w.Header().Set("X-CSRF-Token", session.CSRFToken)
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

type createRemoteHostRequest struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	PrivateKey      string `json:"privateKey"`
	KeyMode         string `json:"keyMode"`
	HostFingerprint string `json:"hostFingerprint"`
}

func (s *Server) createRemoteHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	var input createRemoteHostRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	now := time.Now().UTC()
	host := domain.RemoteHost{
		ID: newID("host"), Name: input.Name, Host: input.Host, Port: input.Port,
		Username: input.Username, HostFingerprint: input.HostFingerprint,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := host.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "远程主机配置无效")
		return
	}
	privateKey := []byte(input.PrivateKey)
	publicKey := ""
	var err error
	if input.KeyMode == "generated" {
		privateKey, publicKey, err = sshhost.GenerateKeyPair()
		if err != nil {
			s.log.Error("generate SSH key pair", "error", err)
			writeError(w, http.StatusInternalServerError, "无法生成 SSH 密钥对")
			return
		}
	} else if len(privateKey) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "请提供 SSH 私钥或选择由应用生成")
		return
	}
	secretID, err := s.secrets.Put(r.Context(), "ssh-private-key", privateKey)
	if err != nil {
		s.log.Error("store ssh private key", "error", err)
		writeError(w, http.StatusInternalServerError, "无法保存 SSH 私钥")
		return
	}
	resources, ok := s.store.(*store.Store)
	if !ok {
		_ = s.secrets.Delete(r.Context(), secretID)
		writeError(w, http.StatusInternalServerError, "资源存储不可用")
		return
	}
	if err := resources.CreateRemoteHost(r.Context(), host, secretID); err != nil {
		_ = s.secrets.Delete(r.Context(), secretID)
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "远程主机名称已存在")
			return
		}
		s.log.Error("create remote host", "error", err)
		writeError(w, http.StatusInternalServerError, "无法创建远程主机")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		domain.RemoteHost
		PublicKey string `json:"publicKey,omitempty"`
	}{RemoteHost: host, PublicKey: publicKey})
}

func (s *Server) remoteHostPublicKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	resources, ok := s.store.(*store.Store)
	if !ok {
		writeError(w, http.StatusInternalServerError, "资源存储不可用")
		return
	}
	secretID, err := resources.RemoteHostPrivateKeySecretID(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "远程主机不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取远程主机密钥")
		return
	}
	privateKey, err := s.secrets.Get(r.Context(), secretID, "ssh-private-key")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取 SSH 私钥")
		return
	}
	publicKey, err := sshhost.PublicKey(privateKey)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "已保存的 SSH 私钥无法导出公钥")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": publicKey})
}

func (s *Server) testRemoteHostConnection(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	resources, ok := s.store.(*store.Store)
	if !ok {
		writeError(w, http.StatusInternalServerError, "资源存储不可用")
		return
	}
	hostID := r.PathValue("id")
	hosts, err := resources.ListRemoteHosts(r.Context())
	if err != nil {
		s.log.Error("list remote hosts for connection test", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取远程主机")
		return
	}
	var host *domain.RemoteHost
	for index := range hosts {
		if hosts[index].ID == hostID {
			host = &hosts[index]
			break
		}
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "远程主机不存在")
		return
	}
	if strings.TrimSpace(host.HostFingerprint) == "" {
		writeError(w, http.StatusUnprocessableEntity, "请先获取并核对主机密钥，再测试连接")
		return
	}
	secretID, err := resources.RemoteHostPrivateKeySecretID(r.Context(), host.ID)
	if err != nil {
		s.log.Error("load remote host private key id for connection test", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取 SSH 私钥")
		return
	}
	privateKey, err := s.secrets.Get(r.Context(), secretID, "ssh-private-key")
	if err != nil {
		s.log.Error("load remote host private key for connection test", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取 SSH 私钥")
		return
	}
	if err := sshhost.TestConnection(r.Context(), host.Host, host.Port, host.Username, privateKey, host.HostFingerprint); err != nil {
		s.log.Info("remote host connection test failed", "host_id", host.ID, "error", err)
		writeError(w, http.StatusBadGateway, "SSH 连接验证失败："+err.Error())
		return
	}
	s.appendSemanticAudit(r.Context(), username, "remote_host.connection_test", "remote_host", host.ID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (s *Server) listRemoteHosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources, ok := s.store.(*store.Store)
	if !ok {
		writeError(w, http.StatusInternalServerError, "资源存储不可用")
		return
	}
	hosts, err := resources.ListRemoteHosts(r.Context())
	if err != nil {
		s.log.Error("list remote hosts", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取远程主机")
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	cookie, err := r.Cookie("rc_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "未登录")
		return "", false
	}
	username, err := s.auth.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "会话无效或已过期")
		return "", false
	}
	return username, true
}

func (s *Server) requireMutationSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	username, ok := s.requireSession(w, r)
	if !ok {
		return "", false
	}
	cookie, _ := r.Cookie("rc_session")
	if !s.auth.ValidateCSRF(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token")) {
		writeError(w, http.StatusForbidden, "CSRF 令牌无效")
		return "", false
	}
	return username, true
}

func newID(prefix string) string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(raw)
}

type maintenancePolicyRequest struct {
	Schedule             domain.Schedule        `json:"schedule"`
	Timezone             string                 `json:"timezone"`
	Retention            domain.RetentionPolicy `json:"retention"`
	Enabled              bool                   `json:"enabled"`
	CatchUpWindowMinutes *int                   `json:"catchUpWindowMinutes"`
	PreviewID            string                 `json:"previewId"`
}

func (p maintenancePolicyRequest) policy(repositoryID string, defaultCatchUp int, updatedAt time.Time) domain.MaintenancePolicy {
	catchUp := defaultCatchUp
	if p.CatchUpWindowMinutes != nil {
		catchUp = *p.CatchUpWindowMinutes
	}
	return domain.MaintenancePolicy{RepositoryID: repositoryID, Schedule: p.Schedule, Timezone: p.Timezone, Retention: p.Retention, Enabled: p.Enabled, CatchUpWindowMinutes: catchUp, UpdatedAt: updatedAt}
}

type createRepositoryRequest struct {
	Name              string                    `json:"name"`
	Engine            domain.EngineKind         `json:"engine"`
	Kind              domain.RepositoryKind     `json:"kind"`
	RemoteHostID      string                    `json:"remoteHostId"`
	Path              string                    `json:"path"`
	Password          string                    `json:"password"`
	PasswordConfirmed bool                      `json:"passwordConfirmed"`
	Maintenance       *maintenancePolicyRequest `json:"maintenance,omitempty"`
	S3                *s3RepositoryRequest      `json:"s3,omitempty"`
}

type s3RepositoryRequest struct {
	Endpoint             string `json:"endpoint"`
	Bucket               string `json:"bucket"`
	Region               string `json:"region"`
	Prefix               string `json:"prefix"`
	PathStyle            bool   `json:"pathStyle"`
	AccessKey            string `json:"accessKey"`
	SecretKey            string `json:"secretKey"`
	CredentialsConfirmed bool   `json:"credentialsConfirmed"`
}

func applyS3RepositoryInput(repository *domain.Repository, input createRepositoryRequest) {
	if repository.EffectiveKind() != domain.S3Repository || input.S3 == nil {
		return
	}
	repository.Path = input.S3.Prefix
	repository.RemoteHostID = ""
	repository.S3 = &domain.S3RepositoryConfig{
		Endpoint: input.S3.Endpoint, Bucket: input.S3.Bucket, Region: input.S3.Region,
		PathStyle: input.S3.PathStyle, CredentialsConfigured: true,
	}
}

func s3Credentials(input createRepositoryRequest, required bool) (*s3backend.Credentials, error) {
	if input.Kind != domain.S3Repository {
		if input.S3 != nil {
			return nil, errors.New("S3 settings require an S3 repository")
		}
		return nil, nil
	}
	if input.S3 == nil {
		return nil, errors.New("S3 settings are required")
	}
	provided := input.S3.AccessKey != "" || input.S3.SecretKey != ""
	if !provided && !required {
		return nil, nil
	}
	credentials := &s3backend.Credentials{AccessKey: input.S3.AccessKey, SecretKey: input.S3.SecretKey}
	if !input.S3.CredentialsConfirmed {
		return nil, errors.New("S3 credentials must be confirmed")
	}
	if _, err := s3backend.EncodeCredentials(*credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func (s *Server) createRepository(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	var input createRepositoryRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	now := time.Now().UTC()
	repository := domain.Repository{
		ID: newID("repo"), Name: input.Name, Engine: input.Engine, Kind: input.Kind, RemoteHostID: input.RemoteHostID, Path: input.Path,
		Status: "uninitialized", CreatedAt: now, UpdatedAt: now,
	}
	if repository.Kind == "" {
		repository.Kind = domain.SFTPRepository
	}
	applyS3RepositoryInput(&repository, input)
	if repository.EffectiveEngine() == domain.RsyncEngine {
		repository.Status = "ready"
	}
	credentials, credentialsErr := s3Credentials(input, repository.EffectiveKind() == domain.S3Repository)
	if err := repository.Validate(); err != nil || credentialsErr != nil || (repository.EffectiveEngine() == domain.ResticEngine && (len(input.Password) < 12 || !input.PasswordConfirmed)) {
		writeError(w, http.StatusUnprocessableEntity, "仓库配置无效")
		return
	}
	if input.Maintenance != nil && repository.EffectiveEngine() != domain.ResticEngine {
		writeError(w, http.StatusUnprocessableEntity, "rsync 同步仓库不支持 Restic 维护策略")
		return
	}
	var maintenancePolicy *domain.MaintenancePolicy
	if input.Maintenance != nil {
		policy := input.Maintenance.policy(repository.ID, defaultCatchUpWindowMinutes, now)
		maintenancePolicy = &policy
		if err := policy.Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "仓库维护配置无效："+err.Error())
			return
		}
	}
	secretID := ""
	var err error
	if repository.EffectiveEngine() == domain.ResticEngine {
		secretID, err = s.secrets.Put(r.Context(), "repository-password", []byte(input.Password))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法保存仓库密码")
			return
		}
	}
	backendSecretID := ""
	if credentials != nil {
		encoded, _ := s3backend.EncodeCredentials(*credentials)
		backendSecretID, err = s.secrets.Put(r.Context(), s3backend.CredentialPurpose, encoded)
		clear(encoded)
		if err != nil {
			_ = s.secrets.Delete(context.WithoutCancel(r.Context()), secretID)
			writeError(w, http.StatusInternalServerError, "无法保存 S3 凭据")
			return
		}
		repository.BackendSecretID = backendSecretID
	}
	resources := s.resourceStore(w)
	if resources == nil {
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), secretID)
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), backendSecretID)
		return
	}
	if err := resources.CreateRepository(r.Context(), repository, secretID); err != nil {
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), secretID)
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), backendSecretID)
		writeResourceError(w, err, "无法创建仓库")
		return
	}
	if maintenancePolicy != nil {
		if err := resources.SaveMaintenancePolicy(r.Context(), *maintenancePolicy); err != nil {
			writeError(w, http.StatusInternalServerError, "无法保存仓库维护配置")
			return
		}
	}
	writeJSON(w, http.StatusCreated, repository)
}

func (s *Server) connectExistingRepository(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	connector, ok := s.repositories.(repositoryConnector)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "已有仓库连接服务尚未配置")
		return
	}
	var input createRepositoryRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	if input.Engine == "" {
		input.Engine = domain.ResticEngine
	}
	if input.Kind == "" {
		input.Kind = domain.SFTPRepository
	}
	now := time.Now().UTC()
	candidate := domain.Repository{
		ID: newID("repo"), Name: input.Name, Engine: input.Engine, Kind: input.Kind,
		RemoteHostID: input.RemoteHostID, Path: input.Path, CreatedAt: now, UpdatedAt: now,
	}
	applyS3RepositoryInput(&candidate, input)
	credentials, credentialsErr := s3Credentials(input, candidate.EffectiveKind() == domain.S3Repository)
	if candidate.EffectiveEngine() != domain.ResticEngine || candidate.Validate() != nil || credentialsErr != nil || input.Password == "" || !input.PasswordConfirmed || input.Maintenance != nil {
		writeError(w, http.StatusUnprocessableEntity, "已有 Restic 仓库连接配置无效")
		return
	}
	password := input.Password
	input.Password = ""
	record, _, err := s.operations.StartUnique("repository:"+candidate.ID+":connect", operationruntime.StartRequest{
		Kind: "repository_connect", Actor: username, RepositoryID: candidate.ID, Detail: map[string]any{"mode": "existing"},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_ = reporter.Stage("verifying_read_only", nil)
		snapshots, connectErr := connector.ConnectExisting(ctx, candidate, password, credentials)
		if connectErr != nil {
			return connectErr
		}
		_ = reporter.Stage("connected", map[string]any{"snapshotCount": len(snapshots)})
		s.appendSemanticAudit(context.WithoutCancel(ctx), username, "repository.connect", "repository", candidate.ID, map[string]any{"mode": "existing", "snapshotCount": len(snapshots)})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动已有仓库连接："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"repositoryId": candidate.ID, "operationId": record.ID, "status": record.Status})
}

func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	items, err := resources.ListRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取仓库")
		return
	}
	policies, err := resources.ListRepositoryCapacityPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取容量监控策略")
		return
	}
	tasks, _ := resources.ListTasks(r.Context())
	runs, _ := resources.ListRuns(r.Context(), 1000)
	plans, _ := resources.ListPlans(r.Context())
	taskRepository := map[string]string{}
	for _, task := range tasks {
		taskRepository[task.ID] = task.RepositoryID
	}
	for _, run := range runs {
		repositoryID := taskRepository[run.TaskID]
		for index := range items {
			if items[index].ID == repositoryID && items[index].LastRun == nil {
				items[index].LastRun = &domain.RepositoryRun{Status: run.Status, StartedAt: run.StartedAt, Summary: run.Summary}
			}
		}
	}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		anchor := plan.ScheduleAnchorAt
		if anchor.IsZero() {
			anchor = plan.CreatedAt
		}
		cursor := anchor
		if occurrence, occurrenceErr := resources.LatestScheduleOccurrence(r.Context(), "plan", plan.ID, anchor); occurrenceErr == nil {
			cursor = occurrence.ScheduledAt
		}
		when, nextErr := schedule.NextAnchored(plan.Schedule, plan.Timezone, anchor, cursor)
		if nextErr != nil {
			continue
		}
		for _, taskID := range plan.TaskIDs {
			repositoryID := taskRepository[taskID]
			for index := range items {
				formatted := when.Format(time.RFC3339)
				if items[index].ID == repositoryID && (items[index].NextRun == "" || formatted < items[index].NextRun) {
					items[index].NextRun = formatted
				}
			}
		}
	}
	policyByRepository := make(map[string]domain.RepositoryCapacityPolicy, len(policies))
	for _, policy := range policies {
		policyByRepository[policy.RepositoryID] = policy
	}
	type repositoryListItem struct {
		domain.Repository
		CapacityPolicy *repositoryCapacityPolicyView `json:"capacityPolicy,omitempty"`
	}
	now := time.Now().UTC()
	response := make([]repositoryListItem, 0, len(items))
	for _, item := range items {
		view := repositoryListItem{Repository: item}
		if policy, ok := policyByRepository[item.ID]; ok {
			policyView := repositoryCapacityPolicyViewFor(policy, item.Status, now)
			view.CapacityPolicy = &policyView
		}
		response = append(response, view)
	}
	writeJSON(w, http.StatusOK, response)
}

type createDatabaseConnectionRequest struct {
	Name       string                   `json:"name"`
	Engine     domain.DatabaseEngine    `json:"engine"`
	Purpose    domain.ConnectionPurpose `json:"purpose"`
	Network    domain.NetworkKind       `json:"network"`
	Host       string                   `json:"host"`
	Port       int                      `json:"port"`
	SocketPath string                   `json:"socketPath"`
	Username   string                   `json:"username"`
	Password   string                   `json:"password"`
	TLS        domain.TLSConfig         `json:"tls"`
	ToolPaths  map[string]string        `json:"toolPaths"`
}

func (s *Server) createDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	var input createDatabaseConnectionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	now := time.Now().UTC()
	connection := domain.DatabaseConnection{
		ID: newID("dbconn"), Name: input.Name, Engine: input.Engine, Purpose: input.Purpose,
		Network: input.Network, Host: input.Host, Port: input.Port, SocketPath: input.SocketPath,
		Username: input.Username, TLS: input.TLS, ToolPaths: input.ToolPaths, CreatedAt: now, UpdatedAt: now,
	}
	if err := connection.Validate(); err != nil || input.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "数据库连接配置无效")
		return
	}
	verification := s.databaseVerifier.Verify(r.Context(), connection, input.Password)
	connection.Status = "ready"
	if verification.Error != "" {
		connection.Status = "draft"
	}
	connection.Preflight = domain.DatabasePreflight{CheckedAt: verification.CheckedAt, ClientVersion: verification.ClientVersion, ServerVersion: verification.ServerVersion, Error: verification.Error}
	secretID, err := s.secrets.Put(r.Context(), "database-"+string(connection.Purpose)+"-password", []byte(input.Password))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存数据库密码")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		_ = s.secrets.Delete(r.Context(), secretID)
		return
	}
	if err := resources.CreateDatabaseConnection(r.Context(), connection, secretID); err != nil {
		_ = s.secrets.Delete(r.Context(), secretID)
		writeResourceError(w, err, "无法创建数据库连接")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "database_connection.preflight", "database_connection", connection.ID, map[string]any{"status": connection.Status, "clientVersion": connection.Preflight.ClientVersion, "serverVersion": connection.Preflight.ServerVersion, "error": connection.Preflight.Error})
	writeJSON(w, http.StatusCreated, connection)
}

func (s *Server) createTemporaryDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	var input createDatabaseConnectionRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	input.Purpose = domain.RestoreConnection
	now := time.Now().UTC()
	connection := domain.DatabaseConnection{ID: newID("temporary-dbconn"), Name: input.Name, Engine: input.Engine, Purpose: input.Purpose, Network: input.Network, Host: input.Host, Port: input.Port, SocketPath: input.SocketPath, Username: input.Username, TLS: input.TLS, ToolPaths: input.ToolPaths, CreatedAt: now, UpdatedAt: now}
	if err := connection.Validate(); err != nil || input.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "临时恢复连接配置无效")
		return
	}
	verification := s.databaseVerifier.Verify(r.Context(), connection, input.Password)
	if verification.Error != "" {
		writeError(w, http.StatusUnprocessableEntity, "临时恢复连接预检失败："+verification.Error)
		return
	}
	connection.Status = "ready"
	connection.Preflight = domain.DatabasePreflight{CheckedAt: verification.CheckedAt, ClientVersion: verification.ClientVersion, ServerVersion: verification.ServerVersion}
	secretID, err := s.secrets.Put(r.Context(), "temporary-database-restore-password", []byte(input.Password))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保护临时数据库密码")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		_ = s.secrets.Delete(r.Context(), secretID)
		return
	}
	if err := resources.CreateDatabaseConnection(r.Context(), connection, secretID); err != nil {
		_ = s.secrets.Delete(r.Context(), secretID)
		writeResourceError(w, err, "无法创建临时恢复连接")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "database_connection.temporary.create", "database_connection", connection.ID, map[string]any{"clientVersion": connection.Preflight.ClientVersion, "serverVersion": connection.Preflight.ServerVersion})
	writeJSON(w, http.StatusCreated, struct {
		domain.DatabaseConnection
		Temporary bool `json:"temporary"`
	}{connection, true})
}

func (s *Server) listDatabaseConnections(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	s.cleanupExpiredTemporaryDatabaseConnections(r.Context(), resources)
	items, err := resources.ListDatabaseConnections(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取数据库连接")
		return
	}
	saved := make([]domain.DatabaseConnection, 0, len(items))
	for _, item := range items {
		if !strings.HasPrefix(item.ID, "temporary-dbconn_") {
			saved = append(saved, item)
		}
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) cleanupExpiredTemporaryDatabaseConnections(ctx context.Context, resources *store.Store) {
	secretIDs, err := resources.DeleteExpiredTemporaryDatabaseConnections(ctx, time.Now().UTC().Add(-15*time.Minute))
	if err != nil {
		s.log.Error("expire temporary database connections", "error", err)
		return
	}
	for _, secretID := range secretIDs {
		if s.secrets != nil {
			_ = s.secrets.Delete(ctx, secretID)
		}
	}
	if len(secretIDs) > 0 {
		s.appendSemanticAudit(ctx, "system", "database_connection.temporary.expire", "database_connection", "expired", map[string]any{"count": len(secretIDs)})
	}
}

type taskMutationInput struct {
	domain.Task
	PreviewID            string `json:"previewId,omitempty"`
	RsyncDeleteConfirmed bool   `json:"rsyncDeleteConfirmed,omitempty"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	var input taskMutationInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	task := input.Task
	// A scope confirmation is server-issued evidence. Never accept one from a
	// mutation body, even if all fields look structurally valid.
	task.ScopeConfirmation = domain.TaskScopeConfirmation{}
	task.ID = newID("task")
	now := time.Now().UTC()
	task.CreatedAt, task.UpdatedAt = now, now
	if err := task.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if taskScopeRequiresPreview(task) && task.Enabled {
		writeError(w, http.StatusConflict, "目录或 rsync 任务必须先保存为停用草稿并完成范围预览")
		return
	}
	if err := validateTaskActivation(r.Context(), resources, task); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := resources.CreateTask(r.Context(), task); err != nil {
		writeResourceError(w, err, "无法创建任务")
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) previewTaskScope(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.taskPreviewer == nil {
		writeError(w, http.StatusServiceUnavailable, "任务范围预览未启用")
		return
	}
	preview, err := s.taskPreviewer.Preview(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	case errors.Is(err, taskpreview.ErrAgentUnavailable):
		writeError(w, http.StatusConflict, "目标 Agent 离线或缺少范围预览能力")
		return
	case errors.Is(err, taskpreview.ErrUnsupportedTask):
		writeError(w, http.StatusUnprocessableEntity, "该任务不需要目录范围预览")
		return
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "等待任务范围预览超时")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "任务范围预览失败")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "task.scope.preview", "task", preview.TaskID, map[string]any{"previewId": preview.ID, "fingerprint": preview.Fingerprint, "requiresDeleteConfirmation": preview.RequiresDeleteConfirmation, "truncated": preview.Summary["truncated"]})
	writeJSON(w, http.StatusCreated, preview)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	items, err := resources.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	agents, err := resources.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取 Agent")
		return
	}
	type agentView struct {
		ID                  string     `json:"id"`
		RemoteHostID        string     `json:"remoteHostId,omitempty"`
		CertificateSerial   string     `json:"certificateSerial"`
		CertificateNotAfter *time.Time `json:"certificateNotAfter,omitempty"`
		CertificateStatus   string     `json:"certificateStatus"`
		Capabilities        []string   `json:"capabilities"`
		BuildVersion        string     `json:"buildVersion,omitempty"`
		ProtocolMin         int        `json:"protocolMin"`
		ProtocolMax         int        `json:"protocolMax"`
		ProtocolCompatible  bool       `json:"protocolCompatible"`
		CompatibilityStatus string     `json:"compatibilityStatus"`
		Platform            string     `json:"platform,omitempty"`
		ResticVersion       string     `json:"resticVersion,omitempty"`
		RsyncVersion        string     `json:"rsyncVersion,omitempty"`
		ServiceURL          string     `json:"serviceUrl,omitempty"`
		EndpointStatus      string     `json:"endpointStatus"`
		RenewalStatus       string     `json:"renewalStatus,omitempty"`
		TaskEligible        bool       `json:"taskEligible"`
		UpgradeAvailable    bool       `json:"upgradeAvailable"`
		TargetVersion       string     `json:"targetVersion,omitempty"`
		Status              string     `json:"status"`
		RuntimeStatus       string     `json:"runtimeStatus"`
		LastHeartbeatAt     *time.Time `json:"lastHeartbeatAt,omitempty"`
		CreatedAt           time.Time  `json:"createdAt"`
		RevokedAt           *time.Time `json:"revokedAt,omitempty"`
		UninstalledAt       *time.Time `json:"uninstalledAt,omitempty"`
		DrainingAt          *time.Time `json:"drainingAt,omitempty"`
	}
	items := make([]agentView, 0, len(agents))
	now := time.Now().UTC()
	currentServiceURL := ""
	if s.agentService != nil {
		currentServiceURL = strings.TrimRight(s.agentService.Status().ServiceURL, "/")
	}
	for _, agent := range agents {
		status := agent.Status
		if status == "online" && (agent.LastHeartbeatAt == nil || agent.LastHeartbeatAt.Before(now.Add(-alerting.AgentHeartbeatTimeout))) {
			status = "offline"
		}
		runtimeStatus := "unknown"
		if agent.StoppedAt != nil {
			runtimeStatus = "stopped"
		} else if status == "online" {
			runtimeStatus = "running"
		}
		protocolCompatible := agent.ProtocolMin >= 1 && agent.ProtocolMin <= agentprotocol.Version && agent.ProtocolMax >= agentprotocol.Version
		certificateStatus := agentCertificateStatus(agent.CertificateNotAfter, now)
		compatibilityStatus := "compatible"
		switch {
		case agent.RevokedAt != nil || agent.UninstalledAt != nil:
			compatibilityStatus = "revoked"
		case agent.DrainingAt != nil:
			compatibilityStatus = "draining"
		case status != "online":
			compatibilityStatus = "offline"
		case !protocolCompatible:
			compatibilityStatus = "incompatible"
		case certificateStatus == "expired":
			compatibilityStatus = "certificate_expired"
		}
		endpointStatus := "unknown"
		if agent.ServiceURL != "" && currentServiceURL != "" {
			endpointStatus = "current"
			if strings.TrimRight(agent.ServiceURL, "/") != currentServiceURL {
				endpointStatus = "migration_required"
			}
		}
		taskEligible := compatibilityStatus == "compatible" && agentHasTaskEngine(agent.Capabilities)
		managedResticRepairAvailable := agent.RemoteHostID != "" && agent.OS == "linux" && agent.RevokedAt == nil && agent.UninstalledAt == nil &&
			!slices.Contains(agent.Capabilities, agentprotocol.ManagedResticInstallCapability)
		upgradeAvailable := agent.BuildVersion != "" && s.applicationVersion != "" &&
			(agent.BuildVersion != s.applicationVersion || managedResticRepairAvailable)
		items = append(items, agentView{
			ID: agent.ID, RemoteHostID: agent.RemoteHostID, CertificateSerial: agent.CertificateSerial,
			CertificateNotAfter: agent.CertificateNotAfter, CertificateStatus: certificateStatus,
			Capabilities: agent.Capabilities, BuildVersion: agent.BuildVersion, ProtocolMin: agent.ProtocolMin, ProtocolMax: agent.ProtocolMax,
			ProtocolCompatible: protocolCompatible, CompatibilityStatus: compatibilityStatus,
			Platform: strings.Trim(strings.Join([]string{agent.OS, agent.Arch}, "/"), "/"), ResticVersion: agent.ResticVersion, RsyncVersion: agent.RsyncVersion,
			ServiceURL: agent.ServiceURL, EndpointStatus: endpointStatus, RenewalStatus: agent.RenewalStatus,
			TaskEligible: taskEligible, UpgradeAvailable: upgradeAvailable, TargetVersion: s.applicationVersion,
			Status: status, RuntimeStatus: runtimeStatus, LastHeartbeatAt: agent.LastHeartbeatAt, CreatedAt: agent.CreatedAt,
			RevokedAt: agent.RevokedAt, UninstalledAt: agent.UninstalledAt, DrainingAt: agent.DrainingAt,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

func agentHasTaskEngine(capabilities []string) bool {
	for _, capability := range capabilities {
		if capability == string(domain.ResticEngine) || capability == string(domain.RsyncEngine) {
			return true
		}
	}
	return false
}

func agentCertificateStatus(notAfter *time.Time, now time.Time) string {
	if notAfter == nil || notAfter.IsZero() {
		return "unknown"
	}
	remaining := notAfter.Sub(now)
	if remaining <= 0 {
		return "expired"
	}
	if remaining <= 7*24*time.Hour {
		return "expiring_7"
	}
	if remaining <= 14*24*time.Hour {
		return "expiring_14"
	}
	if remaining <= 30*24*time.Hour {
		return "expiring_30"
	}
	return "valid"
}

func (s *Server) agentServiceStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.agentService == nil {
		writeJSON(w, http.StatusOK, agentservice.Status{
			Enabled: s.agents != nil, Running: s.agents != nil, Port: agentservice.DefaultPort,
		})
		return
	}
	writeJSON(w, http.StatusOK, s.agentService.Status())
}

func (s *Server) configureAgentService(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentService == nil {
		writeError(w, http.StatusServiceUnavailable, "当前进程不支持页面管理 Agent HTTPS 服务")
		return
	}
	var input struct {
		Enabled        bool   `json:"enabled"`
		Port           int    `json:"port"`
		AdvertisedHost string `json:"advertisedHost"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	settings := agentservice.Settings{
		Enabled: input.Enabled, ListenHost: "0.0.0.0", Port: input.Port,
		AdvertisedHost: strings.TrimSpace(input.AdvertisedHost),
	}
	if err := agentservice.Validate(settings); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Agent 服务配置无效："+err.Error())
		return
	}
	status, err := s.agentService.Configure(r.Context(), settings)
	if err != nil {
		s.log.Error("configure Agent HTTPS service", "error", err)
		writeError(w, http.StatusServiceUnavailable, "无法应用 Agent 服务配置；请确认端口未被占用且当前用户有监听权限")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "agent_service.configure", "application", "agent-service", map[string]any{
		"enabled": status.Enabled, "port": status.Port, "advertisedHost": status.AdvertisedHost,
	})
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) createAgentEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.agentService != nil {
		token, caPEM, err := s.agentService.CreateEnrollmentToken(r.Context(), 15*time.Minute)
		if errors.Is(err, agentservice.ErrDisabled) {
			writeError(w, http.StatusServiceUnavailable, "Agent 服务未启用；请先在页面中启用 Agent HTTPS 服务")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法创建 Agent 注册令牌")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": token, "caPem": caPEM, "expiresAt": time.Now().UTC().Add(15 * time.Minute)})
		return
	}
	if s.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent 服务未启用；请先在 Agent 节点页面启用 Agent HTTPS 服务")
		return
	}
	token, err := s.agents.CreateEnrollmentToken(r.Context(), 15*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建 Agent 注册令牌")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "caPem": s.agents.CAPEM(), "expiresAt": time.Now().UTC().Add(15 * time.Minute)})
}

func (s *Server) deployAgent(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentService == nil && s.agentDeployer == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent 远程部署未启用；请先在页面中启用 Agent HTTPS 服务")
		return
	}
	if s.agentService != nil && !s.agentService.Status().Running {
		writeError(w, http.StatusServiceUnavailable, "Agent 远程部署未启用；请先在页面中启用 Agent HTTPS 服务")
		return
	}
	var input struct {
		HostID     string `json:"hostId"`
		AgentID    string `json:"agentId"`
		ServiceURL string `json:"serviceUrl"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	input.HostID = strings.TrimSpace(input.HostID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.ServiceURL = strings.TrimSpace(input.ServiceURL)
	if input.HostID == "" || input.AgentID == "" || input.ServiceURL == "" {
		writeError(w, http.StatusUnprocessableEntity, "远程主机、Agent ID 和 Service 地址不能为空")
		return
	}
	request := agentdeploy.DeployRequest{HostID: input.HostID, AgentID: input.AgentID, ServiceURL: input.ServiceURL}
	record, _, err := s.operations.StartUnique("agent-deploy:"+input.HostID, operationruntime.StartRequest{
		Kind: "agent_deploy", Actor: username, Target: input.AgentID, Detail: map[string]any{"hostId": input.HostID},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		deploy := s.agentDeployer
		if s.agentService != nil {
			deploy = s.agentService
		}
		_, err := deploy.Deploy(ctx, request, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Agent 部署："+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if err := resources.RevokeAgent(r.Context(), r.PathValue("id"), time.Now().UTC()); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Agent 不存在或已撤销")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "无法撤销 Agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uninstallAgent(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentUninstaller == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent 远程卸载未启用")
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	if agentID == "" {
		writeError(w, http.StatusUnprocessableEntity, "Agent ID 不能为空")
		return
	}
	record, reused, err := s.operations.StartUnique("agent-management:"+agentID, operationruntime.StartRequest{
		Kind: "agent_uninstall", Actor: username, Target: agentID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, err := s.agentUninstaller.Uninstall(ctx, agentID, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Agent 卸载："+err.Error())
		return
	}
	if reused && record.Kind != "agent_uninstall" {
		writeError(w, http.StatusConflict, "Agent 正在执行其他管理操作")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) upgradeAgent(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentUpgrader == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent 托管升级未启用")
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	targetVersion := strings.TrimSpace(s.applicationVersion)
	if agentID == "" || targetVersion == "" {
		writeError(w, http.StatusUnprocessableEntity, "Agent ID 或目标版本无效")
		return
	}
	request := agentdeploy.UpgradeRequest{AgentID: agentID, TargetVersion: targetVersion}
	record, reused, err := s.operations.StartUnique("agent-management:"+agentID, operationruntime.StartRequest{
		Kind: "agent_upgrade", Actor: username, Target: agentID, Detail: map[string]any{"targetVersion": targetVersion},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, err := s.agentUpgrader.Upgrade(ctx, request, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Agent 升级："+err.Error())
		return
	}
	if reused && record.Kind != "agent_upgrade" {
		writeError(w, http.StatusConflict, "Agent 正在执行其他管理操作")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "agent.upgrade.start", "agent", agentID, map[string]any{
		"operationId": record.ID, "targetVersion": targetVersion,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) reprobeAgentTools(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentToolProber == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent 工具重新探测未启用")
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	if agentID == "" {
		writeError(w, http.StatusUnprocessableEntity, "Agent ID 无效")
		return
	}
	record, reused, err := s.operations.StartUnique("agent-management:"+agentID, operationruntime.StartRequest{
		Kind: "agent_tool_probe", Actor: username, Target: agentID,
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, err := s.agentToolProber.ReprobeTools(ctx, agentID, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Agent 工具重新探测："+err.Error())
		return
	}
	if reused && record.Kind != "agent_tool_probe" {
		writeError(w, http.StatusConflict, "Agent 正在执行其他管理操作")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "agent.tools.reprobe.start", "agent", agentID, map[string]any{
		"operationId": record.ID,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) installAgentRestic(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.agentResticInstaller == nil || s.installer == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent Restic 安装器未启用")
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	var input struct {
		Version string `json:"version"`
	}
	if agentID == "" || decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "Agent ID 或 Restic 版本无效")
		return
	}
	versions, err := s.installer.Versions(r.Context())
	if err != nil || len(versions) == 0 {
		writeError(w, http.StatusBadGateway, "无法验证可安装的 Restic 版本")
		return
	}
	input.Version = strings.TrimSpace(input.Version)
	if input.Version == "" {
		input.Version = versions[0]
	}
	allowed := false
	for _, version := range versions {
		allowed = allowed || version == input.Version
	}
	if !allowed {
		writeError(w, http.StatusUnprocessableEntity, "所选 Restic 版本不在当前官方稳定版本列表中")
		return
	}
	version := input.Version
	record, reused, err := s.operations.StartUnique("agent-management:"+agentID, operationruntime.StartRequest{
		Kind: "agent_restic_install", Actor: username, Target: agentID, Detail: map[string]any{"version": version},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, err := s.agentResticInstaller.InstallRestic(ctx, agenttool.InstallRequest{AgentID: agentID, Version: version}, func(stage string) {
			_ = reporter.Stage(stage, nil)
		})
		return err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动 Agent Restic 安装")
		return
	}
	if reused && record.Kind != "agent_restic_install" {
		writeError(w, http.StatusConflict, "Agent 正在执行其他管理操作")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "agent.restic.install.start", "agent", agentID, map[string]any{
		"operationId": record.ID, "version": version,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) localFilesystemSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.localFilesystem == nil {
		writeError(w, http.StatusServiceUnavailable, "本机目录服务尚未配置")
		return
	}
	writeJSON(w, http.StatusOK, s.localFilesystem.Settings())
}

func (s *Server) saveLocalFilesystemSettings(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.localFilesystem == nil {
		writeError(w, http.StatusServiceUnavailable, "本机目录服务尚未配置")
		return
	}
	var input localfilesystem.Settings
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	settings, err := s.localFilesystem.SaveSettings(r.Context(), input.Roots)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "允许根目录无效、不可读或超出数量限制")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "local_filesystem.settings.update", "local_filesystem", "service", map[string]any{"rootCount": len(settings.Roots)})
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) browseLocalFilesystem(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.localFilesystem == nil {
		writeError(w, http.StatusServiceUnavailable, "本机目录服务尚未配置")
		return
	}
	path, ok := decodeFilesystemPath(w, r)
	if !ok {
		return
	}
	result, err := s.localFilesystem.Browse(r.Context(), path)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "目录不可访问或位于允许根目录之外")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createLocalDirectory(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.localFilesystem == nil {
		writeError(w, http.StatusServiceUnavailable, "本机目录服务尚未配置")
		return
	}
	path, ok := decodeFilesystemPath(w, r)
	if !ok {
		return
	}
	if err := s.localFilesystem.CreateDirectory(r.Context(), path); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "无法在允许根目录内创建目录")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "local_filesystem.create_directory", "local_filesystem", "service", map[string]any{"path": path})
	writeJSON(w, http.StatusCreated, map[string]any{"path": path, "created": true})
}

func decodeFilesystemPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Path string `json:"path"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return "", false
	}
	input.Path = strings.TrimSpace(input.Path)
	if input.Path == "" || strings.ContainsAny(input.Path, "\x00\r\n") {
		writeError(w, http.StatusUnprocessableEntity, "目录路径不能为空")
		return "", false
	}
	return input.Path, true
}

func (s *Server) listProtectionTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	items, err := s.protectionSetup.ListTemplates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护模板")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createProtectionTemplate(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	var input protectionsetup.TemplateInput
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	item, err := s.protectionSetup.CreateTemplate(r.Context(), input)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "保护模板名称已存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "保护模板策略无效")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "protection_template.create", "protection_template", item.ID, nil)
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) deleteProtectionTemplate(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}

func (s *Server) listProtectionDrafts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	items, err := s.protectionSetup.ListDrafts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护草稿")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getProtectionDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	item, err := s.protectionSetup.Draft(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "保护草稿不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护草稿")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) createProtectionDraft(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	var input protectionsetup.CreateDraftInput
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	item, err := s.protectionSetup.CreateDraft(r.Context(), input)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "所选保护模板不存在")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "保护映射与现有资源冲突")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "保护草稿配置无效；请检查来源、独立仓库映射、密码与策略")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "protection_draft.create", "protection_draft", item.ID, map[string]any{"itemCount": len(item.Items), "templateId": item.TemplateID})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) applyProtectionDraft(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	draftID := r.PathValue("id")
	draft, err := s.protectionSetup.Draft(r.Context(), draftID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "保护草稿不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护草稿")
		return
	}
	if draft.Status == protectionsetup.DraftCancelled {
		writeError(w, http.StatusConflict, "已清理的保护草稿不能继续执行")
		return
	}
	record, _, err := s.operations.StartUnique("protection-draft:"+draftID, operationruntime.StartRequest{
		Kind: "protection_setup", Actor: username, Target: draftID, Detail: map[string]any{"draftId": draftID, "itemCount": len(draft.Items)},
	}, func(ctx context.Context, reporter operationruntime.Reporter) error {
		_, applyErr := s.protectionSetup.Apply(ctx, draftID, func(stage string, detail map[string]any) {
			_ = reporter.Stage(stage, detail)
		})
		return applyErr
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法启动创建保护操作")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "protection_draft.apply", "protection_draft", draftID, map[string]any{"operationId": record.ID})
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": record.ID, "status": record.Status})
}

func (s *Server) cancelProtectionDraft(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	draft, err := s.protectionSetup.Draft(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "保护草稿不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护草稿")
		return
	}
	if draft.Status == protectionsetup.DraftApplying {
		writeError(w, http.StatusConflict, "请先取消正在运行的持久化操作，再清理保护草稿")
		return
	}
	item, err := s.protectionSetup.Cancel(r.Context(), draft.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "无法清理已完成的保护草稿")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "protection_draft.cancel", "protection_draft", item.ID, map[string]any{"retainedCount": countProtectionItems(item.Items, protectionsetup.ItemRetained)})
	writeJSON(w, http.StatusOK, item)
}

func countProtectionItems(items []protectionsetup.DraftItem, status protectionsetup.ItemStatus) int {
	count := 0
	for _, item := range items {
		if item.Status == status {
			count++
		}
	}
	return count
}

type protectionChecklistItem struct {
	TaskID                     string     `json:"taskId"`
	TaskName                   string     `json:"taskName"`
	ResourceStatus             string     `json:"resourceStatus"`
	ActivationStatus           string     `json:"activationStatus"`
	FirstCompleteSuccessStatus string     `json:"firstCompleteSuccessStatus"`
	FirstCompleteSuccessAt     *time.Time `json:"firstCompleteSuccessAt,omitempty"`
	MaintenanceStatus          string     `json:"maintenanceStatus"`
	RestoreVerificationStatus  string     `json:"restoreVerificationStatus"`
}

type protectionChecklistResponse struct {
	DraftID            string                      `json:"draftId"`
	DraftStatus        protectionsetup.DraftStatus `json:"draftStatus"`
	PlanID             string                      `json:"planId"`
	PlanStatus         string                      `json:"planStatus"`
	NextRun            *time.Time                  `json:"nextRun,omitempty"`
	NotificationStatus string                      `json:"notificationStatus"`
	Items              []protectionChecklistItem   `json:"items"`
	Complete           bool                        `json:"complete"`
}

func (s *Server) protectionChecklist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.protectionSetup == nil {
		writeError(w, http.StatusServiceUnavailable, "创建保护服务尚未配置")
		return
	}
	draft, err := s.protectionSetup.Draft(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "保护草稿不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	tasks, err := resources.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	plans, err := resources.ListPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	maintenance, err := resources.ListMaintenancePolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	verificationPolicies, err := resources.ListRestoreVerificationPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	verificationSuccess, err := resources.LatestSuccessfulRestoreVerifications(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
		return
	}
	result := protectionChecklistResponse{DraftID: draft.ID, DraftStatus: draft.Status, PlanID: draft.PlanID, PlanStatus: "missing", NotificationStatus: "skipped", Complete: draft.Status == protectionsetup.DraftReady}
	plan, hasPlan := planByID(plans, draft.PlanID)
	if hasPlan {
		result.PlanStatus = "disabled"
		if plan.Enabled {
			result.PlanStatus = "scheduled"
			if next, nextErr := schedule.NextAnchored(plan.Schedule, plan.Timezone, plan.ScheduleAnchorAt, time.Now().UTC()); nextErr == nil {
				result.NextRun = &next
			}
		}
	}
	if draft.NotificationMode == protectionsetup.NotificationConfigured {
		result.NotificationStatus = "not_configured"
		if hasReadyNotificationChannel(r.Context(), resources) {
			result.NotificationStatus = "ready"
		}
	}
	taskByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	maintenanceByRepository := make(map[string]domain.MaintenancePolicy, len(maintenance))
	for _, policy := range maintenance {
		maintenanceByRepository[policy.RepositoryID] = policy
	}
	verificationPolicyByTask := make(map[string]domain.RestoreVerificationPolicy, len(verificationPolicies))
	for _, policy := range verificationPolicies {
		verificationPolicyByTask[policy.TaskID] = policy
	}
	for _, draftItem := range draft.Items {
		item := protectionChecklistItem{TaskID: draftItem.TaskID, TaskName: draftItem.TaskName, ResourceStatus: string(draftItem.Status), ActivationStatus: "missing", FirstCompleteSuccessStatus: "pending", MaintenanceStatus: "missing", RestoreVerificationStatus: "not_configured"}
		task, exists := taskByID[draftItem.TaskID]
		if exists {
			item.ActivationStatus = "disabled"
			if task.Enabled {
				item.ActivationStatus = "ready"
			}
			if run, runErr := resources.LatestSuccessfulRun(r.Context(), task.ID); runErr == nil {
				item.FirstCompleteSuccessStatus = "complete"
				item.FirstCompleteSuccessAt = run.FinishedAt
			} else if !errors.Is(runErr, sql.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "无法读取保护检查表")
				return
			}
			if policy, ok := maintenanceByRepository[task.RepositoryID]; ok {
				item.MaintenanceStatus = "pending_review"
				if policy.Enabled {
					item.MaintenanceStatus = "ready"
				}
			}
			if task.Kind == domain.DatabaseTask {
				item.RestoreVerificationStatus = "not_supported"
			} else if _, ok := verificationSuccess[task.ID]; ok {
				item.RestoreVerificationStatus = "verified"
			} else if _, ok := verificationPolicyByTask[task.ID]; ok {
				item.RestoreVerificationStatus = "pending"
			}
		}
		result.Items = append(result.Items, item)
		result.Complete = result.Complete && item.ResourceStatus == string(protectionsetup.ItemReady) && item.ActivationStatus == "ready" && item.FirstCompleteSuccessStatus == "complete" && item.MaintenanceStatus == "ready" && item.RestoreVerificationStatus == "verified"
	}
	result.Complete = result.Complete && result.PlanStatus == "scheduled" && (result.NotificationStatus == "ready" || result.NotificationStatus == "skipped")
	writeJSON(w, http.StatusOK, result)
}

func planByID(items []domain.Plan, id string) (domain.Plan, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return domain.Plan{}, false
}

func (s *Server) browseAgentFilesystem(w http.ResponseWriter, r *http.Request) {
	s.agentFilesystemOperation(w, r, agentfilesystem.Browse)
}

func (s *Server) createAgentDirectory(w http.ResponseWriter, r *http.Request) {
	s.agentFilesystemOperation(w, r, agentfilesystem.CreateDirectory)
}

func (s *Server) agentFilesystemOperation(w http.ResponseWriter, r *http.Request, operation agentfilesystem.Operation) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input struct {
		Path string `json:"path"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	input.Path = strings.TrimSpace(input.Path)
	if input.Path == "" || strings.ContainsAny(input.Path, "\x00\r\n") {
		writeError(w, http.StatusUnprocessableEntity, "目录路径不能为空")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	agentID := r.PathValue("id")
	agents, err := resources.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取 Agent")
		return
	}
	required := "filesystem-browse"
	if operation == agentfilesystem.CreateDirectory {
		required = "filesystem-create-directory"
	}
	available := false
	for _, agent := range agents {
		if agent.ID != agentID || agent.RevokedAt != nil || agent.Status != "online" || agent.LastHeartbeatAt == nil || time.Since(*agent.LastHeartbeatAt) > time.Minute {
			continue
		}
		for _, capability := range agent.Capabilities {
			available = available || capability == required
		}
	}
	if !available {
		writeError(w, http.StatusConflict, "Agent 离线或不支持该目录操作")
		return
	}
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: operation, Path: input.Path})
	now := time.Now().UTC()
	request := store.AgentFilesystemRequest{ID: newID("filesystem"), AgentID: agentID, Definition: definition, ExpiresAt: now.Add(15 * time.Second), CreatedAt: now}
	if err := resources.CreateAgentFilesystemRequest(r.Context(), request); err != nil {
		writeError(w, http.StatusConflict, "无法提交目录操作")
		return
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		current, statusErr := resources.AgentFilesystemRequestStatus(r.Context(), request.ID)
		if statusErr != nil {
			writeError(w, http.StatusInternalServerError, "无法读取目录操作结果")
			return
		}
		if current.Status == "succeeded" || current.Status == "failed" {
			var result agentprotocol.Result
			if json.Unmarshal(current.Result, &result) != nil {
				writeError(w, http.StatusBadGateway, "Agent 返回了无效结果")
				return
			}
			if current.Status == "failed" {
				writeError(w, http.StatusUnprocessableEntity, "Agent 目录操作失败："+result.Error)
				return
			}
			if operation == agentfilesystem.CreateDirectory {
				s.appendSemanticAudit(r.Context(), username, "agent.filesystem.create-directory", "agent", agentID, map[string]any{"path": input.Path})
				writeJSON(w, http.StatusCreated, result.Summary)
			} else {
				writeJSON(w, http.StatusOK, result.Summary)
			}
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timeout.C:
			writeError(w, http.StatusGatewayTimeout, "等待 Agent 目录操作超时")
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	var input planRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	now := time.Now().UTC()
	plan := input.plan(defaultCatchUpWindowMinutes)
	plan.ID = newID("plan")
	plan.ScheduleAnchorAt, plan.CreatedAt, plan.UpdatedAt = now, now, now
	if err := plan.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	if err := resources.CreatePlan(r.Context(), plan); err != nil {
		writeResourceError(w, err, "无法创建计划")
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	items, err := resources.ListPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取计划")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func writeFound[T any](w http.ResponseWriter, id string, items []T, identity func(T) string) {
	for _, item := range items {
		if identity(item) == id {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	writeError(w, http.StatusNotFound, "资源不存在")
}
func (s *Server) getRemoteHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.resourceStore(w).ListRemoteHosts(r.Context())
	if err != nil {
		writeError(w, 500, "读取失败")
		return
	}
	writeFound(w, r.PathValue("id"), items, func(v domain.RemoteHost) string { return v.ID })
}
func (s *Server) updateRemoteHost(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input createRemoteHostRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	now := time.Now().UTC()
	item := domain.RemoteHost{ID: r.PathValue("id"), Name: input.Name, Host: input.Host, Port: input.Port, Username: input.Username, HostFingerprint: input.HostFingerprint, UpdatedAt: now}
	var previous domain.RemoteHost
	if items, err := s.resourceStore(w).ListRemoteHosts(r.Context()); err == nil {
		for _, candidate := range items {
			if candidate.ID == item.ID {
				previous = candidate
				break
			}
		}
	}
	if item.Validate() != nil {
		writeError(w, 422, "远程主机配置无效")
		return
	}
	newSecret := ""
	var err error
	if input.PrivateKey != "" {
		newSecret, err = s.secrets.Put(r.Context(), "ssh-private-key", []byte(input.PrivateKey))
		if err != nil {
			writeError(w, 500, "无法保存 SSH 私钥")
			return
		}
	}
	old, err := s.resourceStore(w).UpdateRemoteHost(r.Context(), item, newSecret)
	if err != nil {
		if newSecret != "" {
			_ = s.secrets.Delete(r.Context(), newSecret)
		}
		writeCRUDOperationError(w, err)
		return
	}
	if newSecret != "" {
		_ = s.secrets.Delete(r.Context(), old)
	}
	if previous.ID != "" && previous.HostFingerprint != item.HostFingerprint {
		s.appendSemanticAudit(r.Context(), username, "remote_host.fingerprint.change", "remote_host", item.ID, map[string]any{"host": item.Host, "port": item.Port, "previousFingerprint": previous.HostFingerprint, "newFingerprint": item.HostFingerprint})
	}
	writeJSON(w, 200, item)
}
func (s *Server) deleteRemoteHost(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}

func (s *Server) getRepository(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.resourceStore(w).ListRepositories(r.Context())
	if err != nil {
		writeError(w, 500, "读取失败")
		return
	}
	writeFound(w, r.PathValue("id"), items, func(v domain.Repository) string { return v.ID })
}
func (s *Server) updateRepository(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	var input createRepositoryRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	if input.Password != "" {
		writeError(w, http.StatusUnprocessableEntity, "仓库密码只能通过两阶段密码轮换修改")
		return
	}
	items, _ := s.resourceStore(w).ListRepositories(r.Context())
	status := "uninitialized"
	created := time.Time{}
	var previous domain.Repository
	for _, v := range items {
		if v.ID == r.PathValue("id") {
			status = v.Status
			created = v.CreatedAt
			previous = v
		}
	}
	item := domain.Repository{ID: r.PathValue("id"), Name: input.Name, Engine: input.Engine, Kind: input.Kind, RemoteHostID: input.RemoteHostID, Path: input.Path, Status: status, CreatedAt: created, UpdatedAt: time.Now().UTC()}
	if item.Kind == "" {
		item.Kind = domain.SFTPRepository
	}
	if item.Engine == "" && previous.ID != "" {
		item.Engine = previous.EffectiveEngine()
	}
	applyS3RepositoryInput(&item, input)
	credentials, credentialsErr := s3Credentials(input, false)
	if item.EffectiveKind() == domain.S3Repository && previous.EffectiveKind() == domain.S3Repository {
		item.BackendSecretID = previous.BackendSecretID
	}
	if previous.ID != "" && repositoryLocationChanged(previous, item) {
		item.Status = "uninitialized"
	}
	if item.EffectiveEngine() == domain.RsyncEngine {
		item.Status = "ready"
	}
	if item.Validate() != nil || credentialsErr != nil || item.EffectiveKind() == domain.S3Repository && item.BackendSecretID == "" && credentials == nil {
		writeError(w, 422, "仓库配置无效")
		return
	}
	newBackendSecret := ""
	var err error
	if credentials != nil {
		encoded, _ := s3backend.EncodeCredentials(*credentials)
		newBackendSecret, err = s.secrets.Put(r.Context(), s3backend.CredentialPurpose, encoded)
		clear(encoded)
		if err != nil {
			writeError(w, 500, "无法保存 S3 凭据")
			return
		}
		item.BackendSecretID = newBackendSecret
	}
	oldSecrets, err := s.resourceStore(w).UpdateRepository(r.Context(), item, "")
	if err != nil {
		if newBackendSecret != "" {
			_ = s.secrets.Delete(context.WithoutCancel(r.Context()), newBackendSecret)
		}
		writeCRUDOperationError(w, err)
		return
	}
	for _, old := range oldSecrets {
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), old)
	}
	writeJSON(w, 200, item)
}

func repositoryLocationChanged(previous, next domain.Repository) bool {
	if previous.EffectiveEngine() != next.EffectiveEngine() || previous.EffectiveKind() != next.EffectiveKind() || previous.RemoteHostID != next.RemoteHostID || previous.Path != next.Path {
		return true
	}
	if previous.S3 == nil || next.S3 == nil {
		return previous.S3 != nil || next.S3 != nil
	}
	return previous.S3.Endpoint != next.S3.Endpoint || previous.S3.Bucket != next.S3.Bucket || previous.S3.Region != next.S3.Region || previous.S3.PathStyle != next.S3.PathStyle
}
func (s *Server) deleteRepository(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}

func (s *Server) getDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.resourceStore(w).ListDatabaseConnections(r.Context())
	if err != nil {
		writeError(w, 500, "读取失败")
		return
	}
	writeFound(w, r.PathValue("id"), items, func(v domain.DatabaseConnection) string { return v.ID })
}

func (s *Server) listLogicalDatabases(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil || s.databaseEnumerator == nil {
		writeError(w, http.StatusServiceUnavailable, "数据库枚举服务尚未配置")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	execution, err := resources.LoadDatabaseConnectionExecution(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "数据库连接不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取数据库连接")
		return
	}
	if execution.Connection.Purpose != domain.BackupConnection {
		writeError(w, http.StatusUnprocessableEntity, "只能枚举已验证的备份连接")
		return
	}
	password, err := s.secrets.Get(r.Context(), execution.PasswordSecretID, "database-backup-password")
	if err != nil {
		writeError(w, http.StatusLocked, "无法读取数据库凭据，请解锁秘密库")
		return
	}
	defer clear(password)
	items, err := s.databaseEnumerator.List(r.Context(), execution.Connection, string(password))
	if err != nil {
		s.appendSemanticAudit(r.Context(), username, "database_connection.enumerate", "database_connection", execution.Connection.ID, map[string]any{"status": "failed"})
		writeError(w, http.StatusUnprocessableEntity, "无法列出逻辑库；请重新预检并检查连接权限")
		return
	}
	s.appendSemanticAudit(r.Context(), username, "database_connection.enumerate", "database_connection", execution.Connection.ID, map[string]any{"status": "success", "count": len(items)})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input createDatabaseConnectionRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	item := domain.DatabaseConnection{ID: r.PathValue("id"), Name: input.Name, Engine: input.Engine, Purpose: input.Purpose, Network: input.Network, Host: input.Host, Port: input.Port, SocketPath: input.SocketPath, Username: input.Username, TLS: input.TLS, ToolPaths: input.ToolPaths, UpdatedAt: time.Now().UTC()}
	existing, _ := s.resourceStore(w).ListDatabaseConnections(r.Context())
	found := false
	var previous domain.DatabaseConnection
	for _, candidate := range existing {
		if candidate.ID == item.ID {
			found = true
			previous = candidate
			item.CreatedAt = candidate.CreatedAt
			if candidate.Purpose != item.Purpose && input.Password == "" {
				writeError(w, 422, "更改连接用途时必须重新输入密码")
				return
			}
			break
		}
	}
	if !found {
		writeError(w, 404, "资源不存在")
		return
	}
	if item.Validate() != nil {
		writeError(w, 422, "数据库连接配置无效")
		return
	}
	verificationPassword := input.Password
	if verificationPassword == "" {
		execution, loadErr := s.resourceStore(w).LoadDatabaseConnectionExecution(r.Context(), item.ID)
		if loadErr != nil {
			writeCRUDOperationError(w, loadErr)
			return
		}
		value, secretErr := s.secrets.Get(r.Context(), execution.PasswordSecretID, "database-"+string(previous.Purpose)+"-password")
		if secretErr != nil {
			writeError(w, http.StatusLocked, "无法读取数据库凭据，请解锁秘密库")
			return
		}
		verificationPassword = string(value)
		clear(value)
	}
	verification := s.databaseVerifier.Verify(r.Context(), item, verificationPassword)
	item.Status = "ready"
	if verification.Error != "" {
		item.Status = "draft"
	}
	item.Preflight = domain.DatabasePreflight{CheckedAt: verification.CheckedAt, ClientVersion: verification.ClientVersion, ServerVersion: verification.ServerVersion, Error: verification.Error}
	newSecret := ""
	var err error
	if input.Password != "" {
		newSecret, err = s.secrets.Put(r.Context(), "database-"+string(item.Purpose)+"-password", []byte(input.Password))
		if err != nil {
			writeError(w, 500, "无法保存密码")
			return
		}
	}
	old, err := s.resourceStore(w).UpdateDatabaseConnection(r.Context(), item, newSecret)
	if err != nil {
		if newSecret != "" {
			_ = s.secrets.Delete(r.Context(), newSecret)
		}
		writeCRUDOperationError(w, err)
		return
	}
	if newSecret != "" {
		_ = s.secrets.Delete(r.Context(), old)
	}
	s.appendSemanticAudit(r.Context(), username, "database_connection.preflight", "database_connection", item.ID, map[string]any{"status": item.Status, "clientVersion": item.Preflight.ClientVersion, "serverVersion": item.Preflight.ServerVersion, "error": item.Preflight.Error})
	writeJSON(w, 200, item)
}
func (s *Server) deleteDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.resourceStore(w).ListTasks(r.Context())
	if err != nil {
		writeError(w, 500, "读取失败")
		return
	}
	writeFound(w, r.PathValue("id"), items, func(v domain.Task) string { return v.ID })
}
func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	var input taskMutationInput
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	item := input.Task
	item.ScopeConfirmation = domain.TaskScopeConfirmation{}
	item.ID = r.PathValue("id")
	item.UpdatedAt = time.Now().UTC()
	if err := item.Validate(); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	existing, err := loadTask(r.Context(), resources, item.ID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务")
		return
	}
	if err := validateTaskActivation(r.Context(), resources, item); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if taskScopeRequiresPreview(item) {
		fingerprint, err := taskpreview.Fingerprint(item)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "无法计算任务范围")
			return
		}
		confirmation := existing.ScopeConfirmation
		confirmedDelete := item.Rsync == nil || !item.Rsync.Delete || confirmation.DeleteConfirmed
		if confirmation.Present() && confirmation.Fingerprint == fingerprint && confirmedDelete {
			item.ScopeConfirmation = confirmation
		} else if item.Enabled {
			if input.PreviewID == "" {
				writeError(w, http.StatusConflict, "启用任务前必须完成与当前配置一致的范围预览")
				return
			}
			confirmation, err = resources.ConsumeTaskScopePreview(r.Context(), input.PreviewID, item.ID, fingerprint, username, input.RsyncDeleteConfirmed, time.Now().UTC())
			if err != nil {
				writeError(w, http.StatusConflict, "范围预览已失效、配置已变化或缺少 rsync 删除确认")
				return
			}
			item.ScopeConfirmation = confirmation
		}
	}
	if err := resources.UpdateTask(r.Context(), item); err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func taskScopeRequiresPreview(task domain.Task) bool {
	return task.EffectiveEngine() == domain.RsyncEngine && task.Rsync != nil || task.EffectiveEngine() == domain.ResticEngine && task.Kind == domain.DirectoryTask && task.Directory != nil
}

func loadTask(ctx context.Context, resources *store.Store, id string) (domain.Task, error) {
	tasks, err := resources.ListTasks(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return domain.Task{}, sql.ErrNoRows
}

func validateTaskActivation(ctx context.Context, resources *store.Store, task domain.Task) error {
	if !task.Enabled {
		return nil
	}
	if task.EffectiveExecutionTarget().Kind == execution.Agent {
		if task.EffectiveEngine() == domain.ResticEngine && task.Kind != domain.DirectoryTask {
			return errors.New("Agent Restic 当前仅支持目录备份")
		}
		if task.EffectiveEngine() == domain.ResticEngine {
			repositories, err := resources.ListRepositories(ctx)
			if err != nil {
				return errors.New("无法验证 Agent Restic 目标仓库")
			}
			remoteRepository := false
			for _, repository := range repositories {
				remoteRepository = remoteRepository || (repository.ID == task.RepositoryID && repository.EffectiveKind() == domain.SFTPRepository)
			}
			if !remoteRepository {
				return errors.New("Agent Restic 远程执行必须使用 SFTP 远程仓库")
			}
		}
		agents, err := resources.ListAgents(ctx)
		if err != nil {
			return errors.New("无法验证目标 Agent")
		}
		for _, agent := range agents {
			if agent.ID == task.EffectiveExecutionTarget().AgentID {
				return validateAgentTaskEligibility(agent, string(task.EffectiveEngine()), time.Now().UTC())
			}
		}
		return errors.New("目标 Agent 不存在")
	}
	if task.EffectiveEngine() == domain.RsyncEngine {
		if task.Rsync == nil {
			return errors.New("rsync 同步配置不存在")
		}
		info, err := os.Stat(task.Rsync.Path)
		if err != nil || !info.IsDir() {
			return errors.New("rsync 源目录不可用；请先保存为草稿或修复目录")
		}
		destinationHostID := task.Rsync.DestinationHostID
		if task.RepositoryID != "" {
			repositories, err := resources.ListRepositories(ctx)
			if err != nil {
				return errors.New("无法验证 rsync 同步仓库")
			}
			found := false
			for _, repository := range repositories {
				if repository.ID != task.RepositoryID {
					continue
				}
				found = true
				if repository.EffectiveEngine() != domain.RsyncEngine || repository.Status != "ready" {
					return errors.New("rsync 同步仓库不可用")
				}
				if repository.EffectiveKind() != domain.SFTPRepository {
					return errors.New("Service 本机 rsync 必须使用 SSH 远程同步仓库")
				}
				destinationHostID = repository.RemoteHostID
				break
			}
			if !found {
				return errors.New("rsync 同步仓库不存在")
			}
		}
		hosts, err := resources.ListRemoteHosts(ctx)
		if err != nil {
			return errors.New("无法验证 rsync 目标主机")
		}
		for _, host := range hosts {
			if host.ID == destinationHostID && strings.TrimSpace(host.HostFingerprint) != "" {
				return nil
			}
		}
		return errors.New("rsync 目标主机不存在或未固定 SSH 主机密钥")
	}
	if task.Kind == domain.DirectoryTask {
		info, err := os.Stat(task.Directory.Path)
		if err != nil || !info.IsDir() {
			return errors.New("源目录不可用；请先保存为草稿或修复目录")
		}
		file, err := os.Open(task.Directory.Path)
		if err != nil {
			return errors.New("源目录不可读；请先保存为草稿或修复权限")
		}
		_ = file.Close()
		return nil
	}
	connections, err := resources.ListDatabaseConnections(ctx)
	if err != nil {
		return errors.New("无法验证数据库工具链")
	}
	for _, connection := range connections {
		if connection.ID != task.Database.ConnectionID {
			continue
		}
		if connection.Status != "ready" || connection.Preflight.CheckedAt.IsZero() || time.Since(connection.Preflight.CheckedAt) > 24*time.Hour {
			return errors.New("数据库连接尚未通过有效预检；请先重新验证连接")
		}
		program := connection.ToolPaths["dump"]
		info, statErr := os.Stat(program)
		admin := connection.ToolPaths["admin"]
		adminInfo, adminErr := os.Stat(admin)
		if connection.Purpose != domain.BackupConnection || !filepath.IsAbs(program) || statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 || !filepath.IsAbs(admin) || adminErr != nil || adminInfo.IsDir() || adminInfo.Mode().Perm()&0o111 == 0 {
			return errors.New("数据库导出工具不可执行；请先保存为草稿或修复工具路径")
		}
		return nil
	}
	return errors.New("数据库备份连接不存在；请先保存为草稿")
}

func validateAgentTaskEligibility(agent store.AgentRecord, requiredCapability string, now time.Time) error {
	if agent.RevokedAt != nil || agent.UninstalledAt != nil {
		return errors.New("目标 Agent 已撤销")
	}
	if agent.Status != "online" || agent.LastHeartbeatAt == nil || agent.LastHeartbeatAt.Before(now.Add(-alerting.AgentHeartbeatTimeout)) {
		return errors.New("目标 Agent 离线；可以先保存为停用草稿")
	}
	if agent.ProtocolMin < 1 || agent.ProtocolMin > agentprotocol.Version || agent.ProtocolMax < agentprotocol.Version {
		return fmt.Errorf("目标 Agent 协议范围 %d-%d 与控制服务协议 %d 不兼容；请先升级 Agent 或保存为停用草稿", agent.ProtocolMin, agent.ProtocolMax, agentprotocol.Version)
	}
	if agent.CertificateNotAfter != nil && !agent.CertificateNotAfter.After(now) {
		return errors.New("目标 Agent 证书已过期；请先恢复证书身份或保存为停用草稿")
	}
	for _, capability := range agent.Capabilities {
		if capability == requiredCapability {
			return nil
		}
	}
	return fmt.Errorf("目标 Agent 未提供所需引擎 %s；请先安装兼容工具或保存为停用草稿", requiredCapability)
}
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}
func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.resourceStore(w).ListPlans(r.Context())
	if err != nil {
		writeError(w, 500, "读取失败")
		return
	}
	writeFound(w, r.PathValue("id"), items, func(v domain.Plan) string { return v.ID })
}
func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	var input planRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	existing, err := findPlan(r.Context(), resources, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取计划失败")
		return
	}
	item := input.plan(existing.CatchUpWindowMinutes)
	item.ID = existing.ID
	item.CreatedAt = existing.CreatedAt
	item.ScheduleAnchorAt = existing.ScheduleAnchorAt
	item.UpdatedAt = time.Now().UTC()
	if item.Validate() != nil {
		writeError(w, 422, "计划配置无效")
		return
	}
	if err := resources.UpdatePlan(r.Context(), item); err != nil {
		writeCRUDOperationError(w, err)
		return
	}
	item, err = findPlan(r.Context(), resources, item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取已更新计划失败")
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	writeError(w, http.StatusPreconditionRequired, "删除操作必须先获取依赖与版本预览")
}

const defaultCatchUpWindowMinutes = 60

type planRequest struct {
	Name                 string          `json:"name"`
	Schedule             domain.Schedule `json:"schedule"`
	Timezone             string          `json:"timezone"`
	MaxParallel          int             `json:"maxParallel"`
	TaskIDs              []string        `json:"taskIds"`
	Enabled              bool            `json:"enabled"`
	CatchUpWindowMinutes *int            `json:"catchUpWindowMinutes"`
}

func (p planRequest) plan(defaultCatchUp int) domain.Plan {
	catchUp := defaultCatchUp
	if p.CatchUpWindowMinutes != nil {
		catchUp = *p.CatchUpWindowMinutes
	}
	return domain.Plan{Name: p.Name, Schedule: p.Schedule, Timezone: p.Timezone, MaxParallel: p.MaxParallel, TaskIDs: p.TaskIDs, Enabled: p.Enabled, CatchUpWindowMinutes: catchUp}
}

func findPlan(ctx context.Context, resources *store.Store, id string) (domain.Plan, error) {
	plans, err := resources.ListPlans(ctx)
	if err != nil {
		return domain.Plan{}, err
	}
	for _, plan := range plans {
		if plan.ID == id {
			return plan, nil
		}
	}
	return domain.Plan{}, sql.ErrNoRows
}
func writeCRUDOperationError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "资源不存在")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "资源正在被引用或配置冲突")
		return
	}
	writeError(w, 500, "资源操作失败")
}

func (s *Server) resourceStore(w http.ResponseWriter) *store.Store {
	resources, ok := s.store.(*store.Store)
	if !ok {
		writeError(w, http.StatusInternalServerError, "资源存储不可用")
		return nil
	}
	return resources
}

func writeResourceError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "资源名称、路径或仓库绑定发生冲突")
		return
	}
	writeError(w, http.StatusInternalServerError, fallback)
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.store.IsInitialized(r.Context())
	if err != nil {
		s.log.Error("read setup status", "error", err)
		writeError(w, http.StatusInternalServerError, "无法读取初始化状态")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": initialized})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
