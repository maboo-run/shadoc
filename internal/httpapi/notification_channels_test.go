package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/webhook"
)

func TestWebhookConfigurationRotatesPurposeBoundSecretWithoutReturningIt(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{
		"endpoint": "https://hooks.example.com/restic-control", "authMode": "bearer", "secret": "first-private", "enabled": true,
	}, cookie)
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), "first-private") {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	state := requestJSON(t, srv, http.MethodGet, "/api/webhook", nil, cookie)
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"hasSecret":true`) || strings.Contains(state.Body.String(), "first-private") || strings.Contains(state.Body.String(), "secretId") {
		t.Fatalf("state=%d %s", state.Code, state.Body.String())
	}
	config := loadWebhookConfig(t, srv)
	oldID := config.SecretID
	if value, err := srv.secrets.Get(t.Context(), oldID, notificationconfig.WebhookSecretPurpose); err != nil || string(value) != "first-private" {
		t.Fatalf("secret=%q err=%v", value, err)
	}
	retained := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{
		"endpoint": "https://hooks.example.com/updated", "authMode": "bearer", "secret": "", "enabled": false,
	}, cookie)
	if retained.Code != http.StatusOK || loadWebhookConfig(t, srv).SecretID != oldID {
		t.Fatalf("retained=%d %s", retained.Code, retained.Body.String())
	}
	replaced := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{
		"endpoint": "https://hooks.example.com/updated", "authMode": "hmac-sha256", "secret": "second-private", "enabled": true,
	}, cookie)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace=%d %s", replaced.Code, replaced.Body.String())
	}
	if _, err := srv.secrets.Get(t.Context(), oldID, notificationconfig.WebhookSecretPurpose); err == nil {
		t.Fatal("replaced webhook secret still exists")
	}
	cleared := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{
		"endpoint": "https://hooks.example.com/updated", "authMode": "none", "clearSecret": true, "enabled": true,
	}, cookie)
	if cleared.Code != http.StatusOK || loadWebhookConfig(t, srv).SecretID != "" {
		t.Fatalf("clear=%d %s", cleared.Code, cleared.Body.String())
	}
}

func TestWebhookRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	for _, payload := range []map[string]any{
		{"endpoint": "http://hooks.example.com/notify", "authMode": "none", "enabled": true},
		{"endpoint": "https://hooks.example.com/notify?token=private", "authMode": "none", "enabled": true},
		{"endpoint": "https://hooks.example.com/notify", "authMode": "bearer", "enabled": true},
		{"endpoint": "https://hooks.example.com/notify", "authMode": "none", "secret": "unexpected", "enabled": true},
	} {
		response := requestJSON(t, srv, http.MethodPost, "/api/webhook", payload, cookie)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "private") {
			t.Fatalf("payload=%v status=%d body=%s", payload, response.Code, response.Body.String())
		}
	}
}

func TestNotificationChannelsRequireExplicitEnablement(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	webhookResponse := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{
		"endpoint": "https://hooks.example.com/notify", "authMode": "none",
	}, cookie)
	if webhookResponse.Code != http.StatusOK || !strings.Contains(webhookResponse.Body.String(), `"enabled":false`) || loadWebhookConfig(t, srv).EnabledValue() {
		t.Fatalf("webhook default=%d %s", webhookResponse.Code, webhookResponse.Body.String())
	}
	ntfyResponse := requestJSON(t, srv, http.MethodPost, "/api/ntfy", map[string]any{
		"baseUrl": "https://ntfy.example", "topic": "backups",
	}, cookie)
	if ntfyResponse.Code != http.StatusOK || !strings.Contains(ntfyResponse.Body.String(), `"enabled":false`) {
		t.Fatalf("ntfy default=%d %s", ntfyResponse.Code, ntfyResponse.Body.String())
	}
	ntfyState := requestJSON(t, srv, http.MethodGet, "/api/ntfy", nil, cookie)
	if ntfyState.Code != http.StatusOK || !strings.Contains(ntfyState.Body.String(), `"enabled":false`) {
		t.Fatalf("ntfy state=%d %s", ntfyState.Code, ntfyState.Body.String())
	}
}

func TestEmailNotificationEndpointsAreRemoved(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	for _, request := range []struct {
		method string
		path   string
	}{{http.MethodGet, "/api/email"}, {http.MethodPost, "/api/email"}, {http.MethodPost, "/api/email/test"}} {
		response := requestJSON(t, srv, request.method, request.path, map[string]any{}, cookie)
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "ntfy") || !strings.Contains(response.Body.String(), "Webhook") {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}

func TestNotificationChannelTestsUseStoredSecretsAndReturnBoundedErrors(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPost, "/api/webhook", map[string]any{"endpoint": "https://hooks.example.com/notify", "authMode": "bearer", "secret": "webhook-private", "enabled": true}, cookie); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	webhookPublisher := &webhookTestStub{}
	srv.webhook = webhookPublisher
	if response := requestJSON(t, srv, http.MethodPost, "/api/webhook/test", map[string]any{}, cookie); response.Code != http.StatusOK {
		t.Fatalf("webhook test=%d %s", response.Code, response.Body.String())
	}
	if webhookPublisher.config.Secret != "webhook-private" {
		t.Fatalf("webhook=%+v", webhookPublisher.config)
	}
	webhookPublisher.err = errors.New("transport failed with webhook-private")
	failed := requestJSON(t, srv, http.MethodPost, "/api/webhook/test", map[string]any{}, cookie)
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "webhook-private") || strings.Contains(failed.Body.String(), "transport failed") {
		t.Fatalf("failed=%d %s", failed.Code, failed.Body.String())
	}
}

func loadWebhookConfig(t *testing.T, srv *Server) notificationconfig.Webhook {
	t.Helper()
	value, err := srv.store.(*store.Store).Metadata(t.Context(), notificationconfig.WebhookMetadataKey)
	if err != nil {
		t.Fatal(err)
	}
	var config notificationconfig.Webhook
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

type webhookTestStub struct {
	config webhook.Config
	event  webhook.Event
	err    error
}

func (s *webhookTestStub) Publish(_ context.Context, config webhook.Config, event webhook.Event) error {
	s.config, s.event = config, event
	return s.err
}
