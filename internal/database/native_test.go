package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

type nativeRowsFake struct {
	values  [][]any
	current []any
	index   int
	err     error
}

func (r *nativeRowsFake) Next() bool {
	if r.index >= len(r.values) {
		r.current = nil
		return false
	}
	r.current = r.values[r.index]
	r.index++
	return true
}

func (r *nativeRowsFake) Scan(dest ...any) error {
	if len(dest) != len(r.current) {
		return errors.New("scan column count mismatch")
	}
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			converted, ok := r.current[index].(string)
			if !ok {
				return errors.New("scan string type mismatch")
			}
			*value = converted
		case *bool:
			converted, ok := r.current[index].(bool)
			if !ok {
				return errors.New("scan bool type mismatch")
			}
			*value = converted
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func (r *nativeRowsFake) Err() error   { return r.err }
func (r *nativeRowsFake) Close() error { return nil }

type nativeDBFake struct {
	pingErr error
	rows    map[string][][]any
	queries []string
	closed  bool
}

func (db *nativeDBFake) PingContext(context.Context) error { return db.pingErr }

func (db *nativeDBFake) QueryContext(_ context.Context, query string, _ ...any) (nativeRows, error) {
	db.queries = append(db.queries, query)
	for marker, values := range db.rows {
		if strings.Contains(query, marker) {
			return &nativeRowsFake{values: values}, nil
		}
	}
	return nil, errors.New("unexpected query")
}

func (db *nativeDBFake) Close() error {
	db.closed = true
	return nil
}

func fixedNativeTester(db nativeDB, openPassword *string) NativeTester {
	return NativeTester{
		Now: func() time.Time { return time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC) },
		Open: func(_ context.Context, _ domain.DatabaseConnection, password string) (nativeDB, func(), error) {
			*openPassword = password
			return db, func() { _ = db.Close() }, nil
		},
	}
}

func TestNativeTesterVerifiesMySQLConnectionAndBackupPrivilege(t *testing.T) {
	db := &nativeDBFake{rows: map[string][][]any{
		"SELECT VERSION()":               {{"8.0.36"}},
		"SHOW GRANTS FOR CURRENT_USER()": {{"GRANT SELECT, SHOW VIEW ON `app`.* TO `backup`@`%`"}},
	}}
	var openedPassword string
	tester := fixedNativeTester(db, &openedPassword)
	result := tester.Test(context.Background(), domain.DatabaseConnection{
		Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", TLS: domain.TLSConfig{Mode: "disabled"},
	}, "mysql-secret")
	if result.Error != "" || result.ServerVersion != "8.0.36" {
		t.Fatalf("result=%+v", result)
	}
	if openedPassword != "mysql-secret" || !db.closed {
		t.Fatalf("password=%q closed=%v", openedPassword, db.closed)
	}
	if len(db.queries) != 2 || strings.Contains(strings.Join(db.queries, " "), "mysql-secret") {
		t.Fatalf("queries=%v", db.queries)
	}
}

func TestNativeTesterRejectsMySQLRestoreWithoutCreatePrivilege(t *testing.T) {
	db := &nativeDBFake{rows: map[string][][]any{
		"SELECT VERSION()":               {{"8.0.36"}},
		"SHOW GRANTS FOR CURRENT_USER()": {{"GRANT SELECT ON `app`.* TO `restore`@`%`"}},
	}}
	var openedPassword string
	result := fixedNativeTester(db, &openedPassword).Test(context.Background(), domain.DatabaseConnection{
		Name: "mysql", Engine: domain.MySQL, Purpose: domain.RestoreConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "restore", TLS: domain.TLSConfig{Mode: "disabled"},
	}, "restore-secret")
	if result.Error != "数据库账号缺少当前用途所需权限" {
		t.Fatalf("result=%+v", result)
	}
}

func TestNativeTesterVerifiesPostgreSQLPurposePrivilege(t *testing.T) {
	db := &nativeDBFake{rows: map[string][][]any{
		"has_database_privilege": {{"16.3", true}},
	}}
	var openedPassword string
	result := fixedNativeTester(db, &openedPassword).Test(context.Background(), domain.DatabaseConnection{
		Name: "postgres", Engine: domain.PostgreSQL, Purpose: domain.RestoreConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 5432, Username: "restore", TLS: domain.TLSConfig{Mode: "disabled"},
	}, "postgres-secret")
	if result.Error != "" || result.ServerVersion != "16.3" {
		t.Fatalf("result=%+v", result)
	}
	if len(db.queries) != 1 || !strings.Contains(db.queries[0], "$1") {
		t.Fatalf("queries=%v", db.queries)
	}
}

func TestNativeTesterReturnsBoundedErrorForDriverFailure(t *testing.T) {
	tester := NativeTester{
		Open: func(context.Context, domain.DatabaseConnection, string) (nativeDB, func(), error) {
			return nil, func() {}, errors.New("password=driver-secret host=db.internal")
		},
	}
	result := tester.Test(context.Background(), domain.DatabaseConnection{
		Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", TLS: domain.TLSConfig{Mode: "disabled"},
	}, "driver-secret")
	if result.Error == "" || strings.Contains(result.Error, "driver-secret") || strings.Contains(result.Error, "password=") {
		t.Fatalf("result=%+v", result)
	}
}

func TestNativeTesterRejectsInvalidConfigurationBeforeOpening(t *testing.T) {
	opened := false
	tester := NativeTester{Open: func(context.Context, domain.DatabaseConnection, string) (nativeDB, func(), error) {
		opened = true
		return nil, func() {}, nil
	}}
	result := tester.Test(context.Background(), domain.DatabaseConnection{
		Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
		Host: "db.internal", Port: 3306, Username: "backup", TLS: domain.TLSConfig{Mode: "disabled"},
	}, "")
	if result.Error != "数据库连接配置或凭据无效" || opened {
		t.Fatalf("result=%+v opened=%v", result, opened)
	}
}

func TestNativeDatabaseOpenBuildsMySQLAndPostgreSQLDriverConfigs(t *testing.T) {
	for _, test := range []struct {
		name       string
		connection domain.DatabaseConnection
	}{
		{
			name: "mysql",
			connection: domain.DatabaseConnection{
				Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
				Host: "db.internal", Port: 3306, Username: "backup", TLS: domain.TLSConfig{Mode: "disabled"},
			},
		},
		{
			name: "postgresql",
			connection: domain.DatabaseConnection{
				Name: "postgres", Engine: domain.PostgreSQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork,
				Host: "db.internal", Port: 5432, Username: "backup", TLS: domain.TLSConfig{Mode: "disabled"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, cleanup, err := openNativeDatabase(context.Background(), test.connection, "driver-secret")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			cleanup()
			if db == nil {
				t.Fatal("expected a database handle")
			}
		})
	}
}
