package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/s3backend"
)

func TestS3RepositoryRoundTripsStructuredBackendAndDeletesBothSecrets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "repository-password", "repository-password", []byte("cipher-password"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, "s3-credentials", s3backend.CredentialPurpose, []byte("cipher-credentials"), now); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{
		ID: "repo-s3", Name: "object archive", Kind: domain.S3Repository, Path: "photos/primary", Status: "ready",
		S3:              &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "eu-west-1", PathStyle: true},
		BackendSecretID: "s3-credentials", CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, repository, "repository-password"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListRepositories(ctx)
	if err != nil || len(items) != 1 || items[0].S3 == nil || !items[0].S3.CredentialsConfigured || items[0].S3.Endpoint != "https://objects.example.com" {
		t.Fatalf("repositories=%+v err=%v", items, err)
	}
	execution, err := s.LoadRepositoryExecution(ctx, repository.ID)
	if err != nil || execution.Repository.BackendSecretID != "s3-credentials" || execution.Repository.S3 == nil {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	policy, err := s.RepositoryCapacityPolicy(ctx, repository.ID)
	if err != nil || policy.Enabled || policy.NextProbeAt != nil {
		t.Fatalf("S3 capacity policy=%+v err=%v", policy, err)
	}
	preview, err := s.ResourceDeletePreview(ctx, "repositories", repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := s.DeleteResourceVersioned(ctx, "repositories", repository.ID, preview.UpdatedAt)
	if err != nil || !slices.Equal(secrets, []string{"repository-password", "s3-credentials"}) {
		t.Fatalf("deleted secret references=%v err=%v", secrets, err)
	}
}

func TestS3RepositoryLocationIsUniqueWithoutComparingCredentials(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"password-a", "password-b"} {
		if err := s.SaveSecret(ctx, id, "repository-password", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"credentials-a", "credentials-b"} {
		if err := s.SaveSecret(ctx, id, s3backend.CredentialPurpose, []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	base := domain.Repository{Kind: domain.S3Repository, Path: "photos", S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}, Status: "ready", CreatedAt: now, UpdatedAt: now}
	first := base
	first.ID, first.Name, first.BackendSecretID = "repo-a", "archive a", "credentials-a"
	if err := s.CreateRepository(ctx, first, "password-a"); err != nil {
		t.Fatal(err)
	}
	second := base
	second.ID, second.Name, second.BackendSecretID = "repo-b", "archive b", "credentials-b"
	if err := s.CreateRepository(ctx, second, "password-b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate S3 location error=%v", err)
	}
}
