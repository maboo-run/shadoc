// Package agenttool installs narrowly enumerated execution tools on managed
// Agents without exposing a general remote command surface.
package agenttool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/installer"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	ListRemoteHosts(context.Context) ([]domain.RemoteHost, error)
	RemoteHostPrivateKeySecretID(context.Context, string) (string, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	BeginAgentDrain(context.Context, string, time.Time) error
	EndAgentDrain(context.Context, string) error
	AgentActiveWorkCount(context.Context, string) (int, error)
}

type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}

type ArtifactSource interface {
	Resolve(context.Context, string, string, string) (installer.Artifact, error)
}

type Remote interface {
	Probe(context.Context) (agentdeploy.Platform, error)
	StageRestic(context.Context, []byte) error
	ActivateRestic(context.Context) error
	RollbackRestic(context.Context) error
	CleanupStagedRestic(context.Context) error
	FinalizeRestic(context.Context) error
	Close() error
}

type Dialer interface {
	Dial(context.Context, agentdeploy.Target) (Remote, error)
}

type StageReporter func(string)

type InstallRequest struct {
	AgentID string
	Version string
}

type InstallResult struct {
	AgentID        string `json:"agentId"`
	HostID         string `json:"hostId"`
	Platform       string `json:"platform"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifactSha256"`
}

type Service struct {
	store            Storage
	secrets          Secrets
	artifacts        ArtifactSource
	dialer           Dialer
	now              func() time.Time
	pollInterval     time.Duration
	drainTimeout     time.Duration
	heartbeatTimeout time.Duration
}

func New(storage Storage, secrets Secrets, artifacts ArtifactSource, dialer Dialer, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: storage, secrets: secrets, artifacts: artifacts, dialer: dialer, now: now,
		pollInterval: time.Second, drainTimeout: 2 * time.Minute, heartbeatTimeout: 90 * time.Second,
	}
}

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Service) InstallRestic(ctx context.Context, request InstallRequest, report StageReporter) (result InstallResult, resultErr error) {
	if s == nil || s.store == nil || s.secrets == nil || s.artifacts == nil || s.dialer == nil {
		return result, errors.New("Agent Restic installer is not configured")
	}
	request.AgentID, request.Version = strings.TrimSpace(request.AgentID), strings.TrimSpace(request.Version)
	if !agentIDPattern.MatchString(request.AgentID) || request.Version == "" || len(request.Version) > 64 || strings.ContainsAny(request.Version, "/\\\x00\r\n") {
		return result, errors.New("valid Agent ID and bounded Restic version are required")
	}
	agent, host, err := s.findManagedAgent(ctx, request.AgentID)
	if err != nil {
		return result, err
	}
	if agent.ResticVersion == request.Version && slices.Contains(agent.Capabilities, "restic") && slices.Contains(agent.Capabilities, "restic-restore") && slices.Contains(agent.Capabilities, "filesystem-restore-target") {
		return result, errors.New("Agent already reports the target Restic version")
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
	remote, err := s.dialer.Dial(ctx, agentdeploy.Target{
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
	if platform.OS != "linux" || platform.Arch != "amd64" && platform.Arch != "arm64" {
		return result, errors.New("one-click Restic installation currently supports managed Linux Agents only")
	}
	reportStage(report, "downloading_agent_restic")
	artifact, err := s.artifacts.Resolve(ctx, request.Version, platform.OS, platform.Arch)
	if err != nil {
		return result, err
	}

	reportStage(report, "draining_agent")
	if err := s.store.BeginAgentDrain(ctx, agent.ID, s.now().UTC()); err != nil {
		return result, err
	}
	defer func() {
		resumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.EndAgentDrain(resumeCtx, agent.ID); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("resume Agent assignments: %w", err)
		}
	}()
	if err := s.waitForDrain(ctx, agent.ID); err != nil {
		return result, err
	}

	reportStage(report, "staging_agent_restic")
	if err := remote.StageRestic(ctx, artifact.Content); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if cleanupErr := remote.CleanupStagedRestic(cleanupCtx); cleanupErr != nil {
			return result, fmt.Errorf("stage Agent Restic: %v; clean staged Restic: %w", err, cleanupErr)
		}
		return result, fmt.Errorf("stage Agent Restic: %w", err)
	}
	reportStage(report, "activating_agent_restic")
	activatedAt := s.now().UTC()
	if err := remote.ActivateRestic(ctx); err != nil {
		return result, s.rollback(ctx, remote, report, fmt.Errorf("activate Agent Restic: %w", err))
	}
	reportStage(report, "waiting_for_agent_restic")
	if err := s.waitForHeartbeat(ctx, agent.ID, request.Version, activatedAt); err != nil {
		return result, s.rollback(ctx, remote, report, err)
	}
	if err := remote.FinalizeRestic(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return result, s.rollback(ctx, remote, report, fmt.Errorf("finalize Agent Restic: %w", err))
		}
		return result, fmt.Errorf("finalize Agent Restic: %w", err)
	}
	reportStage(report, "agent_restic_verified")
	return InstallResult{
		AgentID: agent.ID, HostID: host.ID, Platform: platform.OS + "/" + platform.Arch,
		Version: request.Version, ArtifactSHA256: artifact.SHA256,
	}, nil
}

func (s *Service) findManagedAgent(ctx context.Context, agentID string) (store.AgentRecord, domain.RemoteHost, error) {
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
	if agent.Status != "online" || agent.LastHeartbeatAt == nil || s.now().UTC().Sub(agent.LastHeartbeatAt.UTC()) > 2*time.Minute {
		return agent, domain.RemoteHost{}, errors.New("Agent must be online before installing Restic")
	}
	if !slices.Contains(agent.Capabilities, agentprotocol.ManagedResticInstallCapability) {
		return agent, domain.RemoteHost{}, errors.New("Agent does not support managed Restic installation; upgrade or redeploy the Agent first")
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

func (s *Service) waitForDrain(ctx context.Context, agentID string) error {
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
		if err := wait(waitCtx, s.pollInterval); err != nil {
			return fmt.Errorf("wait for Agent work to drain: %w", err)
		}
	}
}

func (s *Service) waitForHeartbeat(ctx context.Context, agentID, version string, activatedAt time.Time) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.heartbeatTimeout)
	defer cancel()
	var observed store.AgentRecord
	for {
		agents, err := s.store.ListAgents(waitCtx)
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if agent.ID == agentID {
				observed = agent
			}
			if agent.ID == agentID && agent.RevokedAt == nil && agent.Status == "online" && agent.ResticVersion == version &&
				slices.Contains(agent.Capabilities, "restic") && slices.Contains(agent.Capabilities, "restic-restore") && slices.Contains(agent.Capabilities, "filesystem-restore-target") &&
				agent.LastHeartbeatAt != nil && !agent.LastHeartbeatAt.Before(activatedAt) {
				return nil
			}
		}
		if err := wait(waitCtx, s.pollInterval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return heartbeatVerificationError(agentID, version, observed)
			}
			return fmt.Errorf("wait for Agent Restic capability heartbeat: %w", err)
		}
	}
}

func heartbeatVerificationError(agentID, targetVersion string, observed store.AgentRecord) error {
	required := []string{"restic", "restic-restore", "filesystem-restore-target"}
	missing := make([]string, 0, len(required))
	for _, capability := range required {
		if !slices.Contains(observed.Capabilities, capability) {
			missing = append(missing, capability)
		}
	}
	reportedVersion := strings.TrimSpace(observed.ResticVersion)
	if reportedVersion == "" {
		reportedVersion = "not reported"
	}
	lastHeartbeat := "not reported"
	if observed.LastHeartbeatAt != nil {
		lastHeartbeat = observed.LastHeartbeatAt.UTC().Format(time.RFC3339)
	}
	return fmt.Errorf("Agent %s did not verify Restic %s before timeout: reported Restic %s; missing capabilities: %s; last heartbeat: %s", agentID, targetVersion, reportedVersion, strings.Join(missing, ", "), lastHeartbeat)
}

func (s *Service) rollback(ctx context.Context, remote Remote, report StageReporter, cause error) error {
	reportStage(report, "rolling_back_agent_restic")
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := remote.RollbackRestic(rollbackCtx); err != nil {
		return fmt.Errorf("%v; Agent Restic rollback also failed: %w", cause, err)
	}
	return fmt.Errorf("%w; previous Agent Restic restored", cause)
}

func reportStage(report StageReporter, stage string) {
	if report != nil {
		report(stage)
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
