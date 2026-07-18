package alerting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/notification"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServicePublishesAndResolvesDurableAlert(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	service := New(database, func() time.Time { return now })
	signal := store.AlertSignal{StateKey: "task:a:run", Kind: "task_run", Severity: store.AlertCritical, ObjectType: "task", ObjectID: "a", ObjectName: "照片", Reason: "运行失败", Message: "任务运行失败", TargetPage: "运行记录", RecoveryCondition: "下一次完整成功"}
	if state, transition, err := service.Raise(context.Background(), signal); err != nil || transition != store.AlertRaised || state.Status != store.AlertActive {
		t.Fatalf("state=%+v transition=%q err=%v", state, transition, err)
	}
	now = now.Add(time.Hour)
	if state, changed, err := service.Resolve(context.Background(), signal.StateKey); err != nil || !changed || state.Status != store.AlertResolved {
		t.Fatalf("state=%+v changed=%v err=%v", state, changed, err)
	}
}

func TestNotificationFailureRaisesIndependentChannelAlertAndDeliveryRecoveryResolvesIt(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	notifier := &notifierStub{result: notification.DeliveryResult{Attempted: true, Attempts: 3, FailedChannels: []string{"webhook"}}, err: context.DeadlineExceeded}
	service := New(database, func() time.Time { return now })
	service.SetNotifier(notifier)
	signal := store.AlertSignal{StateKey: "task:a:run", Kind: "task_run", Severity: store.AlertCritical, ObjectType: "task", ObjectID: "a", ObjectName: "照片", Reason: "运行失败", Message: "任务运行失败", TargetPage: "运行记录", RecoveryCondition: "下一次完整成功"}
	if _, _, err := service.Raise(context.Background(), signal); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(context.Background())
	if err != nil || len(active) != 2 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if channel := stateByKey(active, notificationChannelStateKey); channel.Reason != "通知投递最终失败" || channel.ObjectName != "webhook" {
		t.Fatalf("channel state=%+v", stateByKey(active, notificationChannelStateKey))
	}

	notifier.result, notifier.err = notification.DeliveryResult{Attempted: true, Attempts: 1}, nil
	now = now.Add(time.Hour)
	if _, changed, err := service.Resolve(context.Background(), signal.StateKey); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	active, err = service.Active(context.Background())
	if err != nil || len(active) != 0 {
		t.Fatalf("active after recovery=%+v err=%v", active, err)
	}
	events, err := service.History(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	channelResolved := false
	for _, event := range events {
		channelResolved = channelResolved || event.StateKey == notificationChannelStateKey && event.Transition == store.AlertResolvedTransition
	}
	if !channelResolved {
		t.Fatalf("channel recovery missing from history: %+v", events)
	}
}

func TestRepeatedAndInformationalAlertsDoNotCreateNotificationStorms(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	notifier := &notifierStub{result: notification.DeliveryResult{Attempted: true, Attempts: 1}}
	service := New(database, func() time.Time { return now })
	service.SetNotifier(notifier)
	warning := store.AlertSignal{StateKey: "task:a:run", Kind: "task_run", Severity: store.AlertWarning, ObjectType: "task", ObjectID: "a", ObjectName: "照片", Reason: "运行部分成功", Message: "任务部分成功", TargetPage: "运行记录", RecoveryCondition: "下一次完整成功"}
	if _, _, err := service.Raise(context.Background(), warning); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, transition, err := service.Raise(context.Background(), warning); err != nil || transition != store.AlertRepeated {
		t.Fatalf("transition=%q err=%v", transition, err)
	}
	info := warning
	info.StateKey, info.Severity, info.Reason = "task:b:run", store.AlertInfo, "运行已取消"
	if _, _, err := service.Raise(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 1 {
		t.Fatalf("notification calls=%d", notifier.calls)
	}
}

func TestObserveRunUsesOneCrossEngineStatusPolicy(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	service := New(database, func() time.Time { return now })
	task := domain.Task{ID: "task-a", Name: "照片"}
	for _, test := range []struct {
		status   string
		severity store.AlertSeverity
		reason   string
	}{
		{status: "failed", severity: store.AlertCritical, reason: "最近运行失败"},
		{status: "partial", severity: store.AlertWarning, reason: "最近运行部分成功"},
		{status: "cancelled", severity: store.AlertInfo, reason: "最近运行已取消"},
		{status: "skipped", severity: store.AlertWarning, reason: "最近运行被跳过"},
	} {
		if err := service.ObserveRun(context.Background(), task, store.RunRecord{ID: "run-" + test.status, TaskID: task.ID, Status: test.status}); err != nil {
			t.Fatal(err)
		}
		active, err := service.Active(context.Background())
		if err != nil || len(active) != 1 || active[0].Severity != test.severity || active[0].Reason != test.reason {
			t.Fatalf("status=%s active=%+v err=%v", test.status, active, err)
		}
		if _, _, err := service.Resolve(context.Background(), active[0].StateKey); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ObserveRun(context.Background(), task, store.RunRecord{ID: "run-failed", TaskID: task.ID, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ObserveRun(context.Background(), task, store.RunRecord{ID: "run-success", TaskID: task.ID, Status: "success"}); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(context.Background())
	if err != nil || len(active) != 0 {
		t.Fatalf("success did not recover run alert: active=%+v err=%v", active, err)
	}
}

type notifierStub struct {
	result notification.DeliveryResult
	err    error
	calls  int
}

func (n *notifierStub) Deliver(context.Context, string, string, string) (notification.DeliveryResult, error) {
	n.calls++
	return n.result, n.err
}

func stateByKey(states []store.AlertState, key string) store.AlertState {
	for _, state := range states {
		if state.StateKey == key {
			return state
		}
	}
	return store.AlertState{}
}
