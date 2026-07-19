package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestToolResolverDiscoversOnlyToolsRequiredByPurpose(t *testing.T) {
	available := map[string]string{
		"mysqldump":  "/usr/bin/mysqldump",
		"mysql":      "/usr/bin/mysql",
		"pg_dump":    "/usr/bin/pg_dump",
		"pg_restore": "/usr/bin/pg_restore",
		"psql":       "/usr/bin/psql",
		"createdb":   "/usr/bin/createdb",
	}
	lookup := func(program string) (string, error) {
		path, ok := available[program]
		if !ok {
			return "", errors.New("not found")
		}
		return path, nil
	}
	resolver := ToolResolver{Lookup: lookup}

	backup := resolver.Resolve(domain.DatabaseConnection{Engine: domain.MySQL, Purpose: domain.BackupConnection})
	if backup["dump"] != "/usr/bin/mysqldump" || backup["admin"] != "/usr/bin/mysql" || backup["restore"] != "" {
		t.Fatalf("mysql backup paths=%v", backup)
	}
	restore := resolver.Resolve(domain.DatabaseConnection{Engine: domain.PostgreSQL, Purpose: domain.RestoreConnection})
	for key, expected := range map[string]string{"restore": "/usr/bin/pg_restore", "admin": "/usr/bin/psql", "create": "/usr/bin/createdb"} {
		if restore[key] != expected {
			t.Fatalf("postgres restore paths=%v", restore)
		}
	}
}

func TestToolResolverPreservesExplicitPaths(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "custom-mysqldump")
	paths := (ToolResolver{Lookup: func(string) (string, error) { return "/usr/bin/discovered", nil }}).Resolve(domain.DatabaseConnection{
		Engine: domain.MySQL, Purpose: domain.BackupConnection,
		ToolPaths: map[string]string{"dump": explicit},
	})
	if paths["dump"] != explicit || paths["admin"] != "/usr/bin/discovered" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestToolResolverRepairsLegacyBackupRoleCollision(t *testing.T) {
	toolsDir := t.TempDir()
	dump := filepath.Join(toolsDir, "mysqldump")
	admin := filepath.Join(toolsDir, "mysql")
	for _, path := range []string{dump, admin} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolver := ToolResolver{Lookup: func(string) (string, error) { return "", errors.New("not found") }}
	paths := resolver.Resolve(domain.DatabaseConnection{
		Engine: domain.MySQL, Purpose: domain.BackupConnection,
		ToolPaths: map[string]string{"dump": dump, "admin": dump},
	})
	if paths["dump"] != dump || paths["admin"] != admin {
		t.Fatalf("paths=%v", paths)
	}
}
