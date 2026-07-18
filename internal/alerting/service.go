// Package alerting owns durable current-alert state and its append-only
// transition history. Producers publish facts here instead of constructing
// ad-hoc dashboard warnings.
package alerting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/notification"
	runcontrol "github.com/maboo-run/shadoc/internal/run"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	RaiseAlert(context.Context, store.AlertSignal, time.Time) (store.AlertState, store.AlertTransition, error)
	ResolveAlert(context.Context, string, time.Time) (store.AlertState, bool, error)
	ListActiveAlerts(context.Context) ([]store.AlertState, error)
	ListAlertEvents(context.Context, int) ([]store.AlertEvent, error)
}

type Service struct {
	store    Storage
	now      func() time.Time
	notifier Notifier
}

type Notifier interface {
	Deliver(context.Context, string, string, string) (notification.DeliveryResult, error)
}

const notificationChannelStateKey = "notification:channels:delivery"

func New(storage Storage, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, now: now}
}

func (s *Service) SetNotifier(notifier Notifier) { s.notifier = notifier }

func (s *Service) Raise(ctx context.Context, signal store.AlertSignal) (store.AlertState, store.AlertTransition, error) {
	state, transition, err := s.store.RaiseAlert(ctx, signal, s.now().UTC())
	if err != nil || transition != store.AlertRaised || signal.StateKey == notificationChannelStateKey || signal.Severity == store.AlertInfo {
		return state, transition, err
	}
	if notifyErr := s.deliver(ctx, state, string(state.Severity)); notifyErr != nil {
		return state, transition, notifyErr
	}
	return state, transition, nil
}

func (s *Service) Resolve(ctx context.Context, stateKey string) (store.AlertState, bool, error) {
	state, changed, err := s.store.ResolveAlert(ctx, stateKey, s.now().UTC())
	if err != nil || !changed || stateKey == notificationChannelStateKey || state.Severity == store.AlertInfo {
		return state, changed, err
	}
	if notifyErr := s.deliver(ctx, state, "resolved"); notifyErr != nil {
		return state, changed, notifyErr
	}
	return state, changed, nil
}

func (s *Service) Active(ctx context.Context) ([]store.AlertState, error) {
	return s.store.ListActiveAlerts(ctx)
}

func (s *Service) History(ctx context.Context, limit int) ([]store.AlertEvent, error) {
	return s.store.ListAlertEvents(ctx, limit)
}

// ObserveRun is the shared post-run policy for local Restic, local rsync, and
// remote Agent execution. The engine that produced the run is intentionally
// irrelevant once its status has crossed the canonical persistence boundary.
func (s *Service) ObserveRun(ctx context.Context, task domain.Task, record store.RunRecord) error {
	status, ok := runcontrol.ParseTerminalStatus(record.Status)
	if !ok {
		status = runcontrol.Failed
	}
	stateKey := "task:" + task.ID + ":run"
	if status == runcontrol.Succeeded {
		_, _, runErr := s.Resolve(ctx, stateKey)
		_, _, staleErr := s.Resolve(ctx, "task:"+task.ID+":stale")
		return errors.Join(runErr, staleErr)
	}
	signal := taskRunSignal(task, status)
	_, _, err := s.Raise(ctx, signal)
	return err
}

func taskRunSignal(task domain.Task, status runcontrol.Status) store.AlertSignal {
	severity, reason, message := store.AlertCritical, "最近运行失败", "任务最近一次运行失败"
	switch status {
	case runcontrol.Partial:
		severity, reason, message = store.AlertWarning, "最近运行部分成功", "任务最近一次运行只完成了部分保护"
	case runcontrol.Cancelled:
		severity, reason, message = store.AlertInfo, "最近运行已取消", "任务最近一次运行被取消"
	case runcontrol.Skipped:
		severity, reason, message = store.AlertWarning, "最近运行被跳过", "任务最近一次应执行运行被跳过"
	}
	name := task.Name
	if name == "" {
		name = task.ID
	}
	return store.AlertSignal{StateKey: "task:" + task.ID + ":run", Kind: "task_run", Severity: severity, ObjectType: "task", ObjectID: task.ID, ObjectName: name, Reason: reason, Message: message, TargetPage: "运行记录", RecoveryCondition: "下一次任务运行完整成功"}
}

func (s *Service) deliver(ctx context.Context, state store.AlertState, transition string) error {
	if s.notifier == nil {
		return nil
	}
	message := fmt.Sprintf("%s：%s。恢复条件：%s", state.ObjectName, state.Message, state.RecoveryCondition)
	result, err := s.notifier.Deliver(ctx, state.StateKey, transition, message)
	if err != nil {
		channels := result.FailedChannels
		if len(channels) == 0 {
			channels = []string{"notification"}
		}
		channelNames := strings.Join(channels, ", ")
		channel := store.AlertSignal{
			StateKey:          notificationChannelStateKey,
			Kind:              "notification_channel",
			Severity:          store.AlertCritical,
			ObjectType:        "notification_channel",
			ObjectID:          "channels",
			ObjectName:        channelNames,
			Reason:            "通知投递最终失败",
			Message:           fmt.Sprintf("%s 通知投递失败，最多已尝试 %d 次：%s", channelNames, result.Attempts, err.Error()),
			TargetPage:        "通知与审计",
			RecoveryCondition: "下一次通知成功送达，或管理员停用该通道",
		}
		_, _, storeErr := s.store.RaiseAlert(context.WithoutCancel(ctx), channel, s.now().UTC())
		return storeErr
	}
	if result.Disabled || result.Unconfigured || result.Deduplicated || result.Attempted {
		_, _, resolveErr := s.store.ResolveAlert(context.WithoutCancel(ctx), notificationChannelStateKey, s.now().UTC())
		return resolveErr
	}
	return nil
}
