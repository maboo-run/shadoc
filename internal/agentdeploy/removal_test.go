package agentdeploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestRemovalServiceStopsBeforeRemovingAndRevokingAgent(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "192.168.0.104", Port: 22, Username: "tmen", HostFingerprint: "known-host"},
		agent:  store.AgentRecord{ID: "mini-debian", RemoteHostID: "host-1", ManagedInstallation: true, Status: "revoked", RevokedAt: &now},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}, events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, func() time.Time { return now })
	stages := []string{}
	result, err := service.Uninstall(context.Background(), "mini-debian", func(stage string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "mini-debian" || result.HostID != "host-1" || result.Platform != "linux/amd64" {
		t.Fatalf("result=%+v", result)
	}
	if want := []string{"stop", "mark-stopped", "remove", "complete"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
	if want := []string{"probing", "stopping_agent", "removing_agent", "revoking_agent"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%v want=%v", stages, want)
	}
	if remote.target.KnownHosts != "known-host" || string(remote.target.PrivateKey) != "PRIVATE KEY" {
		t.Fatalf("target=%+v", remote.target)
	}
}

func TestRemovalServiceDrainsAgentBeforeStoppingIt(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "192.168.0.105", Port: 22, Username: "tmen", HostFingerprint: "known-host"},
		agent:  store.AgentRecord{ID: "ugreen-agent", RemoteHostID: "host-1", ManagedInstallation: true, Status: "online"},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/tmen"}, events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, func() time.Time { return now })

	if _, err := service.Uninstall(t.Context(), "ugreen-agent", nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"begin-drain", "active-work", "stop", "mark-stopped", "remove", "complete", "end-drain"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestRemovalServiceDoesNotRemoveOrChangeStatusWhenStopFails(t *testing.T) {
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "tmen", HostFingerprint: "known"},
		agent:  store.AgentRecord{ID: "agent-1", RemoteHostID: "host-1", ManagedInstallation: true, Status: "online"},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}, stopErr: errors.New("stop failed"), events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, time.Now)
	if _, err := service.Uninstall(context.Background(), "agent-1", nil); err == nil {
		t.Fatal("stop failure was ignored")
	}
	if want := []string{"begin-drain", "active-work", "stop", "end-drain"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestRemovalServiceKeepsConfirmedStoppedStateWhenFileRemovalFails(t *testing.T) {
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "tmen", HostFingerprint: "known"},
		agent:  store.AgentRecord{ID: "agent-1", RemoteHostID: "host-1", ManagedInstallation: true, Status: "online"},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}, removeErr: errors.New("remove failed"), events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, time.Now)
	if _, err := service.Uninstall(context.Background(), "agent-1", nil); err == nil {
		t.Fatal("remove failure was ignored")
	}
	if want := []string{"begin-drain", "active-work", "stop", "mark-stopped", "remove", "end-drain"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestRemovalServiceExplainsMissingManagedRemoteHost(t *testing.T) {
	events := []string{}
	storage := &removalStore{
		agent:  store.AgentRecord{ID: "agent-1", RemoteHostID: "deleted-host", ManagedInstallation: true, Status: "offline"},
		events: &events,
	}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: &removalRemote{events: &events}}, time.Now)

	_, err := service.Uninstall(t.Context(), "agent-1", nil)
	if err == nil {
		t.Fatal("missing managed remote host was accepted")
	}
	if !strings.Contains(err.Error(), "关联的远程主机已被删除") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}

type removalStore struct {
	host   domain.RemoteHost
	agent  store.AgentRecord
	events *[]string
}

func (s *removalStore) ListRemoteHosts(context.Context) ([]domain.RemoteHost, error) {
	return []domain.RemoteHost{s.host}, nil
}

func (s *removalStore) RemoteHostPrivateKeySecretID(context.Context, string) (string, error) {
	return "key-1", nil
}

func (s *removalStore) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return []store.AgentRecord{s.agent}, nil
}

func (s *removalStore) MarkAgentStopped(context.Context, string, time.Time) error {
	*s.events = append(*s.events, "mark-stopped")
	return nil
}

func (s *removalStore) CompleteAgentUninstall(context.Context, string, time.Time) error {
	*s.events = append(*s.events, "complete")
	return nil
}
func (s *removalStore) BeginAgentDrain(context.Context, string, time.Time) error {
	*s.events = append(*s.events, "begin-drain")
	return nil
}
func (s *removalStore) EndAgentDrain(context.Context, string) error {
	*s.events = append(*s.events, "end-drain")
	return nil
}
func (s *removalStore) AgentActiveWorkCount(context.Context, string) (int, error) {
	*s.events = append(*s.events, "active-work")
	return 0, nil
}

type removalSecrets struct{}

func (removalSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte("PRIVATE KEY"), nil
}

type removalDialer struct{ remote *removalRemote }

func (d removalDialer) Dial(_ context.Context, target Target) (RemovalRemote, error) {
	target.PrivateKey = append([]byte(nil), target.PrivateKey...)
	d.remote.target = target
	return d.remote, nil
}

type removalRemote struct {
	platform           Platform
	target             Target
	stopErr, removeErr error
	events             *[]string
}

func (r *removalRemote) Probe(context.Context) (Platform, error) { return r.platform, nil }
func (r *removalRemote) Stop(context.Context, Platform) error {
	*r.events = append(*r.events, "stop")
	return r.stopErr
}
func (r *removalRemote) Remove(context.Context, Platform) error {
	*r.events = append(*r.events, "remove")
	return r.removeErr
}
func (*removalRemote) Close() error { return nil }
