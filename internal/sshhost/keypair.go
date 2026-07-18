package sshhost

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateKeyPair creates an unencrypted Ed25519 private key suitable for
// unattended SFTP use. The caller must store the private key as a secret.
func GenerateKeyPair() ([]byte, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "restic-control")
	if err != nil {
		return nil, "", fmt.Errorf("encode private key: %w", err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, "", fmt.Errorf("encode public key: %w", err)
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublicKey))), nil
}

// PublicKey derives the SSH authorized_keys representation without returning
// the source private key.
func PublicKey(privateKey []byte) (string, error) {
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("parse SSH private key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}
