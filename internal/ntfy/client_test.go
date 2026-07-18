package ntfy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPublishesRedactedStateChangeWithToken(t *testing.T) {
	var authorization, body, title string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		title = r.Header.Get("Title")
		value, _ := io.ReadAll(r.Body)
		body = string(value)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(server.Client())
	err := client.Publish(context.Background(), Config{BaseURL: server.URL, Topic: "restic-alerts", Token: "secret-token"}, Event{Title: "备份失败", Message: "照片任务无法连接远端", Severity: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret-token" || title != "备份失败" || body != "照片任务无法连接远端" {
		t.Fatalf("auth=%q title=%q body=%q", authorization, title, body)
	}
}

func TestClientRejectsUnsafeEndpointAndSecretInMessage(t *testing.T) {
	client := New(http.DefaultClient)
	if err := client.Publish(context.Background(), Config{BaseURL: "ftp://example.com", Topic: "topic"}, Event{Message: "safe"}); err == nil {
		t.Fatal("unsafe URL accepted")
	}
	if err := client.Publish(context.Background(), Config{BaseURL: "https://ntfy.sh", Topic: "topic", Token: "secret"}, Event{Message: "failure secret"}); err == nil {
		t.Fatal("token in message accepted")
	}
}
