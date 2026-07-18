package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
	"golang.org/x/crypto/argon2"
)

var (
	ErrAlreadyInitialized = store.ErrAlreadyInitialized
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidInput       = errors.New("invalid authentication input")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

const sessionDuration = 24 * time.Hour

type authStore interface {
	CreateAdministrator(context.Context, string, string, time.Time) error
	AdministratorByUsername(context.Context, string) (store.Administrator, error)
	CreateSession(context.Context, []byte, []byte, time.Time, time.Time) error
	SessionUsername(context.Context, []byte, time.Time) (string, error)
	SessionCSRFMatches(context.Context, []byte, []byte, time.Time) (bool, error)
	DeleteSession(context.Context, []byte) error
	ResetAdministrator(context.Context, string) error
}

type auditStore interface {
	AppendAudit(context.Context, store.AuditRecord) error
}

type Manager struct {
	store authStore
	now   func() time.Time
}

type Session struct {
	Token     string
	CSRFToken string
	Username  string
	ExpiresAt time.Time
}

func New(s authStore, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{store: s, now: now}
}

func (m *Manager) Setup(ctx context.Context, username, password string) (Session, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(password) < 12 {
		return Session{}, ErrInvalidInput
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	now := m.now()
	if err := m.store.CreateAdministrator(ctx, username, passwordHash, now); err != nil {
		return Session{}, err
	}
	return m.newSession(ctx, username, now)
}

func (m *Manager) Login(ctx context.Context, username, password string) (Session, error) {
	admin, err := m.store.AdministratorByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("load administrator: %w", err)
	}
	valid, err := verifyPassword(password, admin.PasswordHash)
	if err != nil || !valid {
		return Session{}, ErrInvalidCredentials
	}
	return m.newSession(ctx, admin.Username, m.now())
}

func (m *Manager) Reauthenticate(ctx context.Context, username, password string) error {
	admin, err := m.store.AdministratorByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("load administrator: %w", err)
	}
	valid, err := verifyPassword(password, admin.PasswordHash)
	if err != nil || !valid {
		return ErrInvalidCredentials
	}
	return nil
}

func (m *Manager) Authenticate(ctx context.Context, token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return "", ErrUnauthenticated
	}
	hash := sha256.Sum256(raw)
	username, err := m.store.SessionUsername(ctx, hash[:], m.now())
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthenticated
	}
	if err != nil {
		return "", fmt.Errorf("read session: %w", err)
	}
	return username, nil
}

func (m *Manager) ValidateCSRF(ctx context.Context, token, csrf string) bool {
	tokenRaw, tokenErr := base64.RawURLEncoding.DecodeString(token)
	csrfRaw, csrfErr := base64.RawURLEncoding.DecodeString(csrf)
	if tokenErr != nil || csrfErr != nil || len(tokenRaw) != 32 || len(csrfRaw) != 32 {
		return false
	}
	tokenHash := sha256.Sum256(tokenRaw)
	csrfHash := sha256.Sum256(csrfRaw)
	valid, err := m.store.SessionCSRFMatches(ctx, tokenHash[:], csrfHash[:], m.now())
	return err == nil && valid
}

func (m *Manager) Logout(ctx context.Context, token string) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil
	}
	hash := sha256.Sum256(raw)
	if err := m.store.DeleteSession(ctx, hash[:]); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func (m *Manager) ResetPassword(ctx context.Context, password string) error {
	if len(password) < 12 {
		return ErrInvalidInput
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if err := m.store.ResetAdministrator(ctx, passwordHash); err != nil {
		return fmt.Errorf("reset administrator: %w", err)
	}
	if auditor, ok := m.store.(auditStore); ok {
		if err := auditor.AppendAudit(ctx, store.AuditRecord{OccurredAt: m.now().UTC(), Actor: "local-admin-cli", Action: "administrator.password.reset", TargetType: "administrator", TargetID: "primary", Detail: map[string]any{"sessionsRevoked": true}}); err != nil {
			return fmt.Errorf("audit administrator reset: %w", err)
		}
	}
	return nil
}

func (m *Manager) newSession(ctx context.Context, username string, now time.Time) (Session, error) {
	token := make([]byte, 32)
	csrf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, csrf); err != nil {
		return Session{}, fmt.Errorf("generate csrf token: %w", err)
	}
	tokenHash := sha256.Sum256(token)
	csrfHash := sha256.Sum256(csrf)
	expires := now.Add(sessionDuration)
	if err := m.store.CreateSession(ctx, tokenHash[:], csrfHash[:], now, expires); err != nil {
		return Session{}, err
	}
	return Session{
		Token:     base64.RawURLEncoding.EncodeToString(token),
		CSRFToken: base64.RawURLEncoding.EncodeToString(csrf),
		Username:  username,
		ExpiresAt: expires,
	}, nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	parallelism := uint8(min(runtime.NumCPU(), 4))
	digest := argon2.IDKey([]byte(password), salt, 3, 64*1024, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=%d$%s$%s", parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest)), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash format")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, err
	}
	if memory < 8*1024 || iterations == 0 || parallelism == 0 {
		return false, errors.New("invalid password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(digest)))
	return subtle.ConstantTimeCompare(candidate, digest) == 1, nil
}
