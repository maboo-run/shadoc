package taskpreview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

const (
	agentScopePreviewTimeout   = 30 * time.Second
	agentScopeInventoryTimeout = 10 * time.Minute
)

type Storage interface {
	ListTasks(context.Context) ([]domain.Task, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error)
	CreateTaskScopePreview(context.Context, store.TaskScopePreview) error
	TaskScopePreview(context.Context, string) (store.TaskScopePreview, error)
	CreateAgentFilesystemRequest(context.Context, store.AgentFilesystemRequest) error
	AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error)
	ExpireAgentFilesystemRequest(context.Context, string, string, time.Time) error
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
	catalog     *catalog
	remoteMu    sync.Mutex
	remote      map[string]*remoteInventory
}

type remoteInventory struct {
	mu           sync.Mutex
	agentID      string
	writer       *catalogWriter
	nextSequence int
	lastSequence int
	lastOrdinal  int64
	entryCount   int64
	lastDigest   [sha256.Size]byte
	closed       bool
}

func New(storage Storage, secrets Secrets, scopeEngine, rsyncEngine execution.Engine, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: storage, secrets: secrets, scopeEngine: scopeEngine, rsyncEngine: rsyncEngine,
		now: now, poll: 100 * time.Millisecond, remote: make(map[string]*remoteInventory),
	}
}

func NewWithCatalogRoot(storage Storage, secrets Secrets, scopeEngine, rsyncEngine execution.Engine, now func() time.Time, root string) (*Service, error) {
	service := New(storage, secrets, scopeEngine, rsyncEngine, now)
	catalog, err := newCatalog(root, service.now)
	if err != nil {
		return nil, err
	}
	service.catalog = catalog
	return service, nil
}

func (s *Service) Preview(ctx context.Context, taskID string) (store.TaskScopePreview, error) {
	return s.preview(ctx, taskID, false)
}

// Inventory creates the same confirmation evidence as Preview while also
// requiring a browsable per-entry catalog from a remote Agent.
func (s *Service) Inventory(ctx context.Context, taskID string) (store.TaskScopePreview, error) {
	return s.preview(ctx, taskID, true)
}

// InventoryWithExclusions scans an in-memory task scope using proposed
// exclusion rules. It creates short-lived preview evidence but never mutates
// the persisted task.
func (s *Service) InventoryWithExclusions(ctx context.Context, taskID string, exclusions []string) (store.TaskScopePreview, error) {
	task, err := s.task(ctx, taskID)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	task, err = WithExclusions(task, exclusions)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	return s.previewTask(ctx, task, true)
}

func (s *Service) preview(ctx context.Context, taskID string, requireEntries bool) (store.TaskScopePreview, error) {
	task, err := s.task(ctx, taskID)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	return s.previewTask(ctx, task, requireEntries)
}

func (s *Service) previewTask(ctx context.Context, task domain.Task, requireEntries bool) (store.TaskScopePreview, error) {
	path, exclusions, err := previewSource(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	fingerprint, err := Fingerprint(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	now := s.now().UTC()
	previewID := newID("task_scope_preview", now)
	summary, err := s.previewScope(ctx, task, path, exclusions, previewID, requireEntries)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	requiresDeleteConfirmation := task.EffectiveEngine() == domain.RsyncEngine && task.Rsync != nil && task.Rsync.Delete
	if requiresDeleteConfirmation {
		deleteSummary, err := s.previewRsyncDelete(ctx, task)
		if err != nil {
			s.catalog.remove(previewID)
			return store.TaskScopePreview{}, err
		}
		mergeSummary(summary, deleteSummary)
	}
	preview := store.TaskScopePreview{
		ID: previewID, TaskID: task.ID, Fingerprint: fingerprint, Summary: summary,
		RequiresDeleteConfirmation: requiresDeleteConfirmation, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := s.store.CreateTaskScopePreview(ctx, preview); err != nil {
		s.catalog.remove(previewID)
		return store.TaskScopePreview{}, err
	}
	return preview, nil
}

// WithExclusions returns a validated task copy whose source exclusion rules
// are replaced with the proposed values.
func WithExclusions(task domain.Task, exclusions []string) (domain.Task, error) {
	if len(exclusions) > 256 {
		return domain.Task{}, errors.New("task scope preview exceeds the exclusion rule limit")
	}
	for _, exclusion := range exclusions {
		if strings.TrimSpace(exclusion) == "" || len(exclusion) > 1024 || strings.ContainsAny(exclusion, "\x00\r\n") {
			return domain.Task{}, errors.New("task scope exclusion rule is invalid")
		}
	}
	switch task.EffectiveEngine() {
	case domain.ResticEngine:
		if task.Kind != domain.DirectoryTask || task.Directory == nil {
			return domain.Task{}, ErrUnsupportedTask
		}
		source := *task.Directory
		source.Exclusions = append([]string(nil), exclusions...)
		task.Directory = &source
	case domain.RsyncEngine:
		if task.Rsync == nil {
			return domain.Task{}, ErrUnsupportedTask
		}
		source := *task.Rsync
		source.Exclusions = append([]string(nil), exclusions...)
		task.Rsync = &source
	default:
		return domain.Task{}, ErrUnsupportedTask
	}
	if err := task.Validate(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func (s *Service) Entries(ctx context.Context, previewID string, query EntryQuery) (EntryPage, error) {
	if err := ctx.Err(); err != nil {
		return EntryPage{}, err
	}
	preview, err := s.store.TaskScopePreview(ctx, previewID)
	if err != nil {
		return EntryPage{}, err
	}
	if !s.now().UTC().Before(preview.ExpiresAt) || preview.Summary["entriesAvailable"] != true {
		s.catalog.remove(previewID)
		return EntryPage{}, ErrCatalogUnavailable
	}
	return s.catalog.page(previewID, query)
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

type inventoryScopeEngine interface {
	PreviewScope(context.Context, string, []string, int, agentfilesystem.ScopeVisitor) (agentfilesystem.ScopeSummary, error)
}

func (s *Service) previewScope(ctx context.Context, task domain.Task, path string, exclusions []string, previewID string, requireEntries bool) (map[string]any, error) {
	if task.EffectiveExecutionTarget().Kind == execution.Agent {
		if err := s.requireAgent(ctx, task, "filesystem-scope-preview"); err != nil {
			return nil, err
		}
		if requireEntries && s.catalog == nil {
			return nil, ErrCatalogUnavailable
		}
		includeEntries := requireEntries
		if includeEntries && s.requireAgent(ctx, task, agentprotocol.FilesystemScopeEntryCapability) != nil {
			// Preserve the existing summary preview for an online older Agent;
			// the dedicated page will explain that an Agent upgrade is needed.
			includeEntries = false
		}
		definition, _ := json.Marshal(agentfilesystem.Definition{
			Operation: agentfilesystem.PreviewScope, Path: path, Exclusions: exclusions,
			Limit: agentfilesystem.MaxScopeItems, IncludeEntries: includeEntries,
		})
		summary, err := s.previewAgentScope(ctx, task, definition, previewID, includeEntries)
		if summary != nil {
			summary["entriesAvailable"] = includeEntries && err == nil
			if !includeEntries {
				summary["entriesUnavailableReason"] = "agent_inventory_upgrade_required"
			}
		}
		return summary, err
	}
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.PreviewScope, Path: path, Exclusions: exclusions, Limit: agentfilesystem.MaxScopeItems})
	if s.scopeEngine == nil {
		return nil, errors.Join(ErrPreviewFailed, errors.New("local source preview engine is unavailable"))
	}
	if engine, ok := s.scopeEngine.(inventoryScopeEngine); ok && s.catalog != nil {
		writer, err := s.catalog.begin(previewID)
		if err != nil {
			return nil, errors.Join(ErrPreviewFailed, err)
		}
		summary, err := engine.PreviewScope(ctx, path, exclusions, agentfilesystem.MaxScopeItems, writer.write)
		if err != nil {
			writer.abort()
			return nil, errors.Join(ErrPreviewFailed, err)
		}
		if err := writer.close(); err != nil {
			writer.abort()
			return nil, errors.Join(ErrPreviewFailed, err)
		}
		result := scopeSummary(summary)
		result["entriesAvailable"] = true
		return result, nil
	}
	outcome, err := s.scopeEngine.Run(ctx, execution.Assignment{ID: newID("scope", s.now()), TaskID: task.ID, Engine: agentfilesystem.Kind, Target: execution.Target{Kind: execution.Local}, Definition: definition})
	if err != nil || outcome.Status != "succeeded" {
		return nil, errors.Join(ErrPreviewFailed, err)
	}
	result := cloneSummary(outcome.Summary)
	result["entriesAvailable"] = false
	result["entriesUnavailableReason"] = "inventory_not_supported"
	return result, nil
}

func scopeSummary(summary agentfilesystem.ScopeSummary) map[string]any {
	return map[string]any{
		"scannedItems": summary.ScannedItems, "totalFiles": summary.TotalFiles,
		"includedFiles": summary.IncludedFiles, "includedBytes": summary.IncludedBytes,
		"excludedFiles": summary.ExcludedFiles, "excludedBytes": summary.ExcludedBytes,
		"unreadableItems": summary.UnreadableItems, "attentionItems": summary.AttentionItems,
		"truncated": summary.Truncated, "activeRules": summary.ActiveRules, "suggestions": summary.Suggestions,
	}
}

func (s *Service) previewAgentScope(ctx context.Context, task domain.Task, definition json.RawMessage, previewID string, includeEntries bool) (map[string]any, error) {
	now := s.now().UTC()
	timeout := agentScopePreviewTimeout
	if includeEntries {
		timeout = agentScopeInventoryTimeout
	}
	request := store.AgentFilesystemRequest{ID: newID("scope", now), AgentID: task.EffectiveExecutionTarget().AgentID, Definition: definition, ExpiresAt: now.Add(timeout), CreatedAt: now}
	inventoryOpen := false
	if includeEntries {
		if err := s.startRemoteInventory(request.ID, request.AgentID, previewID); err != nil {
			return nil, errors.Join(ErrPreviewFailed, err)
		}
		inventoryOpen = true
		defer func() {
			if inventoryOpen {
				_ = s.finishRemoteInventory(request.ID, false, 0)
			}
		}()
	}
	if err := s.store.CreateAgentFilesystemRequest(ctx, request); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		current, err := s.store.AgentFilesystemRequestStatus(waitCtx, request.ID)
		if err != nil {
			return nil, err
		}
		if current.CompletedAt != nil || current.Status == "succeeded" || current.Status == "failed" {
			summary, resultErr := agentResultSummary(current.Result, current.Status)
			if includeEntries {
				expectedEntries, completeSummary := nonNegativeSummaryInteger(summary, "scannedItems")
				if resultErr == nil && !completeSummary {
					resultErr = errors.Join(ErrPreviewFailed, errors.New("Agent scope summary is incomplete"))
				}
				finishErr := s.finishRemoteInventory(request.ID, resultErr == nil && current.Status == "succeeded", expectedEntries)
				inventoryOpen = false
				if resultErr == nil && finishErr != nil {
					resultErr = finishErr
				}
			}
			return summary, resultErr
		}
		if err := waitPoll(waitCtx, s.poll); err != nil {
			_ = s.store.ExpireAgentFilesystemRequest(context.WithoutCancel(ctx), request.ID, err.Error(), s.now().UTC())
			return nil, errors.Join(ErrPreviewFailed, err)
		}
	}
}

func (s *Service) startRemoteInventory(requestID, agentID, previewID string) error {
	if s == nil || s.catalog == nil {
		return ErrCatalogUnavailable
	}
	writer, err := s.catalog.begin(previewID)
	if err != nil {
		return err
	}
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if _, exists := s.remote[requestID]; exists {
		writer.abort()
		return errors.New("remote scope inventory already exists")
	}
	s.remote[requestID] = &remoteInventory{agentID: agentID, writer: writer, lastSequence: -1}
	return nil
}

// AppendFilesystemScopeEntries is called only by the mTLS Agent control plane.
// It accepts bounded, ordered chunks and writes relative paths directly into
// the short-lived catalog without persisting them in SQLite.
func (s *Service) AppendFilesystemScopeEntries(ctx context.Context, agentID string, chunk agentprotocol.FilesystemScopeEntryChunk) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := chunk.ValidateFor(agentID); err != nil {
		return err
	}
	s.remoteMu.Lock()
	inventory := s.remote[chunk.AssignmentID]
	s.remoteMu.Unlock()
	if inventory == nil || inventory.agentID != agentID {
		return ErrCatalogUnavailable
	}
	encoded, err := json.Marshal(chunk.Entries)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	inventory.mu.Lock()
	defer inventory.mu.Unlock()
	if inventory.closed {
		return ErrCatalogUnavailable
	}
	if chunk.Sequence == inventory.lastSequence && digest == inventory.lastDigest {
		return nil
	}
	if chunk.Sequence != inventory.nextSequence {
		return errors.New("filesystem scope chunks must be uploaded in order")
	}
	if chunk.Entries[0].Ordinal != inventory.lastOrdinal+1 {
		return errors.New("filesystem scope entries must be uploaded in order")
	}
	for index, entry := range chunk.Entries {
		if entry.Ordinal != inventory.lastOrdinal+int64(index)+1 {
			return errors.New("filesystem scope entries must be contiguous")
		}
		if err := inventory.writer.write(agentfilesystem.ScopeEntry{
			Ordinal: entry.Ordinal, Path: entry.Path, Type: agentfilesystem.ScopeEntryType(entry.Type),
			Disposition: agentfilesystem.ScopeDisposition(entry.Disposition), Size: entry.Size,
			ReasonCode: entry.ReasonCode, RuleIndexes: append([]int(nil), entry.RuleIndexes...),
		}); err != nil {
			inventory.closed = true
			inventory.writer.abort()
			return err
		}
	}
	inventory.lastSequence = chunk.Sequence
	inventory.lastOrdinal = chunk.Entries[len(chunk.Entries)-1].Ordinal
	inventory.entryCount += int64(len(chunk.Entries))
	inventory.lastDigest = digest
	inventory.nextSequence++
	return nil
}

func (s *Service) finishRemoteInventory(requestID string, success bool, expectedEntries int64) error {
	s.remoteMu.Lock()
	inventory := s.remote[requestID]
	delete(s.remote, requestID)
	s.remoteMu.Unlock()
	if inventory == nil {
		return ErrCatalogUnavailable
	}
	inventory.mu.Lock()
	defer inventory.mu.Unlock()
	if inventory.closed {
		return ErrCatalogUnavailable
	}
	inventory.closed = true
	if !success {
		inventory.writer.abort()
		return nil
	}
	if expectedEntries < 0 || inventory.entryCount != expectedEntries || inventory.lastOrdinal != expectedEntries {
		inventory.writer.abort()
		return errors.New("Agent scope inventory entry count does not match its summary")
	}
	return inventory.writer.close()
}

func nonNegativeSummaryInteger(summary map[string]any, key string) (int64, bool) {
	if summary == nil {
		return 0, false
	}
	switch value := summary[key].(type) {
	case int:
		return int64(value), value >= 0
	case int64:
		return value, value >= 0
	case float64:
		converted := int64(value)
		return converted, value >= 0 && value == float64(converted)
	case json.Number:
		converted, err := value.Int64()
		return converted, err == nil && converted >= 0
	default:
		return 0, false
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
	// LoadRsyncExecution supplies repository and secret references, while the
	// task argument may carry draft exclusions that are intentionally not in
	// persistent storage yet.
	aggregate.Task = task
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
