package controlplane

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestRecoveryBundleEncryptsSecretsAndAuthenticatesManifest(t *testing.T) {
	manifest, protected := validRecoveryData()
	encoded, err := SealBundle(manifest, protected, SealOptions{
		Passphrase:               "correct horse battery staple",
		CreatedAt:                time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		SourceApplicationVersion: "1.2.3",
		KDF:                      testKDF(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("repository-password-value")) || bytes.Contains(encoded, []byte("PRIVATE KEY")) {
		t.Fatalf("bundle leaked protected material: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("known.example ssh-ed25519 AAAA-pinned")) {
		t.Fatal("clear manifest did not retain pinned host key")
	}

	opened, err := OpenBundle(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Header.FormatVersion != BundleFormatVersion || opened.Header.SourceApplicationVersion != "1.2.3" {
		t.Fatalf("unexpected header: %+v", opened.Header)
	}
	if got := string(opened.Protected.Secrets[1].Value); got != "repository-password-value" {
		t.Fatalf("repository password = %q", got)
	}
	if opened.Header.ResourceCounts["repositories"] != 1 || opened.Header.ManifestSHA256 == "" || opened.Header.EncryptedPayloadSHA256 == "" {
		t.Fatalf("missing recovery metadata: %+v", opened.Header)
	}
	if len(opened.Header.ExcludedTransientClasses) == 0 {
		t.Fatal("excluded transient classes were not declared")
	}
}

func TestRecoveryBundleRejectsWrongPassphraseTamperingVersionAndUnknownFields(t *testing.T) {
	manifest, protected := validRecoveryData()
	encoded, err := SealBundle(manifest, protected, SealOptions{
		Passphrase:               "correct horse battery staple",
		CreatedAt:                time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		SourceApplicationVersion: "1.2.3",
		KDF:                      testKDF(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBundle(encoded, "definitely the wrong passphrase"); err == nil {
		t.Fatal("wrong passphrase was accepted")
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	manifestValue := document["manifest"].(map[string]any)
	manifestValue["repositories"].([]any)[0].(map[string]any)["path"] = "/changed"
	changedManifest, _ := json.Marshal(document)
	if _, err := OpenBundle(changedManifest, "correct horse battery staple"); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("changed manifest error = %v", err)
	}

	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	cipher := document["cipher"].(map[string]any)
	rawCiphertext, _ := base64.StdEncoding.DecodeString(cipher["ciphertext"].(string))
	rawCiphertext[len(rawCiphertext)-1] ^= 0xff
	cipher["ciphertext"] = base64.StdEncoding.EncodeToString(rawCiphertext)
	changedCiphertext, _ := json.Marshal(document)
	if _, err := OpenBundle(changedCiphertext, "correct horse battery staple"); err == nil || !strings.Contains(err.Error(), "encrypted payload") {
		t.Fatalf("changed ciphertext error = %v", err)
	}

	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["formatVersion"] = float64(BundleFormatVersion + 1)
	unsupported, _ := json.Marshal(document)
	if _, err := OpenBundle(unsupported, "correct horse battery staple"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported version error = %v", err)
	}

	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["unexpected"] = true
	unknown, _ := json.Marshal(document)
	if _, err := OpenBundle(unknown, "correct horse battery staple"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestRecoveryBundleRejectsUnsafeKDFAndInvalidRecoveryData(t *testing.T) {
	manifest, protected := validRecoveryData()
	if _, err := SealBundle(manifest, protected, SealOptions{
		Passphrase: "correct horse battery staple",
		CreatedAt:  time.Now(), SourceApplicationVersion: "1.2.3",
		KDF: KDFWorkFactor{Time: 1, MemoryKiB: maximumKDFMemoryKiB + 1, Threads: 1},
	}); err == nil || !strings.Contains(err.Error(), "KDF") {
		t.Fatalf("unsafe KDF error = %v", err)
	}

	duplicate := manifest
	duplicate.Repositories = append(duplicate.Repositories, duplicate.Repositories[0])
	duplicate.Repositories[1].ID = "repo-2"
	if _, err := SealBundle(duplicate, protected, SealOptions{
		Passphrase: "correct horse battery staple", CreatedAt: time.Now(), SourceApplicationVersion: "1.2.3", KDF: testKDF(),
	}); err == nil || !strings.Contains(err.Error(), "duplicate repository name") {
		t.Fatalf("duplicate name error = %v", err)
	}

	badCA := protected
	badCA.AgentCA = &AgentCAMaterial{CertificatePEM: []byte("not a certificate"), PrivateKeyPEM: []byte("not a key")}
	if _, err := SealBundle(manifest, badCA, SealOptions{
		Passphrase: "correct horse battery staple", CreatedAt: time.Now(), SourceApplicationVersion: "1.2.3", KDF: testKDF(),
	}); err == nil || !strings.Contains(err.Error(), "Agent CA") {
		t.Fatalf("malformed CA error = %v", err)
	}
}

func TestRecoveryManifestHashIsStableAcrossInputOrder(t *testing.T) {
	manifest, protected := validRecoveryData()
	secondHost := manifest.RemoteHosts[0]
	secondHost.ID, secondHost.Name = "host-b", "B host"
	manifest.RemoteHosts = append(manifest.RemoteHosts, secondHost)
	protected.Secrets = append(protected.Secrets, ProtectedSecret{ResourceType: "remote_host", ResourceID: "host-b", Field: "private_key", Purpose: "ssh-private-key", Value: []byte("second-private-key")})

	first, err := SealBundle(manifest, protected, SealOptions{Passphrase: "correct horse battery staple", CreatedAt: time.Now(), SourceApplicationVersion: "1.2.3", KDF: testKDF()})
	if err != nil {
		t.Fatal(err)
	}
	manifest.RemoteHosts[0], manifest.RemoteHosts[1] = manifest.RemoteHosts[1], manifest.RemoteHosts[0]
	protected.Secrets[0], protected.Secrets[2] = protected.Secrets[2], protected.Secrets[0]
	second, err := SealBundle(manifest, protected, SealOptions{Passphrase: "correct horse battery staple", CreatedAt: time.Now(), SourceApplicationVersion: "1.2.3", KDF: testKDF()})
	if err != nil {
		t.Fatal(err)
	}
	var firstDocument, secondDocument struct {
		ManifestSHA256 string `json:"manifestSha256"`
	}
	_ = json.Unmarshal(first, &firstDocument)
	_ = json.Unmarshal(second, &secondDocument)
	if firstDocument.ManifestSHA256 != secondDocument.ManifestSHA256 {
		t.Fatalf("manifest hash changed with input order: %s != %s", firstDocument.ManifestSHA256, secondDocument.ManifestSHA256)
	}
}

func validRecoveryData() (Manifest, ProtectedPayload) {
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	manifest := Manifest{
		RemoteHosts:         []domain.RemoteHost{{ID: "host-a", Name: "A host", Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "known.example ssh-ed25519 AAAA-pinned", CreatedAt: now, UpdatedAt: now}},
		Repositories:        []domain.Repository{{ID: "repo-a", Name: "A repository", Engine: domain.ResticEngine, Kind: domain.SFTPRepository, RemoteHostID: "host-a", Path: "/srv/restic", Status: "ready", CreatedAt: now, UpdatedAt: now}},
		DatabaseConnections: []domain.DatabaseConnection{}, Tasks: []domain.Task{}, Plans: []domain.Plan{}, MaintenancePolicies: []domain.MaintenancePolicy{},
		ScheduleWatermarks: []ScheduleWatermark{}, Agents: []AgentIdentity{}, Audits: []AuditEntry{},
	}
	protected := ProtectedPayload{Secrets: []ProtectedSecret{
		{ResourceType: "remote_host", ResourceID: "host-a", Field: "private_key", Purpose: "ssh-private-key", Value: []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----")},
		{ResourceType: "repository", ResourceID: "repo-a", Field: "password", Purpose: "repository-password", Value: []byte("repository-password-value")},
	}}
	return manifest, protected
}

func testKDF() KDFWorkFactor {
	return KDFWorkFactor{Time: 1, MemoryKiB: minimumKDFMemoryKiB, Threads: 1}
}
