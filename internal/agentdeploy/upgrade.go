package agentdeploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type UpgradeStorage interface {
	ListRemoteHosts(context.Context) ([]domain.RemoteHost, error)
	RemoteHostPrivateKeySecretID(context.Context, string) (string, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	BeginAgentDrain(context.Context, string, time.Time) error
	EndAgentDrain(context.Context, string) error
	AgentActiveWorkCount(context.Context, string) (int, error)
}

type UpgradeRemote interface {
	Probe(context.Context) (Platform, error)
	StageUpgrade(context.Context, []byte) error
	VerifyStagedVersion(context.Context, Platform, string) error
	ActivateUpgrade(context.Context, Platform) error
	RollbackUpgrade(context.Context, Platform) error
	FinalizeUpgrade(context.Context, Platform) error
	Restart(context.Context, Platform) error
	Close() error
}

type UpgradeDialer interface {
	Dial(context.Context, Target) (UpgradeRemote, error)
}

type SSHUpgradeDialer struct{}

func (SSHUpgradeDialer) Dial(ctx context.Context, target Target) (UpgradeRemote, error) {
	connection, err := DialPinned(ctx, target)
	if err != nil {
		return nil, err
	}
	return &sshDeploymentRemote{Remote: NewRemote(connection), connection: connection}, nil
}

type UpgradeRequest struct {
	AgentID       string
	TargetVersion string
}

type UpgradeResult struct {
	AgentID        string `json:"agentId"`
	HostID         string `json:"hostId"`
	Platform       string `json:"platform"`
	FromVersion    string `json:"fromVersion"`
	ToVersion      string `json:"toVersion"`
	ArtifactSHA256 string `json:"artifactSha256"`
}

// ToolProbeResult describes a managed Agent whose fixed startup probes have
// been refreshed. Tool versions and capabilities are reported only by the
// following authenticated heartbeat; they are never accepted from SSH output.
type ToolProbeResult struct {
	AgentID  string `json:"agentId"`
	HostID   string `json:"hostId"`
	Platform string `json:"platform"`
}

// HeartbeatProbeResult describes a managed Agent whose service was restarted
// and whose next authenticated heartbeat was observed by the Service.
type HeartbeatProbeResult struct {
	AgentID  string `json:"agentId"`
	HostID   string `json:"hostId"`
	Platform string `json:"platform"`
}

type UpgradeService struct {
	store            UpgradeStorage
	secrets          DeploymentSecrets
	artifacts        ArtifactSource
	dialer           UpgradeDialer
	now              func() time.Time
	pollInterval     time.Duration
	drainTimeout     time.Duration
	heartbeatTimeout time.Duration
}

func NewUpgradeService(storage UpgradeStorage, secrets DeploymentSecrets, artifacts ArtifactSource, dialer UpgradeDialer, now func() time.Time) *UpgradeService {
	if now == nil {
		now = time.Now
	}
	return &UpgradeService{
		store: storage, secrets: secrets, artifacts: artifacts, dialer: dialer, now: now,
		pollInterval: time.Second, drainTimeout: 2 * time.Minute, heartbeatTimeout: 90 * time.Second,
	}
}

func (s *UpgradeService) Upgrade(ctx context.Context, request UpgradeRequest, report StageReporter) (result UpgradeResult, resultErr error) {
	if s == nil || s.store == nil || s.secrets == nil || s.artifacts == nil || s.dialer == nil {
		return result, errors.New("Agent upgrader is not configured")
	}
	if !agentIDPattern.MatchString(request.AgentID) {
		return result, errors.New("valid Agent ID is required")
	}
	if value := strings.TrimSpace(request.TargetVersion); value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
		return result, errors.New("bounded target Agent version is required")
	}
	request.TargetVersion = strings.TrimSpace(request.TargetVersion)

	agent, host, err := s.findManagedAgent(ctx, request.AgentID)
	if err != nil {
		return result, err
	}
	repairManagedRestic := agent.BuildVersion == request.TargetVersion && agent.OS == "linux" &&
		!slices.Contains(agent.Capabilities, agentprotocol.ManagedResticInstallCapability)
	if agent.BuildVersion == request.TargetVersion && !repairManagedRestic {
		return result, errors.New("Agent already reports the target version")
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

	reportStage(report, "probing")
	remote, err := s.dialer.Dial(ctx, Target{
		Host: host.Host, Port: host.Port, Username: host.Username,
		PrivateKey: privateKey, KnownHosts: host.HostFingerprint,
	})
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

	reportStage(report, "draining_agent")
	if err := s.store.BeginAgentDrain(ctx, agent.ID, s.now().UTC()); err != nil {
		return result, err
	}
	defer func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.EndAgentDrain(endCtx, agent.ID); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("resume Agent assignments: %w", err)
		}
	}()
	if err := s.waitForDrain(ctx, agent.ID); err != nil {
		return result, err
	}

	reportStage(report, "staging_agent_upgrade")
	if err := remote.StageUpgrade(ctx, artifact.Content); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if cleanupErr := remote.FinalizeUpgrade(cleanupCtx, platform); cleanupErr != nil {
			return result, fmt.Errorf("stage Agent upgrade: %v; clean partial Agent upgrade: %w", err, cleanupErr)
		}
		return result, fmt.Errorf("stage Agent upgrade: %w", err)
	}
	reportStage(report, "verifying_staged_agent_upgrade")
	if err := remote.VerifyStagedVersion(ctx, platform, request.TargetVersion); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if cleanupErr := remote.FinalizeUpgrade(cleanupCtx, platform); cleanupErr != nil {
			return result, fmt.Errorf("verify staged Agent upgrade: %v; clean partial Agent upgrade: %w", err, cleanupErr)
		}
		return result, fmt.Errorf("verify staged Agent upgrade: %w", err)
	}
	reportStage(report, "activating_agent_upgrade")
	activatedAt := s.now().UTC()
	if err := remote.ActivateUpgrade(ctx, platform); err != nil {
		return result, s.rollback(ctx, remote, platform, report, fmt.Errorf("activate Agent upgrade: %w", err))
	}
	reportStage(report, "waiting_for_agent_upgrade")
	requiredCapability := ""
	if agent.OS == "linux" {
		requiredCapability = agentprotocol.ManagedResticInstallCapability
	}
	if err := s.waitForVersionHeartbeat(ctx, agent.ID, request.TargetVersion, activatedAt, requiredCapability); err != nil {
		return result, s.rollback(ctx, remote, platform, report, err)
	}
	if err := remote.FinalizeUpgrade(ctx, platform); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return result, s.rollback(ctx, remote, platform, report, fmt.Errorf("finalize Agent upgrade: %w", err))
		}
		return result, fmt.Errorf("finalize Agent upgrade: %w", err)
	}
	reportStage(report, "agent_upgrade_verified")
	return UpgradeResult{
		AgentID: agent.ID, HostID: host.ID, Platform: platform.OS + "/" + platform.Arch,
		FromVersion: agent.BuildVersion, ToVersion: request.TargetVersion, ArtifactSHA256: artifact.SHA256,
	}, nil
}

// ReprobeTools restarts a managed Agent after draining its active work. The
// Agent's ordinary startup path then runs only its fixed Restic and rsync
// version probes and reports the resulting capabilities over mTLS.
func (s *UpgradeService) ReprobeTools(ctx context.Context, agentID string, report StageReporter) (result ToolProbeResult, resultErr error) {
	if s == nil || s.store == nil || s.secrets == nil || s.dialer == nil {
		return result, errors.New("Agent tool prober is not configured")
	}
	if !agentIDPattern.MatchString(agentID) {
		return result, errors.New("valid Agent ID is required")
	}
	agent, host, err := s.findManagedAgent(ctx, agentID)
	if err != nil {
		return result, err
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

	reportStage(report, "probing")
	remote, err := s.dialer.Dial(ctx, Target{
		Host: host.Host, Port: host.Port, Username: host.Username,
		PrivateKey: privateKey, KnownHosts: host.HostFingerprint,
	})
	if err != nil {
		return result, err
	}
	defer remote.Close()
	platform, err := remote.Probe(ctx)
	if err != nil {
		return result, err
	}

	reportStage(report, "draining_agent")
	if err := s.store.BeginAgentDrain(ctx, agent.ID, s.now().UTC()); err != nil {
		return result, err
	}
	defer func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.EndAgentDrain(endCtx, agent.ID); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("resume Agent assignments: %w", err)
		}
	}()
	if err := s.waitForDrain(ctx, agent.ID); err != nil {
		return result, err
	}

	reportStage(report, "restarting_agent_for_tool_probe")
	if err := remote.Restart(ctx, platform); err != nil {
		return result, err
	}
	reportStage(report, "waiting_for_agent_tool_probe")
	if err := s.waitForHeartbeat(ctx, agent.ID, s.now().UTC()); err != nil {
		return result, err
	}
	reportStage(report, "agent_tool_probe_verified")
	return ToolProbeResult{AgentID: agent.ID, HostID: host.ID, Platform: platform.OS + "/" + platform.Arch}, nil
}

// ProbeHeartbeat restarts a managed Agent after draining its active work and
// waits for the next authenticated heartbeat. The restart still uses the
// fixed platform command set; this operation does not claim tool capabilities.
func (s *UpgradeService) ProbeHeartbeat(ctx context.Context, agentID string, report StageReporter) (result HeartbeatProbeResult, resultErr error) {
	if s == nil || s.store == nil || s.secrets == nil || s.dialer == nil {
		return result, errors.New("Agent heartbeat prober is not configured")
	}
	if !agentIDPattern.MatchString(agentID) {
		return result, errors.New("valid Agent ID is required")
	}
	agent, host, err := s.findManagedAgent(ctx, agentID)
	if err != nil {
		return result, err
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

	reportStage(report, "probing")
	remote, err := s.dialer.Dial(ctx, Target{
		Host: host.Host, Port: host.Port, Username: host.Username,
		PrivateKey: privateKey, KnownHosts: host.HostFingerprint,
	})
	if err != nil {
		return result, err
	}
	defer remote.Close()
	platform, err := remote.Probe(ctx)
	if err != nil {
		return result, err
	}

	reportStage(report, "draining_agent")
	if err := s.store.BeginAgentDrain(ctx, agent.ID, s.now().UTC()); err != nil {
		return result, err
	}
	defer func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.EndAgentDrain(endCtx, agent.ID); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("resume Agent assignments: %w", err)
		}
	}()
	if err := s.waitForDrain(ctx, agent.ID); err != nil {
		return result, err
	}

	restartedAt := s.now().UTC()
	reportStage(report, "restarting_agent_for_heartbeat")
	if err := remote.Restart(ctx, platform); err != nil {
		return result, err
	}
	reportStage(report, "waiting_for_agent_heartbeat")
	if err := s.waitForHeartbeat(ctx, agent.ID, restartedAt); err != nil {
		return result, err
	}
	reportStage(report, "agent_heartbeat_verified")
	return HeartbeatProbeResult{AgentID: agent.ID, HostID: host.ID, Platform: platform.OS + "/" + platform.Arch}, nil
}

func (s *UpgradeService) findManagedAgent(ctx context.Context, agentID string) (store.AgentRecord, domain.RemoteHost, error) {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return store.AgentRecord{}, domain.RemoteHost{}, err
	}
	var agent store.AgentRecord
	for _, candidate := range agents {
		if candidate.ID == agentID {
			agent = candidate
			break
		}
	}
	if agent.ID == "" || agent.RevokedAt != nil {
		return agent, domain.RemoteHost{}, sql.ErrNoRows
	}
	if agent.RemoteHostID == "" || agent.UninstalledAt != nil {
		return agent, domain.RemoteHost{}, errors.New("Agent is not managed through a remote host")
	}
	hosts, err := s.store.ListRemoteHosts(ctx)
	if err != nil {
		return agent, domain.RemoteHost{}, err
	}
	for _, host := range hosts {
		if host.ID == agent.RemoteHostID {
			return agent, host, nil
		}
	}
	return agent, domain.RemoteHost{}, sql.ErrNoRows
}

func (s *UpgradeService) waitForDrain(ctx context.Context, agentID string) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.drainTimeout)
	defer cancel()
	for {
		count, err := s.store.AgentActiveWorkCount(waitCtx, agentID)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if err := waitForPoll(waitCtx, s.pollInterval); err != nil {
			return fmt.Errorf("wait for Agent work to drain: %w", err)
		}
	}
}

func (s *UpgradeService) waitForVersionHeartbeat(ctx context.Context, agentID, targetVersion string, activatedAt time.Time, requiredCapability string) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.heartbeatTimeout)
	defer cancel()
	for {
		agents, err := s.store.ListAgents(waitCtx)
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if agent.ID == agentID && agent.RevokedAt == nil && agent.Status == "online" &&
				agent.BuildVersion == targetVersion && agent.ProtocolMin <= agentprotocol.Version && agent.ProtocolMax >= agentprotocol.Version &&
				agent.LastHeartbeatAt != nil && !agent.LastHeartbeatAt.Before(activatedAt) &&
				(requiredCapability == "" || slices.Contains(agent.Capabilities, requiredCapability)) {
				return nil
			}
		}
		if err := waitForPoll(waitCtx, s.pollInterval); err != nil {
			return fmt.Errorf("wait for target-version Agent heartbeat: %w", err)
		}
	}
}

func (s *UpgradeService) waitForHeartbeat(ctx context.Context, agentID string, restartedAt time.Time) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.heartbeatTimeout)
	defer cancel()
	for {
		agents, err := s.store.ListAgents(waitCtx)
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if agent.ID == agentID && agent.RevokedAt == nil && agent.Status == "online" &&
				agent.ProtocolMin <= agentprotocol.Version && agent.ProtocolMax >= agentprotocol.Version &&
				agent.LastHeartbeatAt != nil && !agent.LastHeartbeatAt.Before(restartedAt) {
				return nil
			}
		}
		if err := waitForPoll(waitCtx, s.pollInterval); err != nil {
			return fmt.Errorf("wait for Agent tool-probe heartbeat: %w", err)
		}
	}
}

func (s *UpgradeService) rollback(ctx context.Context, remote UpgradeRemote, platform Platform, report StageReporter, cause error) error {
	reportStage(report, "rolling_back_agent_upgrade")
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := remote.RollbackUpgrade(rollbackCtx, platform); err != nil {
		return fmt.Errorf("%v; Agent rollback also failed: %w", cause, err)
	}
	return fmt.Errorf("%w; previous Agent binary restored", cause)
}

func reportStage(report StageReporter, stage string) {
	if report != nil {
		report(stage)
	}
}

func waitForPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
