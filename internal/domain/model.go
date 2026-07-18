package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/execution"
)

type TaskKind string

const (
	DirectoryTask TaskKind = "directory"
	DatabaseTask  TaskKind = "database"
	RsyncTask     TaskKind = "rsync"
)

type EngineKind string

const (
	ResticEngine EngineKind = "restic"
	RsyncEngine  EngineKind = "rsync"
)

type DatabaseEngine string

const (
	MySQL      DatabaseEngine = "mysql"
	PostgreSQL DatabaseEngine = "postgresql"
)

type ConnectionPurpose string

const (
	BackupConnection  ConnectionPurpose = "backup"
	RestoreConnection ConnectionPurpose = "restore"
)

type NetworkKind string

const (
	TCPNetwork  NetworkKind = "tcp"
	UnixNetwork NetworkKind = "unix"
)

type RemoteHost struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	Username        string    `json:"username"`
	HostFingerprint string    `json:"hostFingerprint,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type RepositoryKind string

const (
	LocalRepository RepositoryKind = "local"
	SFTPRepository  RepositoryKind = "sftp"
	S3Repository    RepositoryKind = "s3"
)

func (h RemoteHost) Validate() error {
	if strings.TrimSpace(h.Name) == "" || strings.TrimSpace(h.Host) == "" || strings.TrimSpace(h.Username) == "" {
		return errors.New("remote host name, address and username are required")
	}
	if h.Port < 1 || h.Port > 65535 {
		return errors.New("remote host port must be between 1 and 65535")
	}
	return nil
}

type Repository struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Engine          EngineKind          `json:"engine,omitempty"`
	Kind            RepositoryKind      `json:"kind"`
	RemoteHostID    string              `json:"remoteHostId"`
	Path            string              `json:"path"`
	S3              *S3RepositoryConfig `json:"s3,omitempty"`
	BackendSecretID string              `json:"-"`
	Status          string              `json:"status"`
	Capacity        *RepositoryCapacity `json:"capacity,omitempty"`
	LastRun         *RepositoryRun      `json:"lastRun,omitempty"`
	NextRun         string              `json:"nextRun,omitempty"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

type S3RepositoryConfig struct {
	Endpoint              string `json:"endpoint"`
	Bucket                string `json:"bucket"`
	Region                string `json:"region"`
	PathStyle             bool   `json:"pathStyle"`
	CredentialsConfigured bool   `json:"credentialsConfigured,omitempty"`
}

type RepositoryRun struct {
	Status    string         `json:"status"`
	StartedAt time.Time      `json:"startedAt"`
	Summary   map[string]any `json:"summary,omitempty"`
}

type RepositoryCapacity struct {
	TotalBytes     uint64    `json:"totalBytes"`
	UsedBytes      uint64    `json:"usedBytes"`
	AvailableBytes uint64    `json:"availableBytes"`
	CheckedAt      time.Time `json:"checkedAt"`
	SourceAgentID  string    `json:"sourceAgentId,omitempty"`
}

const (
	CapacityForecastReady               = "ready"
	CapacityForecastInsufficientSamples = "insufficient_samples"
	CapacityForecastInsufficientSpan    = "insufficient_span"
	CapacityForecastNonPositiveGrowth   = "non_positive_growth"
	CapacityForecastBeyondRange         = "beyond_supported_range"
)

type RepositoryCapacityPolicy struct {
	RepositoryID            string     `json:"repositoryId"`
	Enabled                 bool       `json:"enabled"`
	ProbeIntervalMinutes    int        `json:"probeIntervalMinutes"`
	MinimumAvailableBytes   uint64     `json:"minimumAvailableBytes"`
	MinimumAvailablePercent float64    `json:"minimumAvailablePercent"`
	ExhaustionWarningDays   int        `json:"exhaustionWarningDays"`
	NextProbeAt             *time.Time `json:"nextProbeAt,omitempty"`
	LastAttemptAt           *time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt           *time.Time `json:"lastSuccessAt,omitempty"`
	LastError               string     `json:"lastError,omitempty"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

func DefaultRepositoryCapacityPolicy(repositoryID string, now time.Time) RepositoryCapacityPolicy {
	now = now.UTC()
	return RepositoryCapacityPolicy{
		RepositoryID: repositoryID, Enabled: true, ProbeIntervalMinutes: 24 * 60,
		NextProbeAt: &now, UpdatedAt: now,
	}
}

func (p RepositoryCapacityPolicy) Validate() error {
	if strings.TrimSpace(p.RepositoryID) == "" {
		return errors.New("repository capacity policy requires a repository")
	}
	if p.ProbeIntervalMinutes < 15 || p.ProbeIntervalMinutes > 7*24*60 {
		return errors.New("repository capacity probe interval must be between 15 minutes and 7 days")
	}
	if p.MinimumAvailableBytes > math.MaxInt64 {
		return errors.New("repository capacity byte threshold exceeds the supported limit")
	}
	if math.IsNaN(p.MinimumAvailablePercent) || math.IsInf(p.MinimumAvailablePercent, 0) || p.MinimumAvailablePercent < 0 || p.MinimumAvailablePercent > 100 {
		return errors.New("repository capacity percentage threshold must be between 0 and 100")
	}
	if p.ExhaustionWarningDays < 0 || p.ExhaustionWarningDays > 3650 {
		return errors.New("repository capacity forecast horizon must be between 0 and 3650 days")
	}
	return nil
}

type RepositoryCapacitySample struct {
	ID             int64     `json:"id"`
	RepositoryID   string    `json:"repositoryId"`
	TotalBytes     uint64    `json:"totalBytes"`
	UsedBytes      uint64    `json:"usedBytes"`
	AvailableBytes uint64    `json:"availableBytes"`
	CheckedAt      time.Time `json:"checkedAt"`
	SourceAgentID  string    `json:"sourceAgentId,omitempty"`
}

type RepositoryCapacityForecast struct {
	Status                string     `json:"status"`
	SampleCount           int        `json:"sampleCount"`
	ObservationStartedAt  *time.Time `json:"observationStartedAt,omitempty"`
	ObservationEndedAt    *time.Time `json:"observationEndedAt,omitempty"`
	GrowthBytesPerDay     float64    `json:"growthBytesPerDay,omitempty"`
	EstimatedExhaustionAt *time.Time `json:"estimatedExhaustionAt,omitempty"`
}

func (r Repository) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("repository name is required")
	}
	if strings.ContainsRune(r.Path, '\x00') {
		return errors.New("repository path contains an invalid character")
	}
	if r.EffectiveEngine() != ResticEngine && r.EffectiveEngine() != RsyncEngine {
		return fmt.Errorf("unsupported repository engine %q", r.Engine)
	}
	switch r.EffectiveKind() {
	case LocalRepository:
		if r.S3 != nil || r.RemoteHostID != "" || !filepath.IsAbs(r.Path) {
			return errors.New("local repository requires an absolute local path and no remote host")
		}
	case SFTPRepository:
		if r.S3 != nil || strings.TrimSpace(r.Path) == "" || strings.TrimSpace(r.RemoteHostID) == "" {
			return errors.New("sftp repository requires a remote host")
		}
	case S3Repository:
		if r.EffectiveEngine() != ResticEngine || r.RemoteHostID != "" || r.S3 == nil {
			return errors.New("S3 repository requires Restic, structured backend settings, and no remote host")
		}
		if err := r.S3.validate(r.Path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported repository kind %q", r.Kind)
	}
	return nil
}

func (c S3RepositoryConfig) validate(prefix string) error {
	if c.Endpoint != strings.TrimSpace(c.Endpoint) || len(c.Endpoint) > 2048 {
		return errors.New("S3 endpoint must be a bounded origin URL without surrounding whitespace")
	}
	endpoint, err := url.ParseRequestURI(c.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("S3 endpoint must be an origin URL without credentials, path, query, or fragment")
	}
	if endpoint.Scheme != "https" {
		host := endpoint.Hostname()
		ip := net.ParseIP(host)
		if endpoint.Scheme != "http" || !(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
			return errors.New("S3 endpoint requires HTTPS; HTTP is limited to the loopback interface")
		}
	}
	if len(c.Bucket) < 3 || len(c.Bucket) > 63 || !asciiAlphaNumeric(c.Bucket[0]) || !asciiAlphaNumeric(c.Bucket[len(c.Bucket)-1]) || strings.Contains(c.Bucket, "..") {
		return errors.New("S3 bucket name is invalid")
	}
	for _, value := range c.Bucket {
		if !(value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '.' || value == '-') {
			return errors.New("S3 bucket name is invalid")
		}
	}
	if c.Region == "" || len(c.Region) > 64 {
		return errors.New("S3 region is required")
	}
	for _, value := range c.Region {
		if !(value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '_') {
			return errors.New("S3 region is invalid")
		}
	}
	if len(prefix) > 512 || strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "\\\x00\r\n") {
		return errors.New("S3 repository prefix is invalid")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if prefix == "" {
			break
		}
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("S3 repository prefix is invalid")
		}
		for _, value := range segment {
			if !(value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.') {
				return errors.New("S3 repository prefix is invalid")
			}
		}
	}
	return nil
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func (r Repository) EffectiveEngine() EngineKind {
	if r.Engine == "" {
		return ResticEngine
	}
	return r.Engine
}

func (r Repository) EffectiveKind() RepositoryKind {
	if r.Kind == "" {
		return SFTPRepository
	}
	return r.Kind
}

type TLSConfig struct {
	Mode       string `json:"mode"`
	CA         string `json:"ca,omitempty"`
	ClientCert string `json:"clientCert,omitempty"`
	ClientKey  string `json:"clientKey,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

type DatabaseConnection struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Engine     DatabaseEngine    `json:"engine"`
	Purpose    ConnectionPurpose `json:"purpose"`
	Network    NetworkKind       `json:"network"`
	Host       string            `json:"host,omitempty"`
	Port       int               `json:"port,omitempty"`
	SocketPath string            `json:"socketPath,omitempty"`
	Username   string            `json:"username"`
	TLS        TLSConfig         `json:"tls"`
	ToolPaths  map[string]string `json:"toolPaths"`
	Status     string            `json:"status"`
	Preflight  DatabasePreflight `json:"preflight"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

type DatabasePreflight struct {
	CheckedAt     time.Time `json:"checkedAt,omitempty"`
	ClientVersion string    `json:"clientVersion,omitempty"`
	ServerVersion string    `json:"serverVersion,omitempty"`
	Error         string    `json:"error,omitempty"`
}

func (c DatabaseConnection) Validate() error {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Username) == "" {
		return errors.New("database connection name and username are required")
	}
	if c.Engine != MySQL && c.Engine != PostgreSQL {
		return fmt.Errorf("unsupported database engine %q", c.Engine)
	}
	if c.Purpose != BackupConnection && c.Purpose != RestoreConnection {
		return fmt.Errorf("unsupported database connection purpose %q", c.Purpose)
	}
	switch c.Network {
	case TCPNetwork:
		if strings.TrimSpace(c.Host) == "" || c.Port < 1 || c.Port > 65535 || c.SocketPath != "" {
			return errors.New("tcp connection requires host and valid port only")
		}
	case UnixNetwork:
		if !filepath.IsAbs(c.SocketPath) || c.Host != "" || c.Port != 0 {
			return errors.New("unix connection requires an absolute socket path only")
		}
	default:
		return fmt.Errorf("unsupported database network %q", c.Network)
	}
	return nil
}

type DirectorySource struct {
	Path            string   `json:"path"`
	Exclusions      []string `json:"exclusions,omitempty"`
	SkipIfUnchanged bool     `json:"skipIfUnchanged"`
}

type DatabaseSource struct {
	ConnectionID string `json:"connectionId"`
	Database     string `json:"database"`
}

type RsyncDestinationKind string

const (
	RsyncDestinationSSH   RsyncDestinationKind = "ssh"
	RsyncDestinationLocal RsyncDestinationKind = "local"
)

type RsyncSource struct {
	Path              string               `json:"path"`
	DestinationKind   RsyncDestinationKind `json:"destinationKind,omitempty"`
	DestinationHostID string               `json:"destinationHostId"`
	DestinationPath   string               `json:"destinationPath"`
	Exclusions        []string             `json:"exclusions,omitempty"`
	Delete            bool                 `json:"delete"`
}

func (s RsyncSource) EffectiveDestinationKind() RsyncDestinationKind {
	if s.DestinationKind == "" {
		return RsyncDestinationSSH
	}
	return s.DestinationKind
}

func (s RsyncSource) Validate() error {
	if !filepath.IsAbs(s.Path) || !filepath.IsAbs(s.DestinationPath) {
		return errors.New("rsync source and destination paths must be absolute")
	}
	switch s.EffectiveDestinationKind() {
	case RsyncDestinationSSH:
		if strings.TrimSpace(s.DestinationHostID) == "" {
			return errors.New("rsync destination host is required")
		}
	case RsyncDestinationLocal:
		if strings.TrimSpace(s.DestinationHostID) != "" {
			return errors.New("local rsync destination cannot reference a remote host")
		}
		if pathsOverlap(s.Path, s.DestinationPath) {
			return errors.New("local rsync source and destination paths must not overlap")
		}
	default:
		return fmt.Errorf("unsupported rsync destination kind %q", s.DestinationKind)
	}
	if strings.ContainsRune(s.Path, '\x00') || strings.ContainsRune(s.DestinationPath, '\x00') {
		return errors.New("rsync path contains an invalid character")
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

type RetentionPolicy struct {
	KeepWithinDays int `json:"keepWithinDays,omitempty"`
	KeepLast       int `json:"keepLast,omitempty"`
	KeepHourly     int `json:"keepHourly,omitempty"`
	KeepDaily      int `json:"keepDaily,omitempty"`
	KeepWeekly     int `json:"keepWeekly,omitempty"`
	KeepMonthly    int `json:"keepMonthly,omitempty"`
	KeepYearly     int `json:"keepYearly,omitempty"`
}

type ResourcePolicy struct {
	UploadKiBPerSecond   int    `json:"uploadKiBPerSecond,omitempty"`
	DownloadKiBPerSecond int    `json:"downloadKiBPerSecond,omitempty"`
	ReadConcurrency      int    `json:"readConcurrency,omitempty"`
	Compression          string `json:"compression,omitempty"`
}

type TaskHealthPolicy struct {
	// MaxSuccessAgeHours is the maximum accepted age of the latest complete
	// success. Zero deliberately disables this expectation for upgraded tasks
	// until an administrator chooses a value.
	MaxSuccessAgeHours int `json:"maxSuccessAgeHours,omitempty"`
}

// TaskScopeConfirmation is server-issued evidence that an administrator
// reviewed the exact source scope represented by Fingerprint. Clients may
// round-trip it for display, but task mutation handlers must never trust a
// client-provided value as authorization.
type TaskScopeConfirmation struct {
	PreviewID       string         `json:"previewId,omitempty"`
	Fingerprint     string         `json:"fingerprint,omitempty"`
	ConfirmedBy     string         `json:"confirmedBy,omitempty"`
	ConfirmedAt     time.Time      `json:"confirmedAt,omitempty"`
	Summary         map[string]any `json:"summary,omitempty"`
	DeleteConfirmed bool           `json:"deleteConfirmed,omitempty"`
}

func (c TaskScopeConfirmation) Present() bool {
	return c.PreviewID != "" && c.Fingerprint != "" && c.ConfirmedBy != "" && !c.ConfirmedAt.IsZero()
}

type Task struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Engine            EngineKind            `json:"engine,omitempty"`
	Kind              TaskKind              `json:"kind"`
	ExecutionTarget   execution.Target      `json:"executionTarget,omitempty"`
	RepositoryID      string                `json:"repositoryId,omitempty"`
	Directory         *DirectorySource      `json:"directory,omitempty"`
	Database          *DatabaseSource       `json:"database,omitempty"`
	Rsync             *RsyncSource          `json:"rsync,omitempty"`
	Retention         RetentionPolicy       `json:"retention"`
	Resources         ResourcePolicy        `json:"resources"`
	Health            TaskHealthPolicy      `json:"health"`
	ScopeConfirmation TaskScopeConfirmation `json:"scopeConfirmation,omitempty"`
	Enabled           bool                  `json:"enabled"`
	CreatedAt         time.Time             `json:"createdAt"`
	UpdatedAt         time.Time             `json:"updatedAt"`
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("task name is required")
	}
	if err := t.EffectiveExecutionTarget().Validate(); err != nil {
		return err
	}
	if err := t.Health.Validate(); err != nil {
		return err
	}
	switch t.EffectiveEngine() {
	case ResticEngine:
		if strings.TrimSpace(t.RepositoryID) == "" {
			return errors.New("repository is required")
		}
		switch t.Kind {
		case DirectoryTask:
			if t.Directory == nil || t.Database != nil || t.Rsync != nil {
				return errors.New("directory task requires exactly one directory source")
			}
			absolute := filepath.IsAbs(t.Directory.Path)
			if t.EffectiveExecutionTarget().Kind == execution.Agent {
				absolute = portableAbsoluteAgentPath(t.Directory.Path)
			}
			if !absolute {
				return errors.New("source directory must be absolute")
			}
			t.Directory.Path = filepath.Clean(t.Directory.Path)
		case DatabaseTask:
			if t.Database == nil || t.Directory != nil || t.Rsync != nil {
				return errors.New("database task requires exactly one database source")
			}
			if strings.TrimSpace(t.Database.ConnectionID) == "" || strings.TrimSpace(t.Database.Database) == "" {
				return errors.New("database connection and logical database are required")
			}
		default:
			return fmt.Errorf("unsupported restic task kind %q", t.Kind)
		}
		if err := t.Retention.Validate(); err != nil {
			return err
		}
		return t.Resources.Validate()
	case RsyncEngine:
		if t.Kind != RsyncTask || t.Rsync == nil || t.Directory != nil || t.Database != nil {
			return errors.New("rsync task requires exactly one rsync source")
		}
		if t.Retention != (RetentionPolicy{}) || t.Resources != (ResourcePolicy{}) {
			return errors.New("rsync task cannot use restic retention or resource policies")
		}
		if t.RepositoryID != "" {
			if !filepath.IsAbs(t.Rsync.Path) || strings.ContainsAny(t.Rsync.Path, "\x00\r\n") {
				return errors.New("rsync source path must be an absolute valid path")
			}
			if t.Rsync.DestinationKind != "" || t.Rsync.DestinationHostID != "" || t.Rsync.DestinationPath != "" {
				return errors.New("repository-backed rsync task cannot embed a destination")
			}
			return nil
		}
		if err := t.Rsync.Validate(); err != nil {
			return err
		}
		if t.Rsync.EffectiveDestinationKind() == RsyncDestinationLocal && t.EffectiveExecutionTarget().Kind != execution.Agent {
			return errors.New("local rsync destination requires agent execution")
		}
		return nil
	default:
		return fmt.Errorf("unsupported task engine %q", t.Engine)
	}
}

func portableAbsoluteAgentPath(value string) bool {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return !hasPathParent(value, "/")
	}
	windowsPath := strings.ReplaceAll(value, "/", "\\")
	return len(windowsPath) >= 3 && isASCIILetter(windowsPath[0]) && windowsPath[1] == ':' && windowsPath[2] == '\\' && !hasPathParent(windowsPath, "\\")
}

func hasPathParent(value, separator string) bool {
	for _, segment := range strings.Split(value, separator) {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func (p TaskHealthPolicy) Validate() error {
	if p.MaxSuccessAgeHours < 0 || p.MaxSuccessAgeHours > 24*365 {
		return errors.New("maximum success age must be between 1 and 8760 hours, or 0 to disable")
	}
	return nil
}

func (t Task) EffectiveEngine() EngineKind {
	if t.Engine == "" {
		return ResticEngine
	}
	return t.Engine
}

func (t Task) EffectiveExecutionTarget() execution.Target {
	return t.ExecutionTarget.Normalized()
}

func (p RetentionPolicy) Validate() error {
	values := []int{p.KeepWithinDays, p.KeepLast, p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.KeepYearly}
	for _, value := range values {
		if value < 0 {
			return errors.New("retention values cannot be negative")
		}
	}
	return nil
}

// Fingerprint identifies the complete retention contract that a maintenance
// preview reviewed. The explicit version and field order keep it stable across
// JSON encoder changes while allowing a future policy schema to evolve safely.
func (p RetentionPolicy) Fingerprint() string {
	canonical := fmt.Sprintf(
		"retention-policy/v1\nkeepWithinDays=%d\nkeepLast=%d\nkeepHourly=%d\nkeepDaily=%d\nkeepWeekly=%d\nkeepMonthly=%d\nkeepYearly=%d\n",
		p.KeepWithinDays,
		p.KeepLast,
		p.KeepHourly,
		p.KeepDaily,
		p.KeepWeekly,
		p.KeepMonthly,
		p.KeepYearly,
	)
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (p ResourcePolicy) Validate() error {
	if p.UploadKiBPerSecond < 0 || p.DownloadKiBPerSecond < 0 || p.ReadConcurrency < 0 {
		return errors.New("resource limits cannot be negative")
	}
	switch p.Compression {
	case "", "auto", "off", "max":
		return nil
	default:
		return fmt.Errorf("unsupported compression %q", p.Compression)
	}
}

type ScheduleKind string

const (
	DailySchedule    ScheduleKind = "daily"
	WeeklySchedule   ScheduleKind = "weekly"
	IntervalSchedule ScheduleKind = "interval"
)

type Schedule struct {
	Kind          ScheduleKind `json:"kind"`
	TimeOfDay     string       `json:"timeOfDay,omitempty"`
	DayOfWeek     time.Weekday `json:"dayOfWeek,omitempty"`
	IntervalHours int          `json:"intervalHours,omitempty"`
}

type Plan struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Schedule             Schedule  `json:"schedule"`
	Timezone             string    `json:"timezone"`
	MaxParallel          int       `json:"maxParallel"`
	TaskIDs              []string  `json:"taskIds"`
	Enabled              bool      `json:"enabled"`
	CatchUpWindowMinutes int       `json:"catchUpWindowMinutes"`
	ScheduleAnchorAt     time.Time `json:"scheduleAnchorAt"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type MaintenancePolicy struct {
	RepositoryID         string           `json:"repositoryId"`
	Schedule             Schedule         `json:"schedule"`
	Timezone             string           `json:"timezone"`
	Retention            RetentionPolicy  `json:"retention"`
	RetentionSource      RetentionSource  `json:"retentionSource"`
	RetentionConflict    bool             `json:"retentionConflict"`
	ReviewedRetention    *RetentionPolicy `json:"reviewedRetention,omitempty"`
	PolicyFingerprint    string           `json:"policyFingerprint"`
	Enabled              bool             `json:"enabled"`
	CatchUpWindowMinutes int              `json:"catchUpWindowMinutes"`
	ScheduleAnchorAt     time.Time        `json:"scheduleAnchorAt"`
	UpdatedAt            time.Time        `json:"updatedAt"`
}

type RestoreVerificationPolicy struct {
	TaskID                 string    `json:"taskId"`
	Schedule               Schedule  `json:"schedule"`
	Timezone               string    `json:"timezone"`
	SelectionPath          string    `json:"selectionPath"`
	MaximumBytes           int64     `json:"maximumBytes"`
	MaximumSuccessAgeHours int       `json:"maximumSuccessAgeHours"`
	Enabled                bool      `json:"enabled"`
	CatchUpWindowMinutes   int       `json:"catchUpWindowMinutes"`
	ScheduleAnchorAt       time.Time `json:"scheduleAnchorAt"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type RetentionSource string

const (
	RepositoryRetentionSource RetentionSource = "repository"
	TaskRetentionSource       RetentionSource = "task"
)

func (p MaintenancePolicy) Validate() error {
	probe := Plan{Name: "maintenance", Schedule: p.Schedule, Timezone: p.Timezone, MaxParallel: 1, TaskIDs: []string{"repository"}, CatchUpWindowMinutes: p.CatchUpWindowMinutes}
	if err := probe.Validate(); err != nil {
		return err
	}
	return p.Retention.Validate()
}

func (p RestoreVerificationPolicy) Validate() error {
	if strings.TrimSpace(p.TaskID) == "" {
		return errors.New("restore verification task is required")
	}
	selection := strings.TrimSpace(p.SelectionPath)
	clean := path.Clean(selection)
	if selection == "" || selection == "." || strings.ContainsAny(selection, "\\\x00\r\n") || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || clean != selection {
		return errors.New("restore verification selection must be a normalized relative snapshot path")
	}
	if p.MaximumBytes < 1 || p.MaximumBytes > 1<<40 {
		return errors.New("restore verification maximum bytes must be between 1 and 1099511627776")
	}
	if p.MaximumSuccessAgeHours < 1 || p.MaximumSuccessAgeHours > 24*365 {
		return errors.New("restore verification maximum success age must be between 1 and 8760 hours")
	}
	probe := Plan{Name: "restore verification", Schedule: p.Schedule, Timezone: p.Timezone, MaxParallel: 1, TaskIDs: []string{p.TaskID}, CatchUpWindowMinutes: p.CatchUpWindowMinutes}
	return probe.Validate()
}

func (p Plan) Validate() error {
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Timezone) == "" {
		return errors.New("plan name and timezone are required")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("invalid plan timezone: %w", err)
	}
	if p.MaxParallel < 1 {
		return errors.New("plan max parallel must be at least 1")
	}
	if len(p.TaskIDs) == 0 {
		return errors.New("plan requires at least one task")
	}
	if p.CatchUpWindowMinutes < 0 || p.CatchUpWindowMinutes > 7*24*60 {
		return errors.New("plan catch-up window requires 0 to 10080 minutes")
	}
	switch p.Schedule.Kind {
	case DailySchedule:
		if _, err := time.Parse("15:04", p.Schedule.TimeOfDay); err != nil {
			return errors.New("daily schedule requires HH:MM time")
		}
	case WeeklySchedule:
		if _, err := time.Parse("15:04", p.Schedule.TimeOfDay); err != nil || p.Schedule.DayOfWeek < time.Sunday || p.Schedule.DayOfWeek > time.Saturday {
			return errors.New("weekly schedule requires weekday and HH:MM time")
		}
	case IntervalSchedule:
		if p.Schedule.IntervalHours < 1 || p.Schedule.IntervalHours > 8760 {
			return errors.New("interval schedule requires 1 to 8760 hours")
		}
	default:
		return fmt.Errorf("unsupported schedule kind %q", p.Schedule.Kind)
	}
	return nil
}
