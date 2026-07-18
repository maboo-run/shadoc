package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/maboo-run/shadoc/internal/domain"
)

// SystemToolChecker verifies the target Service's Restic program and the
// exact database client paths carried by each imported connection. It never
// executes those programs during import preflight.
type SystemToolChecker struct{ ResticPath string }

func (checker SystemToolChecker) MissingTools(ctx context.Context, manifest Manifest) ([]MissingTool, error) {
	required := map[string]map[string]bool{}
	add := func(tool, path, resource string) {
		key := tool + "\x00" + path
		if required[key] == nil {
			required[key] = map[string]bool{}
		}
		required[key][resource] = true
	}
	for _, repository := range manifest.Repositories {
		if repository.EffectiveEngine() == domain.ResticEngine {
			add("restic", checker.ResticPath, "repository:"+repository.ID)
		}
	}
	for _, connection := range manifest.DatabaseConnections {
		prefix := "database_connection:" + connection.ID
		if connection.Purpose == domain.BackupConnection {
			add(databaseToolName(connection.Engine, "dump"), connection.ToolPaths["dump"], prefix)
		} else {
			add(databaseToolName(connection.Engine, "restore"), connection.ToolPaths["restore"], prefix)
		}
		add(databaseToolName(connection.Engine, "admin"), connection.ToolPaths["admin"], prefix)
	}
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	missing := make([]MissingTool, 0)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		separator := -1
		for index := range key {
			if key[index] == 0 {
				separator = index
				break
			}
		}
		tool, path := key, ""
		if separator >= 0 {
			tool, path = key[:separator], key[separator+1:]
		}
		if availableToolPath(path) {
			continue
		}
		resources := make([]string, 0, len(required[key]))
		for resource := range required[key] {
			resources = append(resources, resource)
		}
		sort.Strings(resources)
		missing = append(missing, MissingTool{Tool: tool, Path: path, RequiredBy: resources})
	}
	return missing, nil
}

func availableToolPath(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func databaseToolName(engine domain.DatabaseEngine, role string) string {
	if engine == domain.MySQL {
		if role == "dump" {
			return "mysqldump"
		}
		return "mysql"
	}
	if role == "dump" {
		return "pg_dump"
	}
	if role == "restore" {
		return "pg_restore"
	}
	return "psql"
}
