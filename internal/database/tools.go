package database

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maboo-run/shadoc/internal/domain"
)

// ToolResolver fills only the client paths required by the selected database
// engine and connection purpose. Explicit paths win over discovery unless a
// historical connection has an unambiguous role collision, such as using
// mysqldump as both the dump and metadata client.
// The paths remain part of the connection because the backup and restore
// commands must use the same verified toolchain after the connection is saved.
type ToolResolver struct {
	Lookup func(string) (string, error)
}

func ResolveToolPaths(connection domain.DatabaseConnection) map[string]string {
	return (ToolResolver{}).Resolve(connection)
}

func (r ToolResolver) Resolve(connection domain.DatabaseConnection) map[string]string {
	paths := make(map[string]string, len(connection.ToolPaths)+4)
	for key, value := range connection.ToolPaths {
		if value != "" {
			paths[key] = value
		}
	}
	repairLegacyRolePaths(connection, paths)
	lookup := r.Lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	for key, program := range requiredToolPrograms(connection) {
		if paths[key] != "" {
			continue
		}
		if path := companionToolPath(paths, key, program); path != "" {
			paths[key] = path
			continue
		}
		path, err := lookup(program)
		if err == nil && filepath.IsAbs(path) {
			paths[key] = path
		}
	}
	return paths
}

func companionToolPath(paths map[string]string, key, program string) string {
	otherKey := "dump"
	if key == "dump" {
		otherKey = "admin"
	}
	other := paths[otherKey]
	if other == "" {
		return ""
	}
	directory := filepath.Dir(other)
	for _, candidate := range []string{filepath.Join(directory, program), filepath.Join(directory, program+".exe")} {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func repairLegacyRolePaths(connection domain.DatabaseConnection, paths map[string]string) {
	if connection.Purpose == domain.RestoreConnection {
		// MySQL restore intentionally uses mysql for both restore and metadata;
		// do not treat that valid shared path as a collision.
		return
	}
	switch connection.Engine {
	case domain.MySQL:
		if isMySQLDumpPath(paths["admin"]) || paths["admin"] == paths["dump"] {
			delete(paths, "admin")
		}
		if isMySQLAdminPath(paths["dump"]) {
			delete(paths, "dump")
		}
	case domain.PostgreSQL:
		if toolBaseName(paths["admin"]) == "pg_dump" || paths["admin"] == paths["dump"] {
			delete(paths, "admin")
		}
		if toolBaseName(paths["dump"]) == "psql" {
			delete(paths, "dump")
		}
	}
}

func isMySQLDumpPath(path string) bool {
	name := toolBaseName(path)
	return name == "mysqldump" || name == "mariadb-dump"
}

func isMySQLAdminPath(path string) bool {
	name := toolBaseName(path)
	return name == "mysql" || name == "mariadb"
}

func toolBaseName(path string) string {
	name := strings.ToLower(filepath.Base(path))
	return strings.TrimSuffix(name, ".exe")
}

func requiredToolPrograms(connection domain.DatabaseConnection) map[string]string {
	if connection.Engine == domain.MySQL {
		if connection.Purpose == domain.RestoreConnection {
			return map[string]string{"restore": "mysql", "admin": "mysql"}
		}
		return map[string]string{"dump": "mysqldump", "admin": "mysql"}
	}
	if connection.Purpose == domain.RestoreConnection {
		return map[string]string{"restore": "pg_restore", "admin": "psql", "create": "createdb"}
	}
	return map[string]string{"dump": "pg_dump", "admin": "psql"}
}
