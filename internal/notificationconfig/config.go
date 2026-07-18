package notificationconfig

import (
	"errors"
	"net"
	"net/mail"
	"net/url"
	"strings"
)

const (
	WebhookMetadataKey   = "webhook.config"
	WebhookSecretPurpose = "webhook-auth-secret"
	WebhookNone          = "none"
	WebhookBearer        = "bearer"
	WebhookHMACSHA256    = "hmac-sha256"
	EmailMetadataKey     = "email.config"
	EmailPasswordPurpose = "smtp-password"
	EmailImplicitTLS     = "implicit-tls"
	EmailSTARTTLS        = "starttls"
)

type Webhook struct {
	Endpoint string `json:"endpoint"`
	AuthMode string `json:"authMode"`
	SecretID string `json:"secretId,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

func (c Webhook) EnabledValue() bool { return c.Enabled != nil && *c.Enabled }

func (c Webhook) Validate() error {
	if c.Endpoint != strings.TrimSpace(c.Endpoint) || len(c.Endpoint) == 0 || len(c.Endpoint) > 2048 || strings.ContainsAny(c.Endpoint, "\x00\r\n") {
		return errors.New("webhook endpoint is invalid")
	}
	endpoint, err := url.ParseRequestURI(c.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("webhook endpoint must be a URL without credentials, query, or fragment")
	}
	if endpoint.Scheme != "https" {
		host := endpoint.Hostname()
		ip := net.ParseIP(host)
		if endpoint.Scheme != "http" || !(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
			return errors.New("webhook endpoint requires HTTPS; HTTP is limited to loopback")
		}
	}
	switch c.AuthMode {
	case "", WebhookNone:
		if c.SecretID != "" {
			return errors.New("unauthenticated webhook cannot contain a secret")
		}
	case WebhookBearer, WebhookHMACSHA256:
		if c.SecretID == "" {
			return errors.New("webhook authentication requires a secret")
		}
	default:
		return errors.New("unsupported webhook authentication mode")
	}
	return nil
}

type Email struct {
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	TLSMode          string   `json:"tlsMode"`
	From             string   `json:"from"`
	To               []string `json:"to"`
	Username         string   `json:"username,omitempty"`
	PasswordSecretID string   `json:"passwordSecretId,omitempty"`
	Enabled          *bool    `json:"enabled,omitempty"`
}

func (c Email) EnabledValue() bool { return c.Enabled != nil && *c.Enabled }

func (c Email) Validate() error {
	if c.Host != strings.TrimSpace(c.Host) || c.Host == "" || len(c.Host) > 253 || strings.ContainsAny(c.Host, "\x00\r\n/ ") || c.Port < 1 || c.Port > 65535 {
		return errors.New("SMTP host and port are invalid")
	}
	if c.TLSMode != EmailImplicitTLS && c.TLSMode != EmailSTARTTLS {
		return errors.New("SMTP requires implicit TLS or STARTTLS")
	}
	if err := validAddress(c.From); err != nil {
		return errors.New("SMTP sender address is invalid")
	}
	if len(c.To) < 1 || len(c.To) > 20 {
		return errors.New("SMTP requires between one and twenty recipients")
	}
	seen := map[string]bool{}
	for _, recipient := range c.To {
		address, err := mail.ParseAddress(recipient)
		if err != nil || strings.ContainsAny(recipient, "\r\n\x00") || seen[strings.ToLower(address.Address)] {
			return errors.New("SMTP recipient address is invalid or duplicated")
		}
		seen[strings.ToLower(address.Address)] = true
	}
	if c.Username != strings.TrimSpace(c.Username) || len(c.Username) > 320 || strings.ContainsAny(c.Username, "\x00\r\n") {
		return errors.New("SMTP username is invalid")
	}
	if (c.Username == "") != (c.PasswordSecretID == "") {
		return errors.New("SMTP username and password secret must be configured together")
	}
	return nil
}

func validAddress(value string) error {
	if value != strings.TrimSpace(value) || value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid email address")
	}
	_, err := mail.ParseAddress(value)
	return err
}
