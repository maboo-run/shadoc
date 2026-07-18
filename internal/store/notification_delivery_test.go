package store

import (
	"context"
	"testing"
	"time"
)

func TestNotificationDeliveryAttemptsAreAppendOnlyAndNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	items := []NotificationDelivery{
		{NotificationID: "notification-a", OccurredAt: first, Channel: "ntfy", StateKey: "task:a:run", Transition: "raised", Attempt: 1, MaxAttempts: 2, Status: DeliveryRetrying, ErrorSummary: "ntfy returned status 503"},
		{NotificationID: "notification-a", OccurredAt: first.Add(time.Second), Channel: "ntfy", StateKey: "task:a:run", Transition: "raised", Attempt: 2, MaxAttempts: 2, Status: DeliveryFinalFailure, ErrorSummary: "ntfy returned status 503"},
		{NotificationID: "notification-b", OccurredAt: first.Add(time.Minute), Channel: "ntfy", StateKey: "task:a:run", Transition: "resolved", Attempt: 1, MaxAttempts: 2, Status: DeliveryDelivered, DeliveredAt: timePointer(first.Add(time.Minute))},
	}
	for _, item := range items {
		if err := s.RecordNotificationDelivery(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListNotificationDeliveries(ctx, 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("deliveries=%+v err=%v", got, err)
	}
	if got[0].NotificationID != "notification-b" || got[0].Status != DeliveryDelivered || got[0].DeliveredAt == nil || got[1].Status != DeliveryFinalFailure || got[2].Status != DeliveryRetrying {
		t.Fatalf("deliveries=%+v", got)
	}
}

func TestNotificationDeliveryRejectsInvalidAttempt(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	invalid := NotificationDelivery{NotificationID: "notification-a", OccurredAt: now, Channel: "ntfy", StateKey: "task:a:run", Transition: "raised", Attempt: 2, MaxAttempts: 1, Status: DeliveryRetrying}
	if err := s.RecordNotificationDelivery(context.Background(), invalid); err == nil {
		t.Fatal("invalid notification delivery was accepted")
	}
}

func TestNotificationDeliveryTracksPerChannelSuccessAndDurableRateWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	items := []NotificationDelivery{
		{NotificationID: "notification-a", OccurredAt: now, Channel: "ntfy", StateKey: "task:a", Transition: "critical", Attempt: 1, MaxAttempts: 2, Status: DeliveryRetrying, ErrorSummary: "temporary failure"},
		{NotificationID: "notification-a", OccurredAt: now.Add(time.Second), Channel: "ntfy", StateKey: "task:a", Transition: "critical", Attempt: 2, MaxAttempts: 2, Status: DeliveryDelivered, DeliveredAt: timePointer(now.Add(time.Second))},
		{NotificationID: "notification-a", OccurredAt: now.Add(time.Second), Channel: "webhook", StateKey: "task:a", Transition: "critical", Attempt: 1, MaxAttempts: 1, Status: DeliveryFinalFailure, ErrorSummary: "temporary failure"},
	}
	for _, item := range items {
		if err := s.RecordNotificationDelivery(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	delivered, err := s.NotificationChannelDelivered(ctx, "ntfy", "task:a", "critical")
	if err != nil || !delivered {
		t.Fatalf("ntfy delivered=%v err=%v", delivered, err)
	}
	delivered, err = s.NotificationChannelDelivered(ctx, "webhook", "task:a", "critical")
	if err != nil || delivered {
		t.Fatalf("webhook delivered=%v err=%v", delivered, err)
	}
	resolvedAt := now.Add(2 * time.Second)
	if err := s.RecordNotificationDelivery(ctx, NotificationDelivery{NotificationID: "notification-resolved", OccurredAt: resolvedAt, Channel: "ntfy", StateKey: "task:a", Transition: "resolved", Attempt: 1, MaxAttempts: 1, Status: DeliveryDelivered, DeliveredAt: &resolvedAt}); err != nil {
		t.Fatal(err)
	}
	delivered, err = s.NotificationChannelDelivered(ctx, "ntfy", "task:a", "critical")
	if err != nil || delivered {
		t.Fatalf("a new critical cycle was incorrectly deduplicated: delivered=%v err=%v", delivered, err)
	}
	count, err := s.CountNotificationEventsSince(ctx, "ntfy", now.Add(-time.Minute))
	if err != nil || count != 2 {
		t.Fatalf("event count=%d err=%v", count, err)
	}
	rateLimited := NotificationDelivery{NotificationID: "notification-b", OccurredAt: now.Add(time.Minute), Channel: "webhook", StateKey: "task:b", Transition: "critical", Status: DeliveryRateLimited, ErrorSummary: "channel rate limit reached"}
	if err := s.RecordNotificationDelivery(ctx, rateLimited); err != nil {
		t.Fatal(err)
	}
}
