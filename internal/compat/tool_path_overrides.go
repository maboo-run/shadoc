package compat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/maboo-run/shadoc/internal/command"
)

const ToolPathOverridesMetadataKey = "compatibility.tool-path-overrides"

type ToolPathOverrides struct {
	Rsync           string `json:"rsync"`
	MySQLDump       string `json:"mysqlDump"`
	MySQLRestore    string `json:"mysqlRestore"`
	PostgresDump    string `json:"postgresDump"`
	PostgresRestore string `json:"postgresRestore"`
}

type ToolPathMetadata interface {
	Metadata(context.Context, string) (string, error)
	SetMetadata(context.Context, string, string) error
}

func LoadToolPathOverrides(ctx context.Context, storage ToolPathMetadata) (ToolPathOverrides, error) {
	var result ToolPathOverrides
	if storage == nil {
		return result, nil
	}
	encoded, err := storage.Metadata(ctx, ToolPathOverridesMetadataKey)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("load tool path overrides: %w", err)
	}
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return ToolPathOverrides{}, fmt.Errorf("decode tool path overrides: %w", err)
	}
	return result, nil
}

func SaveToolPathOverrides(ctx context.Context, storage ToolPathMetadata, value ToolPathOverrides) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := storage.SetMetadata(ctx, ToolPathOverridesMetadataKey, string(encoded)); err != nil {
		return fmt.Errorf("save tool path overrides: %w", err)
	}
	return nil
}

func ApplyToolPathOverrides(paths ToolPaths, overrides ToolPathOverrides) ToolPaths {
	if overrides.Rsync != "" {
		paths.Rsync = overrides.Rsync
	}
	if overrides.MySQLDump != "" {
		paths.MySQLDump = overrides.MySQLDump
	}
	if overrides.MySQLRestore != "" {
		paths.MySQLRestore = overrides.MySQLRestore
	}
	if overrides.PostgresDump != "" {
		paths.PostgresDump = overrides.PostgresDump
	}
	if overrides.PostgresRestore != "" {
		paths.PostgresRestore = overrides.PostgresRestore
	}
	return paths
}

func (p *Probe) ValidateToolPathOverrides(ctx context.Context, value ToolPathOverrides) error {
	checks := []struct {
		label    string
		path     string
		programs []string
		args     []string
	}{
		{label: "rsync", path: value.Rsync, programs: []string{"rsync"}, args: []string{"--version"}},
		{label: "mysqldump", path: value.MySQLDump, programs: []string{"mysqldump", "mariadb-dump"}, args: []string{"--version"}},
		{label: "mysql", path: value.MySQLRestore, programs: []string{"mysql", "mariadb"}, args: []string{"--version"}},
		{label: "pg_dump", path: value.PostgresDump, programs: []string{"pg_dump"}, args: []string{"--version"}},
		{label: "pg_restore", path: value.PostgresRestore, programs: []string{"pg_restore"}, args: []string{"--version"}},
	}
	for _, check := range checks {
		if check.path == "" {
			continue
		}
		if err := validateExecutablePath(check.path, check.programs); err != nil {
			return fmt.Errorf("%s 路径无效：%w", check.label, err)
		}
		result, err := p.executor.Run(ctx, command.Spec{Program: check.path, Args: check.args})
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("%s 路径无法执行版本检测", check.label)
		}
	}
	return nil
}

func validateExecutablePath(value string, programs []string) error {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("必须是规范的绝对路径")
	}
	name := strings.ToLower(filepath.Base(value))
	name = strings.TrimSuffix(name, ".exe")
	matched := false
	for _, program := range programs {
		if name == program {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("文件名必须是 %s", strings.Join(programs, " 或 "))
	}
	info, err := os.Stat(value)
	if err != nil {
		return errors.New("文件不存在或无法访问")
	}
	if !info.Mode().IsRegular() {
		return errors.New("目标不是普通文件")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("文件没有执行权限")
	}
	return nil
}
