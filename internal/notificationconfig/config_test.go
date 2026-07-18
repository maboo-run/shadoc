package notificationconfig

import "testing"

func TestWebhookConfigurationRequiresSecureStructuredEndpointAndPurposeBoundSecret(t *testing.T) {
	enabled := true
	valid := Webhook{Endpoint: "https://hooks.example.com/restic-control", AuthMode: WebhookBearer, SecretID: "secret", Enabled: &enabled}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []Webhook{
		{Endpoint: "http://hooks.example.com/notify", AuthMode: WebhookNone},
		{Endpoint: "https://user:pass@hooks.example.com/notify", AuthMode: WebhookNone},
		{Endpoint: "https://hooks.example.com/notify?token=secret", AuthMode: WebhookNone},
		{Endpoint: "https://hooks.example.com/notify", AuthMode: "custom", SecretID: "secret"},
		{Endpoint: "https://hooks.example.com/notify", AuthMode: WebhookHMACSHA256},
		{Endpoint: "https://hooks.example.com/notify", AuthMode: WebhookNone, SecretID: "unexpected"},
	} {
		if err := item.Validate(); err == nil {
			t.Fatalf("invalid webhook accepted: %+v", item)
		}
	}
	loopback := Webhook{Endpoint: "http://127.0.0.1:9000/notify", AuthMode: WebhookNone}
	if err := loopback.Validate(); err != nil {
		t.Fatalf("loopback webhook rejected: %v", err)
	}
}

func TestWebhookDefaultsToDisabledWhenEnablementIsMissing(t *testing.T) {
	if (Webhook{}).EnabledValue() {
		t.Fatal("webhook without explicit enablement must remain disabled")
	}
}

func TestEmailConfigurationRequiresVerifiedTLSAndValidAddresses(t *testing.T) {
	enabled := true
	valid := Email{Host: "smtp.example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "Backups <backup@example.com>", To: []string{"ops@example.com"}, Username: "backup", PasswordSecretID: "secret", Enabled: &enabled}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []Email{
		{Host: "smtp.example.com", Port: 25, TLSMode: "plain", From: "backup@example.com", To: []string{"ops@example.com"}},
		{Host: "smtp.example.com\r\nBCC: attacker@example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "backup@example.com", To: []string{"ops@example.com"}},
		{Host: "smtp.example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "backup@example.com\r\nBCC: attacker@example.com", To: []string{"ops@example.com"}},
		{Host: "smtp.example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "backup@example.com", To: nil},
		{Host: "smtp.example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "backup@example.com", To: []string{"ops@example.com"}, Username: "backup"},
		{Host: "smtp.example.com", Port: 587, TLSMode: EmailSTARTTLS, From: "backup@example.com", To: []string{"ops@example.com"}, PasswordSecretID: "unexpected"},
	} {
		if err := item.Validate(); err == nil {
			t.Fatalf("invalid email accepted: %+v", item)
		}
	}
}

func TestLegacyEmailDefaultsToDisabledWhenEnablementIsMissing(t *testing.T) {
	if (Email{}).EnabledValue() {
		t.Fatal("legacy email configuration without explicit enablement must remain disabled")
	}
}
