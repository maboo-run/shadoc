package s3backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestCredentialsArePurposeBoundAndMaterialUsesOnlyStructuredFields(t *testing.T) {
	encoded, err := EncodeCredentials(Credentials{AccessKey: "access-id", SecretKey: "secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err := json.Unmarshal(encoded, &raw); err != nil || raw["accessKey"] != "access-id" || raw["secretKey"] != "secret-value" {
		t.Fatalf("encoded credentials=%q raw=%v err=%v", encoded, raw, err)
	}
	credentials, err := DecodeCredentials(encoded)
	if err != nil {
		t.Fatal(err)
	}
	material, err := Material(domain.Repository{
		Name: "archive", Kind: domain.S3Repository, Path: "photos/2026",
		S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "eu-west-1", PathStyle: true},
	}, "repository-password", credentials)
	if err != nil {
		t.Fatal(err)
	}
	if material.Location != "s3:https://objects.example.com/backup-prod/photos/2026" || material.S3AccessKey != "access-id" || material.S3SecretKey != "secret-value" || material.S3Region != "eu-west-1" || material.S3BucketLookup != "path" {
		t.Fatalf("material=%+v", material)
	}
	if strings.Contains(material.Location, credentials.AccessKey) || strings.Contains(material.Location, credentials.SecretKey) {
		t.Fatalf("S3 location leaked credentials: %s", material.Location)
	}
}

func TestCredentialsRejectControlCharactersAndIncompletePairs(t *testing.T) {
	for _, credentials := range []Credentials{{AccessKey: "only-access"}, {SecretKey: "only-secret"}, {AccessKey: "bad\nkey", SecretKey: "secret"}} {
		if _, err := EncodeCredentials(credentials); err == nil {
			t.Fatalf("unsafe credentials accepted: %+v", credentials)
		}
	}
	if _, err := DecodeCredentials([]byte(`{"accessKey":"access"}`)); err == nil {
		t.Fatal("incomplete persisted credentials accepted")
	}
}
