package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestClientPostsFixedSignedJSONWithoutSecretInBody(t *testing.T) {
	secret := "webhook-secret-private"
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("X-Restic-Control-Event") != "alert.transition" {
			t.Fatalf("request method=%s headers=%v", request.Method, request.Header)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if request.Header.Get("X-Restic-Control-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("signature=%q", request.Header.Get("X-Restic-Control-Signature"))
		}
		if strings.Contains(string(body), secret) || !json.Valid(body) || !strings.Contains(string(body), `"transition":"critical"`) {
			t.Fatalf("body=%s", body)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	err := client.Publish(context.Background(), Config{Endpoint: "https://hooks.example.com/alerts", AuthMode: "hmac-sha256", Secret: secret}, Event{ID: "notification-1", OccurredAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), StateKey: "task:a", Transition: "critical", Title: "Protection issue", Message: "Task failed", Severity: "error"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientDeliversThroughARealVerifiedTLSServer(t *testing.T) {
	received := make(chan Event, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var event Event
		if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&event) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		received <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := New(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}})
	event := Event{ID: "notification-real-tls", OccurredAt: time.Now().UTC(), StateKey: "task:a", Transition: "critical", Title: "Protection issue", Message: "Task failed", Severity: "error"}
	if err := client.Publish(t.Context(), Config{Endpoint: server.URL + "/events", AuthMode: "none"}, event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.ID != event.ID || got.StateKey != event.StateKey || got.Message != event.Message {
			t.Fatalf("event=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("verified TLS webhook did not receive the event")
	}
}

func TestClientRejectsUnsafeEndpointAndBoundedErrorsRedactSecret(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("private response")), Header: make(http.Header)}, nil
	})})
	for _, endpoint := range []string{"http://hooks.example.com/alerts", "https://user:pass@hooks.example.com/alerts", "https://hooks.example.com/alerts?token=private"} {
		if err := client.Publish(context.Background(), Config{Endpoint: endpoint}, Event{ID: "id", OccurredAt: time.Now(), StateKey: "state", Transition: "critical", Message: "failure"}); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	err := client.Publish(context.Background(), Config{Endpoint: "https://hooks.example.com/alerts", AuthMode: "bearer", Secret: "bearer-private"}, Event{ID: "id", OccurredAt: time.Now(), StateKey: "state", Transition: "critical", Message: "failure"})
	if err == nil || strings.Contains(err.Error(), "bearer-private") || strings.Contains(err.Error(), "private response") {
		t.Fatalf("error=%v", err)
	}
}

func TestClientDoesNotReturnSecretEndpointPathOnTransportFailure(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed for " + request.URL.String())
	})})
	err := client.Publish(t.Context(), Config{Endpoint: "https://hooks.example.com/private-receiver-token", AuthMode: "none"}, Event{ID: "id", OccurredAt: time.Now(), StateKey: "state", Transition: "critical", Message: "failure"})
	if err == nil || strings.Contains(err.Error(), "private-receiver-token") || strings.Contains(err.Error(), "hooks.example.com") {
		t.Fatalf("transport error exposed the endpoint: %v", err)
	}
}
