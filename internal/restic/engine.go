package restic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/maboo-run/shadoc/internal/command"
	"golang.org/x/crypto/ssh"
)

type OperationKind string

const (
	InitializeRepo       OperationKind = "initialize-repository"
	BackupDirectory      OperationKind = "backup-directory"
	BackupCommand        OperationKind = "backup-command"
	VerifyRepository     OperationKind = "verify-repository"
	ListSnapshots        OperationKind = "list-snapshots"
	ListSnapshotContents OperationKind = "list-snapshot-contents"
	ForgetSnapshots      OperationKind = "forget-snapshots"
	PruneRepository      OperationKind = "prune-repository"
	CheckRepository      OperationKind = "check-repository"
	DumpSnapshot         OperationKind = "dump-snapshot"
	RestoreDirectory     OperationKind = "restore-directory"
	ListKeys             OperationKind = "list-keys"
	AddKey               OperationKind = "add-key"
	RemoveKey            OperationKind = "remove-key"
	TagSnapshot          OperationKind = "tag-snapshot"
)

type Repository struct {
	Location       string
	Password       string
	SSHPrivateKey  []byte
	SSHPort        int
	KnownHosts     []byte
	S3AccessKey    string
	S3SecretKey    string
	S3Region       string
	S3BucketLookup string
}

type DirectoryBackup struct {
	Path            string
	Exclusions      []string
	SkipIfUnchanged bool
	Compression     string
}

type Operation struct {
	Kind           OperationKind
	Repository     Repository
	Directory      *DirectoryBackup
	Command        *command.Spec
	Filename       string
	CommandCleanup func()
	Arguments      []string
	Output         io.Writer
	NewPassword    string
}

type Outcome string

const (
	Success Outcome = "success"
	Partial Outcome = "partial"
	Failure Outcome = "failure"
)

type Result struct {
	Outcome    Outcome
	SnapshotID string
	ExitCode   int
	Stdout     string
	Stderr     string
	Summary    map[string]any
}

type Engine struct {
	mu       sync.RWMutex
	program  string
	executor command.Executor
	tempRoot string
}

func (e *Engine) SetProgram(program string) { e.mu.Lock(); e.program = program; e.mu.Unlock() }

func New(program string, executor command.Executor, tempRoot string) *Engine {
	return &Engine{program: program, executor: executor, tempRoot: tempRoot}
}

func (e *Engine) Execute(ctx context.Context, operation Operation) (Result, error) {
	e.mu.RLock()
	program := e.program
	e.mu.RUnlock()
	if program == "" || operation.Repository.Location == "" || operation.Repository.Password == "" {
		return Result{Outcome: Failure, ExitCode: -1}, errors.New("restic program, repository and password are required")
	}
	args := []string{"-r", operation.Repository.Location}
	usesS3 := strings.HasPrefix(operation.Repository.Location, "s3:") || operation.Repository.S3AccessKey != "" || operation.Repository.S3SecretKey != "" || operation.Repository.S3Region != "" || operation.Repository.S3BucketLookup != ""
	if usesS3 {
		if err := validateS3Material(operation.Repository); err != nil {
			return Result{Outcome: Failure, ExitCode: -1}, err
		}
		args = append(args, "-o", "s3.region="+operation.Repository.S3Region, "-o", "s3.bucket-lookup="+operation.Repository.S3BucketLookup)
	}
	cleanup := func() {}
	if len(operation.Repository.SSHPrivateKey) != 0 {
		sshArgs, remove, err := e.sshArguments(operation.Repository)
		if err != nil {
			return Result{Outcome: Failure, ExitCode: -1}, err
		}
		cleanup = remove
		args = append(args, sshArgs...)
	}
	defer cleanup()
	if operation.CommandCleanup != nil {
		defer operation.CommandCleanup()
	}
	if operation.Kind == AddKey {
		if operation.NewPassword == "" {
			return Result{Outcome: Failure, ExitCode: -1}, errors.New("new repository password is required")
		}
		dir, err := os.MkdirTemp(e.tempRoot, "restic-key-")
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "new-password")
		if err := os.WriteFile(path, []byte(operation.NewPassword), 0o600); err != nil {
			return Result{}, err
		}
		operation.Arguments = append(operation.Arguments, "--new-password-file", path)
	}

	operationArgs, err := buildOperationArguments(operation)
	if err != nil {
		return Result{Outcome: Failure, ExitCode: -1}, err
	}
	args = append(args, operationArgs...)
	environment := make(map[string]string)
	if operation.Command != nil {
		for key, value := range operation.Command.Env {
			environment[key] = value
		}
	}
	environment["RESTIC_PASSWORD"] = operation.Repository.Password
	if usesS3 {
		environment["AWS_ACCESS_KEY_ID"] = operation.Repository.S3AccessKey
		environment["AWS_SECRET_ACCESS_KEY"] = operation.Repository.S3SecretKey
		environment["AWS_DEFAULT_REGION"] = operation.Repository.S3Region
	}
	var summaryCollector *resticSummaryCollector
	output := operation.Output
	if operation.Kind == BackupDirectory || operation.Kind == BackupCommand {
		summaryCollector = &resticSummaryCollector{}
		if output == nil {
			output = summaryCollector
		} else {
			output = io.MultiWriter(output, summaryCollector)
		}
	}
	processResult, processErr := e.executor.Run(ctx, command.Spec{
		Program: program,
		Args:    args,
		Env:     environment,
		Stdout:  output,
	})
	result := Result{
		Outcome:  Success,
		ExitCode: processResult.ExitCode,
		Stdout:   processResult.Stdout,
		Stderr:   processResult.Stderr,
	}
	if summaryCollector != nil && summaryCollector.sawOutput() {
		result.SnapshotID, result.Summary = summaryCollector.result()
	} else {
		result.SnapshotID, result.Summary = parsedBackupSummary(processResult.Stdout)
	}
	if result.SnapshotID == "" {
		result.SnapshotID = snapshotID(processResult.Stdout)
	}
	if processResult.ExitCode == 3 && operation.Kind == BackupDirectory {
		result.Outcome = Partial
		return result, nil
	}
	if processErr != nil && (errors.Is(processErr, os.ErrNotExist) || errors.Is(processErr, os.ErrPermission)) {
		return result, errors.New("restic executable is missing or not executable; install Restic before repository operations")
	}
	if processErr != nil || processResult.ExitCode != 0 {
		result.Outcome = Failure
		return result, executionError{kind: operation.Kind, exitCode: processResult.ExitCode, stderr: processResult.Stderr, cause: processErr}
	}
	return result, nil
}

func validateS3Material(repository Repository) error {
	if !strings.HasPrefix(repository.Location, "s3:") || repository.S3AccessKey == "" || repository.S3SecretKey == "" || repository.S3Region == "" {
		return errors.New("structured S3 location, access key, secret key, and region are required")
	}
	if strings.ContainsAny(repository.S3AccessKey+repository.S3SecretKey, "\x00\r\n") {
		return errors.New("S3 credentials contain control characters")
	}
	for _, value := range repository.S3Region {
		if !(value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '_') {
			return errors.New("S3 region is invalid")
		}
	}
	if repository.S3BucketLookup != "path" && repository.S3BucketLookup != "dns" {
		return errors.New("S3 bucket lookup must be path or dns")
	}
	endpoint := strings.TrimPrefix(repository.Location, "s3:")
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" {
		return errors.New("S3 repository location is invalid")
	}
	if parsed.Scheme != "https" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if parsed.Scheme != "http" || !(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
			return errors.New("S3 repository location requires HTTPS")
		}
	}
	return nil
}

type executionError struct {
	kind     OperationKind
	exitCode int
	stderr   string
	cause    error
}

func (e executionError) Error() string {
	stderr := strings.TrimSpace(e.stderr)
	if e.cause != nil && stderr != "" {
		return fmt.Sprintf("restic %s failed with exit code %d: %s: %v", e.kind, e.exitCode, stderr, e.cause)
	}
	if e.cause != nil {
		return fmt.Sprintf("restic %s failed with exit code %d: %v", e.kind, e.exitCode, e.cause)
	}
	if stderr != "" {
		return fmt.Sprintf("restic %s failed with exit code %d: %s", e.kind, e.exitCode, stderr)
	}
	return fmt.Sprintf("restic %s failed with exit code %d", e.kind, e.exitCode)
}
func (e executionError) Unwrap() error { return e.cause }
func (e executionError) Temporary() bool {
	if e.exitCode == 11 {
		return true
	}
	message := strings.ToLower(e.stderr)
	for _, fragment := range []string{"connection refused", "connection reset", "network is unreachable", "no route to host", "i/o timeout", "timed out", "temporary failure", "ssh: connect"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func buildOperationArguments(operation Operation) ([]string, error) {
	switch operation.Kind {
	case InitializeRepo:
		return []string{"init", "--json"}, nil
	case BackupDirectory:
		if operation.Directory == nil || operation.Directory.Path == "" {
			return nil, errors.New("directory backup source is required")
		}
		args := append([]string{"backup", "--json"}, operation.Arguments...)
		if operation.Directory.SkipIfUnchanged {
			args = append(args, "--skip-if-unchanged")
		}
		if operation.Directory.Compression != "" {
			args = append(args, "--compression", operation.Directory.Compression)
		}
		for _, exclusion := range operation.Directory.Exclusions {
			args = append(args, "--exclude", exclusion)
		}
		return append(args, operation.Directory.Path), nil
	case BackupCommand:
		if operation.Command == nil || operation.Command.Program == "" || operation.Filename == "" {
			return nil, errors.New("command backup requires program and snapshot filename")
		}
		args := append([]string{"backup", "--json"}, operation.Arguments...)
		args = append(args, "--stdin-filename", operation.Filename, "--stdin-from-command", "--", operation.Command.Program)
		return append(args, operation.Command.Args...), nil
	case VerifyRepository:
		if len(operation.Arguments) != 0 {
			return nil, errors.New("repository verification does not accept caller arguments")
		}
		return []string{"snapshots", "--json", "--no-lock"}, nil
	case ListSnapshots:
		return append([]string{"snapshots", "--json"}, operation.Arguments...), nil
	case ListSnapshotContents:
		return append([]string{"ls"}, append(operation.Arguments, "--json")...), nil
	case ForgetSnapshots:
		return append([]string{"forget", "--json"}, operation.Arguments...), nil
	case PruneRepository:
		return append([]string{"prune", "--json"}, operation.Arguments...), nil
	case CheckRepository:
		return append([]string{"check", "--json"}, operation.Arguments...), nil
	case DumpSnapshot:
		return append([]string{"dump"}, operation.Arguments...), nil
	case RestoreDirectory:
		return append([]string{"restore", "--json"}, operation.Arguments...), nil
	case ListKeys:
		return append([]string{"key", "list", "--json"}, operation.Arguments...), nil
	case AddKey:
		return append([]string{"key", "add", "--json"}, operation.Arguments...), nil
	case RemoveKey:
		if len(operation.Arguments) != 1 {
			return nil, errors.New("key removal requires exact key ID")
		}
		return []string{"key", "remove", operation.Arguments[0]}, nil
	case TagSnapshot:
		return append([]string{"tag", "--json"}, operation.Arguments...), nil
	default:
		return nil, fmt.Errorf("unsupported restic operation %q", operation.Kind)
	}
}

func (e *Engine) sshArguments(repository Repository) ([]string, func(), error) {
	if err := os.MkdirAll(e.tempRoot, 0o700); err != nil {
		return nil, func() {}, fmt.Errorf("create restic temp root: %w", err)
	}
	dir, err := os.MkdirTemp(e.tempRoot, "restic-run-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create restic temp directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	keyPath := filepath.Join(dir, "identity")
	if err := os.WriteFile(keyPath, repository.SSHPrivateKey, 0o600); err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("write ssh identity: %w", err)
	}
	port := repository.SSHPort
	if port == 0 {
		port = 22
	}
	endpoint, err := sftpEndpoint(repository.Location)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	sshCommand := "ssh -i " + quoteSSHArgument(keyPath) + " -p " + strconv.Itoa(port) + " -o BatchMode=yes"
	if len(repository.KnownHosts) != 0 {
		hostKeyAlgorithms, err := pinnedHostKeyAlgorithms(repository.KnownHosts)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		knownHostsPath := filepath.Join(dir, "known_hosts")
		if err := os.WriteFile(knownHostsPath, repository.KnownHosts, 0o600); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write known hosts: %w", err)
		}
		configPath := filepath.Join(dir, "ssh_config")
		config := "Host *\n" +
			"  BatchMode yes\n" +
			"  StrictHostKeyChecking yes\n" +
			"  UserKnownHostsFile " + quoteOpenSSHConfigValue(knownHostsPath) + "\n" +
			"  HostKeyAlgorithms " + hostKeyAlgorithms + "\n"
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write ssh config: %w", err)
		}
		sshCommand = "ssh -F " + quoteSSHArgument(configPath) + " -i " + quoteSSHArgument(keyPath) + " -p " + strconv.Itoa(port)
	}
	sshCommand += " " + quoteSSHArgument(endpoint) + " -s sftp"
	return []string{"-o", "sftp.command=" + sshCommand}, cleanup, nil
}

func sftpEndpoint(location string) (string, error) {
	value := strings.TrimPrefix(location, "sftp:")
	delimiter := strings.Index(value, ":")
	if bracket := strings.Index(value, "]:"); bracket >= 0 {
		delimiter = bracket + 1
	}
	if delimiter <= 0 {
		return "", errors.New("invalid SFTP repository location")
	}
	endpoint := value[:delimiter]
	at := strings.LastIndex(endpoint, "@")
	if at < 1 || at == len(endpoint)-1 {
		return "", errors.New("SFTP repository must include user and host")
	}
	// Restic's repository URL requires brackets around IPv6 literals, while the
	// OpenSSH destination argument expects the raw literal after user@.
	host := endpoint[at+1:]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return endpoint[:at+1] + host, nil
}

func quoteSSHArgument(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func quoteOpenSSHConfigValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func pinnedHostKeyAlgorithms(data []byte) (string, error) {
	remaining := data
	seen := make(map[string]struct{})
	algorithms := make([]string, 0, 2)
	for {
		_, _, publicKey, _, rest, err := ssh.ParseKnownHosts(remaining)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse pinned SSH host key: %w", err)
		}
		mapped := openSSHHostKeyAlgorithms(publicKey.Type())
		if len(mapped) == 0 {
			return "", fmt.Errorf("unsupported pinned SSH host key algorithm %q", publicKey.Type())
		}
		for _, algorithm := range mapped {
			if _, ok := seen[algorithm]; ok {
				continue
			}
			seen[algorithm] = struct{}{}
			algorithms = append(algorithms, algorithm)
		}
		remaining = rest
	}
	if len(algorithms) == 0 {
		return "", errors.New("pinned SSH host key is empty")
	}
	return strings.Join(algorithms, ","), nil
}

func openSSHHostKeyAlgorithms(keyType string) []string {
	switch keyType {
	case ssh.KeyAlgoRSA:
		// A known_hosts RSA public key can be verified with the modern RSA/SHA-2
		// signature algorithms. Do not re-enable the legacy ssh-rsa/SHA-1 mode.
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoED25519, ssh.KeyAlgoSKECDSA256, ssh.KeyAlgoSKED25519:
		return []string{keyType}
	default:
		return nil
	}
}

func snapshotID(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var message struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err == nil && message.SnapshotID != "" {
			return message.SnapshotID
		}
	}
	return ""
}

type resticSummaryMessage struct {
	MessageType         string   `json:"message_type"`
	SnapshotID          string   `json:"snapshot_id"`
	FilesNew            *int64   `json:"files_new"`
	FilesChanged        *int64   `json:"files_changed"`
	FilesUnmodified     *int64   `json:"files_unmodified"`
	TotalFilesProcessed *int64   `json:"total_files_processed"`
	TotalBytesProcessed *int64   `json:"total_bytes_processed"`
	DataAdded           *int64   `json:"data_added"`
	DataAddedPacked     *int64   `json:"data_added_packed"`
	TotalDuration       *float64 `json:"total_duration"`
}

func parsedBackupSummary(output string) (string, map[string]any) {
	var latest resticSummaryMessage
	found := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 16<<10), 1<<20)
	for scanner.Scan() {
		var message resticSummaryMessage
		if json.Unmarshal(scanner.Bytes(), &message) == nil && message.MessageType == "summary" {
			latest, found = message, true
		}
	}
	if !found {
		return "", nil
	}
	return resticSummaryValues(latest)
}

func resticSummaryValues(latest resticSummaryMessage) (string, map[string]any) {
	summary := map[string]any{}
	add := func(key string, value *int64) {
		if value != nil && *value >= 0 {
			summary[key] = *value
		}
	}
	add("filesNew", latest.FilesNew)
	add("filesModified", latest.FilesChanged)
	add("filesUnmodified", latest.FilesUnmodified)
	add("filesProcessed", latest.TotalFilesProcessed)
	add("bytesProcessed", latest.TotalBytesProcessed)
	add("dataAdded", latest.DataAdded)
	add("dataAddedPacked", latest.DataAddedPacked)
	if latest.FilesNew != nil || latest.FilesChanged != nil {
		newFiles, changedFiles := int64(0), int64(0)
		if latest.FilesNew != nil && *latest.FilesNew >= 0 {
			newFiles = *latest.FilesNew
		}
		if latest.FilesChanged != nil && *latest.FilesChanged >= 0 {
			changedFiles = *latest.FilesChanged
		}
		if newFiles <= math.MaxInt64-changedFiles {
			summary["filesChanged"] = newFiles + changedFiles
		}
	}
	if latest.DataAdded != nil && *latest.DataAdded >= 0 {
		summary["bytesChanged"] = *latest.DataAdded
	}
	if latest.TotalDuration != nil && !math.IsNaN(*latest.TotalDuration) && !math.IsInf(*latest.TotalDuration, 0) && *latest.TotalDuration >= 0 && *latest.TotalDuration <= float64(math.MaxInt64)/1000 {
		summary["totalDurationSeconds"] = *latest.TotalDuration
		summary["durationMilliseconds"] = int64(math.Round(*latest.TotalDuration * 1000))
	}
	return latest.SnapshotID, summary
}

type resticSummaryCollector struct {
	mu     sync.Mutex
	buffer strings.Builder
	seen   bool
	found  bool
	latest resticSummaryMessage
}

func (c *resticSummaryCollector) Write(value []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := len(value)
	c.seen = c.seen || written > 0
	for len(value) > 0 {
		index := strings.IndexByte(string(value), '\n')
		if index < 0 {
			c.append(value)
			break
		}
		c.append(value[:index])
		c.consume(c.buffer.String())
		c.buffer.Reset()
		value = value[index+1:]
	}
	return written, nil
}

func (c *resticSummaryCollector) append(value []byte) {
	const maximumLineBytes = 1 << 20
	remaining := maximumLineBytes - c.buffer.Len()
	if remaining > 0 {
		_, _ = c.buffer.Write(value[:min(len(value), remaining)])
	}
}

func (c *resticSummaryCollector) consume(line string) {
	var message resticSummaryMessage
	if json.Unmarshal([]byte(line), &message) == nil && message.MessageType == "summary" {
		c.latest, c.found = message, true
	}
}

func (c *resticSummaryCollector) sawOutput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

func (c *resticSummaryCollector) result() (string, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buffer.Len() > 0 {
		c.consume(c.buffer.String())
		c.buffer.Reset()
	}
	if !c.found {
		return "", nil
	}
	return resticSummaryValues(c.latest)
}
