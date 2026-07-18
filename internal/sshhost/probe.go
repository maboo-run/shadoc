package sshhost

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	Fingerprint string `json:"fingerprint"`
	KnownHosts  string `json:"knownHosts"`
	Algorithm   string `json:"algorithm"`
}

func Probe(ctx context.Context, host string, port int) (Result, error) {
	if host == "" || port < 1 || port > 65535 {
		return Result{}, errors.New("valid SSH host and port are required")
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return Result{}, err
	}
	defer connection.Close()
	var result Result
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		knownHost := host
		if port != 22 {
			knownHost = fmt.Sprintf("[%s]:%d", host, port)
		}
		result = Result{Fingerprint: ssh.FingerprintSHA256(key), KnownHosts: knownhosts.Line([]string{knownHost}, key), Algorithm: key.Type()}
		return nil
	}
	config := &ssh.ClientConfig{User: "restic-control-probe", HostKeyCallback: callback, Timeout: 10 * time.Second}
	client, _, _, handshakeErr := ssh.NewClientConn(connection, address, config)
	if client != nil {
		_ = client.Close()
	}
	if result.Fingerprint != "" {
		return result, nil
	}
	return Result{}, fmt.Errorf("SSH handshake did not provide a host key: %w", handshakeErr)
}

// TestConnection verifies the saved SSH credentials against the remote host.
// It opens an SSH connection only; it does not run a command or write remote data.
func TestConnection(ctx context.Context, host string, port int, username string, privateKey []byte, knownHostsLine string) error {
	if host == "" || port < 1 || port > 65535 || strings.TrimSpace(username) == "" {
		return errors.New("valid SSH host, port, and username are required")
	}
	if len(privateKey) == 0 {
		return errors.New("SSH private key is required")
	}
	if strings.TrimSpace(knownHostsLine) == "" {
		return errors.New("pinned SSH host key is required")
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("parse SSH private key: %w", err)
	}
	knownHostsFile, err := os.CreateTemp("", "restic-control-known-hosts-*")
	if err != nil {
		return fmt.Errorf("create temporary known_hosts file: %w", err)
	}
	knownHostsPath := knownHostsFile.Name()
	defer os.Remove(knownHostsPath)
	if err := knownHostsFile.Chmod(0o600); err != nil {
		_ = knownHostsFile.Close()
		return fmt.Errorf("protect temporary known_hosts file: %w", err)
	}
	if _, err := knownHostsFile.WriteString(strings.TrimSpace(knownHostsLine) + "\n"); err != nil {
		_ = knownHostsFile.Close()
		return fmt.Errorf("write temporary known_hosts file: %w", err)
	}
	if err := knownHostsFile.Close(); err != nil {
		return fmt.Errorf("close temporary known_hosts file: %w", err)
	}
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return fmt.Errorf("load pinned SSH host key: %w", err)
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return err
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	return nil
}
