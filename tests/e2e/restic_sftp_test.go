//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/maboo-run/shadoc/internal/backup"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repolock"
	"github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestRealResticSFTP(t *testing.T) {
	resticPath, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal("release E2E requires a real restic binary in PATH")
	}
	root := t.TempDir()
	server := startSFTPServer(t, filepath.Join(root, "remote"))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	s, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.New(s, v, time.Now)
	keySecret, err := secrets.Put(ctx, "ssh-private-key", server.ClientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	const oldPassword = "old-repository-password-long"
	passwordSecret, err := secrets.Put(ctx, "repository-password", []byte(oldPassword))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	host := domain.RemoteHost{
		ID: "host", Name: "ephemeral-sftp", Host: "127.0.0.1", Port: server.Port,
		Username: "backup", HostFingerprint: server.KnownHostsLine,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateRemoteHost(ctx, host, keySecret); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{
		ID: "repo", Name: "real-sftp", RemoteHostID: host.ID, Path: "repository",
		Status: "uninitialized", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateRepository(ctx, repo, passwordSecret); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	want := []byte("restic-control real SFTP payload\x00\x01\n")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "payload.bin"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := restic.New(resticPath, command.OSExecutor{}, filepath.Join(root, "run with space"))
	runner := diagnosticRunner{engine: engine, t: t}
	locks := repolock.New()
	repositories := repository.New(s, secrets, runner)
	repositories.SetLocker(locks)
	if err := repositories.Initialize(ctx, repo.ID); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}

	task := domain.Task{
		ID: "task", Name: "real-directory", Kind: domain.DirectoryTask, RepositoryID: repo.ID,
		Directory: &domain.DirectorySource{Path: source}, Resources: domain.ResourcePolicy{Compression: "auto"},
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	backups := backup.New(s, secrets, runner, nil, nil, time.Now)
	backups.SetRepositoryLocker(locks)

	run, err := backups.Run(ctx, task.ID, "", "e2e")
	if err != nil || run.Status != "success" || run.SnapshotID == "" {
		t.Fatalf("backup run=%+v err=%v", run, err)
	}
	snapshots, err := repositories.Snapshots(ctx, repo.ID)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != run.SnapshotID {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	restoreTarget := filepath.Join(root, "restored")
	if err := repositories.RestoreDirectory(ctx, repo.ID, run.SnapshotID, restoreTarget, nil, 0); err != nil {
		t.Fatalf("restore directory: %v", err)
	}
	restoredPayload := filepath.Join(restoreTarget, "nested", "payload.bin")
	got, err := os.ReadFile(restoredPayload)
	if err != nil {
		t.Fatalf("read restored payload at %q: %v", restoredPayload, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored payload=%q want=%q", got, want)
	}
	oldHierarchy := filepath.Join(restoreTarget, strings.TrimPrefix(source, string(filepath.Separator)), "nested", "payload.bin")
	if _, err := os.Stat(oldHierarchy); !os.IsNotExist(err) {
		t.Fatalf("restore recreated source absolute hierarchy at %q: %v", oldHierarchy, err)
	}
	if err := repositories.RotatePassword(ctx, repo.ID, "new-repository-password-long"); err != nil {
		t.Fatalf("rotate password: %v", err)
	}
	rotationStatus, err := repositories.PasswordRotationStatus(ctx, repo.ID)
	if err != nil || !rotationStatus.Pending {
		t.Fatalf("rotation status=%+v err=%v", rotationStatus, err)
	}
	if _, err := repositories.Snapshots(ctx, repo.ID); err != nil {
		t.Fatalf("read with rotated password: %v", err)
	}
	if err := repositories.RevokeOldPassword(ctx, repo.ID); err != nil {
		t.Fatalf("revoke old repository key: %v", err)
	}
	rotationStatus, err = repositories.PasswordRotationStatus(ctx, repo.ID)
	if err != nil || rotationStatus.Pending {
		t.Fatalf("completed rotation status=%+v err=%v", rotationStatus, err)
	}
	if err := repositories.Maintain(ctx, repo.ID, domain.RetentionPolicy{KeepLast: 1}, false); err != nil {
		t.Fatalf("maintain repository: %v", err)
	}
	recordCheck("real-restic-sftp", "passed", resticPath)
}

type diagnosticRunner struct {
	engine *restic.Engine
	t      *testing.T
}

func (r diagnosticRunner) Execute(ctx context.Context, operation restic.Operation) (restic.Result, error) {
	result, err := r.engine.Execute(ctx, operation)
	if err != nil {
		r.t.Logf("restic operation=%s exit=%d stderr=%s", operation.Kind, result.ExitCode, result.Stderr)
	}
	return result, err
}

type sftpTestServer struct {
	listener         net.Listener
	Port             int
	ClientPrivateKey []byte
	KnownHostsLine   string
	wg               sync.WaitGroup
}

func startSFTPServer(t *testing.T, root string) *sftpTestServer {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	hostPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, preferredHostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	preferredHostSigner, err := ssh.NewSignerFromKey(preferredHostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	privateBlock, err := ssh.MarshalPrivateKey(clientPrivate, "restic-control-e2e")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, errors.New("unauthorized key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	// The saved key is ECDSA, while OpenSSH normally prefers the server's
	// ED25519 key. This verifies that Restic pins the saved key algorithm rather
	// than failing strict host-key checking after selecting a different key.
	config.AddHostKey(preferredHostSigner)
	server := &sftpTestServer{
		listener: listener, Port: port, ClientPrivateKey: pem.EncodeToMemory(privateBlock),
		KnownHostsLine: knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey()),
	}
	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer conn.Close()
				_, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for channelRequest := range channels {
					if channelRequest.ChannelType() != "session" {
						_ = channelRequest.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, channelRequests, err := channelRequest.Accept()
					if err != nil {
						continue
					}
					server.wg.Add(1)
					go serveSFTPChannel(channel, channelRequests, root, &server.wg)
				}
			}()
		}
	}()
	return server
}

func serveSFTPChannel(channel ssh.Channel, requests <-chan *ssh.Request, root string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer channel.Close()
	for request := range requests {
		if request.Type != "subsystem" {
			_ = request.Reply(false, nil)
			continue
		}
		var subsystem struct{ Name string }
		unmarshalErr := ssh.Unmarshal(request.Payload, &subsystem)
		if unmarshalErr != nil || subsystem.Name != "sftp" {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
		if err != nil {
			return
		}
		_ = server.Serve()
		_ = server.Close()
		return
	}
}

func (s *sftpTestServer) Close() {
	_ = s.listener.Close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
