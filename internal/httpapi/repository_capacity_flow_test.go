package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/store"
)

type fakeCapacityService struct {
	repositoryID string
	failure      error
}

func (s *fakeCapacityService) Probe(_ context.Context, repositoryID string, report repositorycapacity.StageReporter) (domain.RepositoryCapacity, error) {
	s.repositoryID = repositoryID
	report("waiting_for_agent_capacity")
	if s.failure != nil {
		return domain.RepositoryCapacity{}, s.failure
	}
	return domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, CheckedAt: time.Now()}, nil
}

func TestRepositoryCapacityProbeReturnsTrackedOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	capacity := &fakeCapacityService{}
	srv.repositoryCapacity = capacity
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/capacity", map[string]any{}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &accepted)
	if response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "repository_capacity_probe" || operation.RepositoryID != "repo" || capacity.repositoryID != "repo" {
		t.Fatalf("operation=%+v probed=%q", operation, capacity.repositoryID)
	}
}

func TestRepositoryCapacityPolicyHistoryAndForecastRequireAuthentication(t *testing.T) {
	srv := newResourceTestServer(t)
	storage := srv.store.(*store.Store)
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	createRepositoryForCapacityAPI(t, storage, "repo", now)
	for index, available := range []uint64{900, 800, 700} {
		if err := storage.SaveRepositoryCapacity(t.Context(), "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: available, CheckedAt: now.Add(time.Duration(index) * 12 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"/api/repositories/repo/capacity-policy",
		"/api/repositories/repo/capacity-samples?limit=2",
		"/api/repositories/repo/capacity-forecast",
	} {
		response := requestJSON(t, srv, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s status=%d", path, response.Code)
		}
	}

	cookie := setupSession(t, srv)
	policyResponse := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/capacity-policy", nil, cookie)
	var policy domain.RepositoryCapacityPolicy
	if policyResponse.Code != http.StatusOK || json.Unmarshal(policyResponse.Body.Bytes(), &policy) != nil || policy.RepositoryID != "repo" || !policy.Enabled {
		t.Fatalf("policy status=%d body=%s", policyResponse.Code, policyResponse.Body.String())
	}
	samplesResponse := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/capacity-samples?limit=2", nil, cookie)
	var samples []domain.RepositoryCapacitySample
	if samplesResponse.Code != http.StatusOK || json.Unmarshal(samplesResponse.Body.Bytes(), &samples) != nil || len(samples) != 2 || samples[0].AvailableBytes != 700 {
		t.Fatalf("samples status=%d body=%s", samplesResponse.Code, samplesResponse.Body.String())
	}
	forecastResponse := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/capacity-forecast", nil, cookie)
	var forecast domain.RepositoryCapacityForecast
	if forecastResponse.Code != http.StatusOK || json.Unmarshal(forecastResponse.Body.Bytes(), &forecast) != nil || forecast.Status != domain.CapacityForecastReady || forecast.EstimatedExhaustionAt == nil {
		t.Fatalf("forecast status=%d body=%s", forecastResponse.Code, forecastResponse.Body.String())
	}
}

func TestRepositoryListIncludesDurableCapacityPolicyHealth(t *testing.T) {
	srv := newResourceTestServer(t)
	storage := srv.store.(*store.Store)
	now := time.Now().UTC()
	checkedAt := now.Add(-48 * time.Hour)
	createRepositoryForCapacityAPI(t, storage, "repo", checkedAt)
	if err := storage.SaveRepositoryCapacity(t.Context(), "repo", domain.RepositoryCapacity{
		TotalBytes: 1000, AvailableBytes: 400, CheckedAt: checkedAt, SourceAgentID: "agent-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordRepositoryCapacityFailure(t.Context(), "repo", checkedAt.Add(time.Hour), "capacity endpoint unavailable"); err != nil {
		t.Fatal(err)
	}
	cookie := setupSession(t, srv)

	type policyView struct {
		RepositoryID  string     `json:"repositoryId"`
		NextProbeAt   *time.Time `json:"nextProbeAt"`
		LastSuccessAt *time.Time `json:"lastSuccessAt"`
		LastError     string     `json:"lastError"`
		Stale         bool       `json:"stale"`
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/repositories", nil, cookie)
	var items []struct {
		ID             string      `json:"id"`
		CapacityPolicy *policyView `json:"capacityPolicy"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 1 {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	policy := items[0].CapacityPolicy
	if items[0].ID != "repo" || policy == nil || policy.RepositoryID != "repo" || policy.NextProbeAt == nil || policy.LastSuccessAt == nil || policy.LastError != "capacity endpoint unavailable" || !policy.Stale {
		t.Fatalf("repository=%+v", items[0])
	}

	policyResponse := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/capacity-policy", nil, cookie)
	var detail policyView
	if policyResponse.Code != http.StatusOK || json.Unmarshal(policyResponse.Body.Bytes(), &detail) != nil || !detail.Stale {
		t.Fatalf("policy status=%d body=%s", policyResponse.Code, policyResponse.Body.String())
	}
}

func TestRepositoryCapacityPolicySaveRequiresCSRFAndValidatesBoundaries(t *testing.T) {
	srv := newResourceTestServer(t)
	storage := srv.store.(*store.Store)
	createRepositoryForCapacityAPI(t, storage, "repo", time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC))
	cookie := setupSession(t, srv)
	payload := map[string]any{
		"enabled": true, "probeIntervalMinutes": 90, "minimumAvailableBytes": 1024,
		"minimumAvailablePercent": 12.5, "exhaustionWarningDays": 45,
	}
	withoutCSRF := *cookie
	withoutCSRF.Raw = ""
	forbidden := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo/capacity-policy", payload, &withoutCSRF)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	invalid := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo/capacity-policy", map[string]any{
		"enabled": true, "probeIntervalMinutes": 5, "minimumAvailableBytes": 0,
		"minimumAvailablePercent": 10, "exhaustionWarningDays": 30,
	}, cookie)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	response := requestJSON(t, srv, http.MethodPut, "/api/repositories/repo/capacity-policy", payload, cookie)
	var saved domain.RepositoryCapacityPolicy
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved.ProbeIntervalMinutes != 90 || saved.MinimumAvailableBytes != 1024 || saved.MinimumAvailablePercent != 12.5 || saved.ExhaustionWarningDays != 45 {
		t.Fatalf("saved status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManualRepositoryCapacityProbePersistsFailureMetadata(t *testing.T) {
	srv := newResourceTestServer(t)
	storage := srv.store.(*store.Store)
	createRepositoryForCapacityAPI(t, storage, "repo", time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC))
	srv.repositoryCapacity = &fakeCapacityService{failure: errors.New("capacity endpoint unavailable")}
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/capacity", map[string]any{}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &accepted)
	if response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	waitForOperation(t, srv, cookie, accepted.OperationID, "failed")
	policy, err := storage.RepositoryCapacityPolicy(t.Context(), "repo")
	if err != nil || policy.LastError != "capacity endpoint unavailable" || policy.LastAttemptAt == nil {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}

func createRepositoryForCapacityAPI(t *testing.T, storage *store.Store, id string, now time.Time) {
	t.Helper()
	secretID := id + "-password"
	if err := storage.SaveSecret(t.Context(), secretID, "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(t.Context(), domain.Repository{ID: id, Name: id, Kind: domain.LocalRepository, Path: "/backup/" + id, Status: "ready", CreatedAt: now, UpdatedAt: now}, secretID); err != nil {
		t.Fatal(err)
	}
}
