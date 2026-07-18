package rsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/execution"
)

type Destination struct {
	Kind       DestinationKind `json:"kind,omitempty"`
	Host       string          `json:"host"`
	Port       int             `json:"port"`
	Username   string          `json:"username"`
	Path       string          `json:"path"`
	PrivateKey string          `json:"privateKey"`
	KnownHosts string          `json:"knownHosts"`
}

type DestinationKind string

const (
	DestinationSSH   DestinationKind = "ssh"
	DestinationLocal DestinationKind = "local"
)

func (d Destination) effectiveKind() DestinationKind {
	if d.Kind == "" {
		return DestinationSSH
	}
	return d.Kind
}

type Definition struct {
	SourcePath  string      `json:"sourcePath"`
	Destination Destination `json:"destination"`
	Exclusions  []string    `json:"exclusions,omitempty"`
	Delete      bool        `json:"delete"`
	DryRun      bool        `json:"dryRun,omitempty"`
}

type Engine struct {
	program  string
	executor command.Executor
	tempRoot string
}

var rsyncVersionPattern = regexp.MustCompile(`(?i)version\s+(\d+)\.(\d+)`)

const maxRsyncRawLogBytes = 4 << 20

func New(program string, executor command.Executor, tempRoot string) *Engine {
	if program == "" {
		program = "rsync"
	}
	return &Engine{program: program, executor: executor, tempRoot: tempRoot}
}

func (e *Engine) Kind() execution.EngineKind { return "rsync" }

func (e *Engine) Probe(ctx context.Context) error {
	if e.executor == nil {
		return errors.New("rsync command executor is required")
	}
	result, err := e.executor.Run(ctx, command.Spec{Program: e.program, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("probe rsync: %w", err)
	}
	match := rsyncVersionPattern.FindStringSubmatch(result.Stdout)
	if len(match) != 3 {
		return errors.New("unable to determine rsync version")
	}
	major, _ := strconv.Atoi(match[1])
	if major < 3 {
		return fmt.Errorf("rsync 3 or newer is required; found %s.%s", match[1], match[2])
	}
	return nil
}

func (e *Engine) Validate(raw json.RawMessage) error {
	_, err := decodeDefinition(raw)
	return err
}

func (e *Engine) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	definition, err := decodeDefinition(assignment.Definition)
	if err != nil {
		return execution.Outcome{Status: "failed"}, err
	}
	if e.executor == nil {
		return execution.Outcome{Status: "failed"}, errors.New("rsync command executor is required")
	}
	destination := definition.Destination.Path
	sshCommand := ""
	if definition.Destination.effectiveKind() == DestinationSSH {
		if err := os.MkdirAll(e.tempRoot, 0o700); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		dir, err := os.MkdirTemp(e.tempRoot, "rsync-")
		if err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		defer os.RemoveAll(dir)
		identityPath := filepath.Join(dir, "identity")
		knownHostsPath := filepath.Join(dir, "known_hosts")
		if err := os.WriteFile(identityPath, []byte(definition.Destination.PrivateKey), 0o600); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		if err := os.WriteFile(knownHostsPath, []byte(definition.Destination.KnownHosts), 0o600); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		hostKeyAlgorithms, err := pinnedOpenSSHHostKeyAlgorithms(definition.Destination.KnownHosts)
		if err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		knownHostsConfigPath, err := escapeOpenSSHConfigPath(knownHostsPath)
		if err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		sshCommand = strings.Join([]string{"ssh", "-i", shellQuote(identityPath), "-p", strconv.Itoa(definition.Destination.Port), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", shellQuote("HostKeyAlgorithms=" + hostKeyAlgorithms), "-o", shellQuote("UserKnownHostsFile=" + knownHostsConfigPath)}, " ")
		destinationHost := definition.Destination.Host
		if strings.Contains(destinationHost, ":") && net.ParseIP(destinationHost) != nil {
			destinationHost = "[" + destinationHost + "]"
		}
		destination = definition.Destination.Username + "@" + destinationHost + ":" + definition.Destination.Path
	}
	args := buildArguments(definition, destination, sshCommand)
	collector := &rsyncMetricsCollector{}
	result, err := e.executor.Run(ctx, command.Spec{Program: e.program, Args: args, Env: map[string]string{"LC_ALL": "C"}, Stdout: collector})
	if !collector.sawOutput() {
		_, _ = collector.Write([]byte(result.Stdout))
	}
	metrics := collector.finish()
	changedItems, deleteFiles, deleteDirectories := metrics.changedItems, metrics.deleteFiles, metrics.deleteDirectories
	truncated := len(result.Stdout) >= maxRsyncRawLogBytes || len(result.Stderr) >= maxRsyncRawLogBytes
	summary := map[string]any{
		"exitCode": result.ExitCode, "duration": result.Duration.String(), "changedItems": changedItems,
		"dryRun": definition.DryRun, "deleteFiles": deleteFiles, "deleteDirectories": deleteDirectories,
		"targetIdentity": targetIdentity(definition), "truncated": truncated,
		"filesChanged": int64(changedItems), "durationMilliseconds": result.Duration.Milliseconds(),
	}
	if metrics.filesProcessed != nil {
		summary["filesProcessed"] = *metrics.filesProcessed
	}
	if metrics.bytesProcessed != nil {
		summary["bytesProcessed"] = *metrics.bytesProcessed
	}
	if metrics.bytesChanged != nil {
		summary["bytesChanged"] = *metrics.bytesChanged
	}
	if metrics.regularFilesTransferred != nil {
		summary["regularFilesTransferred"] = *metrics.regularFilesTransferred
	}
	outcome := execution.Outcome{Status: "succeeded", RawLog: boundedRsyncLog(result.Stdout, result.Stderr), Summary: summary}
	if err != nil {
		outcome.Status = "failed"
		return outcome, fmt.Errorf("run rsync: %w", err)
	}
	return outcome, nil
}

func buildArguments(definition Definition, destination, sshCommand string) []string {
	args := []string{"--archive", "--partial", "--delay-updates", "--itemize-changes", "--stats", "--protect-args"}
	if definition.DryRun {
		args = append(args, "--dry-run")
	}
	if definition.Delete {
		args = append(args, "--delete-delay")
	}
	for _, exclusion := range definition.Exclusions {
		args = append(args, "--exclude", exclusion)
	}
	if sshCommand != "" {
		args = append(args, "-e", sshCommand)
	}
	sourcePath := definition.SourcePath
	if !strings.HasSuffix(sourcePath, string(filepath.Separator)) {
		sourcePath += string(filepath.Separator)
	}
	return append(args, "--", sourcePath, destination)
}

func summarizeOutput(output string) (changedItems, deleteFiles, deleteDirectories int) {
	collector := &rsyncMetricsCollector{}
	_, _ = collector.Write([]byte(output))
	metrics := collector.finish()
	return metrics.changedItems, metrics.deleteFiles, metrics.deleteDirectories
}

type rsyncParsedMetrics struct {
	changedItems            int
	deleteFiles             int
	deleteDirectories       int
	filesProcessed          *int64
	regularFilesTransferred *int64
	bytesProcessed          *int64
	bytesChanged            *int64
}

type rsyncMetricsCollector struct {
	mu      sync.Mutex
	buffer  strings.Builder
	seen    bool
	metrics rsyncParsedMetrics
}

func (c *rsyncMetricsCollector) Write(value []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := len(value)
	c.seen = c.seen || len(value) > 0
	for len(value) > 0 {
		index := strings.IndexByte(string(value), '\n')
		if index < 0 {
			c.appendLineFragment(value)
			break
		}
		c.appendLineFragment(value[:index])
		c.consumeLine(c.buffer.String())
		c.buffer.Reset()
		value = value[index+1:]
	}
	return written, nil
}

func (c *rsyncMetricsCollector) appendLineFragment(value []byte) {
	const maximumLineBytes = 64 << 10
	remaining := maximumLineBytes - c.buffer.Len()
	if remaining > 0 {
		_, _ = c.buffer.Write(value[:min(len(value), remaining)])
	}
}

func (c *rsyncMetricsCollector) sawOutput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

func (c *rsyncMetricsCollector) finish() rsyncParsedMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buffer.Len() > 0 {
		c.consumeLine(c.buffer.String())
		c.buffer.Reset()
	}
	return c.metrics
}

func (c *rsyncMetricsCollector) consumeLine(value string) {
	line := strings.TrimSpace(value)
	if strings.HasPrefix(line, "*deleting") {
		c.metrics.changedItems++
		deleted := strings.TrimSpace(strings.TrimPrefix(line, "*deleting"))
		if strings.HasSuffix(deleted, "/") {
			c.metrics.deleteDirectories++
		} else if deleted != "" {
			c.metrics.deleteFiles++
		}
		return
	}
	if len(line) >= 11 && strings.ContainsAny(line[:11], "<>ch.*") {
		c.metrics.changedItems++
	}
	for prefix, target := range map[string]**int64{
		"Number of files:":                     &c.metrics.filesProcessed,
		"Number of regular files transferred:": &c.metrics.regularFilesTransferred,
		"Total file size:":                     &c.metrics.bytesProcessed,
		"Total transferred file size:":         &c.metrics.bytesChanged,
	} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if parsed, ok := parseRsyncStat(strings.TrimSpace(strings.TrimPrefix(line, prefix))); ok {
			value := parsed
			*target = &value
		}
		return
	}
}

func parseRsyncStat(value string) (int64, bool) {
	if index := strings.IndexAny(value, " ("); index >= 0 {
		value = value[:index]
	}
	value = strings.ReplaceAll(value, ",", "")
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && parsed >= 0
}

func targetIdentity(definition Definition) string {
	if definition.Destination.effectiveKind() == DestinationLocal {
		return "local:" + definition.Destination.Path
	}
	host := definition.Destination.Host
	if strings.Contains(host, ":") && net.ParseIP(host) != nil {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	return "ssh://" + definition.Destination.Username + "@" + host + ":" + strconv.Itoa(definition.Destination.Port) + definition.Destination.Path
}

func boundedRsyncLog(stdout, stderr string) string {
	value := strings.TrimSpace(strings.Join([]string{stdout, stderr}, "\n"))
	if len(value) > maxRsyncRawLogBytes {
		return value[:maxRsyncRawLogBytes]
	}
	return value
}

func pinnedOpenSSHHostKeyAlgorithms(knownHosts string) (string, error) {
	algorithms := make([]string, 0, 2)
	seen := map[string]bool{}
	appendAlgorithm := func(algorithm string) {
		if !seen[algorithm] {
			seen[algorithm] = true
			algorithms = append(algorithms, algorithm)
		}
	}
	for _, line := range strings.Split(knownHosts, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		algorithmIndex := 1
		if strings.HasPrefix(fields[0], "@") {
			algorithmIndex = 2
		}
		if len(fields) <= algorithmIndex {
			return "", errors.New("pinned SSH host key entry is invalid")
		}
		switch fields[algorithmIndex] {
		case "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
			appendAlgorithm(fields[algorithmIndex])
		case "ssh-rsa":
			appendAlgorithm("rsa-sha2-512")
			appendAlgorithm("rsa-sha2-256")
		default:
			return "", fmt.Errorf("unsupported pinned SSH host key algorithm %q", fields[algorithmIndex])
		}
	}
	if len(algorithms) == 0 {
		return "", errors.New("pinned SSH host key algorithm is required")
	}
	return strings.Join(algorithms, ","), nil
}

func escapeOpenSSHConfigPath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("SSH configuration path contains an invalid character")
	}
	return strings.NewReplacer(
		`\`, `\\`,
		" ", `\ `,
		"\t", "\\\t",
	).Replace(value), nil
}

func decodeDefinition(raw json.RawMessage) (Definition, error) {
	var definition Definition
	if !json.Valid(raw) || json.Unmarshal(raw, &definition) != nil {
		return definition, errors.New("valid rsync definition is required")
	}
	invalidText := func(value string) bool {
		return strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n")
	}
	if !filepath.IsAbs(definition.SourcePath) || !filepath.IsAbs(definition.Destination.Path) {
		return definition, errors.New("rsync source and destination paths must be absolute")
	}
	if strings.ContainsAny(definition.SourcePath, "\x00\r\n") || strings.ContainsAny(definition.Destination.Path, "\x00\r\n") {
		return definition, errors.New("rsync path contains an invalid character")
	}
	switch definition.Destination.effectiveKind() {
	case DestinationSSH:
		if invalidText(definition.Destination.Host) || invalidText(definition.Destination.Username) || strings.ContainsAny(definition.Destination.Host, "@[] ") || strings.ContainsAny(definition.Destination.Username, "@:/ ") {
			return definition, errors.New("rsync destination identity is invalid")
		}
		if definition.Destination.Port < 1 || definition.Destination.Port > 65535 || definition.Destination.PrivateKey == "" || strings.TrimSpace(definition.Destination.KnownHosts) == "" {
			return definition, errors.New("rsync destination SSH credentials are incomplete")
		}
	case DestinationLocal:
		if definition.Destination.Host != "" || definition.Destination.Username != "" || definition.Destination.Port != 0 || definition.Destination.PrivateKey != "" || definition.Destination.KnownHosts != "" {
			return definition, errors.New("local rsync destination cannot include SSH configuration")
		}
		if pathsOverlap(definition.SourcePath, definition.Destination.Path) {
			return definition, errors.New("local rsync source and destination paths must not overlap")
		}
	default:
		return definition, fmt.Errorf("unsupported rsync destination kind %q", definition.Destination.Kind)
	}
	for _, exclusion := range definition.Exclusions {
		if strings.ContainsAny(exclusion, "\x00\r\n") {
			return definition, errors.New("rsync exclusion contains an invalid character")
		}
	}
	return definition, nil
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
