package compat

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
)

type Severity string

const (
	Blocker Severity = "blocker"
	Warning Severity = "warning"
	Info    Severity = "info"
)

type Finding struct {
	Capability string   `json:"capability"`
	Tool       string   `json:"tool"`
	Path       string   `json:"path,omitempty"`
	Severity   Severity `json:"severity"`
	Message    string   `json:"message"`
	Version    string   `json:"version,omitempty"`
}

func System(dataDir string) Report {
	report := Report{Findings: []Finding{{Capability: "system", Tool: runtime.GOOS + "/" + runtime.GOARCH, Severity: Info, Message: "操作系统与架构受支持"}}}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		report.Findings[0].Severity = Blocker
		report.Findings[0].Message = "当前操作系统不受首版支持"
		report.Blocked = true
	}
	finding := Finding{Capability: "data-directory", Tool: "filesystem", Path: dataDir, Severity: Info, Message: "数据目录可写"}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		finding.Severity = Blocker
		finding.Message = "无法创建数据目录"
		report.Blocked = true
	} else if dir, err := os.MkdirTemp(dataDir, "probe-"); err != nil {
		finding.Severity = Blocker
		finding.Message = "数据目录不可写"
		report.Blocked = true
	} else {
		_ = os.RemoveAll(dir)
	}
	report.Findings = append(report.Findings, finding)
	zone := time.Now().Location().String()
	report.Findings = append(report.Findings, Finding{Capability: "timezone", Tool: "system", Severity: Info, Version: zone, Message: "系统时区可用"})
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &stat); err == nil {
		free := uint64(stat.Bavail) * uint64(stat.Bsize)
		space := Finding{Capability: "temporary-space", Tool: "filesystem", Path: dataDir, Severity: Info, Version: fmt.Sprintf("%d MiB", free/(1024*1024)), Message: "临时空间可用"}
		if free < 1<<30 {
			space.Severity = Warning
			space.Message = "可用临时空间低于 1 GiB"
		}
		report.Findings = append(report.Findings, space)
	}
	return report
}
func Merge(reports ...Report) Report {
	result := Report{Findings: []Finding{}}
	for _, report := range reports {
		result.Blocked = result.Blocked || report.Blocked
		result.Findings = append(result.Findings, report.Findings...)
	}
	return result
}

type Report struct {
	Blocked  bool      `json:"blocked"`
	Findings []Finding `json:"findings"`
}

type ToolPaths struct {
	Restic          string `json:"restic"`
	Rsync           string `json:"rsync"`
	MySQLDump       string `json:"mysqlDump"`
	MySQLRestore    string `json:"mysqlRestore"`
	PostgresDump    string `json:"postgresDump"`
	PostgresRestore string `json:"postgresRestore"`
}

type Probe struct {
	executor command.Executor
}

func NewProbe(executor command.Executor) *Probe {
	return &Probe{executor: executor}
}

func (p *Probe) Tools(ctx context.Context, paths ToolPaths) Report {
	checks := []struct {
		capability string
		tool       string
		path       string
		args       []string
	}{
		{"restic", "restic", paths.Restic, []string{"version"}},
		{"rsync", "rsync", paths.Rsync, []string{"--version"}},
		{"mysql-backup", "mysqldump", paths.MySQLDump, []string{"--version"}},
		{"mysql-restore", "mysql", paths.MySQLRestore, []string{"--version"}},
		{"postgres-backup", "pg_dump", paths.PostgresDump, []string{"--version"}},
		{"postgres-restore", "pg_restore", paths.PostgresRestore, []string{"--version"}},
	}
	report := Report{Findings: make([]Finding, 0, len(checks))}
	for _, check := range checks {
		finding := Finding{Capability: check.capability, Tool: check.tool, Path: check.path}
		if check.path == "" {
			finding.Severity = Blocker
			finding.Message = fmt.Sprintf("未配置 %s 的绝对路径", check.tool)
			report.Blocked = true
			report.Findings = append(report.Findings, finding)
			continue
		}
		result, err := p.executor.Run(ctx, command.Spec{Program: check.path, Args: check.args})
		if err != nil || result.ExitCode != 0 {
			finding.Severity = Blocker
			finding.Message = fmt.Sprintf("无法执行 %s", check.tool)
			report.Blocked = true
			report.Findings = append(report.Findings, finding)
			continue
		}
		output := strings.TrimSpace(result.Stdout)
		if output == "" {
			output = strings.TrimSpace(result.Stderr)
		}
		finding.Version = firstVersion(output)
		finding.Severity = Info
		finding.Message = fmt.Sprintf("%s 可用", check.tool)
		if check.tool == "restic" && !supportedRestic(finding.Version) {
			finding.Severity = Blocker
			finding.Message = "Restic 版本缺少首版所需能力"
			report.Blocked = true
		}
		if check.tool == "rsync" && !supportedRsync(finding.Version) {
			finding.Severity = Blocker
			finding.Message = "rsync 版本低于 3，不支持受控同步参数"
			report.Blocked = true
		}
		report.Findings = append(report.Findings, finding)
	}
	return report
}

func supportedRsync(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	return err == nil && major >= 3
}

var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

func firstVersion(value string) string {
	return versionPattern.FindString(value)
}

func supportedRestic(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return false
	}
	// Database backups require backup --stdin-from-command, introduced in 0.17.0.
	return major > 0 || minor >= 17
}
