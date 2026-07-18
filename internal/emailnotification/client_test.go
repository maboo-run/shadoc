package emailnotification

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeSession struct {
	from, subject, body string
	to                  []string
	closed              bool
}

func (s *fakeSession) Send(from string, to []string, subject, body string) error {
	s.from, s.to, s.subject, s.body = from, append([]string(nil), to...), subject, body
	return nil
}
func (s *fakeSession) Close() error { s.closed = true; return nil }

func TestClientSendsFixedMessageThroughVerifiedTransport(t *testing.T) {
	session := &fakeSession{}
	client := New()
	client.connect = func(context.Context, Config) (sessionSender, error) { return session, nil }
	err := client.Send(context.Background(), Config{Host: "smtp.example.com", Port: 587, TLSMode: "starttls", From: "backup@example.com", To: []string{"ops@example.com"}, Username: "backup", Password: "smtp-private"}, Event{ID: "notification-1", OccurredAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), Transition: "critical", Subject: "Protection issue", Message: "Task failed"})
	if err != nil {
		t.Fatal(err)
	}
	if !session.closed || session.from != "backup@example.com" || len(session.to) != 1 || session.subject != "[Shadoc] Protection issue" || !strings.Contains(session.body, "Task failed") || strings.Contains(session.body, "smtp-private") {
		t.Fatalf("session=%+v", session)
	}
}

func TestClientRejectsPlaintextSMTPAndHeaderInjection(t *testing.T) {
	client := New()
	for _, config := range []Config{
		{Host: "smtp.example.com", Port: 25, TLSMode: "plain", From: "backup@example.com", To: []string{"ops@example.com"}},
		{Host: "smtp.example.com", Port: 587, TLSMode: "starttls", From: "backup@example.com\r\nBCC: attacker@example.com", To: []string{"ops@example.com"}},
	} {
		if err := client.Send(context.Background(), config, Event{ID: "id", OccurredAt: time.Now(), Transition: "critical", Subject: "Issue", Message: "failure"}); err == nil {
			t.Fatalf("unsafe SMTP config accepted: %+v", config)
		}
	}
}

func TestClientDeliversThroughARealVerifiedImplicitTLSSMTPServer(t *testing.T) {
	certificate, roots := smtpTestCertificate(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received, serverErrors := make(chan string, 1), make(chan error, 1)
	go func() { serverErrors <- serveOneSMTPMessage(listener, received) }()
	port := listener.Addr().(*net.TCPAddr).Port
	client := New()
	client.connect = func(ctx context.Context, config Config) (sessionSender, error) {
		return connectSMTPWithTLSConfig(ctx, config, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	}
	err = client.Send(t.Context(), Config{Host: "127.0.0.1", Port: port, TLSMode: "implicit-tls", From: "backup@example.com", To: []string{"ops@example.com"}}, Event{
		ID: "notification-real-smtp", OccurredAt: time.Now().UTC(), Transition: "critical", Subject: "Protection issue", Message: "Task failed over verified TLS",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if !strings.Contains(message, "Subject: [Shadoc] Protection issue") || !strings.Contains(message, "Task failed over verified TLS") {
			t.Fatalf("message=%q", message)
		}
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-received:
			if !strings.Contains(message, "Subject: [Shadoc] Protection issue") || !strings.Contains(message, "Task failed over verified TLS") {
				t.Fatalf("message=%q", message)
			}
		default:
			t.Fatal("SMTP server exited without a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("verified TLS SMTP server did not receive the message")
	}
}

func smtpTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func serveOneSMTPMessage(listener net.Listener, received chan<- string) error {
	connection, err := listener.Accept()
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	reader, writer := bufio.NewReader(connection), bufio.NewWriter(connection)
	respond := func(message string) error {
		if _, err := writer.WriteString(message); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := respond("220 smtp.test ESMTP ready\r\n"); err != nil {
		return err
	}
	var message strings.Builder
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		command := strings.TrimRight(line, "\r\n")
		if inData {
			if command == "." {
				inData = false
				received <- message.String()
				if err := respond("250 2.0.0 queued\r\n"); err != nil {
					return err
				}
				continue
			}
			message.WriteString(line)
			continue
		}
		switch {
		case strings.HasPrefix(command, "EHLO"):
			err = respond("250-smtp.test\r\n250 8BITMIME\r\n")
		case strings.HasPrefix(command, "HELO"), strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
			err = respond("250 2.0.0 ok\r\n")
		case command == "DATA":
			inData = true
			err = respond("354 end with <CRLF>.<CRLF>\r\n")
		case command == "QUIT":
			_ = respond("221 2.0.0 bye\r\n")
			return nil
		default:
			err = fmt.Errorf("unexpected SMTP command %q", command)
		}
		if err != nil {
			return err
		}
	}
}
