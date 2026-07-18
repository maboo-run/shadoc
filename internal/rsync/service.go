package rsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/execution"
	runcontrol "github.com/maboo-run/shadoc/internal/run"
	"github.com/maboo-run/shadoc/internal/store"
)

type ServiceStore interface {
	LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error)
	StartRun(context.Context, store.RunRecord) error
	FinishRun(context.Context, string, string, time.Time, int, string, map[string]any, string) error
}

type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}

type Service struct {
	store   ServiceStore
	secrets Secrets
	engine  execution.Engine
	now     func() time.Time
}

func NewService(storage ServiceStore, secrets Secrets, engine execution.Engine, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, secrets: secrets, engine: engine, now: now}
}

func (s *Service) Run(ctx context.Context, taskID, planID, trigger string) (store.RunRecord, error) {
	aggregate, err := s.store.LoadRsyncExecution(ctx, taskID)
	if err != nil {
		return store.RunRecord{}, err
	}
	if !aggregate.Task.Enabled {
		return store.RunRecord{}, errors.New("rsync task is disabled")
	}
	var key []byte
	if aggregate.PrivateKeySecretID != "" {
		key, err = s.secrets.Get(ctx, aggregate.PrivateKeySecretID, "ssh-private-key")
		if err != nil {
			return store.RunRecord{}, err
		}
		defer clear(key)
	}
	definition, err := json.Marshal(DefinitionFromExecution(aggregate, key))
	if err != nil {
		return store.RunRecord{}, err
	}
	started := s.now().UTC()
	record := store.RunRecord{ID: fmt.Sprintf("run_%d", started.UnixNano()), TaskID: taskID, PlanID: planID, Trigger: trigger, Status: "running", StartedAt: started}
	if err := s.store.StartRun(ctx, record); err != nil {
		return store.RunRecord{}, err
	}
	outcome, runErr := s.engine.Run(ctx, execution.Assignment{ID: record.ID, TaskID: taskID, Engine: "rsync", Target: execution.Target{Kind: execution.Local}, Definition: definition})
	status, validStatus := runcontrol.NormalizeTerminalStatus(outcome.Status)
	record.Status = string(status)
	if !validStatus && runErr == nil {
		runErr = fmt.Errorf("rsync engine returned unsupported terminal status %q", outcome.Status)
	}
	record.AttemptCount = 1
	record.RawLog = redactRsync(outcome.RawLog, string(key))
	finished := s.now().UTC()
	record.FinishedAt = &finished
	summary := outcome.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	if runErr != nil {
		summary["error"] = rsyncFailureSummary(redactRsync(runErr.Error(), string(key)), record.RawLog)
	}
	if aggregate.Task.ScopeConfirmation.Present() {
		summary["scopeConfirmation"] = aggregate.Task.ScopeConfirmation
	}
	record.Summary = summary
	finishErr := s.store.FinishRun(context.WithoutCancel(ctx), record.ID, record.Status, finished, 1, "", summary, record.RawLog)
	return record, errors.Join(runErr, finishErr)
}

func rsyncFailureSummary(runError, rawLog string) string {
	diagnostic := strings.TrimSpace(rawLog)
	if index := strings.IndexAny(diagnostic, "\r\n"); index >= 0 {
		diagnostic = diagnostic[:index]
	}
	diagnostic = strings.Join(strings.Fields(strings.ToValidUTF8(diagnostic, "�")), " ")
	result := strings.TrimSpace(runError)
	if diagnostic != "" && !strings.Contains(result, diagnostic) {
		if result != "" {
			result += ": "
		}
		result += diagnostic
	}
	const maximumRunes = 512
	runes := []rune(result)
	if len(runes) > maximumRunes {
		result = string(runes[:maximumRunes])
	}
	return result
}

func redactRsync(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}
