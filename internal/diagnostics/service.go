package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode"

	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/store"
)

const (
	FormatVersion            = 1
	MaxBundleBytes           = 64 * 1024
	MaxCompatibilityFindings = 32
	MaxRecentFailures        = 50
	MaxActiveAlerts          = 50
	MaxStringBytes           = 64
)

var ErrBundleTooLarge = errors.New("diagnostic bundle exceeds the maximum size")

type Source interface {
	DiagnosticSnapshot(context.Context, time.Time, int, int) (store.DiagnosticSnapshot, error)
}

type Service struct {
	source Source
	now    func() time.Time
}

type Request struct {
	ApplicationVersion string
	Compatibility      compat.Report
}

type Result struct {
	Bytes  []byte
	Bundle Bundle
	Counts SectionCounts
}

type Bundle struct {
	FormatVersion      int                  `json:"formatVersion"`
	GeneratedAt        time.Time            `json:"generatedAt"`
	ApplicationVersion string               `json:"applicationVersion"`
	Compatibility      CompatibilitySection `json:"compatibility"`
	Resources          ResourceSection      `json:"resources"`
	RecentFailures     FailureSection       `json:"recentFailures"`
	ActiveAlerts       AlertSection         `json:"activeAlerts"`
	Notifications      NotificationSection  `json:"notifications"`
	Capacity           CapacitySection      `json:"capacity"`
	Limits             Limits               `json:"limits"`
}

type CompatibilitySection struct {
	Blocked   bool                   `json:"blocked"`
	Findings  []CompatibilityFinding `json:"findings"`
	Truncated bool                   `json:"truncated"`
}

type CompatibilityFinding struct {
	Capability string `json:"capability"`
	Severity   string `json:"severity"`
}

type ResourceSection struct {
	RemoteHosts         int              `json:"remoteHosts"`
	Repositories        RepositoryCounts `json:"repositories"`
	DatabaseConnections DatabaseCounts   `json:"databaseConnections"`
	Tasks               TaskCounts       `json:"tasks"`
	Plans               PlanCounts       `json:"plans"`
	Agents              AgentCounts      `json:"agents"`
}

type RepositoryCounts struct {
	Total         int `json:"total"`
	Ready         int `json:"ready"`
	Uninitialized int `json:"uninitialized"`
	Disconnected  int `json:"disconnected"`
	Abnormal      int `json:"abnormal"`
	Local         int `json:"local"`
	SFTP          int `json:"sftp"`
	S3            int `json:"s3"`
}

type DatabaseCounts struct {
	Total      int `json:"total"`
	Ready      int `json:"ready"`
	Draft      int `json:"draft"`
	MySQL      int `json:"mysql"`
	PostgreSQL int `json:"postgresql"`
	Backup     int `json:"backup"`
	Restore    int `json:"restore"`
}

type TaskCounts struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	Restic    int `json:"restic"`
	Rsync     int `json:"rsync"`
	Directory int `json:"directory"`
	Database  int `json:"database"`
}

type PlanCounts struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
}

type AgentCounts struct {
	Total       int `json:"total"`
	Online      int `json:"online"`
	Offline     int `json:"offline"`
	Revoked     int `json:"revoked"`
	Stopped     int `json:"stopped"`
	Uninstalled int `json:"uninstalled"`
}

type FailureSection struct {
	Items     []FailureSummary `json:"items"`
	Truncated bool             `json:"truncated"`
}

type FailureSummary struct {
	RecordType   string    `json:"recordType"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	OccurredAt   time.Time `json:"occurredAt"`
	AttemptCount int       `json:"attemptCount"`
}

type AlertSection struct {
	Items     []AlertSummary `json:"items"`
	Truncated bool           `json:"truncated"`
}

type AlertSummary struct {
	Kind            string    `json:"kind"`
	Severity        string    `json:"severity"`
	ObjectType      string    `json:"objectType"`
	FirstAt         time.Time `json:"firstAt"`
	LastAt          time.Time `json:"lastAt"`
	OccurrenceCount int       `json:"occurrenceCount"`
}

type NotificationSection struct {
	Configured         bool       `json:"configured"`
	Enabled            bool       `json:"enabled"`
	ConfiguredChannels int        `json:"configuredChannels"`
	EnabledChannels    int        `json:"enabledChannels"`
	Delivered          int        `json:"delivered"`
	Retrying           int        `json:"retrying"`
	FailedFinal        int        `json:"failedFinal"`
	RateLimited        int        `json:"rateLimited"`
	SkippedDisabled    int        `json:"skippedDisabled"`
	LastDeliveryAt     *time.Time `json:"lastDeliveryAt,omitempty"`
}

type CapacitySection struct {
	Repositories         int `json:"repositories"`
	MonitoringEnabled    int `json:"monitoringEnabled"`
	ReadyForMonitoring   int `json:"readyForMonitoring"`
	WithSuccessfulSample int `json:"withSuccessfulSample"`
	Stale                int `json:"stale"`
	ProbeFailures        int `json:"probeFailures"`
	BelowThreshold       int `json:"belowThreshold"`
	LowAlerts            int `json:"lowAlerts"`
	ForecastAlerts       int `json:"forecastAlerts"`
}

type Limits struct {
	CompatibilityFindings int `json:"compatibilityFindings"`
	RecentFailures        int `json:"recentFailures"`
	ActiveAlerts          int `json:"activeAlerts"`
	MaximumStringBytes    int `json:"maximumStringBytes"`
	MaximumBytes          int `json:"maximumBytes"`
}

type SectionCounts struct {
	Resources             int
	CompatibilityFindings int
	RecentFailures        int
	ActiveAlerts          int
	NotificationChannels  int
	CapacityRepositories  int
}

func New(source Source, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{source: source, now: now}
}

func (s *Service) Generate(ctx context.Context, request Request) (Result, error) {
	var result Result
	generatedAt := s.now().UTC()
	snapshot, err := s.source.DiagnosticSnapshot(ctx, generatedAt, MaxRecentFailures, MaxActiveAlerts)
	if err != nil {
		return result, err
	}
	result.Bundle = Bundle{
		FormatVersion: FormatVersion, GeneratedAt: generatedAt, ApplicationVersion: safeVersion(request.ApplicationVersion),
		Compatibility:  compatibilitySection(request.Compatibility),
		Resources:      resourceSection(snapshot.Resources),
		RecentFailures: failureSection(snapshot.RecentFailures, snapshot.FailuresTruncated),
		ActiveAlerts:   alertSection(snapshot.ActiveAlerts, snapshot.AlertsTruncated),
		Notifications:  notificationSection(snapshot.Notifications),
		Capacity:       capacitySection(snapshot.Capacity),
		Limits: Limits{
			CompatibilityFindings: MaxCompatibilityFindings, RecentFailures: MaxRecentFailures,
			ActiveAlerts: MaxActiveAlerts, MaximumStringBytes: MaxStringBytes, MaximumBytes: MaxBundleBytes,
		},
	}
	result.Counts = sectionCounts(result.Bundle)
	result.Bytes, err = json.MarshalIndent(result.Bundle, "", "  ")
	if err != nil {
		return Result{}, err
	}
	result.Bytes = append(result.Bytes, '\n')
	if len(result.Bytes) > MaxBundleBytes {
		return Result{}, ErrBundleTooLarge
	}
	return result, nil
}

func compatibilitySection(report compat.Report) CompatibilitySection {
	section := CompatibilitySection{Blocked: report.Blocked, Findings: make([]CompatibilityFinding, 0, min(len(report.Findings), MaxCompatibilityFindings))}
	for _, finding := range report.Findings {
		if len(section.Findings) == MaxCompatibilityFindings {
			section.Truncated = true
			break
		}
		section.Findings = append(section.Findings, CompatibilityFinding{
			Capability: allowlisted(finding.Capability, compatibilityCapabilities),
			Severity:   allowlisted(string(finding.Severity), severities),
		})
	}
	return section
}

func failureSection(items []store.DiagnosticFailure, sourceTruncated bool) FailureSection {
	section := FailureSection{Items: make([]FailureSummary, 0, min(len(items), MaxRecentFailures)), Truncated: sourceTruncated || len(items) > MaxRecentFailures}
	for _, item := range items {
		if len(section.Items) == MaxRecentFailures {
			break
		}
		recordType := allowlisted(item.RecordType, recordTypes)
		kind := "other"
		if recordType == "run" {
			kind = allowlisted(item.Kind, runKinds)
		} else if recordType == "operation" {
			kind = allowlisted(item.Kind, operationKinds)
		}
		section.Items = append(section.Items, FailureSummary{
			RecordType: recordType, Kind: kind, Status: allowlisted(item.Status, failureStatuses),
			OccurredAt: item.OccurredAt.UTC(), AttemptCount: max(item.AttemptCount, 0),
		})
	}
	return section
}

func alertSection(items []store.DiagnosticAlert, sourceTruncated bool) AlertSection {
	section := AlertSection{Items: make([]AlertSummary, 0, min(len(items), MaxActiveAlerts)), Truncated: sourceTruncated || len(items) > MaxActiveAlerts}
	for _, item := range items {
		if len(section.Items) == MaxActiveAlerts {
			break
		}
		section.Items = append(section.Items, AlertSummary{
			Kind: allowlisted(item.Kind, alertKinds), Severity: allowlisted(item.Severity, severities), ObjectType: allowlisted(item.ObjectType, objectTypes),
			FirstAt: item.FirstAt.UTC(), LastAt: item.LastAt.UTC(), OccurrenceCount: max(item.OccurrenceCount, 0),
		})
	}
	return section
}

func resourceSection(value store.DiagnosticResourceCounts) ResourceSection {
	return ResourceSection{
		RemoteHosts: value.RemoteHosts,
		Repositories: RepositoryCounts{
			Total: value.Repositories.Total, Ready: value.Repositories.Ready, Uninitialized: value.Repositories.Uninitialized,
			Disconnected: value.Repositories.Disconnected, Abnormal: value.Repositories.Abnormal, Local: value.Repositories.Local, SFTP: value.Repositories.SFTP, S3: value.Repositories.S3,
		},
		DatabaseConnections: DatabaseCounts{
			Total: value.DatabaseConnections.Total, Ready: value.DatabaseConnections.Ready, Draft: value.DatabaseConnections.Draft,
			MySQL: value.DatabaseConnections.MySQL, PostgreSQL: value.DatabaseConnections.PostgreSQL,
			Backup: value.DatabaseConnections.Backup, Restore: value.DatabaseConnections.Restore,
		},
		Tasks: TaskCounts{
			Total: value.Tasks.Total, Enabled: value.Tasks.Enabled, Restic: value.Tasks.Restic, Rsync: value.Tasks.Rsync,
			Directory: value.Tasks.Directory, Database: value.Tasks.Database,
		},
		Plans:  PlanCounts{Total: value.Plans.Total, Enabled: value.Plans.Enabled},
		Agents: AgentCounts{Total: value.Agents.Total, Online: value.Agents.Online, Offline: value.Agents.Offline, Revoked: value.Agents.Revoked, Stopped: value.Agents.Stopped, Uninstalled: value.Agents.Uninstalled},
	}
}

func notificationSection(value store.DiagnosticNotificationState) NotificationSection {
	return NotificationSection{
		Configured: value.Configured, Enabled: value.Enabled, ConfiguredChannels: value.ConfiguredChannels, EnabledChannels: value.EnabledChannels,
		Delivered: value.Delivered, Retrying: value.Retrying, FailedFinal: value.FailedFinal, RateLimited: value.RateLimited,
		SkippedDisabled: value.SkippedDisabled, LastDeliveryAt: value.LastDeliveryAt,
	}
}

func capacitySection(value store.DiagnosticCapacityState) CapacitySection {
	return CapacitySection{
		Repositories: value.Repositories, MonitoringEnabled: value.MonitoringEnabled, ReadyForMonitoring: value.ReadyForMonitoring,
		WithSuccessfulSample: value.WithSuccessfulSample, Stale: value.Stale, ProbeFailures: value.ProbeFailures,
		BelowThreshold: value.BelowThreshold, LowAlerts: value.LowAlerts, ForecastAlerts: value.ForecastAlerts,
	}
}

func sectionCounts(bundle Bundle) SectionCounts {
	resources := bundle.Resources.RemoteHosts + bundle.Resources.Repositories.Total + bundle.Resources.DatabaseConnections.Total + bundle.Resources.Tasks.Total + bundle.Resources.Plans.Total + bundle.Resources.Agents.Total
	channels := bundle.Notifications.ConfiguredChannels
	if channels == 0 && bundle.Notifications.Configured {
		channels = 1
	}
	return SectionCounts{
		Resources: resources, CompatibilityFindings: len(bundle.Compatibility.Findings), RecentFailures: len(bundle.RecentFailures.Items),
		ActiveAlerts: len(bundle.ActiveAlerts.Items), NotificationChannels: channels, CapacityRepositories: bundle.Capacity.Repositories,
	}
}

func safeVersion(value string) string {
	if value == "" || len(value) > MaxStringBytes {
		return "unknown"
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '+' || character == '-') {
			return "unknown"
		}
	}
	return value
}

func allowlisted(value string, allowed map[string]struct{}) string {
	if _, ok := allowed[value]; ok {
		return value
	}
	return "other"
}

func tokens(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var (
	compatibilityCapabilities = tokens("system", "data-directory", "timezone", "temporary-space", "restic", "mysql-backup", "mysql-restore", "postgres-backup", "postgres-restore")
	severities                = tokens("info", "warning", "blocker", "critical")
	recordTypes               = tokens("run", "operation")
	runKinds                  = tokens("restic", "rsync")
	failureStatuses           = tokens("failed", "partial", "cleanup_required")
	objectTypes               = tokens("task", "repository", "agent", "notification", "restore_verification", "plan", "maintenance", "application")
	operationKinds            = tokens(
		"agent_deploy", "agent_uninstall", "backup", "control_plane_import", "database_restore", "directory_restore",
		"repository_capacity_probe", "repository_connect", "repository_initialize", "repository_maintenance", "repository_password_rotation",
		"repository_verify_existing", "restic_install", "restore_verification", "restore_verification_cleanup", "sync",
	)
	alertKinds = tokens(
		"agent_offline", "notification_channel", "repository_abnormal", "repository_capacity_forecast", "repository_capacity_low",
		"repository_capacity_probe_failed", "repository_capacity_stale", "restore_verification_cleanup", "restore_verification_result",
		"restore_verification_schedule", "restore_verification_stale", "task_run", "task_stale",
	)
)
