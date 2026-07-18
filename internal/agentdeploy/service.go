package agentdeploy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type Artifact struct {
	Path    string
	Content []byte
	SHA256  string
}

type ArtifactSource interface {
	Resolve(Platform) (Artifact, error)
}

type ArtifactResolver struct {
	Dir       string
	LocalOS   string
	LocalArch string
}

func ArtifactFilenames() []string {
	return []string{
		"shadoc-agent-linux-amd64",
		"shadoc-agent-linux-arm64",
		"shadoc-agent-darwin-amd64",
		"shadoc-agent-darwin-arm64",
		"shadoc-agent-windows-amd64.exe",
		"shadoc-agent-windows-arm64.exe",
	}
}

func (r ArtifactResolver) Resolve(platform Platform) (Artifact, error) {
	if platform.OS != "linux" && platform.OS != "darwin" && platform.OS != "windows" || platform.Arch != "amd64" && platform.Arch != "arm64" {
		return Artifact{}, errors.New("unsupported Agent artifact platform")
	}
	dir := filepath.Clean(r.Dir)
	if dir == "." || dir == "" {
		return Artifact{}, errors.New("Agent artifact directory is required")
	}
	path := filepath.Join(dir, "shadoc-agent-"+platform.OS+"-"+platform.Arch)
	if platform.OS == "windows" {
		path += ".exe"
	}
	if platform.OS != "windows" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			legacyPath := filepath.Join(dir, "restic-control-agent-"+platform.OS+"-"+platform.Arch)
			if legacyInfo, legacyErr := os.Stat(legacyPath); legacyErr == nil && legacyInfo.Mode().IsRegular() {
				path = legacyPath
			}
		}
	}
	localOS, localArch := r.LocalOS, r.LocalArch
	if localOS == "" {
		localOS = runtime.GOOS
	}
	if localArch == "" {
		localArch = runtime.GOARCH
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && platform.OS == localOS && platform.Arch == localArch {
		path = filepath.Join(dir, "shadoc-agent")
		if platform.OS != "windows" {
			if _, currentErr := os.Stat(path); errors.Is(currentErr, os.ErrNotExist) {
				path = filepath.Join(dir, "restic-control-agent")
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 20 || info.Size() > 256<<20 {
		return Artifact{}, fmt.Errorf("未找到适用于 %s/%s 的 Agent 安装文件；请重新执行完整构建，并保持控制服务与 Agent 文件位于同一目录", platform.OS, platform.Arch)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, err
	}
	if !matchesExecutable(content, platform) {
		return Artifact{}, fmt.Errorf("Agent 安装文件与远程主机平台 %s/%s 不匹配；请重新执行完整构建", platform.OS, platform.Arch)
	}
	digest := sha256.Sum256(content)
	return Artifact{Path: path, Content: content, SHA256: hex.EncodeToString(digest[:])}, nil
}

func matchesExecutable(content []byte, platform Platform) bool {
	if len(content) < 20 {
		return false
	}
	if platform.OS == "linux" {
		if string(content[:4]) != "\x7fELF" || content[5] != 1 {
			return false
		}
		machine := binary.LittleEndian.Uint16(content[18:20])
		return machine == map[string]uint16{"amd64": 62, "arm64": 183}[platform.Arch]
	}
	if platform.OS == "windows" {
		if len(content) < 70 || string(content[:2]) != "MZ" {
			return false
		}
		offset := int(binary.LittleEndian.Uint32(content[0x3c:0x40]))
		if offset < 0 || offset+6 > len(content) || string(content[offset:offset+4]) != "PE\x00\x00" {
			return false
		}
		machine := binary.LittleEndian.Uint16(content[offset+4 : offset+6])
		return machine == map[string]uint16{"amd64": 0x8664, "arm64": 0xaa64}[platform.Arch]
	}
	if len(content) < 8 || binary.LittleEndian.Uint32(content[:4]) != 0xfeedfacf {
		return false
	}
	cpu := binary.LittleEndian.Uint32(content[4:8])
	return cpu == map[string]uint32{"amd64": 0x01000007, "arm64": 0x0100000c}[platform.Arch]
}

type DeploymentStorage interface {
	ListRemoteHosts(context.Context) ([]domain.RemoteHost, error)
	RemoteHostPrivateKeySecretID(context.Context, string) (string, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	BindAgentRemoteHost(context.Context, string, string) error
}

type DeploymentSecrets interface {
	Get(context.Context, string, string) ([]byte, error)
}
type EnrollmentControl interface {
	CreateEnrollmentToken(context.Context, time.Duration) (string, error)
	CAPEM() string
}

type DeploymentRemote interface {
	Probe(context.Context) (Platform, error)
	Upload(context.Context, RemoteFile, []byte) error
	Activate(context.Context, Platform) error
	Finalize(context.Context, Platform) error
	Cleanup(context.Context, Platform) error
	Close() error
}
type DeploymentDialer interface {
	Dial(context.Context, Target) (DeploymentRemote, error)
}

type SSHDialer struct{}

func (SSHDialer) Dial(ctx context.Context, target Target) (DeploymentRemote, error) {
	connection, err := DialPinned(ctx, target)
	if err != nil {
		return nil, err
	}
	return &sshDeploymentRemote{Remote: NewRemote(connection), connection: connection}, nil
}

func (SSHDialer) DialRemoval(ctx context.Context, target Target) (RemovalRemote, error) {
	connection, err := DialPinned(ctx, target)
	if err != nil {
		return nil, err
	}
	return &sshDeploymentRemote{Remote: NewRemote(connection), connection: connection}, nil
}

type SSHRemovalDialer struct{}

func (SSHRemovalDialer) Dial(ctx context.Context, target Target) (RemovalRemote, error) {
	return (SSHDialer{}).DialRemoval(ctx, target)
}

type sshDeploymentRemote struct {
	*Remote
	connection *SSHRemote
}

func (r *sshDeploymentRemote) Close() error { return r.connection.Close() }

type DeployRequest struct{ HostID, AgentID, ServiceURL string }
type DeployResult struct{ AgentID, HostID, Platform, ArtifactSHA256 string }
type StageReporter func(string)

type Service struct {
	store            DeploymentStorage
	secrets          DeploymentSecrets
	control          EnrollmentControl
	artifacts        ArtifactSource
	dialer           DeploymentDialer
	now              func() time.Time
	pollInterval     time.Duration
	heartbeatTimeout time.Duration
}

func NewService(storage DeploymentStorage, secrets DeploymentSecrets, control EnrollmentControl, artifacts ArtifactSource, dialer DeploymentDialer, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, secrets: secrets, control: control, artifacts: artifacts, dialer: dialer, now: now, pollInterval: time.Second, heartbeatTimeout: 90 * time.Second}
}

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Service) Deploy(ctx context.Context, request DeployRequest, report StageReporter) (result DeployResult, resultErr error) {
	if s == nil || s.store == nil || s.secrets == nil || s.control == nil || s.artifacts == nil || s.dialer == nil {
		return result, errors.New("Agent deployer is not configured")
	}
	if !agentIDPattern.MatchString(request.AgentID) {
		return result, errors.New("Agent ID must use 1-64 letters, numbers, dots, underscores, or dashes")
	}
	parsedURL, err := url.Parse(request.ServiceURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return result, errors.New("externally reachable Agent Service HTTPS URL is required")
	}
	hosts, err := s.store.ListRemoteHosts(ctx)
	if err != nil {
		return result, err
	}
	var host domain.RemoteHost
	for _, candidate := range hosts {
		if candidate.ID == request.HostID {
			host = candidate
			break
		}
	}
	if host.ID == "" {
		return result, sql.ErrNoRows
	}
	secretID, err := s.store.RemoteHostPrivateKeySecretID(ctx, host.ID)
	if err != nil {
		return result, err
	}
	privateKey, err := s.secrets.Get(ctx, secretID, "ssh-private-key")
	if err != nil {
		return result, err
	}
	defer clear(privateKey)
	if report != nil {
		report("probing")
	}
	remote, err := s.dialer.Dial(ctx, Target{Host: host.Host, Port: host.Port, Username: host.Username, PrivateKey: privateKey, KnownHosts: host.HostFingerprint})
	if err != nil {
		return result, err
	}
	defer remote.Close()
	platform, err := remote.Probe(ctx)
	if err != nil {
		return result, err
	}
	artifact, err := s.artifacts.Resolve(platform)
	if err != nil {
		return result, err
	}
	token, err := s.control.CreateEnrollmentToken(ctx, 15*time.Minute)
	if err != nil {
		return result, err
	}
	existingCapabilities := []string(nil)
	existingAgents, err := s.store.ListAgents(ctx)
	if err != nil {
		return result, err
	}
	for _, agent := range existingAgents {
		if agent.ID == request.AgentID && agent.RevokedAt == nil {
			existingCapabilities = append(existingCapabilities, agent.Capabilities...)
			break
		}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = remote.Cleanup(context.WithoutCancel(ctx), platform)
		}
	}()
	if report != nil {
		report("uploading")
	}
	files := []struct {
		kind    RemoteFile
		content []byte
	}{
		{BinaryFile, artifact.Content}, {CAFile, []byte(s.control.CAPEM())}, {TokenFile, []byte(token)},
	}
	serviceDefinition, err := deploymentServiceDefinition(platform, request)
	if err != nil {
		return result, err
	}
	if platform.OS == "linux" {
		files = append(files, struct {
			kind    RemoteFile
			content []byte
		}{LinuxUnitFile, serviceDefinition})
	} else if platform.OS == "darwin" {
		files = append(files, struct {
			kind    RemoteFile
			content []byte
		}{DarwinPlistFile, serviceDefinition})
	} else {
		files = append(files, struct {
			kind    RemoteFile
			content []byte
		}{WindowsServiceFile, serviceDefinition})
	}
	for _, file := range files {
		if err := remote.Upload(ctx, file.kind, file.content); err != nil {
			return result, err
		}
	}
	if report != nil {
		report("activating")
	}
	if err := remote.Activate(ctx, platform); err != nil {
		return result, err
	}
	if report != nil {
		report("waiting_for_heartbeat")
	}
	started := s.now().UTC()
	waitCtx, cancel := context.WithTimeout(ctx, s.heartbeatTimeout)
	defer cancel()
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		agents, listErr := s.store.ListAgents(waitCtx)
		if listErr != nil {
			return result, listErr
		}
		for _, agent := range agents {
			missingCapability := slices.ContainsFunc(existingCapabilities, func(capability string) bool {
				return !slices.Contains(agent.Capabilities, capability)
			})
			if agent.ID == request.AgentID && agent.RevokedAt == nil && agent.LastHeartbeatAt != nil && !agent.LastHeartbeatAt.Before(started) && !missingCapability {
				if err := s.store.BindAgentRemoteHost(context.WithoutCancel(ctx), request.AgentID, request.HostID); err != nil {
					return result, fmt.Errorf("bind Agent to remote host: %w", err)
				}
				if report != nil {
					report("finalizing")
				}
				if err := remote.Finalize(context.WithoutCancel(ctx), platform); err != nil {
					return result, fmt.Errorf("finalize Agent service migration: %w", err)
				}
				succeeded = true
				return DeployResult{AgentID: request.AgentID, HostID: request.HostID, Platform: platform.OS + "/" + platform.Arch, ArtifactSHA256: artifact.SHA256}, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return result, fmt.Errorf("wait for Agent heartbeat: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func deploymentServiceDefinition(platform Platform, request DeployRequest) ([]byte, error) {
	if !agentIDPattern.MatchString(request.AgentID) || strings.ContainsAny(request.ServiceURL, "\r\n\x00\"'") {
		return nil, errors.New("unsafe Agent service definition value")
	}
	if platform.OS == "linux" {
		serviceURL := strings.ReplaceAll(request.ServiceURL, "%", "%%")
		unit := `[Unit]
Description=Shadoc source Agent
After=network-online.target

[Service]
ExecStart=%h/.local/bin/shadoc-agent --service "` + serviceURL + `" --id "` + request.AgentID + `" --data-dir %h/.local/share/shadoc-agent --ca-file %h/.config/shadoc-agent/ca.crt --enrollment-token-file %h/.config/shadoc-agent/enrollment.token
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`
		return []byte(unit), nil
	}
	if platform.OS == "darwin" {
		home := html.EscapeString(platform.Home)
		serviceURL := html.EscapeString(request.ServiceURL)
		agentID := html.EscapeString(request.AgentID)
		plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>io.shadoc-agent</string><key>ProgramArguments</key><array><string>` + home + `/.local/bin/shadoc-agent</string><string>--service</string><string>` + serviceURL + `</string><string>--id</string><string>` + agentID + `</string><string>--data-dir</string><string>` + home + `/.local/share/shadoc-agent</string><string>--ca-file</string><string>` + home + `/.config/shadoc-agent/ca.crt</string><string>--enrollment-token-file</string><string>` + home + `/.config/shadoc-agent/enrollment.token</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict></dict></plist>`
		return []byte(plist), nil
	}
	if platform.OS == "windows" {
		script := `$ErrorActionPreference='Stop'
$root=Join-Path $env:ProgramData 'shadoc-agent'
$binary=Join-Path $root 'shadoc-agent.exe'
$arguments='--service "` + request.ServiceURL + `" --id "` + request.AgentID + `" --data-dir "'+$root+'\data" --ca-file "'+$root+'\ca.crt" --enrollment-token-file "'+$root+'\enrollment.token"'
$command='"'+$binary+'" '+$arguments
if (Get-Service shadoc-agent -ErrorAction SilentlyContinue) {
  Stop-Service shadoc-agent -Force -ErrorAction SilentlyContinue
  & sc.exe config shadoc-agent binPath= $command start= auto DisplayName= 'Shadoc Agent' | Out-Null
} else {
  & sc.exe create shadoc-agent binPath= $command start= auto DisplayName= 'Shadoc Agent' | Out-Null
}
if ($LASTEXITCODE -ne 0) { throw 'unable to create Windows service' }
& sc.exe start shadoc-agent | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'unable to start Windows service' }
`
		return []byte(script), nil
	}
	return nil, errors.New("unsupported Agent service platform")
}
