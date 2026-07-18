package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/webhook"
)

func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	value, err := resources.Metadata(r.Context(), notificationconfig.WebhookMetadataKey)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取 Webhook 配置")
		return
	}
	var config notificationconfig.Webhook
	if json.Unmarshal([]byte(value), &config) != nil {
		writeError(w, http.StatusInternalServerError, "已保存的 Webhook 配置无效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"enabled":    config.EnabledValue(),
		"endpoint":   config.Endpoint,
		"authMode":   normalizedWebhookAuthMode(config.AuthMode),
		"hasSecret":  config.SecretID != "",
	})
}

func (s *Server) saveWebhook(w http.ResponseWriter, r *http.Request) {
	username, ok := s.requireMutationSession(w, r)
	if !ok {
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "秘密库尚未配置")
		return
	}
	var input struct {
		Endpoint    string `json:"endpoint"`
		AuthMode    string `json:"authMode"`
		Secret      string `json:"secret"`
		ClearSecret bool   `json:"clearSecret"`
		Enabled     *bool  `json:"enabled"`
	}
	if decodeJSON(r, &input) != nil || input.ClearSecret && input.Secret != "" || !validChannelSecret(input.Secret) {
		writeError(w, http.StatusBadRequest, "Webhook 配置无效")
		return
	}
	resources := s.resourceStore(w)
	if resources == nil {
		return
	}
	var previous notificationconfig.Webhook
	if value, err := resources.Metadata(r.Context(), notificationconfig.WebhookMetadataKey); err == nil {
		_ = json.Unmarshal([]byte(value), &previous)
	}
	enabled := false
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	authMode := normalizedWebhookAuthMode(input.AuthMode)
	secretID := previous.SecretID
	secretAction := "retained"
	if input.ClearSecret {
		secretID, secretAction = "", "cleared"
	} else if input.Secret != "" {
		secretID, secretAction = "pending", "replaced"
	}
	config := notificationconfig.Webhook{Endpoint: input.Endpoint, AuthMode: authMode, SecretID: secretID, Enabled: &enabled}
	if err := config.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "Webhook 配置无效；认证模式与秘密必须匹配，且公网地址必须使用 HTTPS")
		return
	}
	newSecretID := ""
	if input.Secret != "" {
		id, err := s.secrets.Put(r.Context(), notificationconfig.WebhookSecretPurpose, []byte(input.Secret))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法保存 Webhook 认证秘密")
			return
		}
		config.SecretID, newSecretID = id, id
	}
	encoded, _ := json.Marshal(config)
	if err := resources.SetMetadata(r.Context(), notificationconfig.WebhookMetadataKey, string(encoded)); err != nil {
		if newSecretID != "" {
			_ = s.secrets.Delete(context.WithoutCancel(r.Context()), newSecretID)
		}
		writeError(w, http.StatusInternalServerError, "无法保存 Webhook 配置")
		return
	}
	if previous.SecretID != "" && previous.SecretID != config.SecretID {
		_ = s.secrets.Delete(context.WithoutCancel(r.Context()), previous.SecretID)
	}
	s.appendSemanticAudit(r.Context(), username, "webhook.config.update", "notification", "webhook", map[string]any{"enabled": enabled, "authMode": authMode, "secretAction": secretAction})
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "enabled": enabled, "hasSecret": config.SecretID != ""})
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	if s.webhook == nil || s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Webhook 通知适配器尚未配置")
		return
	}
	config, ok := s.savedWebhook(w, r)
	if !ok {
		return
	}
	if !config.EnabledValue() {
		writeError(w, http.StatusConflict, "Webhook 通知已停用，请先启用后再发送测试")
		return
	}
	secretValue, ok := s.notificationSecret(w, r, config.SecretID, notificationconfig.WebhookSecretPurpose, "无法读取 Webhook 认证秘密")
	if !ok {
		return
	}
	now := time.Now().UTC()
	if err := s.webhook.Publish(r.Context(), webhook.Config{Endpoint: config.Endpoint, AuthMode: config.AuthMode, Secret: secretValue}, webhook.Event{ID: "test_" + now.Format("20060102T150405.000000000Z"), OccurredAt: now, StateKey: "notification:webhook:test", Transition: "info", Title: "Shadoc 测试", Message: "Webhook 通知通道连接成功", Severity: "success"}); err != nil {
		writeError(w, http.StatusBadGateway, "Webhook 测试失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
}

func (s *Server) savedWebhook(w http.ResponseWriter, r *http.Request) (notificationconfig.Webhook, bool) {
	resources := s.resourceStore(w)
	if resources == nil {
		return notificationconfig.Webhook{}, false
	}
	value, err := resources.Metadata(r.Context(), notificationconfig.WebhookMetadataKey)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "尚未保存 Webhook 配置")
		return notificationconfig.Webhook{}, false
	}
	var config notificationconfig.Webhook
	if err != nil || json.Unmarshal([]byte(value), &config) != nil || config.Validate() != nil {
		writeError(w, http.StatusInternalServerError, "已保存的 Webhook 配置无效")
		return notificationconfig.Webhook{}, false
	}
	return config, true
}

func (s *Server) emailNotificationRemoved(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if _, ok := s.requireSession(w, r); !ok {
			return
		}
	} else if _, ok := s.requireMutationSession(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "邮件通知通道已移除，请使用 ntfy 或 Webhook")
}

func (s *Server) notificationSecret(w http.ResponseWriter, r *http.Request, id, purpose, message string) (string, bool) {
	if id == "" {
		return "", true
	}
	value, err := s.secrets.Get(r.Context(), id, purpose)
	if err != nil {
		writeError(w, http.StatusInternalServerError, message)
		return "", false
	}
	defer clear(value)
	return string(value), true
}

func normalizedWebhookAuthMode(value string) string {
	if value == "" {
		return notificationconfig.WebhookNone
	}
	return value
}

func validChannelSecret(value string) bool {
	return len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n")
}

func hasReadyNotificationChannel(ctx context.Context, resources *store.Store) bool {
	if encoded, err := resources.Metadata(ctx, "ntfy.config"); err == nil {
		var settings ntfyStored
		if json.Unmarshal([]byte(encoded), &settings) == nil && settings.BaseURL != "" && settings.Topic != "" && settings.enabled() {
			return true
		}
	}
	if encoded, err := resources.Metadata(ctx, notificationconfig.WebhookMetadataKey); err == nil {
		var settings notificationconfig.Webhook
		if json.Unmarshal([]byte(encoded), &settings) == nil && settings.Validate() == nil && settings.EnabledValue() {
			return true
		}
	}
	return false
}
