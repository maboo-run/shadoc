package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/ntfy"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
	"github.com/maboo-run/shadoc/internal/webhook"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifierSendsFirstFailureCoalescesRepeatAndSendsRecovery(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return httpResponse(http.StatusOK), nil
	})}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _ := vault.New(bytes.Repeat([]byte{7}, 32))
	secrets := secret.New(s, v, nil)
	tokenID, err := secrets.Put(context.Background(), "ntfy-token", []byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", TokenSecretID: tokenID, Enabled: notificationFlag(true)})
	if err := s.SetMetadata(context.Background(), "ntfy.config", string(config)); err != nil {
		t.Fatal(err)
	}
	service := New(s, secrets, ntfy.New(client))
	for _, status := range []string{"failed", "failed", "success"} {
		if err := service.Notify(context.Background(), "task:a", status, "状态变化"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("notifications=%d", calls.Load())
	}
}

func TestNotifierHonorsDisabledConfigurationWithoutNetworkDelivery(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return httpResponse(http.StatusOK), nil
	})}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	enabled := false
	config, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", Enabled: &enabled})
	if err := s.SetMetadata(context.Background(), "ntfy.config", string(config)); err != nil {
		t.Fatal(err)
	}
	service := New(s, nil, ntfy.New(client))
	result, err := service.Deliver(context.Background(), "task:a", "critical", "任务异常")
	if err != nil || !result.Disabled || result.Attempted || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	deliveries, err := s.ListNotificationDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != store.DeliverySkippedDisabled || deliveries[0].Attempt != 0 {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
}

func TestNotifierTreatsMissingEnablementAsDisabled(t *testing.T) {
	var calls atomic.Int32
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	config, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts"})
	if err := database.SetMetadata(t.Context(), "ntfy.config", string(config)); err != nil {
		t.Fatal(err)
	}
	service := New(database, nil, ntfy.New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return httpResponse(http.StatusOK), nil
	})}))
	result, deliverErr := service.Deliver(t.Context(), "task:a", "critical", "任务异常")
	if deliverErr != nil || !result.Disabled || result.Attempted || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), deliverErr)
	}
}

func TestDisabledCompanionChannelDoesNotHideEnabledChannelDeduplication(t *testing.T) {
	var calls atomic.Int32
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ntfyConfig, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", Enabled: notificationFlag(true)})
	disabled := false
	webhookConfig, _ := json.Marshal(notificationconfig.Webhook{Endpoint: "https://hooks.example.com/alerts", AuthMode: notificationconfig.WebhookNone, Enabled: &disabled})
	if err := database.SetMetadata(t.Context(), "ntfy.config", string(ntfyConfig)); err != nil {
		t.Fatal(err)
	}
	if err := database.SetMetadata(t.Context(), notificationconfig.WebhookMetadataKey, string(webhookConfig)); err != nil {
		t.Fatal(err)
	}
	service := New(database, nil, ntfy.New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return httpResponse(http.StatusOK), nil
	})}))
	for range 2 {
		result, deliverErr := service.Deliver(t.Context(), "task:a", "critical", "任务异常")
		if deliverErr != nil {
			t.Fatal(deliverErr)
		}
		if calls.Load() == 1 && !result.Attempted && !result.Deduplicated {
			t.Fatalf("disabled companion hid deduplication: %+v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("enabled channel calls=%d", calls.Load())
	}
}

func TestNotifierPersistsEveryRetryAndFinalFailureThenRecovery(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail.Load() {
			return httpResponse(http.StatusServiceUnavailable), nil
		}
		return httpResponse(http.StatusOK), nil
	})}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	config, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", Enabled: notificationFlag(true)})
	if err := s.SetMetadata(context.Background(), "ntfy.config", string(config)); err != nil {
		t.Fatal(err)
	}
	service := New(s, nil, ntfy.New(client))
	service.maxAttempts = 3
	service.retryDelay = time.Millisecond
	if result, err := service.Deliver(context.Background(), "task:a", "critical", "任务异常"); err == nil || result.Attempts != 3 || !result.Attempted {
		t.Fatalf("failure result=%+v err=%v", result, err)
	}
	fail.Store(false)
	if result, err := service.Deliver(context.Background(), "task:a", "critical", "任务异常"); err != nil || result.Attempts != 1 {
		t.Fatalf("recovery result=%+v err=%v", result, err)
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
	deliveries, err := s.ListNotificationDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 4 {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
	if deliveries[0].Status != store.DeliveryDelivered || deliveries[1].Status != store.DeliveryFinalFailure || deliveries[2].Status != store.DeliveryRetrying || deliveries[3].Status != store.DeliveryRetrying {
		t.Fatalf("deliveries=%+v", deliveries)
	}
}

func TestNotifierRedactsTokenFromReturnedAndPersistedDeliveryErrors(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection failed with secret-token")
	})}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _ := vault.New(bytes.Repeat([]byte{9}, 32))
	secrets := secret.New(s, v, nil)
	tokenID, err := secrets.Put(context.Background(), "ntfy-token", []byte("secret-token"))
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", TokenSecretID: tokenID, Enabled: notificationFlag(true)})
	if err := s.SetMetadata(context.Background(), "ntfy.config", string(config)); err != nil {
		t.Fatal(err)
	}
	service := New(s, secrets, ntfy.New(client))
	service.maxAttempts = 1
	_, deliverErr := service.Deliver(context.Background(), "task:a", "critical", "任务异常")
	if deliverErr == nil || strings.Contains(deliverErr.Error(), "secret-token") {
		t.Fatalf("delivery error=%v", deliverErr)
	}
	deliveries, err := s.ListNotificationDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || strings.Contains(deliveries[0].ErrorSummary, "secret-token") || !strings.Contains(deliveries[0].ErrorSummary, "[redacted]") {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
}

func TestNotifierRetriesOnlyChannelsThatHaveNotDelivered(t *testing.T) {
	var ntfyCalls atomic.Int32
	ntfyClient := ntfy.New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		ntfyCalls.Add(1)
		return httpResponse(http.StatusOK), nil
	})})
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ntfyConfig, _ := json.Marshal(stored{BaseURL: "https://ntfy.test", Topic: "alerts", Enabled: notificationFlag(true)})
	if err := database.SetMetadata(t.Context(), "ntfy.config", string(ntfyConfig)); err != nil {
		t.Fatal(err)
	}
	webhookConfig, _ := json.Marshal(notificationconfig.Webhook{Endpoint: "https://hooks.example.com/alerts", AuthMode: notificationconfig.WebhookNone, Enabled: notificationFlag(true)})
	if err := database.SetMetadata(t.Context(), notificationconfig.WebhookMetadataKey, string(webhookConfig)); err != nil {
		t.Fatal(err)
	}
	publisher := &webhookStub{err: errors.New("temporary webhook failure")}
	service := New(database, nil, ntfyClient)
	service.SetWebhook(publisher)
	service.maxAttempts = 1
	if result, deliverErr := service.Deliver(t.Context(), "task:a", "critical", "任务异常"); deliverErr == nil || fmt.Sprint(result.FailedChannels) != "[webhook]" {
		t.Fatalf("first result=%+v err=%v", result, deliverErr)
	}
	publisher.err = nil
	if result, deliverErr := service.Deliver(t.Context(), "task:a", "critical", "任务异常"); deliverErr != nil || result.Attempts != 1 {
		t.Fatalf("second result=%+v err=%v", result, deliverErr)
	}
	if ntfyCalls.Load() != 1 || publisher.calls != 2 {
		t.Fatalf("ntfy calls=%d webhook calls=%d", ntfyCalls.Load(), publisher.calls)
	}
}

func TestNotifierIgnoresRetainedEmailConfiguration(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	emailConfig, _ := json.Marshal(notificationconfig.Email{Host: "smtp.example.com", Port: 587, TLSMode: notificationconfig.EmailSTARTTLS, From: "backup@example.com", To: []string{"ops@example.com"}})
	if err := database.SetMetadata(t.Context(), notificationconfig.EmailMetadataKey, string(emailConfig)); err != nil {
		t.Fatal(err)
	}
	service := New(database, nil, nil)
	result, err := service.Deliver(t.Context(), "task:a", "critical", "任务异常")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unconfigured || result.Attempted || len(result.Channels) != 0 {
		t.Fatalf("retained email configuration must not be active: %+v", result)
	}
}

func TestNotifierAppliesDurablePerChannelRateLimit(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	config, _ := json.Marshal(notificationconfig.Webhook{Endpoint: "https://hooks.example.com/alerts", AuthMode: notificationconfig.WebhookNone, Enabled: notificationFlag(true)})
	if err := database.SetMetadata(t.Context(), notificationconfig.WebhookMetadataKey, string(config)); err != nil {
		t.Fatal(err)
	}
	publisher := &webhookStub{}
	service := New(database, nil, nil)
	service.SetWebhook(publisher)
	service.rateLimit, service.rateWindow = 1, time.Hour
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.Deliver(t.Context(), "task:a", "critical", "任务 A 异常"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Deliver(t.Context(), "task:b", "critical", "任务 B 异常")
	if err == nil || fmt.Sprint(result.FailedChannels) != "[webhook]" || publisher.calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, publisher.calls, err)
	}
	deliveries, listErr := database.ListNotificationDeliveries(t.Context(), 10)
	if listErr != nil || len(deliveries) != 2 || deliveries[0].Status != store.DeliveryRateLimited {
		t.Fatalf("deliveries=%+v err=%v", deliveries, listErr)
	}
}

type webhookStub struct {
	calls int
	err   error
	event webhook.Event
}

func notificationFlag(value bool) *bool { return &value }

func (s *webhookStub) Publish(_ context.Context, _ webhook.Config, event webhook.Event) error {
	s.calls++
	s.event = event
	return s.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func httpResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
}
