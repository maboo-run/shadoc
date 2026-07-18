package operation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

type Persistence interface {
	CreateOperation(context.Context, store.OperationRecord) error
	StartOperation(context.Context, string, string, time.Time) error
	UpdateOperationStage(context.Context, string, string, map[string]any) error
	FinishOperation(context.Context, string, string, string, time.Time, string, map[string]any) error
	Operation(context.Context, string) (store.OperationRecord, error)
	ListOperations(context.Context, int, string, string) ([]store.OperationRecord, error)
	RecoverInterruptedOperations(context.Context, time.Time) (int, error)
}

type auditPersistence interface {
	AppendAudit(context.Context, store.AuditRecord) error
}

type StartRequest struct {
	Kind         string
	Actor        string
	RepositoryID string
	TaskID       string
	SnapshotID   string
	Target       string
	Detail       map[string]any
}

type Reporter interface {
	Stage(string, map[string]any) error
}

type Work func(context.Context, Reporter) error

type Manager struct {
	persistence Persistence
	parent      context.Context
	now         func() time.Time
	newID       func() string
	mu          sync.Mutex
	active      map[string]*activeOperation
	unique      map[string]string
	closed      bool
	wg          sync.WaitGroup
}

type activeOperation struct {
	cancel    context.CancelFunc
	done      chan struct{}
	uniqueKey string
}

func New(persistence Persistence, parent context.Context, now func() time.Time, newID func() string) (*Manager, error) {
	if persistence == nil {
		return nil, errors.New("operation persistence is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = operationID
	}
	manager := &Manager{persistence: persistence, parent: parent, now: now, newID: newID, active: make(map[string]*activeOperation), unique: make(map[string]string)}
	if _, err := persistence.RecoverInterruptedOperations(context.WithoutCancel(parent), now().UTC()); err != nil {
		return nil, fmt.Errorf("recover interrupted operations: %w", err)
	}
	return manager, nil
}

func (m *Manager) Start(request StartRequest, work Work) (store.OperationRecord, error) {
	return m.start("", request, work)
}

func (m *Manager) StartUnique(key string, request StartRequest, work Work) (store.OperationRecord, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		record, err := m.Start(request, work)
		return record, false, err
	}
	m.mu.Lock()
	if id, exists := m.unique[key]; exists {
		m.mu.Unlock()
		if id == "" {
			return store.OperationRecord{}, true, errors.New("matching operation is starting")
		}
		record, err := m.persistence.Operation(context.WithoutCancel(m.parent), id)
		return record, true, err
	}
	m.unique[key] = ""
	m.mu.Unlock()
	record, err := m.start(key, request, work)
	if err != nil {
		m.mu.Lock()
		delete(m.unique, key)
		m.mu.Unlock()
		return store.OperationRecord{}, false, err
	}
	return record, false, nil
}

func (m *Manager) start(uniqueKey string, request StartRequest, work Work) (store.OperationRecord, error) {
	if strings.TrimSpace(request.Kind) == "" || strings.TrimSpace(request.Actor) == "" || work == nil {
		return store.OperationRecord{}, errors.New("operation kind, actor and work are required")
	}
	record := store.OperationRecord{
		ID: m.newID(), Kind: request.Kind, Actor: request.Actor, RepositoryID: request.RepositoryID, TaskID: request.TaskID,
		SnapshotID: request.SnapshotID, Target: request.Target, Status: "queued", Stage: "queued", CreatedAt: m.now().UTC(), Detail: request.Detail,
	}
	if record.ID == "" {
		return store.OperationRecord{}, errors.New("operation id is required")
	}
	if err := m.persistence.CreateOperation(context.WithoutCancel(m.parent), record); err != nil {
		return store.OperationRecord{}, err
	}
	if auditor, ok := m.persistence.(auditPersistence); ok {
		_ = auditor.AppendAudit(context.WithoutCancel(m.parent), store.AuditRecord{OccurredAt: record.CreatedAt, Actor: record.Actor, Action: "operation.start", TargetType: "operation", TargetID: record.ID, Detail: map[string]any{"kind": record.Kind, "repositoryId": record.RepositoryID, "taskId": record.TaskID, "snapshotId": record.SnapshotID}})
	}
	ctx, cancel := context.WithCancel(m.parent)
	active := &activeOperation{cancel: cancel, done: make(chan struct{}), uniqueKey: uniqueKey}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		_ = m.persistence.FinishOperation(context.WithoutCancel(m.parent), record.ID, "cancelled", "cancelled", m.now().UTC(), "operation runtime is closed", nil)
		return store.OperationRecord{}, errors.New("operation runtime is closed")
	}
	m.active[record.ID] = active
	if uniqueKey != "" {
		m.unique[uniqueKey] = record.ID
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(ctx, record.ID, active, work)
	return record, nil
}

func (m *Manager) run(ctx context.Context, id string, active *activeOperation, work Work) {
	defer m.wg.Done()
	defer func() {
		active.cancel()
		m.mu.Lock()
		delete(m.active, id)
		if active.uniqueKey != "" && m.unique[active.uniqueKey] == id {
			delete(m.unique, active.uniqueKey)
		}
		close(active.done)
		m.mu.Unlock()
	}()
	if err := m.persistence.StartOperation(context.WithoutCancel(m.parent), id, "starting", m.now().UTC()); err != nil {
		_ = m.persistence.FinishOperation(context.WithoutCancel(m.parent), id, "failed", "failed", m.now().UTC(), summarize(err), nil)
		return
	}
	reporter := operationReporter{persistence: m.persistence, context: context.WithoutCancel(m.parent), id: id}
	var workErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				workErr = fmt.Errorf("operation panic: %v", recovered)
			}
		}()
		workErr = work(ctx, reporter)
	}()
	status, stage, detail := classify(ctx, workErr)
	finishedAt := m.now().UTC()
	_ = m.persistence.FinishOperation(context.WithoutCancel(m.parent), id, status, stage, finishedAt, summarize(workErr), detail)
	if auditor, ok := m.persistence.(auditPersistence); ok {
		if record, err := m.persistence.Operation(context.WithoutCancel(m.parent), id); err == nil {
			_ = auditor.AppendAudit(context.WithoutCancel(m.parent), store.AuditRecord{OccurredAt: finishedAt, Actor: record.Actor, Action: "operation.finish", TargetType: "operation", TargetID: id, Detail: map[string]any{"kind": record.Kind, "status": status, "stage": stage}})
		}
	}
}

func (m *Manager) Get(ctx context.Context, id string) (store.OperationRecord, error) {
	return m.persistence.Operation(ctx, id)
}

func (m *Manager) List(ctx context.Context, limit int, kind, status string) ([]store.OperationRecord, error) {
	return m.persistence.ListOperations(ctx, limit, kind, status)
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	active := m.active[id]
	m.mu.Unlock()
	if active != nil {
		active.cancel()
		return nil
	}
	record, err := m.persistence.Operation(context.WithoutCancel(m.parent), id)
	if err != nil {
		return err
	}
	if terminal(record.Status) {
		return nil
	}
	return errors.New("operation is not active in this process")
}

func (m *Manager) Wait(ctx context.Context, id string) (store.OperationRecord, error) {
	m.mu.Lock()
	active := m.active[id]
	m.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return store.OperationRecord{}, ctx.Err()
		}
	}
	return m.persistence.Operation(ctx, id)
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, active := range m.active {
		active.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

type operationReporter struct {
	persistence Persistence
	context     context.Context
	id          string
}

func (r operationReporter) Stage(stage string, detail map[string]any) error {
	if strings.TrimSpace(stage) == "" {
		return errors.New("operation stage is required")
	}
	return r.persistence.UpdateOperationStage(r.context, r.id, stage, detail)
}

func classify(ctx context.Context, err error) (string, string, map[string]any) {
	if err == nil {
		return "success", "completed", nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return "cancelled", "cancelled", nil
	}
	detail := map[string]any{}
	var residual interface{ RestoreResidualPath() string }
	if errors.As(err, &residual) && residual.RestoreResidualPath() != "" {
		detail["residualPath"] = residual.RestoreResidualPath()
	}
	cleanupRequired := len(detail) != 0
	var cleanup interface{ CleanupIsRequired() bool }
	if errors.As(err, &cleanup) && cleanup.CleanupIsRequired() {
		cleanupRequired = true
	}
	if cleanupRequired {
		return "cleanup_required", "cleanup", detail
	}
	return "failed", "failed", detail
}

func summarize(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 2000 {
		value = value[:2000]
	}
	return value
}

func terminal(status string) bool {
	switch status {
	case "success", "partial", "failed", "cancelled", "cleanup_required":
		return true
	default:
		return false
	}
}

func operationID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "op_" + hex.EncodeToString(raw)
}
