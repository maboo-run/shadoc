package agentdeploy

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestArtifactFilenamesAreFixedDistributionCompanions(t *testing.T) {
	want := []string{
		"shadoc-agent-linux-amd64",
		"shadoc-agent-linux-arm64",
		"shadoc-agent-darwin-amd64",
		"shadoc-agent-darwin-arm64",
		"shadoc-agent-windows-amd64.exe",
		"shadoc-agent-windows-arm64.exe",
	}
	if got := ArtifactFilenames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact filenames = %v", got)
	}
}

func TestArtifactResolverSelectsPlatformSpecificBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shadoc-agent-linux-arm64")
	if err := os.WriteFile(path, fakeELF(183), 0o700); err != nil {
		t.Fatal(err)
	}
	artifact, err := (ArtifactResolver{Dir: dir}).Resolve(Platform{OS: "linux", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != path || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact=%+v", artifact)
	}
	if _, err := (ArtifactResolver{Dir: dir}).Resolve(Platform{OS: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("missing amd64 artifact accepted")
	} else if got := err.Error(); got != "未找到适用于 linux/amd64 的 Agent 安装文件；请重新执行完整构建，并保持控制服务与 Agent 文件位于同一目录" {
		t.Fatalf("unexpected missing artifact error: %q", got)
	}
}

func TestArtifactResolverFallsBackToLegacyCompanionFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restic-control-agent-linux-arm64")
	if err := os.WriteFile(path, fakeELF(183), 0o700); err != nil {
		t.Fatal(err)
	}
	artifact, err := (ArtifactResolver{Dir: dir}).Resolve(Platform{OS: "linux", Arch: "arm64"})
	if err != nil || artifact.Path != path {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestArtifactResolverDoesNotDeployLegacyWindowsBinaryUnderShadocServiceIdentity(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "restic-control-agent-windows-amd64.exe")
	if err := os.WriteFile(legacy, fakePE(0x8664), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ArtifactResolver{Dir: dir, LocalOS: "linux", LocalArch: "amd64"}).Resolve(Platform{OS: "windows", Arch: "amd64"}); err == nil {
		t.Fatal("legacy Windows Agent was deployed under the Shadoc SCM identity")
	}
}

func TestLinuxServiceDefinitionEscapesSystemdPercentSpecifiersInURL(t *testing.T) {
	definition, err := deploymentServiceDefinition(Platform{OS: "linux"}, DeployRequest{AgentID: "agent-a", ServiceURL: "https://service.example:9443/agent%2Fcontrol"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(definition), "agent%%2Fcontrol") {
		t.Fatalf("definition=%s", definition)
	}
}

func TestArtifactResolverRejectsWrongExecutableArchitecture(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shadoc-agent-linux-amd64"), fakeELF(183), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (ArtifactResolver{Dir: dir}).Resolve(Platform{OS: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("arm64 ELF accepted as amd64")
	} else if got := err.Error(); got != "Agent 安装文件与远程主机平台 linux/amd64 不匹配；请重新执行完整构建" {
		t.Fatalf("unexpected architecture mismatch error: %q", got)
	}
}

func TestArtifactResolverSelectsWindowsExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shadoc-agent-windows-amd64.exe")
	if err := os.WriteFile(path, fakePE(0x8664), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := (ArtifactResolver{Dir: dir}).Resolve(Platform{OS: "windows", Arch: "amd64"})
	if err != nil || artifact.Path != path {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestServiceDeploysAndWaitsForHeartbeat(t *testing.T) {
	remote := &deploymentRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	now := time.Now().UTC()
	storage := &deploymentStore{host: domain.RemoteHost{ID: "host-1", Host: "source.example", Port: 22, Username: "backup", HostFingerprint: "source.example ssh-ed25519 AAAA"}, agents: []store.AgentRecord{{ID: "source-1", Status: "online", Capabilities: []string{"restic"}, LastHeartbeatAt: &now}}}
	service := NewService(storage, deploymentSecrets{}, &deploymentControl{}, staticArtifacts{}, deploymentDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	result, err := service.Deploy(context.Background(), DeployRequest{HostID: "host-1", AgentID: "source-1", ServiceURL: "https://service.example:9443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Platform != "linux/amd64" || !remote.activated || !remote.finalized || remote.cleaned {
		t.Fatalf("result=%+v remote=%+v", result, remote)
	}
	if string(remote.files[TokenFile]) != "enrollment-token" {
		t.Fatalf("token=%q", remote.files[TokenFile])
	}
	if containsSecret(remote.commands, "enrollment-token") {
		t.Fatalf("token leaked in commands: %v", remote.commands)
	}
	if storage.boundAgentID != "source-1" || storage.boundHostID != "host-1" {
		t.Fatalf("Agent host binding=%q/%q", storage.boundAgentID, storage.boundHostID)
	}
}

func TestServiceRollsBackWhenActivationFails(t *testing.T) {
	remote := &deploymentRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}, activateErr: errors.New("failed")}
	service := NewService(&deploymentStore{host: domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"}}, deploymentSecrets{}, &deploymentControl{}, staticArtifacts{}, deploymentDialer{remote: remote}, time.Now)
	if _, err := service.Deploy(context.Background(), DeployRequest{HostID: "host-1", AgentID: "source-1", ServiceURL: "https://service.example:9443"}, nil); err == nil {
		t.Fatal("activation failure ignored")
	}
	if !remote.cleaned {
		t.Fatal("failed deployment was not cleaned")
	}
}

func TestServiceRollsBackWhenMigrationFinalizationFails(t *testing.T) {
	now := time.Now().UTC()
	remote := &deploymentRemote{
		platform:    Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"},
		finalizeErr: errors.New("failed"),
	}
	storage := &deploymentStore{
		host:   domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"},
		agents: []store.AgentRecord{{ID: "source-1", Status: "online", LastHeartbeatAt: &now}},
	}
	service := NewService(storage, deploymentSecrets{}, &deploymentControl{}, staticArtifacts{}, deploymentDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	if _, err := service.Deploy(context.Background(), DeployRequest{HostID: "host-1", AgentID: "source-1", ServiceURL: "https://service.example:9443"}, nil); err == nil {
		t.Fatal("finalization failure ignored")
	}
	if !remote.cleaned {
		t.Fatal("failed migration finalization was not rolled back")
	}
}

func TestServiceRollsBackWhenMigratedAgentLosesAnExistingCapability(t *testing.T) {
	now := time.Now().UTC()
	remote := &deploymentRemote{platform: Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/backup"}}
	storage := &deploymentStore{
		host: domain.RemoteHost{ID: "host-1", Host: "host", Port: 22, Username: "backup", HostFingerprint: "known"},
		agents: []store.AgentRecord{{
			ID: "source-1", Status: "online", Capabilities: []string{"restic"}, LastHeartbeatAt: &now,
		}},
	}
	service := NewService(storage, deploymentSecrets{}, &deploymentControl{}, staticArtifacts{}, deploymentDialer{remote: remote}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	service.heartbeatTimeout = 5 * time.Millisecond
	listCalls := 0
	storage.listAgentsHook = func(records []store.AgentRecord) []store.AgentRecord {
		listCalls++
		result := append([]store.AgentRecord(nil), records...)
		if listCalls > 1 {
			result[0].Capabilities = nil
		}
		return result
	}
	if _, err := service.Deploy(context.Background(), DeployRequest{HostID: "host-1", AgentID: "source-1", ServiceURL: "https://service.example:9443"}, nil); err == nil {
		t.Fatal("capability loss was accepted")
	}
	if !remote.cleaned {
		t.Fatal("capability-loss migration was not rolled back")
	}
}

func fakeELF(machine uint16) []byte {
	value := make([]byte, 64)
	copy(value, []byte{0x7f, 'E', 'L', 'F'})
	value[4], value[5] = 2, 1
	binary.LittleEndian.PutUint16(value[18:20], machine)
	return value
}

func fakePE(machine uint16) []byte {
	value := make([]byte, 128)
	copy(value, []byte{'M', 'Z'})
	binary.LittleEndian.PutUint32(value[0x3c:0x40], 64)
	copy(value[64:68], []byte{'P', 'E', 0, 0})
	binary.LittleEndian.PutUint16(value[68:70], machine)
	return value
}

type deploymentStore struct {
	host                      domain.RemoteHost
	agents                    []store.AgentRecord
	boundAgentID, boundHostID string
	listAgentsHook            func([]store.AgentRecord) []store.AgentRecord
}

func (s *deploymentStore) ListRemoteHosts(context.Context) ([]domain.RemoteHost, error) {
	return []domain.RemoteHost{s.host}, nil
}
func (*deploymentStore) RemoteHostPrivateKeySecretID(context.Context, string) (string, error) {
	return "key-1", nil
}
func (s *deploymentStore) ListAgents(context.Context) ([]store.AgentRecord, error) {
	if s.listAgentsHook != nil {
		return s.listAgentsHook(s.agents), nil
	}
	return s.agents, nil
}
func (s *deploymentStore) BindAgentRemoteHost(_ context.Context, agentID, hostID string) error {
	s.boundAgentID, s.boundHostID = agentID, hostID
	return nil
}

type deploymentSecrets struct{}

func (deploymentSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte("PRIVATE KEY"), nil
}

type deploymentControl struct{}

func (*deploymentControl) CreateEnrollmentToken(context.Context, time.Duration) (string, error) {
	return "enrollment-token", nil
}
func (*deploymentControl) CAPEM() string { return "CA PEM" }

type staticArtifacts struct{}

func (staticArtifacts) Resolve(Platform) (Artifact, error) {
	return Artifact{Content: []byte("AGENT BINARY"), SHA256: "digest"}, nil
}

type deploymentDialer struct{ remote *deploymentRemote }

func (d deploymentDialer) Dial(context.Context, Target) (DeploymentRemote, error) {
	return d.remote, nil
}

type deploymentRemote struct {
	platform                      Platform
	files                         map[RemoteFile][]byte
	commands                      []string
	activated, finalized, cleaned bool
	activateErr                   error
	finalizeErr                   error
}

func (r *deploymentRemote) Probe(context.Context) (Platform, error) { return r.platform, nil }
func (r *deploymentRemote) Upload(_ context.Context, file RemoteFile, content []byte) error {
	if r.files == nil {
		r.files = map[RemoteFile][]byte{}
	}
	r.files[file] = append([]byte(nil), content...)
	return nil
}
func (r *deploymentRemote) Activate(context.Context, Platform) error {
	r.activated = true
	return r.activateErr
}
func (r *deploymentRemote) Finalize(context.Context, Platform) error {
	r.finalized = true
	return r.finalizeErr
}
func (r *deploymentRemote) Cleanup(context.Context, Platform) error { r.cleaned = true; return nil }
func (*deploymentRemote) Close() error                              { return nil }
func containsSecret(commands []string, secret string) bool {
	for _, command := range commands {
		if command == secret {
			return true
		}
	}
	return false
}
