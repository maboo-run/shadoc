package controlplane

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestNotificationChannelsRoundTripWithProtectedSecretsAndConservativeActivation(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	source, err := store.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	crypt, err := vault.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sourceSecrets := secret.New(source, crypt, func() time.Time { return now })
	webhookSecretID, err := sourceSecrets.Put(t.Context(), notificationconfig.WebhookSecretPurpose, []byte("webhook-recovery-private"))
	if err != nil {
		t.Fatal(err)
	}
	emailSecretID, err := sourceSecrets.Put(t.Context(), notificationconfig.EmailPasswordPurpose, []byte("smtp-recovery-private"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	webhookConfig, _ := json.Marshal(notificationconfig.Webhook{Endpoint: "https://hooks.example.com/backup", AuthMode: notificationconfig.WebhookHMACSHA256, SecretID: webhookSecretID, Enabled: &enabled})
	emailConfig, _ := json.Marshal(notificationconfig.Email{Host: "smtp.example.com", Port: 587, TLSMode: notificationconfig.EmailSTARTTLS, From: "backup@example.com", To: []string{"ops@example.com"}, Username: "backup", PasswordSecretID: emailSecretID, Enabled: &enabled})
	if err := source.SetMetadata(t.Context(), notificationconfig.WebhookMetadataKey, string(webhookConfig)); err != nil {
		t.Fatal(err)
	}
	if err := source.SetMetadata(t.Context(), notificationconfig.EmailMetadataKey, string(emailConfig)); err != nil {
		t.Fatal(err)
	}
	exporter := NewService(source, sourceSecrets, nil, "source-version", func() time.Time { return now })
	exporter.kdf = testKDF()
	bundle, err := exporter.Export(t.Context(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bundle, []byte("webhook-recovery-private")) || bytes.Contains(bundle, []byte("smtp-recovery-private")) || bytes.Contains(bundle, []byte(webhookSecretID)) || bytes.Contains(bundle, []byte(emailSecretID)) {
		t.Fatal("recovery document exposed notification secret material or local secret ids")
	}
	opened, err := OpenBundle(bundle, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Manifest.Webhook == nil || !opened.Manifest.Webhook.HasSecret || opened.Manifest.Email != nil || len(opened.Protected.Secrets) != 1 {
		t.Fatalf("opened bundle=%+v", opened)
	}

	target, targetSecrets := openRecoveryTarget(t, now)
	importer := NewService(target, targetSecrets, nil, "target-version", func() time.Time { return now })
	importer.kdf = testKDF()
	preview, err := importer.PreflightImport(t.Context(), bundle, "correct horse battery staple")
	if err != nil || !preview.CanImport {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := importer.Import(t.Context(), bundle, "correct horse battery staple", preview.PreviewID); err != nil {
		t.Fatal(err)
	}
	storedWebhookJSON, err := target.Metadata(t.Context(), notificationconfig.WebhookMetadataKey)
	if err != nil {
		t.Fatal(err)
	}
	var storedWebhook notificationconfig.Webhook
	if json.Unmarshal([]byte(storedWebhookJSON), &storedWebhook) != nil {
		t.Fatal("imported notification configuration is not valid JSON")
	}
	if storedWebhook.EnabledValue() || storedWebhook.SecretID == webhookSecretID {
		t.Fatalf("webhook=%+v", storedWebhook)
	}
	webhookValue, err := targetSecrets.Get(t.Context(), storedWebhook.SecretID, notificationconfig.WebhookSecretPurpose)
	if err != nil || string(webhookValue) != "webhook-recovery-private" {
		t.Fatalf("webhook secret=%q err=%v", webhookValue, err)
	}
	if _, err := target.Metadata(t.Context(), notificationconfig.EmailMetadataKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed email configuration was imported: %v", err)
	}
}
