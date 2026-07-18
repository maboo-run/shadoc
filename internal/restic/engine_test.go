package restic

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type recordingExecutor struct {
	spec   command.Spec
	result command.Result
	err    error
	check  func(command.Spec) error
}

type streamingSummaryExecutor struct{}

func (streamingSummaryExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	if spec.Stdout != nil {
		_, _ = spec.Stdout.Write([]byte(`{"message_type":"status","percent_done":0.9}` + "\n"))
		_, _ = spec.Stdout.Write([]byte(`{"message_type":"summary","snapshot_id":"streamed","files_new":2,"total_files_processed":3,"total_bytes_processed":300,"data_added":120,"total_duration":0.5}` + "\n"))
	}
	return command.Result{ExitCode: 0, Stdout: `{"message_type":"status","percent_done":0.9}` + "\n"}, nil
}

func (r *recordingExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	r.spec = spec
	if r.check != nil {
		if err := r.check(spec); err != nil {
			return command.Result{ExitCode: -1}, err
		}
	}
	return r.result, r.err
}

func TestEngineBacksUpDirectoryWithoutPuttingSecretsInArguments(t *testing.T) {
	recorder := &recordingExecutor{
		result: command.Result{ExitCode: 0, Stdout: `{"message_type":"summary","snapshot_id":"abc123","files_new":5,"files_changed":2,"files_unmodified":9,"total_files_processed":16,"total_bytes_processed":8192,"data_added":4096,"data_added_packed":2048,"total_duration":1.25}` + "\n"},
		check: func(spec command.Spec) error {
			joined := strings.Join(spec.Args, " ")
			if strings.Contains(joined, "repository-secret") || strings.Contains(joined, "PRIVATE KEY") {
				return errors.New("secret leaked into argv")
			}
			if !strings.Contains(joined, "'backup@192.168.1.20' -s sftp") {
				return errors.New("custom SFTP command omitted endpoint or subsystem")
			}
			keyPath := identityPath(spec.Args)
			info, err := os.Stat(keyPath)
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0o600 {
				return errors.New("identity file is not private")
			}
			return nil
		},
	}
	engine := New("/tools/restic", recorder, t.TempDir())
	result, err := engine.Execute(context.Background(), Operation{
		Kind: BackupDirectory,
		Repository: Repository{
			Location:      "sftp:backup@192.168.1.20:/volume1/restic/photos",
			Password:      "repository-secret",
			SSHPrivateKey: []byte("PRIVATE KEY"),
			SSHPort:       22,
		},
		Directory: &DirectoryBackup{
			Path: "/srv/photos", Exclusions: []string{"**/.cache"}, SkipIfUnchanged: true, Compression: "auto",
		},
	})
	if err != nil {
		t.Fatalf("execute backup: %v", err)
	}
	if result.Outcome != Success || result.SnapshotID != "abc123" {
		t.Fatalf("unexpected result: %+v", result)
	}
	for key, expected := range map[string]any{"filesProcessed": int64(16), "filesChanged": int64(7), "bytesProcessed": int64(8192), "bytesChanged": int64(4096), "durationMilliseconds": int64(1250), "filesNew": int64(5), "filesModified": int64(2), "dataAddedPacked": int64(2048)} {
		if result.Summary[key] != expected {
			t.Fatalf("summary[%s]=%#v want %#v; summary=%+v", key, result.Summary[key], expected, result.Summary)
		}
	}
	if recorder.spec.Env["RESTIC_PASSWORD"] != "repository-secret" {
		t.Fatal("repository password was not supplied through the environment")
	}
	if recorder.spec.Program != "/tools/restic" {
		t.Fatalf("program = %q", recorder.spec.Program)
	}
	keyPath := identityPath(recorder.spec.Args)
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("identity file was not removed: %v", err)
	}
}

func TestEngineUsesOnlyFixedS3EnvironmentAndOptions(t *testing.T) {
	recorder := &recordingExecutor{result: command.Result{ExitCode: 0, Stdout: `[]`}}
	engine := New("/tools/restic", recorder, t.TempDir())
	_, err := engine.Execute(t.Context(), Operation{Kind: ListSnapshots, Repository: Repository{
		Location: "s3:https://objects.example.com/backup-prod/photos", Password: "repository-secret",
		S3AccessKey: "access-id", S3SecretKey: "secret-value", S3Region: "eu-west-1", S3BucketLookup: "path",
	}})
	if err != nil {
		t.Fatalf("execute S3 operation: %v", err)
	}
	joined := strings.Join(recorder.spec.Args, " ")
	if !strings.Contains(joined, "-o s3.region=eu-west-1") || !strings.Contains(joined, "-o s3.bucket-lookup=path") {
		t.Fatalf("fixed S3 options missing: %s", joined)
	}
	if strings.Contains(joined, "access-id") || strings.Contains(joined, "secret-value") || recorder.spec.Env["AWS_ACCESS_KEY_ID"] != "access-id" || recorder.spec.Env["AWS_SECRET_ACCESS_KEY"] != "secret-value" || recorder.spec.Env["AWS_DEFAULT_REGION"] != "eu-west-1" {
		t.Fatalf("S3 credentials leaked or fixed environment missing: args=%s env=%v", joined, recorder.spec.Env)
	}
	if len(recorder.spec.Env) != 4 {
		t.Fatalf("unexpected environment keys: %v", recorder.spec.Env)
	}
}

func TestRepositoryCredentialsOverrideCommandEnvironmentCollisions(t *testing.T) {
	recorder := &recordingExecutor{result: command.Result{ExitCode: 0, Stdout: `{"message_type":"summary","snapshot_id":"snapshot"}`}}
	engine := New("/tools/restic", recorder, t.TempDir())
	_, err := engine.Execute(t.Context(), Operation{
		Kind:       BackupCommand,
		Repository: Repository{Location: "s3:https://objects.example.com/backup-prod", Password: "repository-secret", S3AccessKey: "access-id", S3SecretKey: "secret-value", S3Region: "us-east-1", S3BucketLookup: "dns"},
		Command:    &command.Spec{Program: "/tools/export", Env: map[string]string{"RESTIC_PASSWORD": "wrong", "AWS_ACCESS_KEY_ID": "wrong", "AWS_SECRET_ACCESS_KEY": "wrong", "AWS_DEFAULT_REGION": "wrong"}}, Filename: "export.bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"RESTIC_PASSWORD": "repository-secret", "AWS_ACCESS_KEY_ID": "access-id", "AWS_SECRET_ACCESS_KEY": "secret-value", "AWS_DEFAULT_REGION": "us-east-1"} {
		if recorder.spec.Env[key] != want {
			t.Fatalf("environment[%s]=%q want %q", key, recorder.spec.Env[key], want)
		}
	}
}

func TestEngineRejectsIncompleteOrUnsafeS3Material(t *testing.T) {
	engine := New("/tools/restic", &recordingExecutor{}, t.TempDir())
	for _, repository := range []Repository{
		{Location: "s3:https://objects.example.com/bucket", Password: "password"},
		{Location: "s3:http://objects.example.com/bucket", Password: "password", S3AccessKey: "access", S3SecretKey: "secret", S3Region: "us-east-1", S3BucketLookup: "path"},
		{Location: "s3:https://objects.example.com/bucket", Password: "password", S3AccessKey: "access", S3SecretKey: "secret", S3Region: "us-east-1", S3BucketLookup: "arbitrary"},
	} {
		if _, err := engine.Execute(t.Context(), Operation{Kind: ListSnapshots, Repository: repository}); err == nil {
			t.Fatalf("unsafe S3 material accepted: %+v", repository)
		}
	}
}

func TestEngineReadsFinalSummaryFromUnboundedOutputStream(t *testing.T) {
	engine := New("/tools/restic", streamingSummaryExecutor{}, t.TempDir())
	result, err := engine.Execute(context.Background(), Operation{Kind: BackupDirectory, Repository: Repository{Location: "/repo", Password: "secret"}, Directory: &DirectoryBackup{Path: "/source"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotID != "streamed" || result.Summary["filesProcessed"] != int64(3) || result.Summary["bytesChanged"] != int64(120) {
		t.Fatalf("result=%+v", result)
	}
}

func TestEngineListsSnapshotContentsAsJSON(t *testing.T) {
	recorder := &recordingExecutor{result: command.Result{ExitCode: 0}}
	engine := New("/tools/restic", recorder, t.TempDir())
	_, err := engine.Execute(context.Background(), Operation{Kind: ListSnapshotContents, Repository: Repository{Location: "/repo", Password: "secret"}, Arguments: []string{"snapshot-id"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(recorder.spec.Args, " "), "ls snapshot-id --json") {
		t.Fatalf("args=%v", recorder.spec.Args)
	}
}

func TestEngineVerifiesExistingRepositoryWithFixedReadOnlyArguments(t *testing.T) {
	recorder := &recordingExecutor{result: command.Result{ExitCode: 0, Stdout: `[]`}}
	engine := New("/tools/restic", recorder, t.TempDir())
	_, err := engine.Execute(context.Background(), Operation{Kind: VerifyRepository, Repository: Repository{Location: "/repo", Password: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(recorder.spec.Args, " "); !strings.HasSuffix(got, "snapshots --json --no-lock") || strings.Contains(got, " init ") {
		t.Fatalf("verification args=%v", recorder.spec.Args)
	}

	recorder.spec = command.Spec{}
	_, err = engine.Execute(context.Background(), Operation{Kind: VerifyRepository, Repository: Repository{Location: "/repo", Password: "secret"}, Arguments: []string{"--latest", "1"}})
	if err == nil || recorder.spec.Program != "" {
		t.Fatalf("verification accepted caller arguments: err=%v spec=%+v", err, recorder.spec)
	}
}

func TestEngineMapsResticExitThreeToPartialSuccess(t *testing.T) {
	recorder := &recordingExecutor{
		result: command.Result{ExitCode: 3, Stdout: `{"message_type":"summary","snapshot_id":"partial1"}` + "\n"},
		err:    errors.New("exit status 3"),
	}
	engine := New("/tools/restic", recorder, t.TempDir())
	result, err := engine.Execute(context.Background(), Operation{
		Kind:       BackupDirectory,
		Repository: Repository{Location: "/tmp/repository", Password: "secret"},
		Directory:  &DirectoryBackup{Path: "/srv/photos"},
	})
	if err != nil {
		t.Fatalf("partial backup must return a domain result, got %v", err)
	}
	if result.Outcome != Partial || result.SnapshotID != "partial1" {
		t.Fatalf("unexpected partial result: %+v", result)
	}
}

func TestEngineClassifiesRepositoryLockAndNetworkFailuresAsTemporary(t *testing.T) {
	for _, result := range []command.Result{
		{ExitCode: 11, Stderr: "unable to create lock"},
		{ExitCode: 1, Stderr: "ssh: connect to host failed: connection refused"},
	} {
		engine := New("/tools/restic", &recordingExecutor{result: result, err: errors.New("exit status")}, t.TempDir())
		_, err := engine.Execute(context.Background(), Operation{Kind: ListSnapshots, Repository: Repository{Location: "/repo", Password: "secret"}})
		var temporary interface{ Temporary() bool }
		if !errors.As(err, &temporary) || !temporary.Temporary() {
			t.Fatalf("failure was not retryable: result=%+v err=%v", result, err)
		}
		if strings.TrimSpace(result.Stderr) != "" && !strings.Contains(err.Error(), strings.TrimSpace(result.Stderr)) {
			t.Fatalf("restic stderr missing from error: result=%+v err=%v", result, err)
		}
	}
}

func TestSFTPEndpointRemovesURLBracketsForOpenSSHIPv6Destination(t *testing.T) {
	endpoint, err := sftpEndpoint("sftp:backup@[2001:db8::20]:/volume/restic")
	if err != nil || endpoint != "backup@2001:db8::20" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
}

func TestSSHArgumentsUseConfigFileForKnownHostsPathWithSpaces(t *testing.T) {
	engine := New("/tools/restic", &recordingExecutor{}, filepath.Join(t.TempDir(), "run with space"))
	args, cleanup, err := engine.sshArguments(Repository{
		Location: "sftp:backup@nas:repository", Password: "secret",
		SSHPrivateKey: []byte("PRIVATE KEY"), KnownHosts: []byte(testED25519KnownHostsLine(t, "nas")),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "UserKnownHostsFile=") || !strings.Contains(joined, " -F '") {
		t.Fatalf("known_hosts must be supplied through an OpenSSH config file: %s", joined)
	}
	configPath := sshConfigPath(args)
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read OpenSSH config: %v", err)
	}
	config := string(contents)
	if !strings.Contains(config, `UserKnownHostsFile "`) || !strings.Contains(config, `/known_hosts"`) {
		t.Fatalf("OpenSSH config does not quote known_hosts path: %s", config)
	}
}

func TestSSHArgumentsPinTheKnownHostKeyAlgorithm(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	engine := New("/tools/restic", &recordingExecutor{}, t.TempDir())
	args, cleanup, err := engine.sshArguments(Repository{
		Location:      "sftp:backup@nas:repository",
		Password:      "secret",
		SSHPrivateKey: []byte("PRIVATE KEY"),
		KnownHosts:    []byte(knownhosts.Line([]string{"nas"}, publicKey)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	contents, err := os.ReadFile(sshConfigPath(args))
	if err != nil {
		t.Fatalf("read OpenSSH config: %v", err)
	}
	if !strings.Contains(string(contents), "HostKeyAlgorithms ecdsa-sha2-nistp256") {
		t.Fatalf("OpenSSH config does not pin the saved host key algorithm: %s", contents)
	}
}

func TestSSHArgumentsRejectMalformedKnownHosts(t *testing.T) {
	engine := New("/tools/restic", &recordingExecutor{}, t.TempDir())
	_, cleanup, err := engine.sshArguments(Repository{
		Location:      "sftp:backup@nas:repository",
		Password:      "secret",
		SSHPrivateKey: []byte("PRIVATE KEY"),
		KnownHosts:    []byte("nas ssh-ed25519 invalid-key"),
	})
	cleanup()
	if err == nil || !strings.Contains(err.Error(), "parse pinned SSH host key") {
		t.Fatalf("malformed known_hosts entry was accepted: %v", err)
	}
}

func TestOpenSSHHostKeyAlgorithmsUseModernRSA(t *testing.T) {
	algorithms := openSSHHostKeyAlgorithms(ssh.KeyAlgoRSA)
	if got := strings.Join(algorithms, ","); got != "rsa-sha2-512,rsa-sha2-256" {
		t.Fatalf("RSA host key algorithms = %q", got)
	}
}

func testED25519KnownHostsLine(t *testing.T, host string) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return knownhosts.Line([]string{host}, sshKey)
}

func TestEngineStreamsDatabaseExportAndCleansCredentials(t *testing.T) {
	connector := database.NewMySQL(t.TempDir())
	prepared, metadata, err := connector.PrepareExport(context.Background(), database.Connection{
		Engine: database.MySQL, Purpose: database.Backup, Network: database.TCP,
		Host: "127.0.0.1", Port: 3306, Username: "backup", Password: "database-secret",
		DumpProgram: "/tools/mysqldump",
	}, "gitea")
	if err != nil {
		t.Fatalf("prepare export: %v", err)
	}
	recorder := &recordingExecutor{result: command.Result{ExitCode: 0, Stdout: `{"message_type":"summary","snapshot_id":"db123"}`}}
	engine := New("/tools/restic", recorder, t.TempDir())
	result, err := engine.Execute(context.Background(), Operation{
		Kind: BackupCommand, Repository: Repository{Location: "/repo", Password: "repo-secret"},
		Command: &prepared.Spec, Filename: metadata.Filename, CommandCleanup: prepared.Cleanup, Arguments: []string{"--tag", "rc:engine=mysql"},
	})
	if err != nil {
		t.Fatalf("execute database backup: %v", err)
	}
	if result.SnapshotID != "db123" {
		t.Fatalf("snapshot id = %q", result.SnapshotID)
	}
	joined := strings.Join(recorder.spec.Args, " ")
	if !strings.Contains(joined, "--stdin-from-command -- /tools/mysqldump") || !strings.Contains(joined, "--stdin-filename gitea.sql") {
		t.Fatalf("unexpected command backup argv: %s", joined)
	}
	if !strings.Contains(joined, "--tag rc:engine=mysql") {
		t.Fatalf("snapshot metadata tag missing: %s", joined)
	}
	if strings.Contains(joined, "database-secret") || strings.Contains(joined, "repo-secret") {
		t.Fatalf("secret leaked into argv: %s", joined)
	}
	if _, err := os.Stat(prepared.CredentialPath); !os.IsNotExist(err) {
		t.Fatalf("database credentials were not removed: %v", err)
	}
}

func identityPath(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "sftp.command=ssh -i ") {
			fields := strings.Fields(strings.TrimPrefix(arg, "sftp.command="))
			for index, field := range fields {
				if field == "-i" && index+1 < len(fields) {
					return strings.Trim(fields[index+1], "'")
				}
			}
		}
	}
	return ""
}

func sshConfigPath(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "sftp.command=") {
			continue
		}
		command := strings.TrimPrefix(arg, "sftp.command=")
		marker := " -F '"
		start := strings.Index(command, marker)
		if start < 0 {
			return ""
		}
		start += len(marker)
		end := strings.Index(command[start:], "'")
		if end < 0 {
			return ""
		}
		return command[start : start+end]
	}
	return ""
}
