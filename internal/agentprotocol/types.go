// Package agentprotocol contains versioned declarative control-plane messages.
package agentprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Version                        = 1
	ManagedResticInstallCapability = "managed-restic-install-v1"
	FilesystemScopeEntryCapability = "filesystem-scope-entries-v1"
	AssignmentProgressCapability   = "assignment-progress-v1"
	MaxFilesystemScopeChunkEntries = 128
)

type EnrollmentRequest struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
	AgentID string `json:"agentId"`
	CSRPEM  string `json:"csrPem"`
}

type EnrollmentResponse struct {
	Version        int    `json:"version"`
	CertificatePEM string `json:"certificatePem"`
	CAPEM          string `json:"caPem"`
}

type RenewalRequest struct {
	Version int    `json:"version"`
	AgentID string `json:"agentId"`
	CSRPEM  string `json:"csrPem"`
}

func (r RenewalRequest) Validate() error {
	if r.Version != Version || !validAgentID(r.AgentID) {
		return errors.New("invalid Agent certificate renewal identity")
	}
	if strings.TrimSpace(r.CSRPEM) == "" || len(r.CSRPEM) > 1<<20 {
		return errors.New("bounded Agent certificate request is required")
	}
	return nil
}

type RenewalResponse struct {
	Version        int       `json:"version"`
	CertificatePEM string    `json:"certificatePem"`
	NotAfter       time.Time `json:"notAfter"`
}

type Heartbeat struct {
	Version      int         `json:"version"`
	AgentID      string      `json:"agentId"`
	Capabilities []string    `json:"capabilities"`
	Runtime      RuntimeInfo `json:"runtime,omitempty"`
}

// RuntimeInfo is intentionally structured instead of encoding versions and
// compatibility facts into the free-form capability list. Empty RuntimeInfo
// remains valid so a newer Service can continue receiving legacy heartbeats
// while keeping those Agents ineligible for newly enabled tasks.
type RuntimeInfo struct {
	BuildVersion  string `json:"buildVersion,omitempty"`
	ProtocolMin   int    `json:"protocolMin,omitempty"`
	ProtocolMax   int    `json:"protocolMax,omitempty"`
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	ResticVersion string `json:"resticVersion,omitempty"`
	RsyncVersion  string `json:"rsyncVersion,omitempty"`
	ServiceURL    string `json:"serviceUrl,omitempty"`
	RenewalStatus string `json:"renewalStatus,omitempty"`
}

func (h Heartbeat) Validate() error {
	if h.Version != Version || !validAgentID(h.AgentID) {
		return errors.New("invalid agent heartbeat identity")
	}
	if len(h.Capabilities) > 64 {
		return errors.New("agent heartbeat has too many capabilities")
	}
	for _, capability := range h.Capabilities {
		if value := strings.TrimSpace(capability); value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("agent heartbeat contains an invalid capability")
		}
	}
	return h.Runtime.validate()
}

func (r RuntimeInfo) validate() error {
	present := r.BuildVersion != "" || r.ProtocolMin != 0 || r.ProtocolMax != 0 || r.OS != "" || r.Arch != "" || r.ResticVersion != "" || r.RsyncVersion != "" || r.ServiceURL != "" || r.RenewalStatus != ""
	if !present {
		return nil
	}
	for _, value := range []string{r.BuildVersion, r.ResticVersion, r.RsyncVersion} {
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("agent runtime version metadata is invalid")
		}
	}
	if r.ProtocolMin < 1 || r.ProtocolMax < r.ProtocolMin {
		return errors.New("agent protocol range is invalid")
	}
	if r.OS != "linux" && r.OS != "darwin" && r.OS != "windows" {
		return errors.New("agent operating system is unsupported")
	}
	if r.Arch != "amd64" && r.Arch != "arm64" {
		return errors.New("agent architecture is unsupported")
	}
	if r.RenewalStatus != "" && r.RenewalStatus != "healthy" && r.RenewalStatus != "failed" {
		return errors.New("agent certificate renewal status is invalid")
	}
	if r.ServiceURL != "" {
		if len(r.ServiceURL) > 2048 {
			return errors.New("agent Service URL is too long")
		}
		parsed, err := url.Parse(r.ServiceURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
			return errors.New("agent Service URL must be an HTTPS origin")
		}
	}
	return nil
}

func validAgentID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index := range value {
		character := value[index]
		allowed := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == '-')
		if !allowed {
			return false
		}
	}
	return true
}

type Assignment struct {
	Version    int             `json:"version"`
	ID         string          `json:"id"`
	AgentID    string          `json:"agentId"`
	TaskID     string          `json:"taskId"`
	Engine     string          `json:"engine"`
	Definition json.RawMessage `json:"definition"`
	ExpiresAt  time.Time       `json:"expiresAt"`
}

type Result struct {
	Version      int            `json:"version"`
	AssignmentID string         `json:"assignmentId"`
	AgentID      string         `json:"agentId"`
	Status       string         `json:"status"`
	SnapshotID   string         `json:"snapshotId,omitempty"`
	Summary      map[string]any `json:"summary,omitempty"`
	RawLog       string         `json:"rawLog,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type AssignmentProgress struct {
	Version            int    `json:"version"`
	AssignmentID       string `json:"assignmentId"`
	AgentID            string `json:"agentId"`
	Sequence           int64  `json:"sequence"`
	Phase              string `json:"phase"`
	BytesTransferred   int64  `json:"bytesTransferred,omitempty"`
	TotalBytes         int64  `json:"totalBytes,omitempty"`
	FilesTransferred   int64  `json:"filesTransferred,omitempty"`
	FilesTotal         int64  `json:"filesTotal,omitempty"`
	RateBytesPerSecond int64  `json:"rateBytesPerSecond,omitempty"`
	ETASeconds         int64  `json:"etaSeconds,omitempty"`
}

func (p AssignmentProgress) ValidateFor(agentID string) error {
	if p.Version != Version || p.AgentID != agentID || !validAgentID(p.AgentID) || strings.TrimSpace(p.AssignmentID) == "" || len(p.AssignmentID) > 256 {
		return errors.New("invalid assignment progress identity")
	}
	if p.Sequence < 0 || p.BytesTransferred < 0 || p.TotalBytes < 0 || p.FilesTransferred < 0 || p.FilesTotal < 0 || p.RateBytesPerSecond < 0 || p.ETASeconds < 0 {
		return errors.New("assignment progress metrics are outside bounds")
	}
	switch p.Phase {
	case "starting", "scanning", "transferring", "finalizing":
		return nil
	default:
		return errors.New("assignment progress phase is invalid")
	}
}

// FilesystemScopeEntryChunk carries a bounded, relative-path-only portion of a
// source inventory over the authenticated Agent channel. The final Result
// remains summary-only so large trees never become one oversized response.
type FilesystemScopeEntryChunk struct {
	Version      int                    `json:"version"`
	AssignmentID string                 `json:"assignmentId"`
	AgentID      string                 `json:"agentId"`
	Sequence     int                    `json:"sequence"`
	Entries      []FilesystemScopeEntry `json:"entries"`
}

type FilesystemScopeEntry struct {
	Ordinal     int64  `json:"ordinal"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	Disposition string `json:"disposition"`
	Size        int64  `json:"size,omitempty"`
	ReasonCode  string `json:"reasonCode,omitempty"`
	RuleIndexes []int  `json:"ruleIndexes,omitempty"`
}

func (c FilesystemScopeEntryChunk) ValidateFor(agentID string) error {
	if c.Version != Version || c.AgentID != agentID || !validAgentID(c.AgentID) || strings.TrimSpace(c.AssignmentID) == "" || len(c.AssignmentID) > 256 {
		return errors.New("invalid filesystem scope chunk identity")
	}
	if c.Sequence < 0 || len(c.Entries) == 0 || len(c.Entries) > MaxFilesystemScopeChunkEntries {
		return errors.New("filesystem scope chunk is outside bounds")
	}
	var previousOrdinal int64
	for _, entry := range c.Entries {
		if entry.Ordinal <= previousOrdinal || !validInventoryPath(entry.Path) || entry.Size < 0 {
			return errors.New("filesystem scope chunk contains an invalid entry")
		}
		previousOrdinal = entry.Ordinal
		switch entry.Type {
		case "file", "directory", "symlink", "special", "unknown":
		default:
			return errors.New("filesystem scope chunk contains an invalid entry type")
		}
		switch entry.Disposition {
		case "included", "excluded", "unreadable", "attention":
		default:
			return errors.New("filesystem scope chunk contains an invalid disposition")
		}
		switch entry.ReasonCode {
		case "", "exclusion_rule", "permission_denied", "unreadable", "special_file", "metadata_unavailable":
		default:
			return errors.New("filesystem scope chunk contains an invalid reason")
		}
		if len(entry.RuleIndexes) > 256 {
			return errors.New("filesystem scope chunk contains too many rule matches")
		}
		for _, index := range entry.RuleIndexes {
			if index < 0 || index >= 256 {
				return errors.New("filesystem scope chunk contains an invalid rule match")
			}
		}
	}
	return nil
}

func validInventoryPath(value string) bool {
	if value == "" || len(value) > 32<<10 || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (a Assignment) ValidateFor(agentID string, now time.Time) error {
	if a.Version != Version {
		return fmt.Errorf("unsupported agent protocol version %d", a.Version)
	}
	if strings.TrimSpace(agentID) == "" || a.AgentID != agentID {
		return errors.New("assignment belongs to a different agent")
	}
	if a.ID == "" || a.TaskID == "" || a.Engine == "" || a.ExpiresAt.IsZero() {
		return errors.New("assignment is incomplete")
	}
	if !a.ExpiresAt.After(now) {
		return errors.New("assignment has expired")
	}
	return nil
}
