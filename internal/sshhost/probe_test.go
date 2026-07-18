package sshhost

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	"strings"
	"testing"
)

func TestProbeReturnsFingerprintAndKnownHostsLineBeforeAuthentication(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, _ := listener.Accept()
		if connection == nil {
			return
		}
		server, _, _, _ := ssh.NewServerConn(connection, config)
		if server != nil {
			_ = server.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	result, err := Probe(context.Background(), "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Fingerprint, "SHA256:") || !strings.Contains(result.KnownHosts, "ssh-ed25519") {
		t.Fatalf("result=%+v", result)
	}
}

func TestConnectionAuthenticatesWithPinnedHostKey(t *testing.T) {
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(clientSigner.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unexpected client key")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		server, _, _, handshakeErr := ssh.NewServerConn(connection, config)
		if server != nil {
			_ = server.Close()
		}
		serverDone <- handshakeErr
	}()
	privateBlock, err := ssh.MarshalPrivateKey(clientPrivateKey, "restic-control-test")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	knownHostsLine := knownhosts.Line([]string{fmt.Sprintf("[127.0.0.1]:%d", port)}, hostSigner.PublicKey())
	if err := TestConnection(context.Background(), "127.0.0.1", port, "backup", pem.EncodeToMemory(privateBlock), knownHostsLine); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
