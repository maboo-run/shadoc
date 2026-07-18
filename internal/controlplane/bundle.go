package controlplane

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"golang.org/x/crypto/argon2"
)

const (
	BundleFormatVersion            = 1
	MaximumBundleBytes             = 32 << 20
	MinimumRecoveryPassphraseBytes = 12
	minimumKDFMemoryKiB            = 8 * 1024
	maximumKDFMemoryKiB            = 256 * 1024
	maximumKDFTime                 = 10
	maximumKDFThreads              = 8
	recoveryCipher                 = "AES-256-GCM"
	recoveryKDF                    = "Argon2id"
	recoveryKeyLength              = 32
	recoverySaltLength             = 16
)

type Manifest struct {
	RemoteHosts                 []domain.RemoteHost                `json:"remoteHosts"`
	Repositories                []domain.Repository                `json:"repositories"`
	DatabaseConnections         []domain.DatabaseConnection        `json:"databaseConnections"`
	Tasks                       []domain.Task                      `json:"tasks"`
	Plans                       []domain.Plan                      `json:"plans"`
	MaintenancePolicies         []domain.MaintenancePolicy         `json:"maintenancePolicies"`
	RestoreVerificationPolicies []domain.RestoreVerificationPolicy `json:"restoreVerificationPolicies"`
	LifecyclePolicy             LifecyclePolicy                    `json:"lifecyclePolicy"`
	ScheduleWatermarks          []ScheduleWatermark                `json:"scheduleWatermarks"`
	Agents                      []AgentIdentity                    `json:"agents"`
	AgentServiceSettings        *AgentServiceSettings              `json:"agentServiceSettings,omitempty"`
	Ntfy                        *NtfySettings                      `json:"ntfy,omitempty"`
	Webhook                     *WebhookSettings                   `json:"webhook,omitempty"`
	Email                       *EmailSettings                     `json:"email,omitempty"`
	Audits                      []AuditEntry                       `json:"audits"`
}

type LifecyclePolicy struct {
	RunDays        int   `json:"runDays"`
	RawLogDays     int   `json:"rawLogDays"`
	AuditDays      int   `json:"auditDays"`
	RawLogMaxBytes int64 `json:"rawLogMaxBytes"`
}

type ScheduleWatermark struct {
	OwnerKind   string    `json:"ownerKind"`
	OwnerID     string    `json:"ownerId"`
	ScheduledAt time.Time `json:"scheduledAt"`
	ObservedAt  time.Time `json:"observedAt"`
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
}

type AgentIdentity struct {
	ID                  string     `json:"id"`
	RemoteHostID        string     `json:"remoteHostId,omitempty"`
	CertificateSerial   string     `json:"certificateSerial"`
	CertificateNotAfter *time.Time `json:"certificateNotAfter,omitempty"`
	Capabilities        []string   `json:"capabilities"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"createdAt"`
	RevokedAt           *time.Time `json:"revokedAt,omitempty"`
}

type AgentServiceSettings struct {
	Enabled        bool     `json:"enabled"`
	ListenHost     string   `json:"listenHost"`
	Port           int      `json:"port"`
	AdvertisedHost string   `json:"advertisedHost"`
	TLSNames       []string `json:"tlsNames"`
}

type NtfySettings struct {
	BaseURL  string `json:"baseUrl"`
	Topic    string `json:"topic"`
	Enabled  bool   `json:"enabled"`
	HasToken bool   `json:"hasToken"`
}

type WebhookSettings struct {
	Endpoint  string `json:"endpoint"`
	AuthMode  string `json:"authMode"`
	Enabled   bool   `json:"enabled"`
	HasSecret bool   `json:"hasSecret"`
}

type EmailSettings struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	TLSMode     string   `json:"tlsMode"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Username    string   `json:"username,omitempty"`
	Enabled     bool     `json:"enabled"`
	HasPassword bool     `json:"hasPassword"`
}

type AuditEntry struct {
	OccurredAt time.Time      `json:"occurredAt"`
	Actor      string         `json:"actor,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId,omitempty"`
	Detail     map[string]any `json:"detail"`
}

type ProtectedSecret struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Field        string `json:"field"`
	Purpose      string `json:"purpose"`
	Value        []byte `json:"value"`
}

type AgentCAMaterial struct {
	CertificatePEM []byte `json:"certificatePem"`
	PrivateKeyPEM  []byte `json:"privateKeyPem"`
}

type ProtectedPayload struct {
	Secrets []ProtectedSecret `json:"secrets"`
	AgentCA *AgentCAMaterial  `json:"agentCa,omitempty"`
}

type KDFWorkFactor struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

type SealOptions struct {
	Passphrase               string
	CreatedAt                time.Time
	SourceApplicationVersion string
	KDF                      KDFWorkFactor
}

type KDFParameters struct {
	Algorithm string `json:"algorithm"`
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
	KeyLength uint32 `json:"keyLength"`
}

type CipherEnvelope struct {
	Algorithm  string `json:"algorithm"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type BundleHeader struct {
	FormatVersion            int            `json:"formatVersion"`
	CreatedAt                time.Time      `json:"createdAt"`
	SourceApplicationVersion string         `json:"sourceApplicationVersion"`
	ResourceCounts           map[string]int `json:"resourceCounts"`
	ExcludedTransientClasses []string       `json:"excludedTransientClasses"`
	ManifestSHA256           string         `json:"manifestSha256"`
	EncryptedPayloadSHA256   string         `json:"encryptedPayloadSha256"`
}

type OpenedBundle struct {
	Header    BundleHeader
	Manifest  Manifest
	Protected ProtectedPayload
}

type bundleDocument struct {
	FormatVersion            int            `json:"formatVersion"`
	CreatedAt                time.Time      `json:"createdAt"`
	SourceApplicationVersion string         `json:"sourceApplicationVersion"`
	ResourceCounts           map[string]int `json:"resourceCounts"`
	ExcludedTransientClasses []string       `json:"excludedTransientClasses"`
	ManifestSHA256           string         `json:"manifestSha256"`
	EncryptedPayloadSHA256   string         `json:"encryptedPayloadSha256"`
	Manifest                 Manifest       `json:"manifest"`
	KDF                      KDFParameters  `json:"kdf"`
	Cipher                   CipherEnvelope `json:"cipher"`
}

type authenticatedHeader struct {
	FormatVersion            int            `json:"formatVersion"`
	CreatedAt                time.Time      `json:"createdAt"`
	SourceApplicationVersion string         `json:"sourceApplicationVersion"`
	ResourceCounts           map[string]int `json:"resourceCounts"`
	ExcludedTransientClasses []string       `json:"excludedTransientClasses"`
	ManifestSHA256           string         `json:"manifestSha256"`
	KDF                      KDFParameters  `json:"kdf"`
	CipherAlgorithm          string         `json:"cipherAlgorithm"`
}

func SealBundle(manifest Manifest, protected ProtectedPayload, options SealOptions) ([]byte, error) {
	if len(options.Passphrase) < MinimumRecoveryPassphraseBytes {
		return nil, fmt.Errorf("recovery passphrase must contain at least %d bytes", MinimumRecoveryPassphraseBytes)
	}
	if options.CreatedAt.IsZero() || strings.TrimSpace(options.SourceApplicationVersion) == "" {
		return nil, errors.New("bundle creation time and source application version are required")
	}
	work := options.KDF
	if work == (KDFWorkFactor{}) {
		work = KDFWorkFactor{Time: 3, MemoryKiB: 64 * 1024, Threads: 2}
	}
	if err := validateKDFWorkFactor(work); err != nil {
		return nil, err
	}
	manifest = normalizeManifest(manifest)
	protected = normalizeProtectedPayload(protected)
	if err := validateRecoveryData(manifest, protected); err != nil {
		return nil, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode recovery manifest: %w", err)
	}
	if err := ensureManifestContainsNoProtectedValues(manifestJSON, protected); err != nil {
		return nil, err
	}
	manifestHash := sha256.Sum256(manifestJSON)
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		return nil, fmt.Errorf("encode protected recovery payload: %w", err)
	}
	salt := make([]byte, recoverySaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate recovery salt: %w", err)
	}
	kdf := KDFParameters{Algorithm: recoveryKDF, Salt: salt, Time: work.Time, MemoryKiB: work.MemoryKiB, Threads: work.Threads, KeyLength: recoveryKeyLength}
	document := bundleDocument{
		FormatVersion: BundleFormatVersion, CreatedAt: options.CreatedAt.UTC(), SourceApplicationVersion: strings.TrimSpace(options.SourceApplicationVersion),
		ResourceCounts: resourceCounts(manifest), ExcludedTransientClasses: excludedTransientClasses(), ManifestSHA256: hex.EncodeToString(manifestHash[:]),
		Manifest: manifest, KDF: kdf, Cipher: CipherEnvelope{Algorithm: recoveryCipher},
	}
	aad, err := documentAssociatedData(document)
	if err != nil {
		return nil, err
	}
	key := deriveRecoveryKey(options.Passphrase, kdf)
	defer clearBytes(key)
	aead, err := recoveryAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate recovery nonce: %w", err)
	}
	document.Cipher.Nonce = nonce
	document.Cipher.Ciphertext = aead.Seal(nil, nonce, protectedJSON, aad)
	ciphertextHash := sha256.Sum256(document.Cipher.Ciphertext)
	document.EncryptedPayloadSHA256 = hex.EncodeToString(ciphertextHash[:])
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode recovery bundle: %w", err)
	}
	if len(encoded) > MaximumBundleBytes {
		return nil, errors.New("recovery bundle exceeds the maximum size")
	}
	return encoded, nil
}

func OpenBundle(encoded []byte, passphrase string) (OpenedBundle, error) {
	if len(encoded) == 0 || len(encoded) > MaximumBundleBytes {
		return OpenedBundle{}, errors.New("invalid recovery bundle size")
	}
	var document bundleDocument
	if err := strictJSON(encoded, &document); err != nil {
		return OpenedBundle{}, fmt.Errorf("decode recovery bundle: %w", err)
	}
	if document.FormatVersion != BundleFormatVersion {
		return OpenedBundle{}, fmt.Errorf("unsupported recovery bundle version %d", document.FormatVersion)
	}
	if document.CreatedAt.IsZero() || strings.TrimSpace(document.SourceApplicationVersion) == "" {
		return OpenedBundle{}, errors.New("invalid recovery bundle header")
	}
	if document.Cipher.Algorithm != recoveryCipher {
		return OpenedBundle{}, fmt.Errorf("unsupported recovery cipher %q", document.Cipher.Algorithm)
	}
	if err := validateKDFParameters(document.KDF); err != nil {
		return OpenedBundle{}, err
	}
	manifestJSON, err := json.Marshal(document.Manifest)
	if err != nil {
		return OpenedBundle{}, fmt.Errorf("encode recovery manifest: %w", err)
	}
	manifestHash := sha256.Sum256(manifestJSON)
	if !equalHexDigest(document.ManifestSHA256, manifestHash[:]) {
		return OpenedBundle{}, errors.New("recovery manifest checksum mismatch")
	}
	if !reflect.DeepEqual(document.ResourceCounts, resourceCounts(document.Manifest)) {
		return OpenedBundle{}, errors.New("recovery manifest resource counts do not match")
	}
	if !reflect.DeepEqual(document.ExcludedTransientClasses, excludedTransientClasses()) {
		return OpenedBundle{}, errors.New("recovery bundle transient exclusions do not match this format version")
	}
	ciphertextHash := sha256.Sum256(document.Cipher.Ciphertext)
	if !equalHexDigest(document.EncryptedPayloadSHA256, ciphertextHash[:]) {
		return OpenedBundle{}, errors.New("recovery encrypted payload checksum mismatch")
	}
	aad, err := documentAssociatedData(document)
	if err != nil {
		return OpenedBundle{}, err
	}
	key := deriveRecoveryKey(passphrase, document.KDF)
	defer clearBytes(key)
	aead, err := recoveryAEAD(key)
	if err != nil {
		return OpenedBundle{}, err
	}
	if len(document.Cipher.Nonce) != aead.NonceSize() {
		return OpenedBundle{}, errors.New("invalid recovery cipher nonce")
	}
	plaintext, err := aead.Open(nil, document.Cipher.Nonce, document.Cipher.Ciphertext, aad)
	if err != nil {
		return OpenedBundle{}, errors.New("recovery passphrase or authenticated bundle data is invalid")
	}
	defer clearBytes(plaintext)
	var protected ProtectedPayload
	if err := strictJSON(plaintext, &protected); err != nil {
		return OpenedBundle{}, fmt.Errorf("decode protected recovery payload: %w", err)
	}
	if err := validateRecoveryData(document.Manifest, protected); err != nil {
		return OpenedBundle{}, err
	}
	if err := ensureManifestContainsNoProtectedValues(manifestJSON, protected); err != nil {
		return OpenedBundle{}, err
	}
	return OpenedBundle{
		Header:   BundleHeader{FormatVersion: document.FormatVersion, CreatedAt: document.CreatedAt, SourceApplicationVersion: document.SourceApplicationVersion, ResourceCounts: document.ResourceCounts, ExcludedTransientClasses: document.ExcludedTransientClasses, ManifestSHA256: document.ManifestSHA256, EncryptedPayloadSHA256: document.EncryptedPayloadSHA256},
		Manifest: document.Manifest, Protected: protected,
	}, nil
}

func validateKDFWorkFactor(work KDFWorkFactor) error {
	if work.Time < 1 || work.Time > maximumKDFTime || work.MemoryKiB < minimumKDFMemoryKiB || work.MemoryKiB > maximumKDFMemoryKiB || work.Threads < 1 || work.Threads > maximumKDFThreads {
		return errors.New("recovery KDF parameters are outside the accepted bounds")
	}
	return nil
}

func validateKDFParameters(parameters KDFParameters) error {
	if parameters.Algorithm != recoveryKDF || parameters.KeyLength != recoveryKeyLength || len(parameters.Salt) != recoverySaltLength {
		return errors.New("unsupported or malformed recovery KDF parameters")
	}
	return validateKDFWorkFactor(KDFWorkFactor{Time: parameters.Time, MemoryKiB: parameters.MemoryKiB, Threads: parameters.Threads})
}

func deriveRecoveryKey(passphrase string, parameters KDFParameters) []byte {
	return argon2.IDKey([]byte(passphrase), parameters.Salt, parameters.Time, parameters.MemoryKiB, parameters.Threads, parameters.KeyLength)
}

func recoveryAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize recovery cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize recovery authentication: %w", err)
	}
	return aead, nil
}

func documentAssociatedData(document bundleDocument) ([]byte, error) {
	return json.Marshal(authenticatedHeader{
		FormatVersion: document.FormatVersion, CreatedAt: document.CreatedAt, SourceApplicationVersion: document.SourceApplicationVersion,
		ResourceCounts: document.ResourceCounts, ExcludedTransientClasses: document.ExcludedTransientClasses,
		ManifestSHA256: document.ManifestSHA256, KDF: document.KDF, CipherAlgorithm: document.Cipher.Algorithm,
	})
}

func strictJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func normalizeManifest(input Manifest) Manifest {
	result := input
	result.RemoteHosts = append([]domain.RemoteHost(nil), input.RemoteHosts...)
	result.Repositories = append([]domain.Repository(nil), input.Repositories...)
	result.DatabaseConnections = append([]domain.DatabaseConnection(nil), input.DatabaseConnections...)
	result.Tasks = append([]domain.Task(nil), input.Tasks...)
	result.Plans = append([]domain.Plan(nil), input.Plans...)
	result.MaintenancePolicies = append([]domain.MaintenancePolicy(nil), input.MaintenancePolicies...)
	result.RestoreVerificationPolicies = append([]domain.RestoreVerificationPolicy(nil), input.RestoreVerificationPolicies...)
	result.ScheduleWatermarks = append([]ScheduleWatermark(nil), input.ScheduleWatermarks...)
	result.Agents = append([]AgentIdentity(nil), input.Agents...)
	result.Audits = append([]AuditEntry(nil), input.Audits...)
	sort.Slice(result.RemoteHosts, func(i, j int) bool { return result.RemoteHosts[i].ID < result.RemoteHosts[j].ID })
	sort.Slice(result.Repositories, func(i, j int) bool { return result.Repositories[i].ID < result.Repositories[j].ID })
	sort.Slice(result.DatabaseConnections, func(i, j int) bool { return result.DatabaseConnections[i].ID < result.DatabaseConnections[j].ID })
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	sort.Slice(result.Plans, func(i, j int) bool { return result.Plans[i].ID < result.Plans[j].ID })
	sort.Slice(result.MaintenancePolicies, func(i, j int) bool {
		return result.MaintenancePolicies[i].RepositoryID < result.MaintenancePolicies[j].RepositoryID
	})
	sort.Slice(result.RestoreVerificationPolicies, func(i, j int) bool {
		return result.RestoreVerificationPolicies[i].TaskID < result.RestoreVerificationPolicies[j].TaskID
	})
	sort.Slice(result.ScheduleWatermarks, func(i, j int) bool {
		left, right := result.ScheduleWatermarks[i], result.ScheduleWatermarks[j]
		if left.OwnerKind != right.OwnerKind {
			return left.OwnerKind < right.OwnerKind
		}
		if left.OwnerID != right.OwnerID {
			return left.OwnerID < right.OwnerID
		}
		return left.ScheduledAt.Before(right.ScheduledAt)
	})
	for index := range result.Agents {
		result.Agents[index].Capabilities = append([]string(nil), result.Agents[index].Capabilities...)
		sort.Strings(result.Agents[index].Capabilities)
	}
	sort.Slice(result.Agents, func(i, j int) bool { return result.Agents[i].ID < result.Agents[j].ID })
	if result.AgentServiceSettings != nil {
		settings := *result.AgentServiceSettings
		settings.TLSNames = append([]string(nil), settings.TLSNames...)
		sort.Strings(settings.TLSNames)
		result.AgentServiceSettings = &settings
	}
	sort.Slice(result.Audits, func(i, j int) bool {
		if !result.Audits[i].OccurredAt.Equal(result.Audits[j].OccurredAt) {
			return result.Audits[i].OccurredAt.Before(result.Audits[j].OccurredAt)
		}
		left := result.Audits[i].Action + "\x00" + result.Audits[i].TargetType + "\x00" + result.Audits[i].TargetID
		right := result.Audits[j].Action + "\x00" + result.Audits[j].TargetType + "\x00" + result.Audits[j].TargetID
		return left < right
	})
	if result.RemoteHosts == nil {
		result.RemoteHosts = []domain.RemoteHost{}
	}
	if result.Repositories == nil {
		result.Repositories = []domain.Repository{}
	}
	if result.DatabaseConnections == nil {
		result.DatabaseConnections = []domain.DatabaseConnection{}
	}
	if result.Tasks == nil {
		result.Tasks = []domain.Task{}
	}
	if result.Plans == nil {
		result.Plans = []domain.Plan{}
	}
	if result.MaintenancePolicies == nil {
		result.MaintenancePolicies = []domain.MaintenancePolicy{}
	}
	if result.RestoreVerificationPolicies == nil {
		result.RestoreVerificationPolicies = []domain.RestoreVerificationPolicy{}
	}
	if result.ScheduleWatermarks == nil {
		result.ScheduleWatermarks = []ScheduleWatermark{}
	}
	if result.Agents == nil {
		result.Agents = []AgentIdentity{}
	}
	if result.Audits == nil {
		result.Audits = []AuditEntry{}
	}
	return result
}

func normalizeProtectedPayload(input ProtectedPayload) ProtectedPayload {
	result := input
	result.Secrets = append([]ProtectedSecret(nil), input.Secrets...)
	for index := range result.Secrets {
		result.Secrets[index].Value = append([]byte(nil), result.Secrets[index].Value...)
	}
	sort.Slice(result.Secrets, func(i, j int) bool { return secretReference(result.Secrets[i]) < secretReference(result.Secrets[j]) })
	if result.Secrets == nil {
		result.Secrets = []ProtectedSecret{}
	}
	if input.AgentCA != nil {
		material := *input.AgentCA
		material.CertificatePEM = append([]byte(nil), material.CertificatePEM...)
		material.PrivateKeyPEM = append([]byte(nil), material.PrivateKeyPEM...)
		result.AgentCA = &material
	}
	return result
}

func validateRecoveryData(manifest Manifest, protected ProtectedPayload) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	expected := map[string]string{}
	for _, item := range manifest.RemoteHosts {
		expected[secretReferenceParts("remote_host", item.ID, "private_key")] = "ssh-private-key"
	}
	for _, item := range manifest.Repositories {
		if item.EffectiveEngine() == domain.ResticEngine {
			expected[secretReferenceParts("repository", item.ID, "password")] = "repository-password"
		}
		if item.EffectiveKind() == domain.S3Repository {
			expected[secretReferenceParts("repository", item.ID, "s3_credentials")] = s3backend.CredentialPurpose
		}
	}
	for _, item := range manifest.DatabaseConnections {
		expected[secretReferenceParts("database_connection", item.ID, "password")] = "database-" + string(item.Purpose) + "-password"
	}
	if manifest.Ntfy != nil && manifest.Ntfy.HasToken {
		expected[secretReferenceParts("notification", "ntfy", "token")] = "ntfy-token"
	}
	if manifest.Webhook != nil && manifest.Webhook.HasSecret {
		expected[secretReferenceParts("notification", "webhook", "auth_secret")] = notificationconfig.WebhookSecretPurpose
	}
	if manifest.Email != nil && manifest.Email.HasPassword {
		expected[secretReferenceParts("notification", "email", "password")] = notificationconfig.EmailPasswordPurpose
	}
	seen := map[string]bool{}
	for _, item := range protected.Secrets {
		key := secretReference(item)
		purpose, present := expected[key]
		if !present {
			return fmt.Errorf("protected recovery payload contains unexpected secret %s", key)
		}
		if seen[key] {
			return fmt.Errorf("protected recovery payload contains duplicate secret %s", key)
		}
		if item.Purpose != purpose || len(item.Value) == 0 {
			return fmt.Errorf("protected recovery secret %s has an invalid purpose or value", key)
		}
		seen[key] = true
	}
	for key := range expected {
		if !seen[key] {
			return fmt.Errorf("protected recovery payload is missing secret %s", key)
		}
	}
	if protected.AgentCA != nil {
		if err := validateAgentCA(*protected.AgentCA); err != nil {
			return fmt.Errorf("invalid Agent CA material: %w", err)
		}
	}
	if (len(manifest.Agents) > 0 || manifest.AgentServiceSettings != nil && manifest.AgentServiceSettings.Enabled) && protected.AgentCA == nil {
		return errors.New("protected recovery payload is missing Agent CA material")
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.LifecyclePolicy.RunDays < 0 || manifest.LifecyclePolicy.RawLogDays < 0 || manifest.LifecyclePolicy.AuditDays < 0 || manifest.LifecyclePolicy.RawLogMaxBytes < 0 {
		return errors.New("invalid lifecycle policy in recovery manifest")
	}
	if manifest.AgentServiceSettings != nil {
		settings := manifest.AgentServiceSettings
		if settings.Port < 1024 || settings.Port > 65535 || settings.Enabled && (strings.TrimSpace(settings.ListenHost) == "" || strings.TrimSpace(settings.AdvertisedHost) == "") {
			return errors.New("invalid Agent service settings in recovery manifest")
		}
	}
	if manifest.Ntfy != nil && (strings.TrimSpace(manifest.Ntfy.BaseURL) == "" || strings.TrimSpace(manifest.Ntfy.Topic) == "") {
		return errors.New("invalid ntfy settings in recovery manifest")
	}
	if manifest.Webhook != nil {
		enabled := manifest.Webhook.Enabled
		secretID := ""
		if manifest.Webhook.HasSecret {
			secretID = "protected"
		}
		config := notificationconfig.Webhook{Endpoint: manifest.Webhook.Endpoint, AuthMode: manifest.Webhook.AuthMode, SecretID: secretID, Enabled: &enabled}
		if err := config.Validate(); err != nil {
			return errors.New("invalid webhook settings in recovery manifest")
		}
	}
	if manifest.Email != nil {
		enabled := manifest.Email.Enabled
		secretID := ""
		if manifest.Email.HasPassword {
			secretID = "protected"
		}
		config := notificationconfig.Email{Host: manifest.Email.Host, Port: manifest.Email.Port, TLSMode: manifest.Email.TLSMode, From: manifest.Email.From, To: manifest.Email.To, Username: manifest.Email.Username, PasswordSecretID: secretID, Enabled: &enabled}
		if err := config.Validate(); err != nil {
			return errors.New("invalid email settings in recovery manifest")
		}
	}
	hosts, repositories, databases, tasks, agents := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	taskByID := map[string]domain.Task{}
	if err := validateNamedResources("remote host", len(manifest.RemoteHosts), func(index int) (string, string) {
		return manifest.RemoteHosts[index].ID, manifest.RemoteHosts[index].Name
	}); err != nil {
		return err
	}
	for _, item := range manifest.RemoteHosts {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid remote host %q: %w", item.ID, err)
		}
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.HostFingerprint) == "" {
			return fmt.Errorf("remote host %q requires identity and a pinned host key", item.ID)
		}
		hosts[item.ID] = true
	}
	if err := validateNamedResources("repository", len(manifest.Repositories), func(index int) (string, string) {
		return manifest.Repositories[index].ID, manifest.Repositories[index].Name
	}); err != nil {
		return err
	}
	for _, item := range manifest.Repositories {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid repository %q: %w", item.ID, err)
		}
		if item.EffectiveKind() == domain.SFTPRepository && !hosts[item.RemoteHostID] {
			return fmt.Errorf("repository %q references an unknown remote host", item.ID)
		}
		repositories[item.ID] = true
	}
	if err := validateNamedResources("database connection", len(manifest.DatabaseConnections), func(index int) (string, string) {
		return manifest.DatabaseConnections[index].ID, manifest.DatabaseConnections[index].Name
	}); err != nil {
		return err
	}
	for _, item := range manifest.DatabaseConnections {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("database connection id is required")
		}
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid database connection %q: %w", item.ID, err)
		}
		databases[item.ID] = true
	}
	if err := validateNamedResources("task", len(manifest.Tasks), func(index int) (string, string) { return manifest.Tasks[index].ID, manifest.Tasks[index].Name }); err != nil {
		return err
	}
	for _, item := range manifest.Tasks {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid task %q: %w", item.ID, err)
		}
		if item.RepositoryID != "" && !repositories[item.RepositoryID] {
			return fmt.Errorf("task %q references an unknown repository", item.ID)
		}
		if item.Database != nil && !databases[item.Database.ConnectionID] {
			return fmt.Errorf("task %q references an unknown database connection", item.ID)
		}
		if item.Rsync != nil && item.Rsync.DestinationHostID != "" && !hosts[item.Rsync.DestinationHostID] {
			return fmt.Errorf("task %q references an unknown rsync host", item.ID)
		}
		tasks[item.ID] = true
		taskByID[item.ID] = item
	}
	if err := validateNamedResources("plan", len(manifest.Plans), func(index int) (string, string) { return manifest.Plans[index].ID, manifest.Plans[index].Name }); err != nil {
		return err
	}
	for _, item := range manifest.Plans {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("plan id is required")
		}
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid plan %q: %w", item.ID, err)
		}
		for _, taskID := range item.TaskIDs {
			if !tasks[taskID] {
				return fmt.Errorf("plan %q references an unknown task", item.ID)
			}
		}
	}
	maintenance := map[string]bool{}
	for _, item := range manifest.MaintenancePolicies {
		if maintenance[item.RepositoryID] {
			return fmt.Errorf("duplicate maintenance policy for repository %q", item.RepositoryID)
		}
		if !repositories[item.RepositoryID] {
			return fmt.Errorf("maintenance policy references an unknown repository %q", item.RepositoryID)
		}
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid maintenance policy for %q: %w", item.RepositoryID, err)
		}
		maintenance[item.RepositoryID] = true
	}
	restoreVerification := map[string]bool{}
	for _, item := range manifest.RestoreVerificationPolicies {
		if restoreVerification[item.TaskID] {
			return fmt.Errorf("duplicate restore verification policy for task %q", item.TaskID)
		}
		task, exists := taskByID[item.TaskID]
		if !exists {
			return fmt.Errorf("restore verification policy references an unknown task %q", item.TaskID)
		}
		if task.EffectiveEngine() != domain.ResticEngine || task.Kind != domain.DirectoryTask || task.EffectiveExecutionTarget().Kind != execution.Local {
			return fmt.Errorf("restore verification policy references unsupported task %q", item.TaskID)
		}
		if err := item.Validate(); err != nil || item.ScheduleAnchorAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid restore verification policy for %q", item.TaskID)
		}
		restoreVerification[item.TaskID] = true
	}
	serials := map[string]bool{}
	for _, item := range manifest.Agents {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.CertificateSerial) == "" {
			return errors.New("Agent identity and certificate serial are required")
		}
		if agents[item.ID] {
			return fmt.Errorf("duplicate Agent id %q", item.ID)
		}
		if serials[item.CertificateSerial] {
			return fmt.Errorf("duplicate Agent certificate serial %q", item.CertificateSerial)
		}
		if item.RemoteHostID != "" && !hosts[item.RemoteHostID] {
			return fmt.Errorf("Agent %q references an unknown remote host", item.ID)
		}
		agents[item.ID], serials[item.CertificateSerial] = true, true
	}
	for _, item := range manifest.Tasks {
		target := item.EffectiveExecutionTarget()
		if target.Kind == execution.Agent && !agents[target.AgentID] {
			return fmt.Errorf("task %q references an unknown Agent", item.ID)
		}
	}
	watermarks := map[string]bool{}
	for _, item := range manifest.ScheduleWatermarks {
		key := item.OwnerKind + "\x00" + item.OwnerID
		if watermarks[key] {
			return fmt.Errorf("duplicate schedule watermark for %s %q", item.OwnerKind, item.OwnerID)
		}
		if item.ScheduledAt.IsZero() || item.ObservedAt.IsZero() || !terminalWatermarkStatus(item.Status) {
			return fmt.Errorf("invalid schedule watermark for %s %q", item.OwnerKind, item.OwnerID)
		}
		if item.OwnerKind == "plan" && !containsPlan(manifest.Plans, item.OwnerID) {
			return fmt.Errorf("schedule watermark references unknown plan %q", item.OwnerID)
		}
		if item.OwnerKind == "maintenance" && !maintenance[item.OwnerID] {
			return fmt.Errorf("schedule watermark references unknown maintenance policy %q", item.OwnerID)
		}
		if item.OwnerKind == "restore_verification" && !restoreVerification[item.OwnerID] {
			return fmt.Errorf("schedule watermark references unknown restore verification policy %q", item.OwnerID)
		}
		if item.OwnerKind != "plan" && item.OwnerKind != "maintenance" && item.OwnerKind != "restore_verification" {
			return fmt.Errorf("schedule watermark has unsupported owner kind %q", item.OwnerKind)
		}
		watermarks[key] = true
	}
	return nil
}

func validateNamedResources(kind string, count int, value func(int) (string, string)) error {
	ids, names := map[string]bool{}, map[string]bool{}
	for index := 0; index < count; index++ {
		rawID, rawName := value(index)
		id, name := strings.TrimSpace(rawID), strings.TrimSpace(rawName)
		if id == "" || name == "" {
			return fmt.Errorf("%s identity and name are required", kind)
		}
		if ids[id] {
			return fmt.Errorf("duplicate %s id %q", kind, id)
		}
		if names[name] {
			return fmt.Errorf("duplicate %s name %q", kind, name)
		}
		ids[id], names[name] = true, true
	}
	return nil
}

func validateAgentCA(material AgentCAMaterial) error {
	certificateBlock, certificateRest := pem.Decode(material.CertificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(certificateRest)) != 0 {
		return errors.New("Agent CA certificate PEM is required")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("Agent CA certificate is invalid")
	}
	keyBlock, keyRest := pem.Decode(material.PrivateKeyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" || len(bytes.TrimSpace(keyRest)) != 0 {
		return errors.New("Agent CA private key PEM is required")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return errors.New("Agent CA private key is invalid")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return errors.New("Agent CA private key must use Ed25519")
	}
	certificateKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(certificateKey, privateKey.Public().(ed25519.PublicKey)) {
		return errors.New("Agent CA certificate and private key do not match")
	}
	return nil
}

func ensureManifestContainsNoProtectedValues(manifest []byte, protected ProtectedPayload) error {
	for _, item := range protected.Secrets {
		if len(item.Value) >= 4 && bytes.Contains(manifest, item.Value) {
			return fmt.Errorf("recovery manifest contains protected value for %s", secretReference(item))
		}
	}
	if protected.AgentCA != nil && len(protected.AgentCA.PrivateKeyPEM) >= 4 && bytes.Contains(manifest, protected.AgentCA.PrivateKeyPEM) {
		return errors.New("recovery manifest contains Agent CA private key material")
	}
	return nil
}

func resourceCounts(manifest Manifest) map[string]int {
	return map[string]int{
		"remoteHosts": len(manifest.RemoteHosts), "repositories": len(manifest.Repositories), "databaseConnections": len(manifest.DatabaseConnections),
		"tasks": len(manifest.Tasks), "plans": len(manifest.Plans), "maintenancePolicies": len(manifest.MaintenancePolicies),
		"restoreVerificationPolicies": len(manifest.RestoreVerificationPolicies),
		"scheduleWatermarks":          len(manifest.ScheduleWatermarks), "agents": len(manifest.Agents), "audits": len(manifest.Audits),
	}
}

func excludedTransientClasses() []string {
	return []string{"active_alert_state", "active_operations", "agent_enrollment_tokens", "agent_filesystem_requests", "agent_leases", "administrator_credentials", "delete_previews", "notification_deliveries", "pending_repository_keys", "repository_capacity_samples", "restore_confirmations", "run_records_and_logs", "sessions", "task_scope_confirmations"}
}

func secretReference(secret ProtectedSecret) string {
	return secretReferenceParts(secret.ResourceType, secret.ResourceID, secret.Field)
}
func secretReferenceParts(resourceType, resourceID, field string) string {
	return resourceType + ":" + resourceID + ":" + field
}
func containsPlan(plans []domain.Plan, id string) bool {
	for _, item := range plans {
		if item.ID == id {
			return true
		}
	}
	return false
}

func terminalWatermarkStatus(status string) bool {
	switch status {
	case "success", "partial", "failed", "cancelled", "skipped", "missed", "interrupted":
		return true
	default:
		return false
	}
}

func equalHexDigest(encoded string, wanted []byte) bool {
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == len(wanted) && bytes.Equal(decoded, wanted)
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
