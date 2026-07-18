package agenttool

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/installer"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServiceInstallsVerifiedResticAndWaitsForCapabilityHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	storage := &toolStore{
		host:  domain.RemoteHost{ID: "host-1", Host: "192.168.0.104", Port: 22, Username: "backup", HostFingerprint: "known-host"},
		agent: store.AgentRecord{ID: "mini-debian", RemoteHostID: "host-1", Status: "online", LastHeartbeatAt: &now, OS: "linux", Arch: "amd64", Capabilities: []string{"managed-restic-install-v1"}},
	}
	remote := &toolRemote{platform: agentdeploy.Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}, activate: func() {
		storage.agent.ResticVersion = "0.18.0"
		storage.agent.Capabilities = []string{"managed-restic-install-v1", "restic", "restic-restore", "filesystem-restore-target"}
		storage.agent.LastHeartbeatAt = &now
	}}
	service := New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	stages := []string{}
	result, err := service.InstallRestic(t.Context(), InstallRequest{AgentID: "mini-debian", Version: "0.18.0"}, func(stage string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "mini-debian" || result.HostID != "host-1" || result.Platform != "linux/amd64" || result.Version != "0.18.0" || result.ArtifactSHA256 != "verified-sha" {
		t.Fatalf("result=%+v", result)
	}
	if want := []string{"probing", "downloading_agent_restic", "draining_agent", "staging_agent_restic", "activating_agent_restic", "waiting_for_agent_restic", "agent_restic_verified"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%v want=%v", stages, want)
	}
	if storage.drained || storage.endDrain != 1 || string(remote.staged) != "restic-binary" || remote.finalized != 1 || remote.rolledBack != 0 {
		t.Fatalf("storage=%+v remote=%+v", storage, remote)
	}
	if remote.target.KnownHosts != "known-host" || string(remote.target.PrivateKey) != "PRIVATE KEY" {
		t.Fatalf("target=%+v", remote.target)
	}
}

func TestServiceRollsBackWhenResticActivationFails(t *testing.T) {
	now := time.Now().UTC()
	storage := &toolStore{
		host:  domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-1", RemoteHostID: "host-1", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{"managed-restic-install-v1"}},
	}
	remote := &toolRemote{platform: agentdeploy.Platform{OS: "linux", Arch: "amd64", Service: "systemd"}, activateErr: errors.New("restart failed")}
	service := New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{remote: remote}, func() time.Time { return now })
	if _, err := service.InstallRestic(t.Context(), InstallRequest{AgentID: "agent-1", Version: "0.18.0"}, nil); err == nil {
		t.Fatal("activation failure accepted")
	}
	if remote.rolledBack != 1 || storage.endDrain != 1 {
		t.Fatalf("remote=%+v storage=%+v", remote, storage)
	}
}

func TestServiceRejectsAgentWithoutManagedResticInstallCapability(t *testing.T) {
	now := time.Now().UTC()
	storage := &toolStore{
		host:  domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "legacy-agent", RemoteHostID: "host-1", Status: "online", LastHeartbeatAt: &now, OS: "linux", Arch: "amd64"},
	}
	service := New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{remote: &toolRemote{platform: agentdeploy.Platform{OS: "linux", Arch: "amd64"}}}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 2 * time.Millisecond
	_, err := service.InstallRestic(t.Context(), InstallRequest{AgentID: "legacy-agent", Version: "0.18.0"}, nil)
	if err == nil || !strings.Contains(err.Error(), "upgrade or redeploy") {
		t.Fatalf("legacy Agent error=%v", err)
	}
}

func TestHeartbeatTimeoutReportsObservedCapabilitiesAsFailure(t *testing.T) {
	now := time.Now().UTC()
	storage := &toolStore{agent: store.AgentRecord{
		ID: "agent-1", Status: "online", LastHeartbeatAt: &now, ResticVersion: "0.18.0",
		Capabilities: []string{"managed-restic-install-v1", "filesystem-restore-target"},
	}}
	service := New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 2 * time.Millisecond
	err := service.waitForHeartbeat(t.Context(), "agent-1", "0.19.1", now)
	if err == nil {
		t.Fatal("capability heartbeat timeout accepted")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("internal verification timeout was misclassified as cancellation: %v", err)
	}
	for _, expected := range []string{"0.18.0", "restic", "restic-restore"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("diagnostic error=%q missing %q", err, expected)
		}
	}
}

func TestServiceRejectsManualAndNonLinuxAgents(t *testing.T) {
	now := time.Now().UTC()
	storage := &toolStore{agent: store.AgentRecord{ID: "manual", Status: "online", LastHeartbeatAt: &now}}
	service := New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{remote: &toolRemote{}}, func() time.Time { return now })
	if _, err := service.InstallRestic(t.Context(), InstallRequest{AgentID: "manual", Version: "0.18.0"}, nil); err == nil {
		t.Fatal("manual Agent accepted")
	}

	storage.host = domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"}
	storage.agent.RemoteHostID = "host-1"
	storage.agent.Capabilities = []string{"managed-restic-install-v1"}
	remote := &toolRemote{platform: agentdeploy.Platform{OS: "darwin", Arch: "arm64", Service: "launchd"}}
	service = New(storage, toolSecrets{}, toolArtifacts{}, toolDialer{remote: remote}, func() time.Time { return now })
	if _, err := service.InstallRestic(t.Context(), InstallRequest{AgentID: "manual", Version: "0.18.0"}, nil); err == nil {
		t.Fatal("non-Linux Agent accepted")
	}
}

type toolStore struct {
	host       domain.RemoteHost
	agent      store.AgentRecord
	drained    bool
	endDrain   int
	activeWork int
}

func (s *toolStore) ListRemoteHosts(context.Context) ([]domain.RemoteHost, error) {
	return []domain.RemoteHost{s.host}, nil
}
func (*toolStore) RemoteHostPrivateKeySecretID(context.Context, string) (string, error) {
	return "key-1", nil
}
func (s *toolStore) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return []store.AgentRecord{s.agent}, nil
}
func (s *toolStore) BeginAgentDrain(context.Context, string, time.Time) error {
	s.drained = true
	return nil
}
func (s *toolStore) EndAgentDrain(context.Context, string) error {
	s.drained = false
	s.endDrain++
	return nil
}
func (s *toolStore) AgentActiveWorkCount(context.Context, string) (int, error) {
	return s.activeWork, nil
}

type toolSecrets struct{}

func (toolSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte("PRIVATE KEY"), nil
}

type toolArtifacts struct{}

func (toolArtifacts) Resolve(_ context.Context, version, goos, goarch string) (installer.Artifact, error) {
	return installer.Artifact{Version: version, GOOS: goos, GOARCH: goarch, Content: []byte("restic-binary"), SHA256: "verified-sha"}, nil
}

type toolDialer struct{ remote *toolRemote }

func (d toolDialer) Dial(_ context.Context, target agentdeploy.Target) (Remote, error) {
	target.PrivateKey = append([]byte(nil), target.PrivateKey...)
	d.remote.target = target
	return d.remote, nil
}

type toolRemote struct {
	platform      agentdeploy.Platform
	target        agentdeploy.Target
	staged        []byte
	activate      func()
	activateErr   error
	rolledBack    int
	finalized     int
	cleanupStaged int
}

func (r *toolRemote) Probe(context.Context) (agentdeploy.Platform, error) { return r.platform, nil }
func (r *toolRemote) StageRestic(_ context.Context, content []byte) error {
	r.staged = append([]byte(nil), content...)
	return nil
}
func (r *toolRemote) ActivateRestic(context.Context) error {
	if r.activate != nil {
		r.activate()
	}
	return r.activateErr
}
func (r *toolRemote) RollbackRestic(context.Context) error      { r.rolledBack++; return nil }
func (r *toolRemote) CleanupStagedRestic(context.Context) error { r.cleanupStaged++; return nil }
func (r *toolRemote) FinalizeRestic(context.Context) error      { r.finalized++; return nil }
func (*toolRemote) Close() error                                { return nil }
