package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/ntfy"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/webhook"
)

type webhookPublisher interface {
	Publish(context.Context, webhook.Config, webhook.Event) error
}

type Service struct {
	store       *store.Store
	secrets     *secret.Manager
	client      *ntfy.Client
	webhook     webhookPublisher
	now         func() time.Time
	maxAttempts int
	retryDelay  time.Duration
	rateLimit   int
	rateWindow  time.Duration
	sequence    atomic.Uint64
}

type stored struct {
	BaseURL       string `json:"baseUrl"`
	Topic         string `json:"topic"`
	TokenSecretID string `json:"tokenSecretId"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

func (c stored) enabled() bool { return c.Enabled != nil && *c.Enabled }

type ChannelResult struct {
	Channel      string               `json:"channel"`
	Attempted    bool                 `json:"attempted"`
	Disabled     bool                 `json:"disabled"`
	Deduplicated bool                 `json:"deduplicated"`
	Attempts     int                  `json:"attempts"`
	Status       store.DeliveryStatus `json:"status,omitempty"`
	ErrorSummary string               `json:"errorSummary,omitempty"`
}

type DeliveryResult struct {
	NotificationID string          `json:"notificationId"`
	Attempted      bool            `json:"attempted"`
	Disabled       bool            `json:"disabled"`
	Unconfigured   bool            `json:"unconfigured"`
	Deduplicated   bool            `json:"deduplicated"`
	Attempts       int             `json:"attempts"`
	FailedChannels []string        `json:"failedChannels,omitempty"`
	Channels       []ChannelResult `json:"channels,omitempty"`
}

func New(s *store.Store, secrets *secret.Manager, client *ntfy.Client) *Service {
	return &Service{
		store:       s,
		secrets:     secrets,
		client:      client,
		now:         time.Now,
		maxAttempts: 3,
		retryDelay:  250 * time.Millisecond,
		rateLimit:   60,
		rateWindow:  time.Hour,
	}
}

func (s *Service) SetWebhook(publisher webhookPublisher) { s.webhook = publisher }

// Notify preserves the legacy notifier interface. Every producer still emits
// one canonical state transition; this service owns channel fan-out.
func (s *Service) Notify(ctx context.Context, stateKey, status, message string) error {
	_, err := s.Deliver(ctx, stateKey, status, message)
	return err
}

type event struct {
	ID         string
	OccurredAt time.Time
	StateKey   string
	Transition string
	Title      string
	Message    string
	Severity   string
}

type configuredChannel struct {
	name    string
	enabled bool
	send    func(context.Context, event) (error, []string)
}

func (s *Service) Deliver(ctx context.Context, stateKey, transition, message string) (DeliveryResult, error) {
	if s == nil || s.store == nil {
		return DeliveryResult{}, errors.New("notification service is not configured")
	}
	channels, err := s.configuredChannels(ctx)
	if err != nil {
		return DeliveryResult{}, err
	}
	if len(channels) == 0 {
		return DeliveryResult{Unconfigured: true}, nil
	}
	occurredAt := s.now().UTC()
	notificationID := fmt.Sprintf("notification_%d_%d", occurredAt.UnixNano(), s.sequence.Add(1))
	title, severity := notificationPresentation(transition)
	shared := event{ID: notificationID, OccurredAt: occurredAt, StateKey: stateKey, Transition: transition, Title: title, Message: message, Severity: severity}
	result := DeliveryResult{NotificationID: notificationID, Channels: make([]ChannelResult, 0, len(channels))}
	allDisabled := true
	allEnabledDeduplicated := true
	var deliveryErrors []error

	for _, channel := range channels {
		channelResult, channelErr := s.deliverChannel(ctx, channel, shared)
		result.Channels = append(result.Channels, channelResult)
		if channel.enabled {
			allDisabled = false
			if !channelResult.Deduplicated {
				allEnabledDeduplicated = false
			}
		}
		if channelResult.Attempted {
			result.Attempted = true
		}
		if channelResult.Attempts > result.Attempts {
			result.Attempts = channelResult.Attempts
		}
		if channelErr != nil {
			result.FailedChannels = append(result.FailedChannels, channel.name)
			deliveryErrors = append(deliveryErrors, fmt.Errorf("%s: %s", channel.name, channelResult.ErrorSummary))
		}
	}
	result.Disabled = allDisabled
	result.Deduplicated = !allDisabled && allEnabledDeduplicated
	if len(deliveryErrors) > 0 {
		return result, errors.Join(deliveryErrors...)
	}
	if !allDisabled && !allEnabledDeduplicated {
		if err := s.store.RecordNotification(context.WithoutCancel(ctx), occurredAt, stateKey, transition, message); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Service) deliverChannel(ctx context.Context, channel configuredChannel, shared event) (ChannelResult, error) {
	result := ChannelResult{Channel: channel.name}
	if !channel.enabled {
		result.Disabled, result.Status = true, store.DeliverySkippedDisabled
		delivery := store.NotificationDelivery{NotificationID: shared.ID, OccurredAt: s.now().UTC(), Channel: channel.name, StateKey: shared.StateKey, Transition: shared.Transition, Status: store.DeliverySkippedDisabled}
		return result, s.store.RecordNotificationDelivery(context.WithoutCancel(ctx), delivery)
	}
	delivered, err := s.store.NotificationChannelDelivered(ctx, channel.name, shared.StateKey, shared.Transition)
	if err != nil {
		return result, err
	}
	if delivered {
		result.Deduplicated = true
		return result, nil
	}
	if s.rateLimit > 0 && s.rateWindow > 0 {
		count, countErr := s.store.CountNotificationEventsSince(ctx, channel.name, s.now().UTC().Add(-s.rateWindow))
		if countErr != nil {
			return result, countErr
		}
		if count >= s.rateLimit {
			result.Status = store.DeliveryRateLimited
			result.ErrorSummary = fmt.Sprintf("channel rate limit reached (%d events per %s)", s.rateLimit, s.rateWindow)
			delivery := store.NotificationDelivery{NotificationID: shared.ID, OccurredAt: s.now().UTC(), Channel: channel.name, StateKey: shared.StateKey, Transition: shared.Transition, Status: store.DeliveryRateLimited, ErrorSummary: result.ErrorSummary}
			if recordErr := s.store.RecordNotificationDelivery(context.WithoutCancel(ctx), delivery); recordErr != nil {
				return result, errors.Join(errors.New(result.ErrorSummary), recordErr)
			}
			return result, errors.New(result.ErrorSummary)
		}
	}
	maxAttempts := s.maxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempted, result.Attempts = true, attempt
		deliveryAt := s.now().UTC()
		publishErr, redactions := channel.send(ctx, shared)
		if publishErr == nil {
			result.Status = store.DeliveryDelivered
			delivery := store.NotificationDelivery{NotificationID: shared.ID, OccurredAt: deliveryAt, Channel: channel.name, StateKey: shared.StateKey, Transition: shared.Transition, Attempt: attempt, MaxAttempts: maxAttempts, Status: store.DeliveryDelivered, DeliveredAt: &deliveryAt}
			return result, s.store.RecordNotificationDelivery(context.WithoutCancel(ctx), delivery)
		}
		result.ErrorSummary = safeDeliveryError(publishErr, redactions...)
		final := attempt == maxAttempts || ctx.Err() != nil
		status := store.DeliveryRetrying
		if final {
			status = store.DeliveryFinalFailure
		}
		result.Status = status
		delivery := store.NotificationDelivery{NotificationID: shared.ID, OccurredAt: deliveryAt, Channel: channel.name, StateKey: shared.StateKey, Transition: shared.Transition, Attempt: attempt, MaxAttempts: maxAttempts, Status: status, ErrorSummary: result.ErrorSummary}
		if recordErr := s.store.RecordNotificationDelivery(context.WithoutCancel(ctx), delivery); recordErr != nil {
			return result, errors.Join(errors.New(result.ErrorSummary), recordErr)
		}
		if final {
			return result, errors.New(result.ErrorSummary)
		}
		timer := time.NewTimer(s.retryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return result, ctx.Err()
		}
	}
	return result, errors.New("notification delivery attempts exhausted")
}

func (s *Service) configuredChannels(ctx context.Context) ([]configuredChannel, error) {
	channels := make([]configuredChannel, 0, 2)
	if value, found, err := s.metadata(ctx, "ntfy.config"); err != nil {
		return nil, err
	} else if found {
		var config stored
		decodeErr := json.Unmarshal([]byte(value), &config)
		valid := decodeErr == nil && config.BaseURL != "" && config.Topic != ""
		channels = append(channels, configuredChannel{name: "ntfy", enabled: !valid || config.enabled(), send: func(ctx context.Context, shared event) (error, []string) {
			if !valid {
				return errors.New("stored ntfy configuration is invalid"), nil
			}
			if s.client == nil {
				return errors.New("ntfy adapter is unavailable"), nil
			}
			token, err := s.readSecret(ctx, config.TokenSecretID, "ntfy-token")
			if err != nil {
				return err, nil
			}
			return s.client.Publish(ctx, ntfy.Config{BaseURL: config.BaseURL, Topic: config.Topic, Token: token}, ntfy.Event{Title: shared.Title, Message: shared.Message, Severity: shared.Severity}), secretRedactions(token)
		}})
	}
	if value, found, err := s.metadata(ctx, notificationconfig.WebhookMetadataKey); err != nil {
		return nil, err
	} else if found {
		var config notificationconfig.Webhook
		decodeErr := json.Unmarshal([]byte(value), &config)
		valid := decodeErr == nil && config.Validate() == nil
		channels = append(channels, configuredChannel{name: "webhook", enabled: !valid || config.EnabledValue(), send: func(ctx context.Context, shared event) (error, []string) {
			if !valid {
				return errors.New("stored webhook configuration is invalid"), nil
			}
			if s.webhook == nil {
				return errors.New("webhook adapter is unavailable"), nil
			}
			secretValue, err := s.readSecret(ctx, config.SecretID, notificationconfig.WebhookSecretPurpose)
			if err != nil {
				return err, nil
			}
			err = s.webhook.Publish(ctx, webhook.Config{Endpoint: config.Endpoint, AuthMode: config.AuthMode, Secret: secretValue}, webhook.Event{ID: shared.ID, OccurredAt: shared.OccurredAt, StateKey: shared.StateKey, Transition: shared.Transition, Title: shared.Title, Message: shared.Message, Severity: shared.Severity})
			return err, secretRedactions(secretValue)
		}})
	}
	return channels, nil
}

func (s *Service) metadata(ctx context.Context, key string) (string, bool, error) {
	value, err := s.store.Metadata(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *Service) readSecret(ctx context.Context, id, purpose string) (string, error) {
	if id == "" {
		return "", nil
	}
	if s.secrets == nil {
		return "", errors.New("notification secret service is unavailable")
	}
	raw, err := s.secrets.Get(ctx, id, purpose)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	return string(raw), nil
}

func secretRedactions(secret string) []string {
	if secret == "" {
		return nil
	}
	return []string{secret}
}

func notificationPresentation(transition string) (string, string) {
	switch transition {
	case "success", "resolved":
		return "保护状态恢复正常", "success"
	case "info":
		return "保护状态提醒", "success"
	default:
		return "保护状态异常", "error"
	}
}

func safeDeliveryError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	for _, secretValue := range secrets {
		if secretValue != "" {
			value = strings.ReplaceAll(value, secretValue, "[redacted]")
		}
	}
	const maxBytes = 512
	if len(value) > maxBytes {
		value = value[:maxBytes]
	}
	return value
}
