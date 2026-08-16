package agentdeploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type RemovalStorage interface {
	ListRemoteHosts(context.Context) ([]domain.RemoteHost, error)
	RemoteHostPrivateKeySecretID(context.Context, string) (string, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	MarkAgentStopped(context.Context, string, time.Time) error
	CompleteAgentUninstall(context.Context, string, time.Time) error
	BeginAgentDrain(context.Context, string, time.Time) error
	EndAgentDrain(context.Context, string) error
	AgentActiveWorkCount(context.Context, string) (int, error)
}

type RemovalRemote interface {
	Probe(context.Context) (Platform, error)
	Stop(context.Context, Platform) error
	Remove(context.Context, Platform, string) error
	Close() error
}

type RemovalDialer interface {
	Dial(context.Context, Target) (RemovalRemote, error)
}

type RemovalResult struct {
	AgentID  string
	HostID   string
	Platform string
}

type RemovalService struct {
	store   RemovalStorage
	secrets DeploymentSecrets
	dialer  RemovalDialer
	now     func() time.Time
	poll    time.Duration
	drain   time.Duration
}

func NewRemovalService(storage RemovalStorage, secrets DeploymentSecrets, dialer RemovalDialer, now func() time.Time) *RemovalService {
	if now == nil {
		now = time.Now
	}
	return &RemovalService{store: storage, secrets: secrets, dialer: dialer, now: now, poll: time.Second, drain: 2 * time.Minute}
}

func (s *RemovalService) Uninstall(ctx context.Context, agentID string, report StageReporter) (result RemovalResult, resultErr error) {
	result = RemovalResult{AgentID: agentID}
	if s == nil || s.store == nil || s.secrets == nil || s.dialer == nil {
		return result, errors.New("Agent remover is not configured")
	}
	if !agentIDPattern.MatchString(agentID) {
		return result, errors.New("valid Agent ID is required")
	}
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return result, err
	}
	var agent store.AgentRecord
	for _, candidate := range agents {
		if candidate.ID == agentID {
			agent = candidate
			break
		}
	}
	if agent.ID == "" {
		return result, sql.ErrNoRows
	}
	if !agent.ManagedInstallation || agent.RemoteHostID == "" {
		return result, errors.New("Agent is not managed through a remote host")
	}
	result.HostID = agent.RemoteHostID
	hosts, err := s.store.ListRemoteHosts(ctx)
	if err != nil {
		return result, err
	}
	var host domain.RemoteHost
	for _, candidate := range hosts {
		if candidate.ID == agent.RemoteHostID {
			host = candidate
			break
		}
	}
	if host.ID == "" {
		return result, errManagedRemoteHostMissing
	}
	if agent.RevokedAt == nil && agent.UninstalledAt == nil {
		if report != nil {
			report("draining_agent")
		}
		if err := s.store.BeginAgentDrain(ctx, agentID, s.now().UTC()); err != nil {
			return result, err
		}
		defer func() {
			endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := s.store.EndAgentDrain(endCtx, agentID); err != nil && resultErr == nil {
				resultErr = fmt.Errorf("resume Agent assignments: %w", err)
			}
		}()
		if err := waitForActiveAgentWork(ctx, s.store, agentID, s.drain, s.poll); err != nil {
			return result, err
		}
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
	result.Platform = platform.OS + "/" + platform.Arch
	if report != nil {
		report("stopping_agent")
	}
	if err := remote.Stop(ctx, platform); err != nil {
		return result, fmt.Errorf("stop Agent service: %w", err)
	}
	if err := s.store.MarkAgentStopped(context.WithoutCancel(ctx), agentID, s.now().UTC()); err != nil {
		return result, fmt.Errorf("persist stopped Agent state: %w", err)
	}
	if report != nil {
		report("removing_agent")
	}
	if err := remote.Remove(ctx, platform, agent.AgentDataDir); err != nil {
		return result, fmt.Errorf("remove Agent service: %w", err)
	}
	if report != nil {
		report("revoking_agent")
	}
	if err := s.store.CompleteAgentUninstall(context.WithoutCancel(ctx), agentID, s.now().UTC()); err != nil {
		return result, fmt.Errorf("complete Agent uninstall: %w", err)
	}
	return result, nil
}
