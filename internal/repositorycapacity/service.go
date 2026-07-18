package repositorycapacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error)
	ListTasks(context.Context) ([]domain.Task, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	CreateAgentLease(context.Context, store.AgentLease) error
	AgentLeaseStatus(context.Context, string) (store.AgentLease, error)
	ExpireAgentLease(context.Context, string, string, time.Time) error
	SaveRepositoryCapacity(context.Context, string, domain.RepositoryCapacity) error
}

type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}

type StageReporter func(string)

type Service struct {
	store        Storage
	secrets      Secrets
	probe        Probe
	now          func() time.Time
	pollInterval time.Duration
}

func NewService(storage Storage, secrets Secrets, probe Probe, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, secrets: secrets, probe: probe, now: now, pollInterval: 250 * time.Millisecond}
}

func (s *Service) Probe(ctx context.Context, repositoryID string, report StageReporter) (domain.RepositoryCapacity, error) {
	if s == nil || s.store == nil || s.probe == nil || repositoryID == "" {
		return domain.RepositoryCapacity{}, errors.New("repository capacity service is not configured")
	}
	aggregate, err := s.store.LoadRepositoryExecution(ctx, repositoryID)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	if aggregate.Repository.EffectiveKind() == domain.S3Repository {
		return domain.RepositoryCapacity{}, errors.New("S3 repository capacity is not available from filesystem probes")
	}
	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	var boundTask domain.Task
	for _, task := range tasks {
		if task.RepositoryID == repositoryID {
			boundTask = task
			break
		}
	}
	if boundTask.ID != "" && boundTask.EffectiveExecutionTarget().Kind == execution.Agent {
		return s.probeThroughAgent(ctx, aggregate.Repository, boundTask, report)
	}
	if report != nil {
		report("probing_capacity")
	}
	definition, err := s.definition(ctx, aggregate)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	measured, err := s.probe.Probe(ctx, definition)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	return s.persist(ctx, aggregate.Repository.ID, measured, "")
}

func (s *Service) definition(ctx context.Context, aggregate store.RepositoryExecution) (Definition, error) {
	if aggregate.Repository.EffectiveKind() == domain.S3Repository {
		return Definition{}, errors.New("S3 repository capacity is not available from filesystem probes")
	}
	definition := Definition{Kind: string(aggregate.Repository.EffectiveKind()), Path: aggregate.Repository.Path}
	if aggregate.Repository.EffectiveKind() == domain.SFTPRepository {
		if s.secrets == nil {
			return Definition{}, errors.New("repository SSH secret service is unavailable")
		}
		key, err := s.secrets.Get(ctx, aggregate.PrivateKeySecretID, "ssh-private-key")
		if err != nil {
			return Definition{}, err
		}
		defer clear(key)
		definition.Host, definition.Port, definition.Username = aggregate.Host.Host, aggregate.Host.Port, aggregate.Host.Username
		definition.PrivateKey, definition.KnownHosts = string(key), aggregate.Host.HostFingerprint
	}
	return definition, definition.Validate()
}

func (s *Service) probeThroughAgent(ctx context.Context, repository domain.Repository, task domain.Task, report StageReporter) (domain.RepositoryCapacity, error) {
	agentID := task.EffectiveExecutionTarget().AgentID
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	available := false
	for _, agent := range agents {
		if agent.ID != agentID || agent.Status != "online" || agent.RevokedAt != nil {
			continue
		}
		for _, capability := range agent.Capabilities {
			available = available || capability == string(Kind)
		}
	}
	if !available {
		return domain.RepositoryCapacity{}, errors.New("目标 Agent 未在线或不支持仓库容量探测；请先升级 Agent")
	}
	if report != nil {
		report("waiting_for_agent_capacity")
	}
	started := s.now().UTC()
	lease := store.AgentLease{ID: fmt.Sprintf("capacity_%d", started.UnixNano()), AgentID: agentID, TaskID: task.ID, Engine: string(Kind), Definition: json.RawMessage(`{}`), ExpiresAt: started.Add(2 * time.Minute)}
	if err := s.store.CreateAgentLease(ctx, lease); err != nil {
		return domain.RepositoryCapacity{}, err
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		current, err := s.store.AgentLeaseStatus(ctx, lease.ID)
		if err != nil {
			return domain.RepositoryCapacity{}, err
		}
		if current.CompletedAt != nil {
			var result agentprotocol.Result
			if err := json.Unmarshal(current.Result, &result); err != nil {
				return domain.RepositoryCapacity{}, errors.New("Agent 返回了无效的容量结果")
			}
			if result.Status != "succeeded" {
				if result.Error == "" {
					result.Error = "Agent 容量探测失败"
				}
				return domain.RepositoryCapacity{}, errors.New(result.Error)
			}
			total, okTotal := summaryUint64(result.Summary["totalBytes"])
			availableBytes, okAvailable := summaryUint64(result.Summary["availableBytes"])
			if !okTotal || !okAvailable {
				return domain.RepositoryCapacity{}, errors.New("Agent 容量结果不完整")
			}
			return s.persist(ctx, repository.ID, Capacity{TotalBytes: total, AvailableBytes: availableBytes}, agentID)
		}
		select {
		case <-ctx.Done():
			_ = s.store.ExpireAgentLease(context.Background(), lease.ID, ctx.Err().Error(), s.now().UTC())
			return domain.RepositoryCapacity{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) persist(ctx context.Context, repositoryID string, measured Capacity, agentID string) (domain.RepositoryCapacity, error) {
	if measured.TotalBytes == 0 || measured.TotalBytes > math.MaxInt64 || measured.AvailableBytes > measured.TotalBytes {
		return domain.RepositoryCapacity{}, errors.New("仓库返回了无效容量")
	}
	capacity := domain.RepositoryCapacity{TotalBytes: measured.TotalBytes, UsedBytes: measured.TotalBytes - measured.AvailableBytes, AvailableBytes: measured.AvailableBytes, CheckedAt: s.now().UTC(), SourceAgentID: agentID}
	if err := s.store.SaveRepositoryCapacity(ctx, repositoryID, capacity); err != nil {
		return domain.RepositoryCapacity{}, err
	}
	return capacity, nil
}

func summaryUint64(value any) (uint64, bool) {
	switch number := value.(type) {
	case float64:
		if number < 0 || number > math.MaxInt64 || math.Trunc(number) != number {
			return 0, false
		}
		return uint64(number), true
	case uint64:
		return number, true
	case int:
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	case json.Number:
		value, err := number.Int64()
		return uint64(value), err == nil && value >= 0
	default:
		return 0, false
	}
}
