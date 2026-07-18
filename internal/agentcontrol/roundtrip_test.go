package agentcontrol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestMTLSControlRoundTripEnrollsLeasesAndCompletes(t *testing.T) {
	now := time.Now().UTC()
	storage, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	authority, err := LoadOrCreateAuthority(filepath.Join(t.TempDir(), "pki"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithStore(authority, storage, func() time.Time { return now })
	serverCertificate, err := LoadOrCreateServerCertificate(t.TempDir(), authority, "127.0.0.1:9443", nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewHandler(service))
	server.TLS = ServerTLSConfig(authority, serverCertificate)
	server.StartTLS()
	defer server.Close()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent-1"}}, private)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateEnrollmentToken(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.Certificate())
	bootstrap := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}}
	enrollment := agentprotocol.EnrollmentRequest{Version: agentprotocol.Version, Token: token, AgentID: "agent-1", CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))}
	response := postAgentJSON(t, bootstrap, server.URL+"/enroll", enrollment)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("enroll status=%d", response.StatusCode)
	}
	var enrolled agentprotocol.EnrollmentResponse
	if err := json.NewDecoder(response.Body).Decode(&enrolled); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	keyDER, _ := x509.MarshalPKCS8PrivateKey(private)
	clientCertificate, err := tls.X509KeyPair([]byte(enrolled.CertificatePEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	clientLeaf, err := x509.ParseCertificate(clientCertificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{clientCertificate}}}}
	heartbeat := postAgentJSON(t, client, server.URL+"/heartbeat", agentprotocol.Heartbeat{
		Version: agentprotocol.Version, AgentID: "agent-1", Capabilities: []string{"rsync"},
		Runtime: agentprotocol.RuntimeInfo{BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64", RsyncVersion: "3.4.1", ServiceURL: "https://control.example:9443"},
	})
	if heartbeat.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d", heartbeat.StatusCode)
	}
	heartbeat.Body.Close()
	agents, err := storage.ListAgents(context.Background())
	if err != nil || len(agents) != 1 || agents[0].BuildVersion != "v1.4.0" || agents[0].CertificateNotAfter == nil || !agents[0].CertificateNotAfter.Equal(clientLeaf.NotAfter) {
		t.Fatalf("structured heartbeat facts=%+v err=%v certificate=%+v", agents, err, clientLeaf)
	}

	task := domain.Task{ID: "task-1", Name: "sync", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Rsync: &domain.RsyncSource{Path: "/source", DestinationHostID: "host-1", DestinationPath: "/target"}, CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateAgentLease(context.Background(), store.AgentLease{ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "rsync", Definition: json.RawMessage(`{"sourcePath":"/source"}`), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	leaseResponse := postAgentJSON(t, client, server.URL+"/lease", struct{}{})
	if leaseResponse.StatusCode != http.StatusOK {
		t.Fatalf("lease status=%d", leaseResponse.StatusCode)
	}
	var assignment agentprotocol.Assignment
	if err := json.NewDecoder(leaseResponse.Body).Decode(&assignment); err != nil {
		t.Fatal(err)
	}
	leaseResponse.Body.Close()
	resultResponse := postAgentJSON(t, client, server.URL+"/result", agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: assignment.ID, AgentID: "agent-1", Status: "succeeded"})
	if resultResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("result status=%d", resultResponse.StatusCode)
	}
	resultResponse.Body.Close()
	lease, err := storage.AgentLeaseStatus(context.Background(), "lease-1")
	if err != nil || lease.Status != "succeeded" || lease.CompletedAt == nil {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
}

func postAgentJSON(t *testing.T, client *http.Client, target string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(target, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}
