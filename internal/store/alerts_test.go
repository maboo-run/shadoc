package store

import (
	"context"
	"testing"
	"time"
)

func TestAlertStateTracksOccurrencesResolutionAndReactivationWithHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	signal := AlertSignal{
		StateKey:          "repository:repo-a:integrity",
		Kind:              "repository_abnormal",
		Severity:          AlertCritical,
		ObjectType:        "repository",
		ObjectID:          "repo-a",
		ObjectName:        "异地仓库",
		Reason:            "完整性检查异常",
		Message:           "仓库完整性检查失败",
		TargetPage:        "备份仓库",
		RecoveryCondition: "下一次完整性检查成功",
	}

	state, transition, err := s.RaiseAlert(ctx, signal, first)
	if err != nil || transition != AlertRaised || state.Status != AlertActive || state.OccurrenceCount != 1 || !state.FirstAt.Equal(first) || !state.LastAt.Equal(first) {
		t.Fatalf("first state=%+v transition=%q err=%v", state, transition, err)
	}

	repeatedAt := first.Add(time.Hour)
	signal.Message = "仓库完整性检查仍然失败"
	state, transition, err = s.RaiseAlert(ctx, signal, repeatedAt)
	if err != nil || transition != AlertRepeated || state.OccurrenceCount != 2 || !state.FirstAt.Equal(first) || !state.LastAt.Equal(repeatedAt) || state.Message != signal.Message {
		t.Fatalf("repeat state=%+v transition=%q err=%v", state, transition, err)
	}

	resolvedAt := repeatedAt.Add(time.Hour)
	state, changed, err := s.ResolveAlert(ctx, signal.StateKey, resolvedAt)
	if err != nil || !changed || state.Status != AlertResolved || state.ResolvedAt == nil || !state.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolved state=%+v changed=%v err=%v", state, changed, err)
	}
	if _, changed, err := s.ResolveAlert(ctx, signal.StateKey, resolvedAt.Add(time.Minute)); err != nil || changed {
		t.Fatalf("second resolve changed=%v err=%v", changed, err)
	}

	reactivatedAt := resolvedAt.Add(time.Hour)
	state, transition, err = s.RaiseAlert(ctx, signal, reactivatedAt)
	if err != nil || transition != AlertRaised || state.Status != AlertActive || state.ResolvedAt != nil || state.OccurrenceCount != 3 || !state.FirstAt.Equal(first) || !state.LastAt.Equal(reactivatedAt) {
		t.Fatalf("reactivated state=%+v transition=%q err=%v", state, transition, err)
	}

	active, err := s.ListActiveAlerts(ctx)
	if err != nil || len(active) != 1 || active[0].StateKey != signal.StateKey {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	events, err := s.ListAlertEvents(ctx, 20)
	if err != nil || len(events) != 4 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	wantTypes := []AlertTransition{AlertRaised, AlertResolvedTransition, AlertRepeated, AlertRaised}
	for index, want := range wantTypes {
		if events[index].Transition != want {
			t.Fatalf("event[%d]=%q want=%q; events=%+v", index, events[index].Transition, want, events)
		}
	}
}

func TestAlertValidationRejectsIncompleteOrUnsupportedSignals(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	valid := AlertSignal{StateKey: "agent:a:offline", Kind: "agent_offline", Severity: AlertWarning, ObjectType: "agent", ObjectID: "a", ObjectName: "a", Reason: "Agent 离线", Message: "Agent 已离线", TargetPage: "Agent", RecoveryCondition: "Agent 恢复心跳"}
	for name, mutate := range map[string]func(*AlertSignal){
		"missing key":         func(signal *AlertSignal) { signal.StateKey = "" },
		"missing reason":      func(signal *AlertSignal) { signal.Reason = "" },
		"invalid severity":    func(signal *AlertSignal) { signal.Severity = "panic" },
		"missing recovery":    func(signal *AlertSignal) { signal.RecoveryCondition = "" },
		"missing target page": func(signal *AlertSignal) { signal.TargetPage = "" },
	} {
		t.Run(name, func(t *testing.T) {
			signal := valid
			mutate(&signal)
			if _, _, err := s.RaiseAlert(context.Background(), signal, now); err == nil {
				t.Fatal("invalid alert signal was accepted")
			}
		})
	}
}
