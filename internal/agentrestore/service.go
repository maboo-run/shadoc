package agentrestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	ListAgents(context.Context) ([]store.AgentRecord, error)
	LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error)
	CreateAgentFilesystemRequest(context.Context, store.AgentFilesystemRequest) error
	AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error)
	CreateAgentRestoreRequest(context.Context, store.AgentRestoreRequest) error
	AgentRestoreRequestStatus(context.Context, string) (store.AgentRestoreRequest, error)
	ExpireAgentRestoreRequest(context.Context, string, string, time.Time) error
}

type Locker interface {
	With(context.Context, string, func() error) error
}

type Request struct {
	AgentID              string
	RepositoryID         string
	SnapshotID           string
	SourcePath           string
	Target               string
	Includes             []string
	DownloadKiBPerSecond int
}

type Service struct {
	store  Storage
	now    func() time.Time
	poll   time.Duration
	locker Locker
}

func NewService(storage Storage, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, now: now, poll: 100 * time.Millisecond}
}

func (s *Service) SetLocker(locker Locker) { s.locker = locker }

func (s *Service) PreflightTarget(ctx context.Context, agentID, repositoryID, target string) error {
	if err := s.requireAgent(ctx, agentID); err != nil {
		return err
	}
	if err := s.requireRemoteRepository(ctx, repositoryID); err != nil {
		return err
	}
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.ValidateRestoreTarget, Path: target})
	now := s.now().UTC()
	request := store.AgentFilesystemRequest{ID: fmt.Sprintf("restore_preflight_%d", now.UnixNano()), AgentID: agentID, Definition: definition, ExpiresAt: now.Add(20 * time.Second), CreatedAt: now}
	if err := s.store.CreateAgentFilesystemRequest(ctx, request); err != nil {
		return err
	}
	return s.waitFilesystem(ctx, request.ID, request.ExpiresAt)
}

func (s *Service) Restore(ctx context.Context, request Request) error {
	if s.locker != nil {
		return s.locker.With(ctx, request.RepositoryID, func() error {
			return s.restore(ctx, request)
		})
	}
	return s.restore(ctx, request)
}

func (s *Service) restore(ctx context.Context, request Request) error {
	if err := s.requireAgent(ctx, request.AgentID); err != nil {
		return err
	}
	if err := s.requireRemoteRepository(ctx, request.RepositoryID); err != nil {
		return err
	}
	definition, err := json.Marshal(Definition{RepositoryID: request.RepositoryID, SnapshotID: request.SnapshotID, SourcePath: request.SourcePath, Target: request.Target, Includes: request.Includes, DownloadKiBPerSecond: request.DownloadKiBPerSecond})
	if err != nil {
		return err
	}
	now := s.now().UTC()
	record := store.AgentRestoreRequest{ID: fmt.Sprintf("restore_%d", now.UnixNano()), AgentID: request.AgentID, Definition: definition, ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now}
	if err := s.store.CreateAgentRestoreRequest(ctx, record); err != nil {
		return err
	}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		current, err := s.store.AgentRestoreRequestStatus(ctx, record.ID)
		if err != nil {
			return err
		}
		if current.CompletedAt != nil {
			var result agentprotocol.Result
			if json.Unmarshal(current.Result, &result) != nil {
				return errors.New("Agent returned an invalid restore result")
			}
			if current.Status != "succeeded" {
				if result.Error == "" {
					result.Error = "Agent directory restore failed"
				}
				return errors.New(result.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			_ = s.store.ExpireAgentRestoreRequest(context.WithoutCancel(ctx), record.ID, "restore cancelled", s.now().UTC())
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) requireRemoteRepository(ctx context.Context, repositoryID string) error {
	aggregate, err := s.store.LoadRepositoryExecution(ctx, repositoryID)
	if err != nil {
		return err
	}
	kind := aggregate.Repository.EffectiveKind()
	if aggregate.Repository.EffectiveEngine() != domain.ResticEngine || kind != domain.SFTPRepository && kind != domain.S3Repository {
		return errors.New("Agent restore requires an SFTP or S3 repository accessible from the Agent")
	}
	return nil
}

func (s *Service) requireAgent(ctx context.Context, agentID string) error {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, agent := range agents {
		if agent.ID != agentID || agent.RevokedAt != nil || agent.DrainingAt != nil || agent.Status != "online" || agent.LastHeartbeatAt == nil || now.Sub(agent.LastHeartbeatAt.UTC()) > time.Minute {
			continue
		}
		hasRestore, hasTargetCheck := false, false
		for _, capability := range agent.Capabilities {
			hasRestore = hasRestore || capability == string(Kind)
			hasTargetCheck = hasTargetCheck || capability == "filesystem-restore-target"
		}
		if hasRestore && hasTargetCheck {
			return nil
		}
	}
	return errors.New("Agent is offline or does not support directory restore")
}

func (s *Service) waitFilesystem(ctx context.Context, id string, expiresAt time.Time) error {
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		current, err := s.store.AgentFilesystemRequestStatus(ctx, id)
		if err != nil {
			return err
		}
		if current.CompletedAt != nil {
			var result agentprotocol.Result
			if json.Unmarshal(current.Result, &result) != nil {
				return errors.New("Agent returned an invalid target preflight result")
			}
			if current.Status != "succeeded" {
				return errors.New(result.Error)
			}
			return nil
		}
		if !expiresAt.After(s.now().UTC()) {
			return errors.New("Agent restore target preflight timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
