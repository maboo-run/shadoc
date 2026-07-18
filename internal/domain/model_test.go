package domain

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/execution"
)

func TestRepositoryCapacityPolicyDefaultsAndValidation(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	policy := DefaultRepositoryCapacityPolicy("repo", now)
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy: %v", err)
	}
	if !policy.Enabled || policy.ProbeIntervalMinutes != 24*60 || policy.MinimumAvailableBytes != 0 || policy.MinimumAvailablePercent != 0 || policy.ExhaustionWarningDays != 0 || policy.NextProbeAt == nil || !policy.NextProbeAt.Equal(now) {
		t.Fatalf("default policy=%+v", policy)
	}

	invalid := []RepositoryCapacityPolicy{
		{RepositoryID: "", Enabled: true, ProbeIntervalMinutes: 360},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 14},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 10081},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, MinimumAvailableBytes: math.MaxInt64 + 1},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, MinimumAvailablePercent: -1},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, MinimumAvailablePercent: 101},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, MinimumAvailablePercent: math.NaN()},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, ExhaustionWarningDays: -1},
		{RepositoryID: "repo", Enabled: true, ProbeIntervalMinutes: 360, ExhaustionWarningDays: 3651},
	}
	for _, candidate := range invalid {
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid capacity policy accepted: %+v", candidate)
		}
	}
}

func TestRetentionPolicyFingerprintCoversEveryFieldAndIsStable(t *testing.T) {
	policy := RetentionPolicy{
		KeepWithinDays: 30,
		KeepLast:       3,
		KeepHourly:     24,
		KeepDaily:      7,
		KeepWeekly:     5,
		KeepMonthly:    12,
		KeepYearly:     3,
	}
	const want = "sha256:69afd368bc8dd0537bb1b09993251bcc9a5f8835dea180935bdcdaee6c088683"
	if got := policy.Fingerprint(); got != want {
		t.Fatalf("fingerprint=%q want=%q", got, want)
	}

	changed := policy
	changed.KeepHourly++
	if changed.Fingerprint() == policy.Fingerprint() {
		t.Fatal("hourly retention was omitted from the policy fingerprint")
	}
	if err := (RetentionPolicy{KeepHourly: -1}).Validate(); err == nil {
		t.Fatal("negative hourly retention was accepted")
	}
}

func TestTaskScopeConfirmationRoundTripsAsServerEvidence(t *testing.T) {
	confirmedAt := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	task := Task{
		Name: "photos", Kind: DirectoryTask, RepositoryID: "repo", Directory: &DirectorySource{Path: "/source"},
		ScopeConfirmation: TaskScopeConfirmation{
			PreviewID: "preview-1", Fingerprint: "abc", ConfirmedBy: "admin", ConfirmedAt: confirmedAt,
			Summary: map[string]any{"includedFiles": float64(12)},
		},
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Task
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ScopeConfirmation.PreviewID != "preview-1" || decoded.ScopeConfirmation.ConfirmedBy != "admin" || !decoded.ScopeConfirmation.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("confirmation=%+v", decoded.ScopeConfirmation)
	}
}

func TestTaskHealthPolicyValidatesMaximumSuccessAge(t *testing.T) {
	for _, test := range []struct {
		hours   int
		wantErr bool
	}{
		{hours: 0},
		{hours: 1},
		{hours: 48},
		{hours: 8760},
		{hours: -1, wantErr: true},
		{hours: 8761, wantErr: true},
	} {
		task := Task{Name: "photos", Kind: DirectoryTask, RepositoryID: "repo", Directory: &DirectorySource{Path: "/source"}, Health: TaskHealthPolicy{MaxSuccessAgeHours: test.hours}}
		if err := task.Validate(); (err != nil) != test.wantErr {
			t.Fatalf("hours=%d err=%v wantErr=%v", test.hours, err, test.wantErr)
		}
	}
}

func TestTaskValidationEnforcesOneSourceAndRepository(t *testing.T) {
	tests := []struct {
		name    string
		task    Task
		wantErr bool
	}{
		{
			name: "valid directory task",
			task: Task{Name: "photos", Kind: DirectoryTask, RepositoryID: "repo-a", Directory: &DirectorySource{Path: "/srv/photos"}},
		},
		{
			name:    "directory must be absolute",
			task:    Task{Name: "photos", Kind: DirectoryTask, RepositoryID: "repo-a", Directory: &DirectorySource{Path: "photos"}},
			wantErr: true,
		},
		{
			name: "valid database task",
			task: Task{Name: "gitea db", Kind: DatabaseTask, RepositoryID: "repo-db", Database: &DatabaseSource{ConnectionID: "conn-a", Database: "gitea"}},
		},
		{
			name:    "database task cannot include directory source",
			task:    Task{Name: "gitea db", Kind: DatabaseTask, RepositoryID: "repo-db", Directory: &DirectorySource{Path: "/srv"}, Database: &DatabaseSource{ConnectionID: "conn-a", Database: "gitea"}},
			wantErr: true,
		},
		{
			name:    "repository is required",
			task:    Task{Name: "photos", Kind: DirectoryTask, Directory: &DirectorySource{Path: "/srv/photos"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.task.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLegacyDirectoryTaskDefaultsToLocalRestic(t *testing.T) {
	task := Task{Name: "photos", Kind: DirectoryTask, RepositoryID: "repo", Directory: &DirectorySource{Path: "/source"}}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	if task.EffectiveEngine() != ResticEngine {
		t.Fatalf("engine=%q", task.EffectiveEngine())
	}
	if task.EffectiveExecutionTarget().Kind != execution.Local {
		t.Fatalf("target=%+v", task.EffectiveExecutionTarget())
	}
}

func TestAgentDirectoryTaskAcceptsPortableAbsolutePaths(t *testing.T) {
	base := Task{
		Name: "remote photos", Kind: DirectoryTask, RepositoryID: "repo",
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
	}
	for _, source := range []string{"/srv/photos", `D:\Backup\Photos`} {
		task := base
		task.Directory = &DirectorySource{Path: source}
		if err := task.Validate(); err != nil {
			t.Fatalf("agent source %q rejected: %v", source, err)
		}
	}
	for _, source := range []string{"relative/photos", `D:relative`, `D:\Backup\..\Secrets`} {
		task := base
		task.Directory = &DirectorySource{Path: source}
		if err := task.Validate(); err == nil {
			t.Fatalf("unsafe agent source %q accepted", source)
		}
	}
}

func TestS3RepositoryRequiresStructuredSecureBackend(t *testing.T) {
	valid := Repository{
		Name: "object archive", Kind: S3Repository, Path: "photos/primary",
		S3: &S3RepositoryConfig{Endpoint: "https://objects.example.com:9443", Bucket: "backup-prod", Region: "eu-west-1", PathStyle: true},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid S3 repository rejected: %v", err)
	}
	loopback := valid
	loopback.S3 = &S3RepositoryConfig{Endpoint: "http://127.0.0.1:9000", Bucket: "backup-dev", Region: "us-east-1", PathStyle: true}
	if err := loopback.Validate(); err != nil {
		t.Fatalf("loopback development endpoint rejected: %v", err)
	}
	invalid := []Repository{
		{Name: "missing", Kind: S3Repository, S3: nil},
		{Name: "plaintext", Kind: S3Repository, S3: &S3RepositoryConfig{Endpoint: "http://objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}},
		{Name: "userinfo", Kind: S3Repository, S3: &S3RepositoryConfig{Endpoint: "https://key:secret@objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}},
		{Name: "query", Kind: S3Repository, S3: &S3RepositoryConfig{Endpoint: "https://objects.example.com?unsafe=1", Bucket: "backup-prod", Region: "us-east-1"}},
		{Name: "bucket", Kind: S3Repository, S3: &S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "../backup", Region: "us-east-1"}},
		{Name: "prefix", Kind: S3Repository, Path: "photos/../private", S3: &S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}},
		{Name: "rsync", Engine: RsyncEngine, Kind: S3Repository, S3: &S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}},
	}
	for _, repository := range invalid {
		if err := repository.Validate(); err == nil {
			t.Fatalf("unsafe S3 repository accepted: %+v", repository)
		}
	}
}

func TestRsyncTaskCannotReferenceRepository(t *testing.T) {
	task := Task{
		Name:         "copy photos",
		Engine:       RsyncEngine,
		Kind:         RsyncTask,
		RepositoryID: "repo",
		Rsync:        &RsyncSource{Path: "/source", DestinationHostID: "host", DestinationPath: "/target"},
	}
	if err := task.Validate(); err == nil {
		t.Fatal("repository accepted for rsync")
	}
}

func TestRsyncLocalDestinationRequiresAgentAndDisjointPaths(t *testing.T) {
	valid := Task{
		Name: "copy between disks", Engine: RsyncEngine, Kind: RsyncTask,
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
		Rsync:           &RsyncSource{Path: "/mnt/disk-a/photos", DestinationKind: RsyncDestinationLocal, DestinationPath: "/mnt/disk-b/photos"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid local rsync task: %v", err)
	}

	serviceTask := valid
	serviceTask.ExecutionTarget = execution.Target{Kind: execution.Local}
	if err := serviceTask.Validate(); err == nil {
		t.Fatal("service-local execution accepted for a local rsync destination")
	}

	for _, destination := range []string{"/mnt/disk-a/photos", "/mnt/disk-a/photos/archive", "/mnt/disk-a"} {
		overlapping := valid
		copySource := *valid.Rsync
		copySource.DestinationPath = destination
		overlapping.Rsync = &copySource
		if err := overlapping.Validate(); err == nil {
			t.Fatalf("overlapping destination %q accepted", destination)
		}
	}
}

func TestLegacyRsyncDestinationDefaultsToSSH(t *testing.T) {
	source := RsyncSource{Path: "/source", DestinationHostID: "host", DestinationPath: "/target"}
	if source.EffectiveDestinationKind() != RsyncDestinationSSH {
		t.Fatalf("destination kind=%q", source.EffectiveDestinationKind())
	}
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRsyncTaskCanReferenceSyncRepository(t *testing.T) {
	task := Task{
		Name: "copy photos", Engine: RsyncEngine, Kind: RsyncTask, RepositoryID: "sync-target",
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
		Rsync:           &RsyncSource{Path: "/source"},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("repository-backed rsync task: %v", err)
	}
}

func TestDatabaseConnectionValidationSeparatesPurposeAndNetwork(t *testing.T) {
	validBackup := DatabaseConnection{
		Name: "local mysql backup", Engine: MySQL, Purpose: BackupConnection,
		Network: TCPNetwork, Host: "127.0.0.1", Port: 3306, Username: "backup",
	}
	if err := validBackup.Validate(); err != nil {
		t.Fatalf("valid backup connection: %v", err)
	}

	validSocket := DatabaseConnection{
		Name: "local postgres restore", Engine: PostgreSQL, Purpose: RestoreConnection,
		Network: UnixNetwork, SocketPath: "/var/run/postgresql", Username: "postgres",
	}
	if err := validSocket.Validate(); err != nil {
		t.Fatalf("valid socket connection: %v", err)
	}

	invalid := []DatabaseConnection{
		{Name: "missing purpose", Engine: MySQL, Network: TCPNetwork, Host: "db", Port: 3306, Username: "backup"},
		{Name: "bad engine", Engine: "sqlite", Purpose: BackupConnection, Network: TCPNetwork, Host: "db", Port: 3306, Username: "backup"},
		{Name: "missing tcp port", Engine: MySQL, Purpose: BackupConnection, Network: TCPNetwork, Host: "db", Username: "backup"},
		{Name: "relative socket", Engine: PostgreSQL, Purpose: BackupConnection, Network: UnixNetwork, SocketPath: "postgres.sock", Username: "backup"},
	}
	for _, connection := range invalid {
		if err := connection.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", connection)
		}
	}
}

func TestRepositoryAndPlanValidation(t *testing.T) {
	repository := Repository{Name: "photos", Kind: SFTPRepository, RemoteHostID: "nas", Path: "/volume1/restic/photos"}
	if err := repository.Validate(); err != nil {
		t.Fatalf("valid repository: %v", err)
	}
	local := Repository{Name: "local photos", Kind: LocalRepository, Path: "/Volumes/Backup/photos"}
	if err := local.Validate(); err != nil {
		t.Fatalf("valid local repository: %v", err)
	}
	if err := (Repository{Name: "local relative", Kind: LocalRepository, Path: "backup/photos"}).Validate(); err == nil {
		t.Fatal("local repository path must be absolute")
	}
	if err := (Repository{Name: "local with host", Kind: LocalRepository, RemoteHostID: "nas", Path: "/backup/photos"}).Validate(); err == nil {
		t.Fatal("local repository must not reference a remote host")
	}
	if err := (Repository{Name: "remote without host", Kind: SFTPRepository, Path: "/backup/photos"}).Validate(); err == nil {
		t.Fatal("sftp repository must reference a remote host")
	}
	if err := (Repository{Name: "photos", Kind: SFTPRepository, RemoteHostID: "nas", Path: ""}).Validate(); err == nil {
		t.Fatal("repository path must be required")
	}

	plan := Plan{
		Name: "nightly", Timezone: "Asia/Shanghai", MaxParallel: 2,
		Schedule: Schedule{Kind: DailySchedule, TimeOfDay: "02:30"},
		TaskIDs:  []string{"task-a", "task-b"},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	plan.Timezone = "Mars/Olympus"
	if err := plan.Validate(); err == nil {
		t.Fatal("unknown timezone must fail")
	}
}

func TestPlanValidatesCatchUpWindow(t *testing.T) {
	tests := []struct {
		minutes int
		valid   bool
	}{
		{minutes: 0, valid: true},
		{minutes: 60, valid: true},
		{minutes: 7 * 24 * 60, valid: true},
		{minutes: -1, valid: false},
		{minutes: 7*24*60 + 1, valid: false},
	}

	for _, tt := range tests {
		plan := Plan{
			Name:                 "nightly",
			Schedule:             Schedule{Kind: DailySchedule, TimeOfDay: "02:30"},
			Timezone:             "UTC",
			MaxParallel:          1,
			TaskIDs:              []string{"task"},
			CatchUpWindowMinutes: tt.minutes,
		}
		err := plan.Validate()
		if (err == nil) != tt.valid {
			t.Fatalf("minutes=%d err=%v valid=%v", tt.minutes, err, tt.valid)
		}
	}
}

func TestRestoreVerificationPolicyValidatesScheduleSelectionAndLimits(t *testing.T) {
	valid := RestoreVerificationPolicy{
		TaskID: "task", SelectionPath: "album/sample.jpg", MaximumBytes: 64 << 20, MaximumSuccessAgeHours: 24 * 8,
		Schedule: Schedule{Kind: WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "04:00"}, Timezone: "UTC", CatchUpWindowMinutes: 60,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	for name, mutate := range map[string]func(*RestoreVerificationPolicy){
		"absolute selection": func(policy *RestoreVerificationPolicy) { policy.SelectionPath = "/srv/secret" },
		"escaping selection": func(policy *RestoreVerificationPolicy) { policy.SelectionPath = "../secret" },
		"empty selection":    func(policy *RestoreVerificationPolicy) { policy.SelectionPath = "" },
		"zero bytes":         func(policy *RestoreVerificationPolicy) { policy.MaximumBytes = 0 },
		"excess bytes":       func(policy *RestoreVerificationPolicy) { policy.MaximumBytes = (1 << 40) + 1 },
		"zero age":           func(policy *RestoreVerificationPolicy) { policy.MaximumSuccessAgeHours = 0 },
		"excess age":         func(policy *RestoreVerificationPolicy) { policy.MaximumSuccessAgeHours = 8761 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("invalid policy accepted: %+v", candidate)
			}
		})
	}
}

func TestRepositoryEngineDefaultsToResticAndAcceptsRsync(t *testing.T) {
	legacy := Repository{Name: "legacy", Kind: LocalRepository, Path: "/backup"}
	if legacy.EffectiveEngine() != ResticEngine {
		t.Fatalf("legacy engine=%q", legacy.EffectiveEngine())
	}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Repository{Name: "mirror", Engine: RsyncEngine, Kind: LocalRepository, Path: "/mnt/disk-b/photos"}).Validate(); err != nil {
		t.Fatalf("rsync repository: %v", err)
	}
}
