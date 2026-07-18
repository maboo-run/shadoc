package diagnostics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/store"
)

type fakeSource struct {
	snapshot     store.DiagnosticSnapshot
	failureLimit int
	alertLimit   int
	now          time.Time
}

func (s *fakeSource) DiagnosticSnapshot(_ context.Context, now time.Time, failureLimit, alertLimit int) (store.DiagnosticSnapshot, error) {
	s.now, s.failureLimit, s.alertLimit = now, failureLimit, alertLimit
	return s.snapshot, nil
}

func TestGenerateBoundsEverySectionAndDropsNonAllowlistedMaterial(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
	secrets := []string{
		"vault-secret-value",
		"backup-user",
		"private.example.internal",
		"/srv/private/photos",
		"https://ntfy.example/private-topic",
		"repository-password-secret-id",
	}
	compatibility := compat.Report{Blocked: true}
	for index := 0; index < MaxCompatibilityFindings+12; index++ {
		compatibility.Findings = append(compatibility.Findings, compat.Finding{
			Capability: "restic", Tool: secrets[1], Path: secrets[3], Version: secrets[0], Severity: compat.Warning,
			Message: strings.Repeat(secrets[4], 1000),
		})
	}
	failures := make([]store.DiagnosticFailure, MaxRecentFailures+15)
	for index := range failures {
		failures[index] = store.DiagnosticFailure{
			RecordType: "operation", Kind: "repository_capacity_probe", Status: "failed",
			OccurredAt: now.Add(-time.Duration(index) * time.Minute), AttemptCount: index + 1,
		}
	}
	failures[0].Kind = secrets[0]
	alerts := make([]store.DiagnosticAlert, MaxActiveAlerts+15)
	for index := range alerts {
		alerts[index] = store.DiagnosticAlert{
			Kind: "repository_capacity_stale", Severity: "warning", ObjectType: "repository",
			FirstAt: now.Add(-time.Hour), LastAt: now, OccurrenceCount: index + 1,
		}
	}
	alerts[0].Kind = secrets[2]
	source := &fakeSource{snapshot: store.DiagnosticSnapshot{
		Resources: store.DiagnosticResourceCounts{
			RemoteHosts:         2,
			Repositories:        store.DiagnosticRepositoryCounts{Total: 3, Ready: 2, SFTP: 2, Local: 1},
			DatabaseConnections: store.DiagnosticDatabaseCounts{Total: 2, Ready: 1, Draft: 1, MySQL: 1, PostgreSQL: 1},
			Tasks:               store.DiagnosticTaskCounts{Total: 4, Enabled: 3, Restic: 3, Rsync: 1},
			Plans:               store.DiagnosticPlanCounts{Total: 2, Enabled: 1},
			Agents:              store.DiagnosticAgentCounts{Total: 2, Online: 1, Revoked: 1},
		},
		RecentFailures: failures, FailuresTruncated: true,
		ActiveAlerts: alerts, AlertsTruncated: true,
		Notifications: store.DiagnosticNotificationState{Configured: true, Enabled: true, Delivered: 7, FailedFinal: 2, LastDeliveryAt: &now},
		Capacity:      store.DiagnosticCapacityState{Repositories: 3, MonitoringEnabled: 2, WithSuccessfulSample: 2, Stale: 1, ProbeFailures: 1, BelowThreshold: 1, ForecastAlerts: 1},
	}}

	result, err := New(source, func() time.Time { return now }).Generate(t.Context(), Request{
		ApplicationVersion: "1.2.3",
		Compatibility:      compatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bytes) > MaxBundleBytes {
		t.Fatalf("bundle bytes=%d limit=%d", len(result.Bytes), MaxBundleBytes)
	}
	for _, secret := range secrets {
		if strings.Contains(string(result.Bytes), secret) {
			t.Fatalf("diagnostic bundle leaked %q", secret)
		}
	}
	if source.failureLimit != MaxRecentFailures || source.alertLimit != MaxActiveAlerts || !source.now.Equal(now) {
		t.Fatalf("source limits=%d/%d now=%s", source.failureLimit, source.alertLimit, source.now)
	}
	if len(result.Bundle.Compatibility.Findings) != MaxCompatibilityFindings || !result.Bundle.Compatibility.Truncated {
		t.Fatalf("compatibility=%+v", result.Bundle.Compatibility)
	}
	if len(result.Bundle.RecentFailures.Items) != MaxRecentFailures || !result.Bundle.RecentFailures.Truncated {
		t.Fatalf("failures=%d truncated=%t", len(result.Bundle.RecentFailures.Items), result.Bundle.RecentFailures.Truncated)
	}
	if result.Bundle.RecentFailures.Items[0].Kind != "other" {
		t.Fatalf("non-allowlisted failure kind=%q", result.Bundle.RecentFailures.Items[0].Kind)
	}
	if len(result.Bundle.ActiveAlerts.Items) != MaxActiveAlerts || !result.Bundle.ActiveAlerts.Truncated || result.Bundle.ActiveAlerts.Items[0].Kind != "other" {
		t.Fatalf("alerts=%+v", result.Bundle.ActiveAlerts)
	}
	if result.Bundle.Limits.MaximumBytes != MaxBundleBytes || result.Counts.RecentFailures != MaxRecentFailures || result.Counts.ActiveAlerts != MaxActiveAlerts {
		t.Fatalf("limits=%+v counts=%+v", result.Bundle.Limits, result.Counts)
	}
}
