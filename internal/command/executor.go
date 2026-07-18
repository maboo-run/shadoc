package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultOutputLimit = 4 << 20

type Spec struct {
	Program string
	Args    []string
	Env     map[string]string
	Dir     string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

type Executor interface {
	Run(context.Context, Spec) (Result, error)
}

type OSExecutor struct {
	InterruptTimeout time.Duration
	OutputLimit      int
}

func (e OSExecutor) Run(ctx context.Context, spec Spec) (Result, error) {
	if spec.Program == "" {
		return Result{ExitCode: -1}, errors.New("program is required")
	}
	interruptTimeout := e.InterruptTimeout
	if interruptTimeout <= 0 {
		interruptTimeout = 5 * time.Second
	}
	limit := e.OutputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}

	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = mergeEnvironment(allowedEnvironment(os.Environ()), spec.Env)
	prepareCommand(cmd)
	cmd.Stdin = spec.Stdin
	stdout := newLimitedBuffer(limit)
	stderr := newLimitedBuffer(limit)
	cmd.Stdout = stdout
	if spec.Stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, spec.Stdout)
	}
	cmd.Stderr = stderr
	if spec.Stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, spec.Stderr)
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("start %s: %w", spec.Program, err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		_ = interruptProcess(cmd)
		timer := time.NewTimer(interruptTimeout)
		select {
		case <-waitCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = killProcess(cmd)
			<-waitCh
		}
		waitErr = ctx.Err()
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	result := Result{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
	}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

func allowedEnvironment(base []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true, "TZ": true, "USER": true, "LOGNAME": true, "XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true}
	result := make([]string, 0, len(allowed))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[key] {
			result = append(result, item)
		}
	}
	return result
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(value)
	if b.remaining > 0 {
		keep := min(len(value), b.remaining)
		_, _ = b.buffer.Write(value[:keep])
		b.remaining -= keep
	}
	return written, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
