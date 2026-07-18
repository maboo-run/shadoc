package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestPlanEndpointsAcceptEveryScheduleAndRejectDisabledTasksWhenEnabled(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	for _, item := range []struct {
		id      string
		enabled bool
	}{
		{id: "active", enabled: true},
		{id: "disabled", enabled: false},
	} {
		hostSecret := "host-secret-" + item.id
		repoSecret := "repo-secret-" + item.id
		if err := s.SaveSecret(ctx, hostSecret, "ssh-private-key", []byte("encrypted"), now); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSecret(ctx, repoSecret, "repository-password", []byte("encrypted"), now); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host-" + item.id, Name: "host-" + item.id, Host: "nas", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, hostSecret); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRepository(ctx, domain.Repository{ID: "repo-" + item.id, Name: "repo-" + item.id, RemoteHostID: "host-" + item.id, Path: "/backup/" + item.id, Status: "ready", CreatedAt: now, UpdatedAt: now}, repoSecret); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateTask(ctx, domain.Task{ID: "task-" + item.id, Name: "task-" + item.id, Kind: domain.DirectoryTask, RepositoryID: "repo-" + item.id, Directory: &domain.DirectorySource{Path: "/source/" + item.id}, Enabled: item.enabled, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	srv := NewWithAuth(s, auth.New(s, time.Now))
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	cookie := sessionCookie(t, setup)
	cookie.Raw = setup.Header().Get("X-CSRF-Token")

	schedules := []map[string]any{
		{"kind": "daily", "timeOfDay": "02:30"},
		{"kind": "weekly", "dayOfWeek": 1, "timeOfDay": "03:15"},
		{"kind": "interval", "intervalHours": 6},
	}
	for index, schedule := range schedules {
		rec := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
			"name": "plan-" + schedule["kind"].(string), "timezone": "Asia/Shanghai",
			"maxParallel": 1, "schedule": schedule, "taskIds": []string{"task-active"}, "enabled": true,
		}, cookie)
		if rec.Code != http.StatusCreated {
			t.Fatalf("schedule[%d]=%v status=%d body=%s", index, schedule, rec.Code, rec.Body.String())
		}
	}
	listed := requestJSON(t, srv, http.MethodGet, "/api/plans", nil, cookie)
	var defaulted []domain.Plan
	if err := json.Unmarshal(listed.Body.Bytes(), &defaulted); err != nil {
		t.Fatal(err)
	}
	if len(defaulted) != len(schedules) {
		t.Fatalf("plans=%+v", defaulted)
	}
	for _, plan := range defaulted {
		if plan.CatchUpWindowMinutes != 60 {
			t.Fatalf("plan %s catch-up=%d want=60", plan.Name, plan.CatchUpWindowMinutes)
		}
	}

	explicitZero := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "no-offline-catch-up", "timezone": "UTC", "maxParallel": 1,
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "04:00"},
		"taskIds":  []string{"task-active"}, "enabled": true, "catchUpWindowMinutes": 0,
	}, cookie)
	var zeroPlan domain.Plan
	if err := json.Unmarshal(explicitZero.Body.Bytes(), &zeroPlan); err != nil || explicitZero.Code != http.StatusCreated || zeroPlan.CatchUpWindowMinutes != 0 {
		t.Fatalf("explicit zero status=%d plan=%+v err=%v", explicitZero.Code, zeroPlan, err)
	}

	preserved := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "preserved-catch-up", "timezone": "UTC", "maxParallel": 1,
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "05:00"},
		"taskIds":  []string{"task-active"}, "enabled": true, "catchUpWindowMinutes": 45,
	}, cookie)
	var preservedPlan domain.Plan
	if err := json.Unmarshal(preserved.Body.Bytes(), &preservedPlan); err != nil || preserved.Code != http.StatusCreated {
		t.Fatalf("preserved create status=%d body=%s err=%v", preserved.Code, preserved.Body.String(), err)
	}
	updated := requestJSON(t, srv, http.MethodPut, "/api/plans/"+preservedPlan.ID, map[string]any{
		"name": "preserved-catch-up", "timezone": "UTC", "maxParallel": 2,
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "05:00"},
		"taskIds":  []string{"task-active"}, "enabled": true,
	}, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("preserved update status=%d body=%s", updated.Code, updated.Body.String())
	}
	readPreserved := requestJSON(t, srv, http.MethodGet, "/api/plans/"+preservedPlan.ID, nil, cookie)
	if err := json.Unmarshal(readPreserved.Body.Bytes(), &preservedPlan); err != nil || preservedPlan.CatchUpWindowMinutes != 45 {
		t.Fatalf("preserved plan=%+v err=%v", preservedPlan, err)
	}
	invalidWeekday := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "invalid-weekday", "timezone": "Asia/Shanghai", "maxParallel": 1,
		"schedule": map[string]any{"kind": "weekly", "dayOfWeek": -1, "timeOfDay": "03:15"},
		"taskIds":  []string{"task-active"}, "enabled": true,
	}, cookie)
	if invalidWeekday.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid weekday status=%d body=%s", invalidWeekday.Code, invalidWeekday.Body.String())
	}
	invalidInterval := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "invalid-interval", "timezone": "Asia/Shanghai", "maxParallel": 1,
		"schedule": map[string]any{"kind": "interval", "intervalHours": 8761},
		"taskIds":  []string{"task-active"}, "enabled": true,
	}, cookie)
	if invalidInterval.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid interval status=%d body=%s", invalidInterval.Code, invalidInterval.Body.String())
	}

	blocked := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "blocked", "timezone": "Asia/Shanghai", "maxParallel": 1,
		"schedule": schedules[0], "taskIds": []string{"task-disabled"}, "enabled": true,
	}, cookie)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("enabled plan with disabled task status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	draft := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "draft", "timezone": "Asia/Shanghai", "maxParallel": 1,
		"schedule": schedules[0], "taskIds": []string{"task-disabled"}, "enabled": false,
	}, cookie)
	if draft.Code != http.StatusCreated {
		t.Fatalf("disabled draft status=%d body=%s", draft.Code, draft.Body.String())
	}
}
