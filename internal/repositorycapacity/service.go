package repositorycapacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/maboo-run/shadoc/internal/agentcontrol"
	"github.com/maboo-run/shadoc/internal/agentfilesystem"
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

// ErrUnsupported indicates that a repository has no capacity probe that is
// valid for its configured engine and execution topology. Callers can use
// errors.Is to avoid treating this as a transient probe failure.
var ErrUnsupported = errors.New("repository capacity probe is unsupported")

type RemoteRsyncCapacityAgentStatus string

const (
	RemoteRsyncCapacityAgentNotApplicable     RemoteRsyncCapacityAgentStatus = "not_applicable"
	RemoteRsyncCapacityAgentAvailable         RemoteRsyncCapacityAgentStatus = "available"
	RemoteRsyncCapacityAgentUnbound           RemoteRsyncCapacityAgentStatus = "unbound"
	RemoteRsyncCapacityAgentOffline           RemoteRsyncCapacityAgentStatus = "offline"
	RemoteRsyncCapacityAgentDraining          RemoteRsyncCapacityAgentStatus = "draining"
	RemoteRsyncCapacityAgentRevoked           RemoteRsyncCapacityAgentStatus = "revoked"
	RemoteRsyncCapacityAgentMissingCapability RemoteRsyncCapacityAgentStatus = "missing_capacity_capability"
)

// RemoteRsyncCapacityAgent is the single decision for whether a remote rsync
// target can have its capacity inspected through its host-bound Agent.
// SSH synchronization never depends on this result.
type RemoteRsyncCapacityAgent struct {
	AgentID string
	Status  RemoteRsyncCapacityAgentStatus
}

func (r RemoteRsyncCapacityAgent) SupportsCapacity() bool {
	return r.Status == RemoteRsyncCapacityAgentAvailable
}

func (r RemoteRsyncCapacityAgent) UnsupportedReason() string {
	switch r.Status {
	case RemoteRsyncCapacityAgentUnbound:
		return "远程 rsync 仓库未关联 Agent；SSH 同步仍可正常使用。"
	case RemoteRsyncCapacityAgentOffline:
		return "关联的 Agent 当前离线或心跳已过期；SSH 同步仍可正常使用。"
	case RemoteRsyncCapacityAgentDraining:
		return "关联的 Agent 正在排空任务；完成后可检测容量。"
	case RemoteRsyncCapacityAgentRevoked:
		return "关联的 Agent 已撤销或卸载；请重新关联可用 Agent。"
	case RemoteRsyncCapacityAgentMissingCapability:
		return "关联的 Agent 不支持目录容量检测；请升级 Agent。"
	default:
		return "远程 rsync 仓库当前不支持容量检测；SSH 同步仍可正常使用。"
	}
}

// ResolveRemoteRsyncCapacityAgent keeps the Agent/host relationship and
// capability checks in one place for both the HTTP presentation and probe.
func ResolveRemoteRsyncCapacityAgent(repository domain.Repository, agents []store.AgentRecord, now time.Time) RemoteRsyncCapacityAgent {
	if repository.EffectiveEngine() != domain.RsyncEngine || repository.EffectiveKind() != domain.SSHRepository {
		return RemoteRsyncCapacityAgent{Status: RemoteRsyncCapacityAgentNotApplicable}
	}
	if repository.RemoteHostID == "" {
		return RemoteRsyncCapacityAgent{Status: RemoteRsyncCapacityAgentUnbound}
	}
	for _, agent := range agents {
		if agent.RemoteHostID != repository.RemoteHostID {
			continue
		}
		result := RemoteRsyncCapacityAgent{AgentID: agent.ID}
		switch {
		case agent.RevokedAt != nil || agent.UninstalledAt != nil || agent.Status == "revoked":
			result.Status = RemoteRsyncCapacityAgentRevoked
		case agent.DrainingAt != nil:
			result.Status = RemoteRsyncCapacityAgentDraining
		case !agentcontrol.IsOnline(agent, now):
			result.Status = RemoteRsyncCapacityAgentOffline
		case !agentcontrol.HasCapability(agent, agentfilesystem.CapacityCapability):
			result.Status = RemoteRsyncCapacityAgentMissingCapability
		default:
			result.Status = RemoteRsyncCapacityAgentAvailable
		}
		return result
	}
	return RemoteRsyncCapacityAgent{Status: RemoteRsyncCapacityAgentUnbound}
}

func ResolveOwnedLocalCapacityAgent(repository domain.Repository, agents []store.AgentRecord, now time.Time) RemoteRsyncCapacityAgent {
	if repository.EffectiveKind() != domain.LocalRepository || repository.EffectiveLocalTarget().Kind != execution.Agent {
		return RemoteRsyncCapacityAgent{Status: RemoteRsyncCapacityAgentNotApplicable}
	}
	agentID := repository.EffectiveLocalTarget().AgentID
	for _, agent := range agents {
		if agent.ID != agentID {
			continue
		}
		result := RemoteRsyncCapacityAgent{AgentID: agentID}
		switch {
		case agent.RevokedAt != nil || agent.UninstalledAt != nil || agent.Status == "revoked":
			result.Status = RemoteRsyncCapacityAgentRevoked
		case agent.DrainingAt != nil:
			result.Status = RemoteRsyncCapacityAgentDraining
		case !agentcontrol.IsOnline(agent, now):
			result.Status = RemoteRsyncCapacityAgentOffline
		case !agentcontrol.HasCapability(agent, agentfilesystem.CapacityCapability):
			result.Status = RemoteRsyncCapacityAgentMissingCapability
		default:
			result.Status = RemoteRsyncCapacityAgentAvailable
		}
		return result
	}
	return RemoteRsyncCapacityAgent{AgentID: agentID, Status: RemoteRsyncCapacityAgentUnbound}
}

type agentFilesystemStorage interface {
	CreateAgentFilesystemRequest(context.Context, store.AgentFilesystemRequest) error
	AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error)
	ExpireAgentFilesystemRequest(context.Context, string, string, time.Time) error
}

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
	if s == nil || s.store == nil || repositoryID == "" {
		return domain.RepositoryCapacity{}, errors.New("repository capacity service is not configured")
	}
	aggregate, err := s.store.LoadRepositoryExecution(ctx, repositoryID)
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	if aggregate.Repository.EffectiveKind() == domain.S3Repository {
		return domain.RepositoryCapacity{}, errors.New("S3 repository capacity is not available from filesystem probes")
	}
	if aggregate.Repository.EffectiveKind() == domain.LocalRepository && aggregate.Repository.EffectiveLocalTarget().Kind == execution.Agent {
		agents, err := s.store.ListAgents(ctx)
		if err != nil {
			return domain.RepositoryCapacity{}, err
		}
		resolved := ResolveOwnedLocalCapacityAgent(aggregate.Repository, agents, s.now().UTC())
		if !resolved.SupportsCapacity() {
			return domain.RepositoryCapacity{}, fmt.Errorf("%w: %s", ErrUnsupported, resolved.UnsupportedReason())
		}
		storage, ok := s.store.(agentFilesystemStorage)
		if !ok {
			return domain.RepositoryCapacity{}, fmt.Errorf("%w: Agent 文件系统请求通道不可用", ErrUnsupported)
		}
		return s.probeThroughAgentFilesystem(ctx, storage, aggregate.Repository, resolved.AgentID, report)
	}
	if aggregate.Repository.EffectiveEngine() == domain.RsyncEngine && aggregate.Repository.EffectiveKind() == domain.SSHRepository {
		agents, err := s.store.ListAgents(ctx)
		if err != nil {
			return domain.RepositoryCapacity{}, err
		}
		resolved := ResolveRemoteRsyncCapacityAgent(aggregate.Repository, agents, s.now().UTC())
		if !resolved.SupportsCapacity() {
			return domain.RepositoryCapacity{}, fmt.Errorf("%w: %s", ErrUnsupported, resolved.UnsupportedReason())
		}
		storage, ok := s.store.(agentFilesystemStorage)
		if !ok {
			return domain.RepositoryCapacity{}, fmt.Errorf("%w: Agent 文件系统请求通道不可用", ErrUnsupported)
		}
		return s.probeThroughAgentFilesystem(ctx, storage, aggregate.Repository, resolved.AgentID, report)
	}
	if s.probe == nil {
		return domain.RepositoryCapacity{}, errors.New("repository capacity service is not configured")
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
		if agent.ID != agentID || agent.RevokedAt != nil || !agentcontrol.IsOnline(agent, s.now().UTC()) {
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
			return s.persistAgentResult(ctx, repository.ID, result, agentID)
		}
		select {
		case <-ctx.Done():
			_ = s.store.ExpireAgentLease(context.Background(), lease.ID, ctx.Err().Error(), s.now().UTC())
			return domain.RepositoryCapacity{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) probeThroughAgentFilesystem(ctx context.Context, storage agentFilesystemStorage, repository domain.Repository, agentID string, report StageReporter) (domain.RepositoryCapacity, error) {
	if report != nil {
		report("waiting_for_agent_capacity")
	}
	started := s.now().UTC()
	definition, err := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.Capacity, Path: repository.Path})
	if err != nil {
		return domain.RepositoryCapacity{}, err
	}
	request := store.AgentFilesystemRequest{ID: fmt.Sprintf("capacity_%d", started.UnixNano()), AgentID: agentID, Definition: definition, ExpiresAt: started.Add(2 * time.Minute), CreatedAt: started}
	if err := storage.CreateAgentFilesystemRequest(ctx, request); err != nil {
		return domain.RepositoryCapacity{}, err
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		current, err := storage.AgentFilesystemRequestStatus(ctx, request.ID)
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
			return s.persistAgentResult(ctx, repository.ID, result, agentID)
		}
		select {
		case <-ctx.Done():
			_ = storage.ExpireAgentFilesystemRequest(context.Background(), request.ID, ctx.Err().Error(), s.now().UTC())
			return domain.RepositoryCapacity{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) persistAgentResult(ctx context.Context, repositoryID string, result agentprotocol.Result, agentID string) (domain.RepositoryCapacity, error) {
	total, okTotal := summaryUint64(result.Summary["totalBytes"])
	availableBytes, okAvailable := summaryUint64(result.Summary["availableBytes"])
	if !okTotal || !okAvailable {
		return domain.RepositoryCapacity{}, errors.New("Agent 容量结果不完整")
	}
	return s.persist(ctx, repositoryID, Capacity{TotalBytes: total, AvailableBytes: availableBytes}, agentID)
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
