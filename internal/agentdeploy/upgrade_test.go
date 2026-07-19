package agentdeploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestUpgradeDrainsWorkStagesFixedArtifactAndWaitsForTargetVersionHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:       domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "source.example ssh-ed25519 AAAA"},
		agent:      store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.3.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now.Add(-time.Second))},
		activeWork: []int{1, 0},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	remote.onActivate = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.BuildVersion = "v1.4.0"
		storage.agent.ProtocolMin = 1
		storage.agent.ProtocolMax = 1
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = time.Second
	stages := []string{}
	result, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, func(stage string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if result.FromVersion != "v1.3.0" || result.ToVersion != "v1.4.0" || !remote.staged || !remote.activated || !remote.finalized || remote.rolledBack {
		t.Fatalf("result=%+v remote=%+v", result, remote)
	}
	if storage.beginDrain != 1 || storage.endDrain != 1 || len(stages) < 5 || stages[1] != "draining_agent" {
		t.Fatalf("drain/stages begin=%d end=%d stages=%v", storage.beginDrain, storage.endDrain, stages)
	}
}

func TestSameVersionUpgradeRepairsManagedResticCapability(t *testing.T) {
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host: domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{
			ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1,
			OS: "linux", Arch: "amd64", Status: "online", LastHeartbeatAt: timePointer(now.Add(-time.Second)),
		},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	remote.onActivate = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.Capabilities = append(storage.agent.Capabilities, agentprotocol.ManagedResticInstallCapability)
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = time.Second

	result, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FromVersion != "v1.4.0" || result.ToVersion != "v1.4.0" || !remote.staged || !remote.activated || !remote.finalized || remote.rolledBack {
		t.Fatalf("result=%+v remote=%+v", result, remote)
	}
}

func TestSameVersionRepairRollsBackWithoutManagedResticCapabilityHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host: domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{
			ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1,
			OS: "linux", Arch: "amd64", Status: "online", LastHeartbeatAt: timePointer(now.Add(-time.Second)),
		},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	remote.onActivate = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 5 * time.Millisecond

	if _, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, nil); err == nil {
		t.Fatal("same-version heartbeat without managed Restic capability was accepted")
	}
	if !remote.rolledBack || remote.finalized || storage.endDrain != 1 {
		t.Fatalf("failed repair did not restore the previous Agent: remote=%+v endDrain=%d", remote, storage.endDrain)
	}
}

func TestUpgradeHeartbeatFailureRollsBackAndResumesOldAgentPath(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:  domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.3.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now)},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 5 * time.Millisecond
	if _, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, nil); err == nil {
		t.Fatal("missing target-version heartbeat was accepted")
	}
	if !remote.rolledBack || storage.endDrain != 1 || storage.agent.BuildVersion != "v1.3.0" {
		t.Fatalf("old path was not preserved: remote=%+v storage=%+v", remote, storage)
	}
}

func TestUpgradeRejectsStagedAgentArtifactVersionBeforeActivation(t *testing.T) {
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:  domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "0.0.0-SNAPSHOT-389df1b", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now)},
	}
	remote := &upgradeRemote{
		platform:      Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"},
		stagedVersion: "0.0.0-SNAPSHOT-389df1b",
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 5 * time.Millisecond

	if _, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "0.1.0"}, nil); err == nil {
		t.Fatal("staged Agent artifact with a snapshot version was accepted")
	}
	if !remote.staged || remote.verifiedTarget != "0.1.0" || remote.activated || !remote.finalized || remote.rolledBack {
		t.Fatalf("stale artifact was not rejected before activation: remote=%+v", remote)
	}
}

func TestUpgradeStageFailureCleansPartialArtifactAndResumesAssignments(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:  domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.3.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now)},
	}
	remote := &upgradeRemote{
		platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"},
		err:      errors.New("upload interrupted"),
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })

	if _, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, nil); err == nil {
		t.Fatal("partial staging failure was ignored")
	}
	if !remote.staged || remote.activated || !remote.finalized || remote.rolledBack {
		t.Fatalf("partial artifact was not cleaned without touching the active binary: %+v", remote)
	}
	if storage.beginDrain != 1 || storage.endDrain != 1 {
		t.Fatalf("Agent assignments were not resumed: begin=%d end=%d", storage.beginDrain, storage.endDrain)
	}
}

func TestUpgradeCancellationDuringFinalizeRestoresPreviousAgent(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:  domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.3.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now)},
	}
	remote := &upgradeRemote{
		platform:    Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"},
		finalizeErr: context.Canceled,
	}
	remote.onActivate = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.BuildVersion = "v1.4.0"
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, staticArtifacts{}, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = time.Second

	if _, err := service.Upgrade(t.Context(), UpgradeRequest{AgentID: "agent-a", TargetVersion: "v1.4.0"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if !remote.rolledBack || storage.endDrain != 1 {
		t.Fatalf("cancelled upgrade did not restore the previous path: remote=%+v endDrain=%d", remote, storage.endDrain)
	}
}

func TestReprobeToolsDrainsAgentRestartsServiceAndWaitsForFreshHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:       domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent:      store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now.Add(-time.Second))},
		activeWork: []int{1, 0},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	remote.onRestart = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.Capabilities = []string{"restic", "rsync"}
		storage.agent.RsyncVersion = "3.4.1"
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, nil, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = time.Second

	var stages []string
	result, err := service.ReprobeTools(t.Context(), "agent-a", func(stage string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "agent-a" || result.HostID != "host-a" || result.Platform != "linux/amd64" {
		t.Fatalf("result=%+v", result)
	}
	if !remote.restarted || storage.beginDrain != 1 || storage.endDrain != 1 || storage.agent.RsyncVersion != "3.4.1" {
		t.Fatalf("remote=%+v storage=%+v", remote, storage)
	}
	if want := []string{"probing", "draining_agent", "restarting_agent_for_tool_probe", "waiting_for_agent_tool_probe", "agent_tool_probe_verified"}; !slices.Equal(stages, want) {
		t.Fatalf("stages=%v want=%v", stages, want)
	}
}

func TestProbeHeartbeatDrainsAgentRestartsServiceAndWaitsForFreshHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:       domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent:      store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now.Add(-time.Second))},
		activeWork: []int{1, 0},
	}
	remote := &upgradeRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	remote.onRestart = func() {
		heartbeat := now.Add(time.Second)
		storage.agent.LastHeartbeatAt = &heartbeat
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, nil, upgradeDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = time.Second

	var stages []string
	result, err := service.ProbeHeartbeat(t.Context(), "agent-a", func(stage string) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "agent-a" || result.HostID != "host-a" || result.Platform != "linux/amd64" {
		t.Fatalf("result=%+v", result)
	}
	if !remote.restarted || storage.beginDrain != 1 || storage.endDrain != 1 {
		t.Fatalf("remote=%+v storage=%+v", remote, storage)
	}
	if want := []string{"probing", "draining_agent", "restarting_agent_for_heartbeat", "waiting_for_agent_heartbeat", "agent_heartbeat_verified"}; !slices.Equal(stages, want) {
		t.Fatalf("stages=%v want=%v", stages, want)
	}
}

func TestProbeHeartbeatResumesAssignmentsWhenRestartFails(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	storage := &upgradeStore{
		host:  domain.RemoteHost{ID: "host-a", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "known"},
		agent: store.AgentRecord{ID: "agent-a", RemoteHostID: "host-a", BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, Status: "online", LastHeartbeatAt: timePointer(now)},
	}
	remote := &upgradeRemote{
		platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"},
		err:      errors.New("restart failed"),
	}
	service := NewUpgradeService(storage, deploymentSecrets{}, nil, upgradeDialer{remote: remote}, func() time.Time { return now })

	if _, err := service.ProbeHeartbeat(t.Context(), "agent-a", nil); err == nil {
		t.Fatal("restart failure was ignored")
	}
	if !remote.restarted || storage.beginDrain != 1 || storage.endDrain != 1 {
		t.Fatalf("failed probe did not resume assignments: remote=%+v storage=%+v", remote, storage)
	}
}

type upgradeStore struct {
	host                 domain.RemoteHost
	agent                store.AgentRecord
	activeWork           []int
	beginDrain, endDrain int
}

func (s *upgradeStore) ListRemoteHosts(context.Context) ([]domain.RemoteHost, error) {
	return []domain.RemoteHost{s.host}, nil
}
func (*upgradeStore) RemoteHostPrivateKeySecretID(context.Context, string) (string, error) {
	return "key-a", nil
}
func (s *upgradeStore) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return []store.AgentRecord{s.agent}, nil
}
func (s *upgradeStore) BeginAgentDrain(context.Context, string, time.Time) error {
	s.beginDrain++
	return nil
}
func (s *upgradeStore) EndAgentDrain(context.Context, string) error {
	s.endDrain++
	return nil
}
func (s *upgradeStore) AgentActiveWorkCount(context.Context, string) (int, error) {
	if len(s.activeWork) == 0 {
		return 0, nil
	}
	value := s.activeWork[0]
	s.activeWork = s.activeWork[1:]
	return value, nil
}

type upgradeDialer struct{ remote *upgradeRemote }

func (d upgradeDialer) Dial(context.Context, Target) (UpgradeRemote, error) { return d.remote, nil }

type upgradeRemote struct {
	platform                                            Platform
	staged, activated, finalized, rolledBack, restarted bool
	stagedVersion, verifiedTarget                       string
	onActivate, onRestart                               func()
	err                                                 error
	finalizeErr                                         error
}

func (r *upgradeRemote) Probe(context.Context) (Platform, error) { return r.platform, nil }
func (r *upgradeRemote) StageUpgrade(context.Context, []byte) error {
	r.staged = true
	return r.err
}
func (r *upgradeRemote) VerifyStagedVersion(_ context.Context, _ Platform, expected string) error {
	r.verifiedTarget = expected
	if r.stagedVersion != "" && r.stagedVersion != expected {
		return fmt.Errorf("staged Agent version %q does not match %q", r.stagedVersion, expected)
	}
	return nil
}
func (r *upgradeRemote) ActivateUpgrade(context.Context, Platform) error {
	r.activated = true
	if r.onActivate != nil {
		r.onActivate()
	}
	return r.err
}
func (r *upgradeRemote) RollbackUpgrade(context.Context, Platform) error {
	r.rolledBack = true
	return nil
}
func (r *upgradeRemote) FinalizeUpgrade(context.Context, Platform) error {
	r.finalized = true
	return r.finalizeErr
}
func (r *upgradeRemote) Restart(context.Context, Platform) error {
	r.restarted = true
	if r.onRestart != nil {
		r.onRestart()
	}
	return r.err
}
func (*upgradeRemote) Close() error { return nil }

func timePointer(value time.Time) *time.Time { return &value }
