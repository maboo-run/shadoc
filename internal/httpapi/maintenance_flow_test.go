package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type maintenanceRepositoryManager struct {
	repositoryManager
	mu     sync.Mutex
	calls  int
	dryRun bool
	policy domain.RetentionPolicy
}

func (m *maintenanceRepositoryManager) Maintain(_ context.Context, _ string, policy domain.RetentionPolicy, dryRun bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.dryRun = dryRun
	m.policy = policy
	return nil
}

func (m *maintenanceRepositoryManager) state() (int, bool, domain.RetentionPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.dryRun, m.policy
}

func TestManualMaintenanceRequiresPreviewConfirmation(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)

	blocked := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{
		"retention": map[string]any{"keepWithinDays": 30},
		"dryRun":    false,
	}, cookie)

	if blocked.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	calls, dryRun, _ := manager.state()
	if calls != 0 {
		t.Fatalf("unsafe maintenance reached repository service: calls=%d dryRun=%v", calls, dryRun)
	}
}

func TestManualMaintenanceAllowsDryRunPreview(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	createReadyMaintenanceRepository(t, srv, "repo-1")

	preview := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{
		"retention": map[string]any{"keepLast": 3},
		"dryRun":    true,
	}, cookie)

	if preview.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", preview.Code, preview.Body.String())
	}
	calls, dryRun, _ := manager.state()
	if calls != 1 || !dryRun {
		t.Fatalf("dry-run preview calls=%d dryRun=%v", calls, dryRun)
	}
	var result store.MaintenancePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil || result.ID == "" || result.Retention.KeepLast != 3 {
		t.Fatalf("preview=%+v err=%v body=%s", result, err, preview.Body.String())
	}
}

func TestConfirmedMaintenanceRejectsChangedPolicyAndRunsExactPreview(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-1")
	now := time.Now().UTC()
	policy := domain.MaintenancePolicy{RepositoryID: "repo-1", Schedule: domain.Schedule{Kind: "weekly", DayOfWeek: 0, TimeOfDay: "03:00"}, Timezone: "Asia/Shanghai", Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, UpdatedAt: now}
	if err := resources.SaveMaintenancePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	preview := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"retention": policy.Retention, "dryRun": true}, cookie)
	var result store.MaintenancePreview
	_ = json.Unmarshal(preview.Body.Bytes(), &result)
	policy.Retention.KeepLast = 4
	policy.UpdatedAt = now.Add(time.Second)
	if err := resources.SaveMaintenancePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	blocked := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"previewId": result.ID, "confirmed": true}, cookie)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("changed policy status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	preview = requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"retention": policy.Retention, "dryRun": true}, cookie)
	_ = json.Unmarshal(preview.Body.Bytes(), &result)
	executed := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"previewId": result.ID, "confirmed": true}, cookie)
	if executed.Code != http.StatusAccepted {
		t.Fatalf("execute status=%d body=%s", executed.Code, executed.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	calls, dryRun, retained := manager.state()
	for calls < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		calls, dryRun, retained = manager.state()
	}
	if calls != 3 || dryRun || retained.KeepLast != 4 {
		t.Fatalf("calls=%d dryRun=%v policy=%+v", calls, dryRun, retained)
	}
}

func TestSavingMaintenancePolicyRequiresMatchingPreviewAndCanDisableSchedule(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-1")
	payload := map[string]any{
		"schedule": map[string]any{"kind": "weekly", "dayOfWeek": 0, "timeOfDay": "03:00"},
		"timezone": "Asia/Shanghai", "retention": map[string]any{"keepLast": 3}, "enabled": false,
	}
	blocked := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo-1/maintenance-policy", payload, cookie)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("unpreviewed save status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	preview := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"retention": map[string]any{"keepLast": 3}, "dryRun": true}, cookie)
	var result store.MaintenancePreview
	_ = json.Unmarshal(preview.Body.Bytes(), &result)
	payload["previewId"] = result.ID
	saved := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo-1/maintenance-policy", payload, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("previewed save status=%d body=%s", saved.Code, saved.Body.String())
	}
	policy, err := resources.MaintenancePolicy(context.Background(), "repo-1")
	if err != nil || policy.Enabled || policy.Retention.KeepLast != 3 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	audits, err := resources.ListAudits(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, audit := range audits {
		actions[audit.Action] = true
	}
	if !actions["maintenance.preview"] || !actions["maintenance.policy.update"] {
		t.Fatalf("maintenance semantic audits missing: actions=%v", actions)
	}
}

func TestMaintenancePolicyDefaultsCatchUpAndReturnsScheduleHealth(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-health")
	ctx := context.Background()

	preview := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-health/maintenance", map[string]any{"retention": map[string]any{"keepLast": 3}, "dryRun": true}, cookie)
	var firstPreview store.MaintenancePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &firstPreview); err != nil || firstPreview.ID == "" {
		t.Fatalf("preview=%+v err=%v body=%s", firstPreview, err, preview.Body.String())
	}
	payload := map[string]any{
		"schedule": map[string]any{"kind": "interval", "intervalHours": 1},
		"timezone": "UTC", "retention": map[string]any{"keepLast": 3}, "enabled": true, "previewId": firstPreview.ID,
	}
	saved := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo-health/maintenance-policy", payload, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("saved=%d body=%s", saved.Code, saved.Body.String())
	}
	policy, err := resources.MaintenancePolicy(ctx, "repo-health")
	if err != nil || policy.CatchUpWindowMinutes != 60 || policy.ScheduleAnchorAt.IsZero() {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	finished := policy.ScheduleAnchorAt.Add(time.Minute)
	occurrence := store.ScheduleOccurrence{ID: "maintenance-health", OwnerKind: "maintenance", OwnerID: policy.RepositoryID, ScheduledAt: policy.ScheduleAnchorAt, ObservedAt: policy.ScheduleAnchorAt, Mode: "missed", Status: "missed", TargetIDs: []string{policy.RepositoryID}, FinishedAt: &finished}
	if created, err := resources.CreateScheduleOccurrence(ctx, occurrence); err != nil || !created {
		t.Fatalf("create occurrence=%v err=%v", created, err)
	}
	read := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo-health/maintenance-policy", nil, cookie)
	var health struct {
		CatchUpWindowMinutes int                           `json:"catchUpWindowMinutes"`
		LastScheduledAt      string                        `json:"lastScheduledAt"`
		NextRun              string                        `json:"nextRun"`
		ScheduleCoverage     store.ScheduleOccurrenceStats `json:"scheduleCoverage"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &health); err != nil || health.CatchUpWindowMinutes != 60 || health.LastScheduledAt != policy.ScheduleAnchorAt.Format(time.RFC3339) || health.NextRun == "" || health.ScheduleCoverage.Total != 1 || health.ScheduleCoverage.Missed != 1 {
		t.Fatalf("health=%+v err=%v body=%s", health, err, read.Body.String())
	}

	preview = requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-health/maintenance", map[string]any{"retention": map[string]any{"keepLast": 3}, "dryRun": true}, cookie)
	var secondPreview store.MaintenancePreview
	_ = json.Unmarshal(preview.Body.Bytes(), &secondPreview)
	payload["previewId"] = secondPreview.ID
	payload["catchUpWindowMinutes"] = 0
	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo-health/maintenance-policy", payload, cookie)
	policy, err = resources.MaintenancePolicy(ctx, "repo-health")
	if updated.Code != http.StatusOK || err != nil || policy.CatchUpWindowMinutes != 0 {
		t.Fatalf("updated=%d policy=%+v err=%v body=%s", updated.Code, policy, err, updated.Body.String())
	}
}

func TestMaintenancePreviewUsesSubmittedRepositoryRetentionAndReturnsPolicyVersion(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &maintenanceRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-1")
	now := time.Now().UTC()
	if err := resources.CreateTask(context.Background(), domain.Task{ID: "task-1", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/srv"}, Retention: domain.RetentionPolicy{KeepLast: 9}, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-1/maintenance", map[string]any{"retention": map[string]any{"keepLast": 1}, "dryRun": true}, cookie)
	var preview store.MaintenancePreview
	_ = json.Unmarshal(response.Body.Bytes(), &preview)
	if response.Code != http.StatusOK || manager.policy.KeepLast != 1 || preview.Retention.KeepLast != 1 || preview.PolicyFingerprint != (domain.RetentionPolicy{KeepLast: 1}).Fingerprint() {
		t.Fatalf("status=%d manager=%+v preview=%+v", response.Code, manager.policy, preview)
	}
	read := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo-1/maintenance-policy", nil, cookie)
	var policy struct {
		Retention         domain.RetentionPolicy `json:"retention"`
		RetentionSource   domain.RetentionSource `json:"retentionSource"`
		PolicyFingerprint string                 `json:"policyFingerprint"`
		BoundTask         map[string]any         `json:"boundTask"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &policy); err != nil || policy.Retention.KeepLast != 9 || policy.RetentionSource != domain.RepositoryRetentionSource || policy.PolicyFingerprint != (domain.RetentionPolicy{KeepLast: 9}).Fingerprint() || policy.BoundTask["id"] != "task-1" {
		t.Fatalf("policy=%+v err=%v body=%s", policy, err, read.Body.String())
	}
}

func TestMaintenancePolicyRemainsRepositoryOwnedWhenTaskPayloadContainsRetention(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-1")
	now := time.Now().UTC()
	stored := domain.MaintenancePolicy{RepositoryID: "repo-1", Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: 0, TimeOfDay: "03:00"}, Timezone: "UTC", Retention: domain.RetentionPolicy{KeepLast: 1}, Enabled: true, UpdatedAt: now}
	if err := resources.SaveMaintenancePolicy(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-1", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/srv"}, Retention: domain.RetentionPolicy{KeepLast: 9}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo-1/maintenance-policy", nil, cookie)
	var policy struct {
		Enabled           bool                   `json:"enabled"`
		RetentionConflict bool                   `json:"retentionConflict"`
		Retention         domain.RetentionPolicy `json:"retention"`
		ReviewedRetention domain.RetentionPolicy `json:"reviewedRetention"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil || response.Code != http.StatusOK || !policy.Enabled || policy.RetentionConflict || policy.Retention.KeepLast != 1 || policy.ReviewedRetention != (domain.RetentionPolicy{}) {
		t.Fatalf("policy=%+v err=%v body=%s", policy, err, response.Body.String())
	}
}

func createReadyMaintenanceRepository(t *testing.T, srv *Server, id string) *store.Store {
	t.Helper()
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	if err := resources.SaveSecret(context.Background(), "secret-"+id, "repository-password", []byte("ciphertext"), now); err != nil {
		t.Fatal(err)
	}
	if err := resources.CreateRepository(context.Background(), domain.Repository{ID: id, Name: id, Kind: domain.LocalRepository, Path: "/backup/" + id, Status: "ready", CreatedAt: now, UpdatedAt: now}, "secret-"+id); err != nil {
		t.Fatal(err)
	}
	return resources
}
