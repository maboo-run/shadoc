package repositorycapacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/maboo-run/shadoc/internal/execution"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const Kind execution.EngineKind = "repository-capacity"

type Definition struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	KnownHosts string `json:"knownHosts,omitempty"`
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.Path) == "" || strings.ContainsRune(d.Path, '\x00') {
		return errors.New("repository capacity path is required")
	}
	switch d.Kind {
	case "local":
		if !filepath.IsAbs(d.Path) || d.Host != "" || d.Username != "" || d.PrivateKey != "" || d.KnownHosts != "" {
			return errors.New("local capacity probe requires only an absolute path")
		}
	case "sftp":
		if strings.TrimSpace(d.Host) == "" || d.Port < 1 || d.Port > 65535 || strings.TrimSpace(d.Username) == "" || d.PrivateKey == "" || strings.TrimSpace(d.KnownHosts) == "" {
			return errors.New("SFTP capacity probe requires a complete pinned SSH target")
		}
	default:
		return fmt.Errorf("unsupported repository capacity kind %q", d.Kind)
	}
	return nil
}

type Capacity struct {
	TotalBytes     uint64 `json:"totalBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
}

type Probe interface {
	Probe(context.Context, Definition) (Capacity, error)
}

type Engine struct{ probe Probe }

func NewEngine(probe Probe) *Engine          { return &Engine{probe: probe} }
func (e *Engine) Kind() execution.EngineKind { return Kind }

func (e *Engine) Validate(raw json.RawMessage) error {
	var definition Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		return errors.New("invalid repository capacity definition")
	}
	return definition.Validate()
}

func (e *Engine) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	if e == nil || e.probe == nil {
		return execution.Outcome{}, errors.New("repository capacity probe is unavailable")
	}
	if err := e.Validate(assignment.Definition); err != nil {
		return execution.Outcome{}, err
	}
	var definition Definition
	_ = json.Unmarshal(assignment.Definition, &definition)
	capacity, err := e.probe.Probe(ctx, definition)
	if err != nil {
		return execution.Outcome{}, err
	}
	if capacity.TotalBytes == 0 || capacity.AvailableBytes > capacity.TotalBytes {
		return execution.Outcome{}, errors.New("repository returned invalid capacity")
	}
	return execution.Outcome{Status: "succeeded", Summary: map[string]any{"totalBytes": capacity.TotalBytes, "availableBytes": capacity.AvailableBytes}}, nil
}

type SystemProbe struct{}

func (SystemProbe) Probe(ctx context.Context, definition Definition) (Capacity, error) {
	if err := definition.Validate(); err != nil {
		return Capacity{}, err
	}
	if definition.Kind == "local" {
		return probeLocalCapacity(definition.Path)
	}
	return probeSFTP(ctx, definition)
}

func probeSFTP(ctx context.Context, definition Definition) (Capacity, error) {
	signer, err := ssh.ParsePrivateKey([]byte(definition.PrivateKey))
	if err != nil {
		return Capacity{}, errors.New("parse repository SSH key")
	}
	knownHostsFile, err := os.CreateTemp("", "restic-control-capacity-known-hosts-*")
	if err != nil {
		return Capacity{}, err
	}
	knownHostsPath := knownHostsFile.Name()
	defer os.Remove(knownHostsPath)
	if err := knownHostsFile.Chmod(0o600); err != nil {
		_ = knownHostsFile.Close()
		return Capacity{}, err
	}
	if _, err := knownHostsFile.WriteString(strings.TrimSpace(definition.KnownHosts) + "\n"); err != nil {
		_ = knownHostsFile.Close()
		return Capacity{}, err
	}
	if err := knownHostsFile.Close(); err != nil {
		return Capacity{}, err
	}
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return Capacity{}, err
	}
	address := net.JoinHostPort(definition.Host, strconv.Itoa(definition.Port))
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return Capacity{}, fmt.Errorf("connect repository SSH: %w", err)
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, &ssh.ClientConfig{User: definition.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKeyCallback, Timeout: 10 * time.Second})
	if err != nil {
		_ = connection.Close()
		return Capacity{}, fmt.Errorf("authenticate repository SSH: %w", err)
	}
	sshClient := ssh.NewClient(clientConnection, channels, requests)
	defer sshClient.Close()
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return Capacity{}, fmt.Errorf("start repository SFTP: %w", err)
	}
	defer sftpClient.Close()
	stat, err := sftpClient.StatVFS(definition.Path)
	if err != nil {
		return Capacity{}, fmt.Errorf("read repository StatVFS: %w", err)
	}
	return Capacity{TotalBytes: stat.Frsize * stat.Blocks, AvailableBytes: stat.Frsize * stat.Bavail}, nil
}
