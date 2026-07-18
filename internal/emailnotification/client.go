package emailnotification

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/notificationconfig"
)

type Config struct {
	Host     string
	Port     int
	TLSMode  string
	From     string
	To       []string
	Username string
	Password string
}

type Event struct {
	ID         string
	OccurredAt time.Time
	Transition string
	Subject    string
	Message    string
}

type sessionSender interface {
	Send(string, []string, string, string) error
	Close() error
}

type Client struct {
	connect func(context.Context, Config) (sessionSender, error)
}

func New() *Client { return &Client{connect: connectSMTP} }

func (c *Client) Send(ctx context.Context, config Config, event Event) error {
	secretID := ""
	if config.Username != "" || config.Password != "" {
		secretID = "configured"
	}
	settings := notificationconfig.Email{Host: config.Host, Port: config.Port, TLSMode: config.TLSMode, From: config.From, To: config.To, Username: config.Username, PasswordSecretID: secretID}
	if err := settings.Validate(); err != nil || secretID != "" && (config.Password == "" || len(config.Password) > 4096 || strings.ContainsAny(config.Password, "\x00\r\n")) {
		return errors.New("valid TLS-protected email configuration is required")
	}
	if event.ID == "" || event.OccurredAt.IsZero() || event.Transition == "" || event.Subject == "" || event.Message == "" || len(event.Subject) > 200 || len(event.Message) > 4096 || strings.ContainsAny(event.ID+event.Transition+event.Subject, "\x00\r\n") || strings.ContainsRune(event.Message, '\x00') {
		return errors.New("valid bounded email event is required")
	}
	from, _ := mail.ParseAddress(config.From)
	recipients := make([]string, 0, len(config.To))
	for _, item := range config.To {
		address, _ := mail.ParseAddress(item)
		recipients = append(recipients, address.Address)
	}
	session, err := c.connect(ctx, config)
	if err != nil {
		return redactError(err, config.Password)
	}
	defer session.Close()
	body := fmt.Sprintf("Time: %s\r\nTransition: %s\r\n\r\n%s", event.OccurredAt.UTC().Format(time.RFC3339), event.Transition, normalizeBody(event.Message))
	if err := session.Send(from.Address, recipients, "[Shadoc] "+event.Subject, body); err != nil {
		return redactError(err, config.Password)
	}
	return nil
}

type smtpSession struct{ client *smtp.Client }

func connectSMTP(ctx context.Context, config Config) (sessionSender, error) {
	return connectSMTPWithTLSConfig(ctx, config, &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12})
}

func connectSMTPWithTLSConfig(ctx context.Context, config Config, tlsConfig *tls.Config) (sessionSender, error) {
	if tlsConfig == nil {
		return nil, errors.New("SMTP TLS configuration is required")
	}
	tlsConfig = tlsConfig.Clone()
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = config.Host
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	address := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect SMTP server: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	if config.TLSMode == notificationconfig.EmailImplicitTLS {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("verify implicit SMTP TLS: %w", err)
		}
		connection = tlsConnection
	}
	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("start SMTP session: %w", err)
	}
	if config.TLSMode == notificationconfig.EmailSTARTTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			_ = client.Close()
			return nil, errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("verify SMTP STARTTLS: %w", err)
		}
	}
	if config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("authenticate SMTP session: %w", err)
		}
	}
	return &smtpSession{client: client}, nil
}

func (s *smtpSession) Send(from string, to []string, subject, body string) error {
	if err := s.client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := s.client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := s.client.Data()
	if err != nil {
		return err
	}
	message := "From: " + from + "\r\nTo: " + strings.Join(to, ", ") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func (s *smtpSession) Close() error { return s.client.Quit() }

func normalizeBody(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func redactError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}
