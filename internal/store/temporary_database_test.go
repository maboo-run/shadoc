package store

import (
	"context"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestDeleteExpiredTemporaryDatabaseConnectionsPreservesActiveOperations(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id, secret string
		created    time.Time
	}{
		{"temporary-dbconn_expired", "secret-expired", now.Add(-time.Hour)},
		{"temporary-dbconn_active", "secret-active", now.Add(-time.Hour)},
		{"temporary-dbconn_recent", "secret-recent", now.Add(-time.Minute)},
	} {
		if err := s.SaveSecret(ctx, item.secret, "temporary-database-restore-password", []byte("encrypted"), item.created); err != nil {
			t.Fatal(err)
		}
		connection := domain.DatabaseConnection{ID: item.id, Name: item.id, Engine: domain.MySQL, Purpose: domain.RestoreConnection, Network: domain.TCPNetwork, Host: "db", Port: 3306, Username: "restore", TLS: domain.TLSConfig{}, ToolPaths: map[string]string{}, Status: "ready", CreatedAt: item.created, UpdatedAt: item.created}
		if err := s.CreateDatabaseConnection(ctx, connection, item.secret); err != nil {
			t.Fatal(err)
		}
	}
	operation := OperationRecord{ID: "active-restore", Kind: "database_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: now, Detail: map[string]any{"connectionId": "temporary-dbconn_active"}}
	if err := s.CreateOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}

	secrets, err := s.DeleteExpiredTemporaryDatabaseConnections(ctx, now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0] != "secret-expired" {
		t.Fatalf("deleted secrets=%v", secrets)
	}
	for _, id := range []string{"temporary-dbconn_active", "temporary-dbconn_recent"} {
		if _, err := s.LoadDatabaseConnectionExecution(ctx, id); err != nil {
			t.Fatalf("%s was deleted: %v", id, err)
		}
	}
}
