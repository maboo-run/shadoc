package rsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestDryRunUsesTheExactRealArgumentsPlusDryRun(t *testing.T) {
	executor := &sequenceExecutor{}
	engine := New("rsync", executor, t.TempDir())
	definition := Definition{
		SourcePath: "/mnt/source/", Destination: Destination{Kind: DestinationLocal, Path: "/mnt/target"},
		Exclusions: []string{"**/.cache"}, Delete: true,
	}
	real, _ := json.Marshal(definition)
	if _, err := engine.Run(context.Background(), execution.Assignment{Definition: real}); err != nil {
		t.Fatal(err)
	}
	definition.DryRun = true
	preview, _ := json.Marshal(definition)
	if _, err := engine.Run(context.Background(), execution.Assignment{Definition: preview}); err != nil {
		t.Fatal(err)
	}
	if len(executor.specs) != 2 {
		t.Fatalf("specs=%d", len(executor.specs))
	}
	previewArgs := append([]string(nil), executor.specs[1].Args...)
	index := slices.Index(previewArgs, "--dry-run")
	if index < 0 {
		t.Fatalf("preview args=%v", previewArgs)
	}
	previewArgs = slices.Delete(previewArgs, index, index+1)
	if !slices.Equal(executor.specs[0].Args, previewArgs) {
		t.Fatalf("real=%v preview without dry-run=%v", executor.specs[0].Args, previewArgs)
	}
}

func TestBuildArgumentsSynchronizesSourceDirectoryContents(t *testing.T) {
	args := buildArguments(Definition{SourcePath: "/mnt/source"}, "/mnt/target", "")
	if got, want := args[len(args)-2], "/mnt/source"+string(filepath.Separator); got != want {
		t.Fatalf("source argument=%q want directory contents form", got)
	}
}

func TestDryRunSummarizesDeletionsAndTargetIdentity(t *testing.T) {
	executor := &sequenceExecutor{result: command.Result{ExitCode: 0, Stdout: "*deleting   old-directory/\n*deleting   old-file.txt\n>f+++++++++ new-file.txt\n"}}
	engine := New("rsync", executor, t.TempDir())
	raw, _ := json.Marshal(Definition{
		SourcePath: "/mnt/source/", Destination: Destination{Kind: DestinationLocal, Path: "/mnt/target"}, Delete: true, DryRun: true,
	})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: raw})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Summary["deleteFiles"] != 1 || outcome.Summary["deleteDirectories"] != 1 || outcome.Summary["targetIdentity"] != "local:/mnt/target" || outcome.Summary["dryRun"] != true {
		t.Fatalf("summary=%+v", outcome.Summary)
	}
}

func TestRunParsesControlledStatsIntoComparableMetrics(t *testing.T) {
	executor := &sequenceExecutor{result: command.Result{ExitCode: 0, Duration: 1750 * time.Millisecond, Stdout: strings.Join([]string{
		">f+++++++++ new-file.txt",
		"Number of files: 12 (reg: 10, dir: 2)",
		"Number of regular files transferred: 3",
		"Total file size: 8,192 bytes",
		"Total transferred file size: 1,024 bytes",
	}, "\n")}}
	engine := New("rsync", executor, t.TempDir())
	raw, _ := json.Marshal(Definition{SourcePath: "/mnt/source/", Destination: Destination{Kind: DestinationLocal, Path: "/mnt/target"}})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(executor.specs[0].Args, "--stats") || executor.specs[0].Env["LC_ALL"] != "C" {
		t.Fatalf("spec=%+v", executor.specs[0])
	}
	for key, expected := range map[string]any{"filesProcessed": int64(12), "filesChanged": int64(1), "bytesProcessed": int64(8192), "bytesChanged": int64(1024), "durationMilliseconds": int64(1750), "regularFilesTransferred": int64(3)} {
		if outcome.Summary[key] != expected {
			t.Fatalf("summary[%s]=%#v want %#v; summary=%+v", key, outcome.Summary[key], expected, outcome.Summary)
		}
	}
}

func TestDryRunMarksBoundedOutputAsTruncated(t *testing.T) {
	executor := &sequenceExecutor{result: command.Result{ExitCode: 0, Stdout: strings.Repeat("x", maxRsyncRawLogBytes)}}
	engine := New("rsync", executor, t.TempDir())
	raw, _ := json.Marshal(Definition{
		SourcePath: "/mnt/source/", Destination: Destination{Kind: DestinationLocal, Path: "/mnt/target"}, DryRun: true,
	})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: raw})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Summary["truncated"] != true || len(outcome.RawLog) > maxRsyncRawLogBytes {
		t.Fatalf("summary=%+v log bytes=%d", outcome.Summary, len(outcome.RawLog))
	}
}

func TestEngineRunsIncrementalSyncWithProtectedSSHFiles(t *testing.T) {
	executor := &captureExecutor{t: t, privateKey: "PRIVATE-KEY"}
	engine := New("rsync", executor, t.TempDir())
	definition, _ := json.Marshal(Definition{SourcePath: "/srv/data/", Destination: Destination{Host: "backup.example", Port: 2222, Username: "backup", Path: "/archive/data", PrivateKey: "PRIVATE-KEY", KnownHosts: "backup.example ssh-ed25519 AAAA"}, Exclusions: []string{"cache/**"}, Delete: true})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "succeeded" || !executor.called {
		t.Fatalf("outcome=%+v called=%v", outcome, executor.called)
	}
	joined := strings.Join(executor.spec.Args, " ")
	for _, required := range []string{"--archive", "--protect-args", "--delete", "--exclude", "cache/**", "backup@backup.example:/archive/data"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %s", required, joined)
		}
	}
	if strings.Contains(joined, "PRIVATE-KEY") {
		t.Fatal("private key leaked into command arguments")
	}
}

func TestEnginePinsOpenSSHToTheStoredHostKeyAlgorithm(t *testing.T) {
	executor := &hostKeyCompatibilityExecutor{}
	engine := New("rsync", executor, filepath.Join(t.TempDir(), "Application Support", "run"))
	definition, _ := json.Marshal(Definition{
		SourcePath: "/srv/data/",
		Destination: Destination{
			Host: "192.168.0.104", Port: 22, Username: "backup", Path: "/archive/data",
			PrivateKey: "PRIVATE-KEY",
			KnownHosts: "192.168.0.104 ecdsa-sha2-nistp256 AAAA",
		},
	})
	if _, err := engine.Run(context.Background(), execution.Assignment{Definition: definition}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRejectsRelativePaths(t *testing.T) {
	engine := New("rsync", &captureExecutor{t: t}, t.TempDir())
	definition, _ := json.Marshal(Definition{SourcePath: "relative", Destination: Destination{Host: "host", Port: 22, Username: "user", Path: "/target", PrivateKey: "key", KnownHosts: "known"}})
	if err := engine.Validate(definition); err == nil {
		t.Fatal("relative source path accepted")
	}
}

func TestEngineRunsLocalToLocalWithoutSSH(t *testing.T) {
	executor := &localCaptureExecutor{}
	engine := New("rsync", executor, t.TempDir())
	definition, _ := json.Marshal(Definition{SourcePath: "/mnt/disk-a/photos/", Destination: Destination{Kind: DestinationLocal, Path: "/mnt/disk-b/photos"}, Delete: true})
	outcome, err := engine.Run(context.Background(), execution.Assignment{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "succeeded" {
		t.Fatalf("outcome=%+v", outcome)
	}
	joined := strings.Join(executor.spec.Args, " ")
	if strings.Contains(joined, "ssh") || strings.Contains(joined, " -e ") || strings.Contains(joined, "@") {
		t.Fatalf("local sync contains SSH arguments: %s", joined)
	}
	if !strings.Contains(joined, "-- /mnt/disk-a/photos/ /mnt/disk-b/photos") {
		t.Fatalf("local source and destination missing: %s", joined)
	}
}

func TestProbeRejectsLegacyRsync(t *testing.T) {
	engine := New("rsync", versionExecutor{output: "rsync  version 2.6.9  protocol version 29"}, t.TempDir())
	if err := engine.Probe(context.Background()); err == nil {
		t.Fatal("legacy rsync accepted")
	}
}

type versionExecutor struct{ output string }

func (e versionExecutor) Run(context.Context, command.Spec) (command.Result, error) {
	return command.Result{Stdout: e.output}, nil
}

type captureExecutor struct {
	t          *testing.T
	privateKey string
	called     bool
	spec       command.Spec
}

type localCaptureExecutor struct{ spec command.Spec }

type hostKeyCompatibilityExecutor struct{}

func (e *hostKeyCompatibilityExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	arguments := strings.Join(spec.Args, " ")
	if !strings.Contains(arguments, "HostKeyAlgorithms=ecdsa-sha2-nistp256") {
		return command.Result{
			ExitCode: 255,
			Stderr:   "No ECDSA host key is known for 192.168.0.104 and you have requested strict checking.",
		}, errors.New("exit status 255")
	}
	if !strings.Contains(arguments, `Application\ Support`) {
		return command.Result{
			ExitCode: 255,
			Stderr:   "No ECDSA host key is known for 192.168.0.104 and you have requested strict checking.",
		}, errors.New("exit status 255")
	}
	return command.Result{}, nil
}

type sequenceExecutor struct {
	specs  []command.Spec
	result command.Result
}

func (e *sequenceExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.specs = append(e.specs, spec)
	return e.result, nil
}

func (e *localCaptureExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.spec = spec
	return command.Result{ExitCode: 0}, nil
}

func (e *captureExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.called, e.spec = true, spec
	joined := strings.Join(spec.Args, " ")
	marker := "-i "
	position := strings.Index(joined, marker)
	if position < 0 {
		e.t.Fatalf("SSH identity option missing: %s", joined)
	}
	identity := strings.Fields(joined[position+len(marker):])[0]
	identity = strings.Trim(identity, "'")
	content, err := os.ReadFile(identity)
	if err != nil {
		e.t.Fatalf("read temporary identity: %v", err)
	}
	if string(content) != e.privateKey {
		e.t.Fatalf("identity content = %q", content)
	}
	info, _ := os.Stat(identity)
	if info.Mode().Perm() != 0o600 {
		e.t.Fatalf("identity permissions = %o", info.Mode().Perm())
	}
	return command.Result{ExitCode: 0, Stdout: "sent 12 bytes"}, nil
}
