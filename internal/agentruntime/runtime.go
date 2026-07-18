package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/execution"
)

type Runtime struct {
	agentID            string
	engines            execution.Registry
	now                func() time.Time
	runtimeInfo        agentprotocol.RuntimeInfo
	lastRenewalAttempt time.Time
}

const (
	certificateRenewalWindow = 30 * 24 * time.Hour
	certificateRetryInterval = 6 * time.Hour
)

func (r *Runtime) SetRuntimeInfo(info agentprotocol.RuntimeInfo) {
	if r != nil {
		r.runtimeInfo = info
	}
}

func New(agentID string, engines execution.Registry, now func() time.Time) *Runtime {
	if now == nil {
		now = time.Now
	}
	return &Runtime{agentID: agentID, engines: engines, now: now}
}

func (r *Runtime) Execute(ctx context.Context, assignment agentprotocol.Assignment) agentprotocol.Result {
	result := agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: assignment.ID, AgentID: r.agentID, Status: "failed"}
	if err := assignment.ValidateFor(r.agentID, r.now().UTC()); err != nil {
		result.Error = err.Error()
		return result
	}
	if r.engines == nil {
		result.Error = "execution engine registry is unavailable"
		return result
	}
	engine, err := r.engines.Engine(execution.EngineKind(assignment.Engine))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if err := engine.Validate(assignment.Definition); err != nil {
		result.Error = err.Error()
		return result
	}
	outcome, err := engine.Run(ctx, execution.Assignment{ID: assignment.ID, TaskID: assignment.TaskID, Engine: execution.EngineKind(assignment.Engine), Target: execution.Target{Kind: execution.Agent, AgentID: r.agentID}, Definition: assignment.Definition, ExpiresAt: assignment.ExpiresAt})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if outcome.Status == "" {
		outcome.Status = "succeeded"
	}
	if outcome.Status != "succeeded" && outcome.Status != "failed" {
		result.Error = errors.New("execution engine returned an invalid status").Error()
		return result
	}
	result.Status, result.SnapshotID, result.Summary, result.RawLog = outcome.Status, outcome.SnapshotID, outcome.Summary, outcome.RawLog
	return result
}

type Control interface {
	Heartbeat(context.Context, agentprotocol.Heartbeat) error
	Lease(context.Context) (agentprotocol.Assignment, bool, error)
	Complete(context.Context, agentprotocol.Result) error
}

func (r *Runtime) Step(ctx context.Context, control Control, capabilities []string) error {
	r.maintainCertificate(ctx, control)
	if err := control.Heartbeat(ctx, agentprotocol.Heartbeat{Version: agentprotocol.Version, AgentID: r.agentID, Capabilities: capabilities, Runtime: r.runtimeInfo}); err != nil {
		return err
	}
	if filesystem, ok := control.(interface {
		ClaimFilesystem(context.Context) (agentprotocol.Assignment, bool, error)
		CompleteFilesystem(context.Context, agentprotocol.Result) error
	}); ok {
		assignment, found, err := filesystem.ClaimFilesystem(ctx)
		if err != nil {
			return err
		}
		if found {
			return filesystem.CompleteFilesystem(ctx, r.Execute(ctx, assignment))
		}
	}
	if restore, ok := control.(interface {
		ClaimRestore(context.Context) (agentprotocol.Assignment, bool, error)
		CompleteRestore(context.Context, agentprotocol.Result) error
	}); ok {
		assignment, found, err := restore.ClaimRestore(ctx)
		if err != nil {
			return err
		}
		if found {
			return restore.CompleteRestore(ctx, r.Execute(ctx, assignment))
		}
	}
	assignment, ok, err := control.Lease(ctx)
	if err != nil || !ok {
		return err
	}
	return control.Complete(ctx, r.Execute(ctx, assignment))
}

func (r *Runtime) maintainCertificate(ctx context.Context, control Control) {
	renewer, ok := control.(interface {
		CertificateNotAfter() (time.Time, error)
		RenewCertificate(context.Context, string) (time.Time, error)
	})
	if !ok {
		return
	}
	now := r.now().UTC()
	expiresAt, err := renewer.CertificateNotAfter()
	if err == nil && expiresAt.After(now.Add(certificateRenewalWindow)) {
		r.runtimeInfo.RenewalStatus = "healthy"
		return
	}
	if !r.lastRenewalAttempt.IsZero() && now.Before(r.lastRenewalAttempt.Add(certificateRetryInterval)) {
		return
	}
	r.lastRenewalAttempt = now
	if err != nil {
		r.runtimeInfo.RenewalStatus = "failed"
		return
	}
	if _, err := renewer.RenewCertificate(ctx, r.agentID); err != nil {
		r.runtimeInfo.RenewalStatus = "failed"
		return
	}
	r.runtimeInfo.RenewalStatus = "healthy"
}

func (r *Runtime) Run(ctx context.Context, control Control, capabilities []string, interval time.Duration) error {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	for {
		_ = r.Step(ctx, control, capabilities)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
