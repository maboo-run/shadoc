package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func createCapacityRepository(t *testing.T, s *Store, id string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	secretID := id + "-password"
	if err := s.SaveSecret(ctx, secretID, "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: id, Name: id, Kind: domain.LocalRepository, Path: "/backup/" + id, Status: "ready", CreatedAt: now, UpdatedAt: now}, secretID); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCapacityPersistenceBackfillsExistingAndDefaultsNewPolicies(t *testing.T) {
	path := t.TempDir() + "/capacity.db"
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createCapacityRepository(t, s, "legacy", now)
	if err := s.SaveRepositoryCapacity(context.Background(), "legacy", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 400, CheckedAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE repository_capacity_samples; DROP TABLE repository_capacity_policies`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	policy, err := s.RepositoryCapacityPolicy(context.Background(), "legacy")
	if err != nil || !policy.Enabled || policy.ProbeIntervalMinutes != 24*60 || policy.MinimumAvailablePercent != 0 || policy.ExhaustionWarningDays != 0 {
		t.Fatalf("backfilled policy=%+v err=%v", policy, err)
	}
	samples, err := s.ListRepositoryCapacitySamples(context.Background(), "legacy", 10)
	if err != nil || len(samples) != 1 || samples[0].AvailableBytes != 400 {
		t.Fatalf("backfilled samples=%+v err=%v", samples, err)
	}

	createCapacityRepository(t, s, "new", now.Add(2*time.Hour))
	created, err := s.RepositoryCapacityPolicy(context.Background(), "new")
	if err != nil || !created.Enabled || created.NextProbeAt == nil || !created.NextProbeAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("new policy=%+v err=%v", created, err)
	}
}

func TestRepositoryCapacitySamplesProduceBoundedForecastAndSuccessMetadata(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	createCapacityRepository(t, s, "repo", start)
	for index, available := range []uint64{900, 800, 700} {
		checked := start.Add(time.Duration(index) * 12 * time.Hour)
		if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: available, CheckedAt: checked, SourceAgentID: "agent-a"}); err != nil {
			t.Fatal(err)
		}
	}

	samples, err := s.ListRepositoryCapacitySamples(ctx, "repo", 2)
	if err != nil || len(samples) != 2 || samples[0].AvailableBytes != 700 || samples[0].UsedBytes != 300 || samples[1].AvailableBytes != 800 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	forecast, err := s.RepositoryCapacityForecast(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if forecast.Status != domain.CapacityForecastReady || forecast.SampleCount != 3 || forecast.ObservationStartedAt == nil || forecast.ObservationEndedAt == nil || !forecast.ObservationStartedAt.Equal(start) || !forecast.ObservationEndedAt.Equal(start.Add(24*time.Hour)) || forecast.GrowthBytesPerDay != 200 || forecast.EstimatedExhaustionAt == nil || !forecast.EstimatedExhaustionAt.Equal(start.Add(24*time.Hour+84*time.Hour)) {
		t.Fatalf("forecast=%+v", forecast)
	}
	policy, err := s.RepositoryCapacityPolicy(ctx, "repo")
	last := start.Add(24 * time.Hour)
	if err != nil || policy.LastAttemptAt == nil || policy.LastSuccessAt == nil || !policy.LastAttemptAt.Equal(last) || !policy.LastSuccessAt.Equal(last) || policy.LastError != "" || policy.NextProbeAt == nil || !policy.NextProbeAt.Equal(last.Add(24*time.Hour)) {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}

func TestRepositoryCapacityPolicyUpdatePreservesProbeMetadataAndDefinesDisabledSemantics(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	checked := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	createCapacityRepository(t, s, "repo", checked.Add(-time.Hour))
	if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 500, CheckedAt: checked}); err != nil {
		t.Fatal(err)
	}
	updatedAt := checked.Add(time.Hour)
	policy := domain.RepositoryCapacityPolicy{
		RepositoryID: "repo", Enabled: false, ProbeIntervalMinutes: 60,
		MinimumAvailableBytes: 128, MinimumAvailablePercent: 7.5, ExhaustionWarningDays: 14,
	}
	if err := s.SaveRepositoryCapacityPolicy(ctx, policy, updatedAt); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.RepositoryCapacityPolicy(ctx, "repo")
	if err != nil || loaded.Enabled || loaded.NextProbeAt != nil || loaded.LastSuccessAt == nil || !loaded.LastSuccessAt.Equal(checked) || loaded.MinimumAvailableBytes != 128 || loaded.MinimumAvailablePercent != 7.5 || loaded.ExhaustionWarningDays != 14 || !loaded.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("disabled policy=%+v err=%v", loaded, err)
	}

	policy.Enabled = true
	reenabledAt := updatedAt.Add(time.Hour)
	if err := s.SaveRepositoryCapacityPolicy(ctx, policy, reenabledAt); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.RepositoryCapacityPolicy(ctx, "repo")
	if err != nil || !loaded.Enabled || loaded.NextProbeAt == nil || !loaded.NextProbeAt.Equal(reenabledAt) {
		t.Fatalf("reenabled policy=%+v err=%v", loaded, err)
	}
}

func TestRepositoryCapacityForecastRequiresEnoughHistoryAndPositiveGrowth(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		offsets    []time.Duration
		available  []uint64
		wantStatus string
	}{
		{name: "samples", offsets: []time.Duration{0, 48 * time.Hour}, available: []uint64{900, 800}, wantStatus: domain.CapacityForecastInsufficientSamples},
		{name: "span", offsets: []time.Duration{0, 10 * time.Hour, 23 * time.Hour}, available: []uint64{900, 850, 800}, wantStatus: domain.CapacityForecastInsufficientSpan},
		{name: "growth", offsets: []time.Duration{0, 12 * time.Hour, 24 * time.Hour}, available: []uint64{700, 800, 900}, wantStatus: domain.CapacityForecastNonPositiveGrowth},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			start := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
			createCapacityRepository(t, s, "repo", start)
			for index, offset := range fixture.offsets {
				if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: fixture.available[index], CheckedAt: start.Add(offset)}); err != nil {
					t.Fatal(err)
				}
			}
			forecast, err := s.RepositoryCapacityForecast(ctx, "repo")
			if err != nil || forecast.Status != fixture.wantStatus || forecast.EstimatedExhaustionAt != nil {
				t.Fatalf("forecast=%+v err=%v", forecast, err)
			}
		})
	}
}

func TestRepositoryCapacityFailurePreservesLastGoodMeasurement(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	checked := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	createCapacityRepository(t, s, "repo", checked.Add(-time.Hour))
	if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 350, CheckedAt: checked}); err != nil {
		t.Fatal(err)
	}
	failedAt := checked.Add(6 * time.Hour)
	longError := strings.Repeat("failure\x00", 100)
	if err := s.RecordRepositoryCapacityFailure(ctx, "repo", failedAt, longError); err != nil {
		t.Fatal(err)
	}

	policy, err := s.RepositoryCapacityPolicy(ctx, "repo")
	if err != nil || policy.LastAttemptAt == nil || !policy.LastAttemptAt.Equal(failedAt) || policy.LastSuccessAt == nil || !policy.LastSuccessAt.Equal(checked) || policy.LastError == "" || len(policy.LastError) > 512 || strings.ContainsRune(policy.LastError, '\x00') {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	repositories, err := s.ListRepositories(ctx)
	if err != nil || len(repositories) != 1 || repositories[0].Capacity == nil || repositories[0].Capacity.AvailableBytes != 350 || !repositories[0].Capacity.CheckedAt.Equal(checked) {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	samples, err := s.ListRepositoryCapacitySamples(ctx, "repo", 10)
	if err != nil || len(samples) != 1 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
}

func TestRepositoryCapacityDuePoliciesAreClaimedAtomicallyAndBecomeRetryable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	createCapacityRepository(t, s, "repo", now)

	claims, err := s.ClaimDueRepositoryCapacityPolicies(ctx, now, 2, 10*time.Minute)
	if err != nil || len(claims) != 1 || claims[0].RepositoryID != "repo" || claims[0].Token == "" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	policy, err := s.RepositoryCapacityPolicy(ctx, "repo")
	if err != nil || policy.LastAttemptAt == nil || !policy.LastAttemptAt.Equal(now) {
		t.Fatalf("claimed policy=%+v err=%v", policy, err)
	}
	duplicate, err := s.ClaimDueRepositoryCapacityPolicies(ctx, now.Add(time.Minute), 2, 10*time.Minute)
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate claims=%+v err=%v", duplicate, err)
	}

	retryAt := now.Add(11 * time.Minute)
	retried, err := s.ClaimDueRepositoryCapacityPolicies(ctx, retryAt, 1, 10*time.Minute)
	if err != nil || len(retried) != 1 || retried[0].RepositoryID != "repo" || retried[0].Token == claims[0].Token {
		t.Fatalf("retried claims=%+v err=%v", retried, err)
	}
	if err := s.RecordRepositoryCapacityFailure(ctx, "repo", retryAt.Add(time.Minute), "probe unavailable"); err != nil {
		t.Fatal(err)
	}
	early, err := s.ClaimDueRepositoryCapacityPolicies(ctx, retryAt.Add(2*time.Minute), 1, 10*time.Minute)
	if err != nil || len(early) != 0 {
		t.Fatalf("early retry=%+v err=%v", early, err)
	}
	due, err := s.ClaimDueRepositoryCapacityPolicies(ctx, retryAt.Add(25*time.Hour), 1, 10*time.Minute)
	if err != nil || len(due) != 1 {
		t.Fatalf("due retry=%+v err=%v", due, err)
	}
}
