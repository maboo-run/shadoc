package agentdeploy

import (
	"context"
	"errors"
	"reflect"
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
		agent:  store.AgentRecord{ID: "mini-debian", RemoteHostID: "host-1", Status: "revoked", RevokedAt: &now},
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

func TestRemovalServiceDoesNotRemoveOrChangeStatusWhenStopFails(t *testing.T) {
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "tmen", HostFingerprint: "known"},
		agent:  store.AgentRecord{ID: "agent-1", RemoteHostID: "host-1", Status: "online"},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}, stopErr: errors.New("stop failed"), events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, time.Now)
	if _, err := service.Uninstall(context.Background(), "agent-1", nil); err == nil {
		t.Fatal("stop failure was ignored")
	}
	if want := []string{"stop"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestRemovalServiceKeepsConfirmedStoppedStateWhenFileRemovalFails(t *testing.T) {
	events := []string{}
	storage := &removalStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "tmen", HostFingerprint: "known"},
		agent:  store.AgentRecord{ID: "agent-1", RemoteHostID: "host-1", Status: "online"},
		events: &events,
	}
	remote := &removalRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}, removeErr: errors.New("remove failed"), events: &events}
	service := NewRemovalService(storage, removalSecrets{}, removalDialer{remote: remote}, time.Now)
	if _, err := service.Uninstall(context.Background(), "agent-1", nil); err == nil {
		t.Fatal("remove failure was ignored")
	}
	if want := []string{"stop", "mark-stopped", "remove"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
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
