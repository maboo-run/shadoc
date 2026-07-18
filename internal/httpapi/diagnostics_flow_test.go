package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/diagnostics"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestDiagnosticExportIsAuthenticatedBoundedRedactedAndAudited(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.applicationVersion = "1.2.3"
	storage := srv.store.(*store.Store)
	now := time.Now().UTC().Add(-time.Hour)
	secrets := []string{
		"vault-secret-value",
		"backup-user",
		"private.example.internal",
		"/srv/private/photos",
		"https://ntfy.example/private",
		"private-notification-topic",
		"repository-password-secret-id",
		"operation-private-detail",
		"https://webhook.private.example/alerts",
		"smtp.private.example",
	}
	if err := storage.SaveSecret(t.Context(), "ssh-secret", "ssh-private-key", []byte(secrets[0]), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveSecret(t.Context(), secrets[6], "repository-password", []byte(secrets[0]), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRemoteHost(t.Context(), domain.RemoteHost{
		ID: "host-private", Name: "private host", Host: secrets[2], Port: 22, Username: secrets[1],
		HostFingerprint: "SHA256:private", CreatedAt: now, UpdatedAt: now,
	}, "ssh-secret"); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(t.Context(), domain.Repository{
		ID: "repo-private", Name: "private repository", Kind: domain.SFTPRepository, RemoteHostID: "host-private",
		Path: secrets[3], Status: "ready", CreatedAt: now, UpdatedAt: now,
	}, secrets[6]); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateTask(t.Context(), domain.Task{
		ID: "task-private", Name: "private task", Kind: domain.DirectoryTask, RepositoryID: "repo-private",
		Directory: &domain.DirectorySource{Path: secrets[3]}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.StartRun(t.Context(), store.RunRecord{ID: "run-private", TaskID: "task-private", Trigger: "manual", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	oversizedLog := strings.Repeat("raw-log-"+secrets[0]+"-", 20_000)
	if err := storage.FinishRun(t.Context(), "run-private", "failed", now.Add(time.Minute), 2, "", map[string]any{"error": secrets[7], "path": secrets[3]}, oversizedLog); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < diagnostics.MaxRecentFailures+5; index++ {
		if err := storage.CreateOperation(t.Context(), store.OperationRecord{
			ID: fmt.Sprintf("operation-private-%03d", index), Kind: "repository_capacity_probe", Actor: secrets[1],
			RepositoryID: "repo-private", Status: "failed", Stage: "failed", CreatedAt: now.Add(time.Duration(index) * time.Second),
			AttemptCount: 1, ErrorSummary: secrets[7], Detail: map[string]any{"path": secrets[3], "url": secrets[4]},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ntfyConfig, _ := json.Marshal(map[string]any{"baseUrl": secrets[4], "topic": secrets[5], "tokenSecretId": "ssh-secret", "enabled": true})
	if err := storage.SetMetadata(t.Context(), "ntfy.config", string(ntfyConfig)); err != nil {
		t.Fatal(err)
	}
	webhookConfig, _ := json.Marshal(map[string]any{"endpoint": secrets[8], "authMode": "none", "enabled": true})
	if err := storage.SetMetadata(t.Context(), "webhook.config", string(webhookConfig)); err != nil {
		t.Fatal(err)
	}
	emailConfig, _ := json.Marshal(map[string]any{"host": secrets[9], "port": 587, "tlsMode": "starttls", "from": "private@example.com", "to": []string{"ops@example.com"}, "enabled": false})
	if err := storage.SetMetadata(t.Context(), "email.config", string(emailConfig)); err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordNotificationDelivery(t.Context(), store.NotificationDelivery{
		NotificationID: "notification-private", OccurredAt: now, Channel: "ntfy", StateKey: "task:private:run",
		Transition: "raised", Attempt: 1, MaxAttempts: 1, Status: store.DeliveryFinalFailure, ErrorSummary: secrets[4],
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < diagnostics.MaxActiveAlerts+5; index++ {
		if _, _, err := storage.RaiseAlert(t.Context(), store.AlertSignal{
			StateKey: fmt.Sprintf("task:private-%03d:run", index), Kind: "task_run", Severity: store.AlertWarning,
			ObjectType: "task", ObjectID: fmt.Sprintf("private-%03d", index), ObjectName: secrets[3], Reason: secrets[7],
			Message: secrets[0], TargetPage: "运行记录", RecoveryCondition: secrets[2],
		}, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.SaveRepositoryCapacity(t.Context(), "repo-private", domain.RepositoryCapacity{
		TotalBytes: 1000, AvailableBytes: 50, CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordRepositoryCapacityFailure(t.Context(), "repo-private", now.Add(time.Minute), secrets[7]); err != nil {
		t.Fatal(err)
	}

	unauthenticated := requestJSON(t, srv, http.MethodGet, "/api/diagnostics/export", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodGet, "/api/diagnostics/export", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Disposition") != `attachment; filename="shadoc-diagnostics.json"` || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("headers=%v", response.Header())
	}
	if response.Body.Len() > diagnostics.MaxBundleBytes {
		t.Fatalf("bundle bytes=%d", response.Body.Len())
	}
	body := response.Body.String()
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("diagnostic bundle leaked %q", secret)
		}
	}
	for _, forbiddenKey := range []string{`"rawLog"`, `"errorSummary"`, `"detail"`, `"path"`, `"host"`, `"username"`, `"url"`, `"topic"`, `"secretId"`} {
		if strings.Contains(body, forbiddenKey) {
			t.Fatalf("diagnostic bundle contains forbidden field %s", forbiddenKey)
		}
	}
	var bundle diagnostics.Bundle
	if err := json.Unmarshal(response.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.FormatVersion != diagnostics.FormatVersion || bundle.Resources.RemoteHosts != 1 || bundle.Resources.Repositories.Total != 1 || bundle.Resources.Tasks.Total != 1 {
		t.Fatalf("bundle summary=%+v", bundle)
	}
	if bundle.Notifications.ConfiguredChannels != 2 || bundle.Notifications.EnabledChannels != 2 {
		t.Fatalf("notification summary=%+v", bundle.Notifications)
	}
	if len(bundle.RecentFailures.Items) != diagnostics.MaxRecentFailures || !bundle.RecentFailures.Truncated || len(bundle.ActiveAlerts.Items) != diagnostics.MaxActiveAlerts || !bundle.ActiveAlerts.Truncated {
		t.Fatalf("limits failures=%d/%t alerts=%d/%t", len(bundle.RecentFailures.Items), bundle.RecentFailures.Truncated, len(bundle.ActiveAlerts.Items), bundle.ActiveAlerts.Truncated)
	}

	audits, err := storage.ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var exportAudit *store.AuditRecord
	for index := range audits {
		if audits[index].Action == "diagnostics.export" {
			exportAudit = &audits[index]
			break
		}
	}
	if exportAudit == nil {
		t.Fatal("diagnostic export semantic audit not found")
	}
	allowedAuditKeys := map[string]bool{"resources": true, "compatibilityFindings": true, "recentFailures": true, "activeAlerts": true, "notificationChannels": true, "capacityRepositories": true}
	if len(exportAudit.Detail) != len(allowedAuditKeys) {
		t.Fatalf("audit detail=%v", exportAudit.Detail)
	}
	for key, value := range exportAudit.Detail {
		if !allowedAuditKeys[key] {
			t.Fatalf("unexpected audit key %q", key)
		}
		if _, ok := value.(float64); !ok {
			t.Fatalf("audit value %q=%T", key, value)
		}
	}
}
