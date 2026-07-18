package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
)

type Config struct {
	Endpoint string
	AuthMode string
	Secret   string
}

type Event struct {
	ID         string    `json:"id"`
	OccurredAt time.Time `json:"occurredAt"`
	StateKey   string    `json:"stateKey"`
	Transition string    `json:"transition"`
	Title      string    `json:"title"`
	Message    string    `json:"message"`
	Severity   string    `json:"severity"`
}

type Client struct{ http *http.Client }

func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copy := *client
	if copy.Timeout <= 0 || copy.Timeout > 30*time.Second {
		copy.Timeout = 15 * time.Second
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("webhook redirects are not allowed") }
	return &Client{http: &copy}
}

func (c *Client) Publish(ctx context.Context, config Config, event Event) error {
	secretID := ""
	if config.AuthMode == notificationconfig.WebhookBearer || config.AuthMode == notificationconfig.WebhookHMACSHA256 {
		secretID = "configured"
	}
	settings := notificationconfig.Webhook{Endpoint: config.Endpoint, AuthMode: config.AuthMode, SecretID: secretID}
	if err := settings.Validate(); err != nil || secretID != "" && (config.Secret == "" || len(config.Secret) > 4096 || strings.ContainsAny(config.Secret, "\x00\r\n")) {
		return errors.New("valid structured webhook configuration is required")
	}
	if event.ID == "" || event.OccurredAt.IsZero() || event.StateKey == "" || event.Transition == "" || event.Message == "" || len(event.StateKey) > 512 || len(event.Title) > 256 || len(event.Message) > 4096 || len(event.Severity) > 64 || strings.ContainsAny(event.ID+event.StateKey+event.Transition+event.Title+event.Severity, "\x00\r\n") {
		return errors.New("valid bounded webhook event is required")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("create webhook request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "shadoc-webhook/1")
	request.Header.Set("X-Restic-Control-Event", "alert.transition")
	switch config.AuthMode {
	case notificationconfig.WebhookBearer:
		request.Header.Set("Authorization", "Bearer "+config.Secret)
	case notificationconfig.WebhookHMACSHA256:
		mac := hmac.New(sha256.New, []byte(config.Secret))
		_, _ = mac.Write(body)
		request.Header.Set("X-Restic-Control-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := c.http.Do(request)
	if err != nil {
		// net/http transport errors commonly include the full request URL. A
		// webhook path may itself be a receiver credential, so it must not flow
		// into durable delivery history or alert messages.
		return errors.New("webhook request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", response.StatusCode)
	}
	return nil
}
