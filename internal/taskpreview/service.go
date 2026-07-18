package taskpreview

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/rsync"
	"github.com/maboo-run/shadoc/internal/store"
)

var (
	ErrAgentUnavailable = errors.New("task preview agent is offline or missing required capabilities")
	ErrUnsupportedTask  = errors.New("task does not support a source scope preview")
	ErrPreviewFailed    = errors.New("task scope preview failed")
)

type Storage interface {
	ListTasks(context.Context) ([]domain.Task, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error)
	CreateTaskScopePreview(context.Context, store.TaskScopePreview) error
	CreateAgentFilesystemRequest(context.Context, store.AgentFilesystemRequest) error
	AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error)
	CreateAgentLease(context.Context, store.AgentLease) error
	AgentLeaseStatus(context.Context, string) (store.AgentLease, error)
	ExpireAgentLease(context.Context, string, string, time.Time) error
}

type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}

type Service struct {
	store       Storage
	secrets     Secrets
	scopeEngine execution.Engine
	rsyncEngine execution.Engine
	now         func() time.Time
	poll        time.Duration
}

func New(storage Storage, secrets Secrets, scopeEngine, rsyncEngine execution.Engine, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, secrets: secrets, scopeEngine: scopeEngine, rsyncEngine: rsyncEngine, now: now, poll: 100 * time.Millisecond}
}

func (s *Service) Preview(ctx context.Context, taskID string) (store.TaskScopePreview, error) {
	task, err := s.task(ctx, taskID)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	path, exclusions, err := previewSource(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	fingerprint, err := Fingerprint(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	summary, err := s.previewScope(ctx, task, path, exclusions)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	requiresDeleteConfirmation := task.EffectiveEngine() == domain.RsyncEngine && task.Rsync != nil && task.Rsync.Delete
	if requiresDeleteConfirmation {
		deleteSummary, err := s.previewRsyncDelete(ctx, task)
		if err != nil {
			return store.TaskScopePreview{}, err
		}
		mergeSummary(summary, deleteSummary)
	}
	now := s.now().UTC()
	preview := store.TaskScopePreview{
		ID: newID("task_scope_preview", now), TaskID: task.ID, Fingerprint: fingerprint, Summary: summary,
		RequiresDeleteConfirmation: requiresDeleteConfirmation, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := s.store.CreateTaskScopePreview(ctx, preview); err != nil {
		return store.TaskScopePreview{}, err
	}
	return preview, nil
}

func (s *Service) task(ctx context.Context, id string) (domain.Task, error) {
	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return domain.Task{}, sql.ErrNoRows
}

func previewSource(task domain.Task) (string, []string, error) {
	switch task.EffectiveEngine() {
	case domain.ResticEngine:
		if task.Kind != domain.DirectoryTask || task.Directory == nil {
			return "", nil, ErrUnsupportedTask
		}
		return task.Directory.Path, append([]string(nil), task.Directory.Exclusions...), nil
	case domain.RsyncEngine:
		if task.Rsync == nil {
			return "", nil, ErrUnsupportedTask
		}
		return task.Rsync.Path, append([]string(nil), task.Rsync.Exclusions...), nil
	default:
		return "", nil, ErrUnsupportedTask
	}
}

func (s *Service) previewScope(ctx context.Context, task domain.Task, path string, exclusions []string) (map[string]any, error) {
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.PreviewScope, Path: path, Exclusions: exclusions, Limit: agentfilesystem.MaxScopeItems})
	if task.EffectiveExecutionTarget().Kind == execution.Agent {
		if err := s.requireAgent(ctx, task, "filesystem-scope-preview"); err != nil {
			return nil, err
		}
		return s.previewAgentScope(ctx, task, definition)
	}
	if s.scopeEngine == nil {
		return nil, errors.Join(ErrPreviewFailed, errors.New("local source preview engine is unavailable"))
	}
	outcome, err := s.scopeEngine.Run(ctx, execution.Assignment{ID: newID("scope", s.now()), TaskID: task.ID, Engine: agentfilesystem.Kind, Target: execution.Target{Kind: execution.Local}, Definition: definition})
	if err != nil || outcome.Status != "succeeded" {
		return nil, errors.Join(ErrPreviewFailed, err)
	}
	return cloneSummary(outcome.Summary), nil
}

func (s *Service) previewAgentScope(ctx context.Context, task domain.Task, definition json.RawMessage) (map[string]any, error) {
	now := s.now().UTC()
	request := store.AgentFilesystemRequest{ID: newID("scope", now), AgentID: task.EffectiveExecutionTarget().AgentID, Definition: definition, ExpiresAt: now.Add(30 * time.Second), CreatedAt: now}
	if err := s.store.CreateAgentFilesystemRequest(ctx, request); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		current, err := s.store.AgentFilesystemRequestStatus(waitCtx, request.ID)
		if err != nil {
			return nil, err
		}
		if current.CompletedAt != nil || current.Status == "succeeded" || current.Status == "failed" {
			return agentResultSummary(current.Result, current.Status)
		}
		if err := waitPoll(waitCtx, s.poll); err != nil {
			return nil, errors.Join(ErrPreviewFailed, err)
		}
	}
}

func (s *Service) previewRsyncDelete(ctx context.Context, task domain.Task) (map[string]any, error) {
	if task.EffectiveExecutionTarget().Kind == execution.Agent {
		if err := s.requireAgent(ctx, task, string(domain.RsyncEngine)); err != nil {
			return nil, err
		}
		return s.previewAgentRsync(ctx, task)
	}
	if s.rsyncEngine == nil {
		return nil, errors.Join(ErrPreviewFailed, errors.New("local rsync preview engine is unavailable"))
	}
	aggregate, err := s.store.LoadRsyncExecution(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	var key []byte
	if aggregate.PrivateKeySecretID != "" {
		if s.secrets == nil {
			return nil, errors.Join(ErrPreviewFailed, errors.New("rsync preview secret store is unavailable"))
		}
		key, err = s.secrets.Get(ctx, aggregate.PrivateKeySecretID, "ssh-private-key")
		if err != nil {
			return nil, err
		}
		defer clear(key)
	}
	definition := rsync.DefinitionFromExecution(aggregate, key)
	definition.DryRun = true
	raw, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	outcome, err := s.rsyncEngine.Run(ctx, execution.Assignment{ID: newID("rsync_preview", s.now()), TaskID: task.ID, Engine: execution.EngineKind(domain.RsyncEngine), Target: execution.Target{Kind: execution.Local}, Definition: raw})
	if err != nil || outcome.Status != "succeeded" {
		return nil, errors.Join(ErrPreviewFailed, err)
	}
	return cloneSummary(outcome.Summary), nil
}

func (s *Service) previewAgentRsync(ctx context.Context, task domain.Task) (map[string]any, error) {
	now := s.now().UTC()
	marker := json.RawMessage(`{"dryRun":true}`)
	lease := store.AgentLease{ID: newID("preview_lease", now), AgentID: task.EffectiveExecutionTarget().AgentID, TaskID: task.ID, Engine: string(domain.RsyncEngine), Definition: marker, ExpiresAt: now.Add(2 * time.Minute)}
	if err := s.store.CreateAgentLease(ctx, lease); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		current, err := s.store.AgentLeaseStatus(waitCtx, lease.ID)
		if err != nil {
			return nil, err
		}
		if current.CompletedAt != nil || current.Status == "succeeded" || current.Status == "failed" {
			return agentResultSummary(current.Result, current.Status)
		}
		if err := waitPoll(waitCtx, s.poll); err != nil {
			_ = s.store.ExpireAgentLease(context.WithoutCancel(ctx), lease.ID, err.Error(), s.now().UTC())
			return nil, errors.Join(ErrPreviewFailed, err)
		}
	}
}

func (s *Service) requireAgent(ctx context.Context, task domain.Task, capabilities ...string) error {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, agent := range agents {
		if agent.ID != task.EffectiveExecutionTarget().AgentID || agent.RevokedAt != nil || agent.Status != "online" || agent.LastHeartbeatAt == nil || now.Sub(*agent.LastHeartbeatAt) > time.Minute {
			continue
		}
		available := make(map[string]bool, len(agent.Capabilities))
		for _, capability := range agent.Capabilities {
			available[capability] = true
		}
		complete := true
		for _, capability := range capabilities {
			complete = complete && available[capability]
		}
		if complete {
			return nil
		}
	}
	return ErrAgentUnavailable
}

func agentResultSummary(raw json.RawMessage, storedStatus string) (map[string]any, error) {
	var result agentprotocol.Result
	if !json.Valid(raw) || json.Unmarshal(raw, &result) != nil {
		return nil, errors.Join(ErrPreviewFailed, errors.New("agent returned an invalid preview result"))
	}
	if storedStatus != "succeeded" || result.Status != "succeeded" {
		if result.Error == "" {
			result.Error = "agent preview failed"
		}
		return nil, errors.Join(ErrPreviewFailed, errors.New(result.Error))
	}
	return cloneSummary(result.Summary), nil
}

func mergeSummary(target, source map[string]any) {
	for key, value := range source {
		if key == "truncated" {
			left, _ := target[key].(bool)
			right, _ := value.(bool)
			target[key] = left || right
			continue
		}
		target[key] = value
	}
}

func cloneSummary(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func waitPoll(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newID(prefix string, now time.Time) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("%s_%d", prefix, now.UnixNano())
}
