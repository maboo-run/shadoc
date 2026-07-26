package agentruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
)

func TestHTTPControlClientLeasesAndCompletesWork(t *testing.T) {
	completed := false
	clientTransport := roundTripFunc(func(r *http.Request) *http.Response {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		switch r.URL.Path {
		case "/heartbeat":
		case "/lease":
			body, _ := json.Marshal(agentprotocol.Assignment{Version: agentprotocol.Version, ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(time.Minute)})
			response.Body = io.NopCloser(strings.NewReader(string(body)))
		case "/result":
			completed = true
			response.StatusCode = http.StatusNoContent
		default:
			response.StatusCode = http.StatusNotFound
		}
		return response
	})
	client, err := NewHTTPControl("https://service.example", &http.Client{Transport: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Heartbeat(ctx, agentprotocol.Heartbeat{Version: 1, AgentID: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	assignment, ok, err := client.Lease(ctx)
	if err != nil || !ok || assignment.ID != "lease-1" {
		t.Fatalf("assignment=%+v ok=%v err=%v", assignment, ok, err)
	}
	if err := client.Complete(ctx, agentprotocol.Result{Version: 1, AssignmentID: assignment.ID, AgentID: "agent-1", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("result was not reported")
	}
}

func TestHTTPControlUploadsFilesystemScopeEntryChunk(t *testing.T) {
	var received agentprotocol.FilesystemScopeEntryChunk
	clientTransport := roundTripFunc(func(r *http.Request) *http.Response {
		response := &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
		if r.URL.Path != "/filesystem/scope-entries" || json.NewDecoder(r.Body).Decode(&received) != nil {
			response.StatusCode = http.StatusUnprocessableEntity
		}
		return response
	})
	client, err := NewHTTPControl("https://service.example", &http.Client{Transport: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	chunk := agentprotocol.FilesystemScopeEntryChunk{
		Version: agentprotocol.Version, AssignmentID: "scope-1", AgentID: "agent-1",
		Entries: []agentprotocol.FilesystemScopeEntry{{Ordinal: 1, Path: "photos/a.jpg", Type: "file", Disposition: "included"}},
	}
	if err := client.UploadFilesystemScopeEntries(t.Context(), chunk); err != nil {
		t.Fatal(err)
	}
	if received.AssignmentID != "scope-1" || len(received.Entries) != 1 || received.Entries[0].Path != "photos/a.jpg" {
		t.Fatalf("received=%+v", received)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}
