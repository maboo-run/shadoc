package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

func TestAlertsAPIExposesCurrentStateHistoryAndNotificationAttempts(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	now := time.Now().UTC()
	signal := store.AlertSignal{StateKey: "repository:repo:integrity", Kind: "repository_abnormal", Severity: store.AlertCritical, ObjectType: "repository", ObjectID: "repo", ObjectName: "异地仓库", Reason: "仓库状态异常", Message: "完整性检查失败", TargetPage: "备份仓库", RecoveryCondition: "完整性检查通过"}
	if _, _, err := srv.alerts.Raise(context.Background(), signal); err != nil {
		t.Fatal(err)
	}
	deliveredAt := now.Add(time.Second)
	if err := srv.store.(*store.Store).RecordNotificationDelivery(context.Background(), store.NotificationDelivery{NotificationID: "notification-1", OccurredAt: now, Channel: "ntfy", StateKey: signal.StateKey, Transition: "critical", Attempt: 1, MaxAttempts: 1, Status: store.DeliveryDelivered, DeliveredAt: &deliveredAt}); err != nil {
		t.Fatal(err)
	}

	if response := requestJSON(t, srv, http.MethodGet, "/api/alerts", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/alerts?limit=25", nil, cookie)
	var payload struct {
		Active     []store.AlertState           `json:"active"`
		Events     []store.AlertEvent           `json:"events"`
		Deliveries []store.NotificationDelivery `json:"deliveries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(payload.Active) != 1 || payload.Active[0].RecoveryCondition == "" || len(payload.Events) != 1 || len(payload.Deliveries) != 1 || payload.Deliveries[0].Status != store.DeliveryDelivered {
		t.Fatalf("payload=%+v status=%d body=%s", payload, response.Code, response.Body.String())
	}

	if _, changed, err := srv.alerts.Resolve(context.Background(), signal.StateKey); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	response = requestJSON(t, srv, http.MethodGet, "/api/alerts", nil, cookie)
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Active) != 0 || len(payload.Events) != 2 || payload.Events[0].Transition != store.AlertResolvedTransition {
		t.Fatalf("resolved payload=%+v", payload)
	}
}
