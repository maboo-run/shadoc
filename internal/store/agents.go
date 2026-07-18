package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type AgentRecord struct {
	ID                  string
	RemoteHostID        string
	CertificateSerial   string
	CertificateNotAfter *time.Time
	Capabilities        []string
	BuildVersion        string
	ProtocolMin         int
	ProtocolMax         int
	OS                  string
	Arch                string
	ResticVersion       string
	RsyncVersion        string
	ServiceURL          string
	RenewalStatus       string
	Status              string
	LastHeartbeatAt     *time.Time
	CreatedAt           time.Time
	RevokedAt           *time.Time
	StoppedAt           *time.Time
	UninstalledAt       *time.Time
	DrainingAt          *time.Time
}

type AgentHeartbeat struct {
	ID                  string
	CertificateSerial   string
	CertificateNotAfter time.Time
	Capabilities        []string
	BuildVersion        string
	ProtocolMin         int
	ProtocolMax         int
	OS                  string
	Arch                string
	ResticVersion       string
	RsyncVersion        string
	ServiceURL          string
	RenewalStatus       string
	ObservedAt          time.Time
}

type AgentLease struct {
	ID             string
	AgentID        string
	TaskID         string
	Engine         string
	Definition     json.RawMessage
	Status         string
	ExpiresAt      time.Time
	AcknowledgedAt *time.Time
	CompletedAt    *time.Time
	Result         json.RawMessage
}

type AgentFilesystemRequest struct {
	ID          string
	AgentID     string
	Definition  json.RawMessage
	Status      string
	Result      json.RawMessage
	ExpiresAt   time.Time
	CreatedAt   time.Time
	CompletedAt *time.Time
}

type AgentRestoreRequest struct {
	ID          string
	AgentID     string
	Definition  json.RawMessage
	Status      string
	Result      json.RawMessage
	ExpiresAt   time.Time
	CreatedAt   time.Time
	CompletedAt *time.Time
}

func (s *Store) CreateAgentRestoreRequest(ctx context.Context, request AgentRestoreRequest) error {
	if !json.Valid(request.Definition) {
		return errors.New("Agent restore request definition must be valid JSON")
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM agent_restore_requests WHERE expires_at<?`, formatTime(request.CreatedAt.Add(-24*time.Hour)))
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_restore_requests(id,agent_id,definition_json,status,expires_at,created_at) VALUES(?,?,?,'queued',?,?)`, request.ID, request.AgentID, string(request.Definition), formatTime(request.ExpiresAt), formatTime(request.CreatedAt))
	return constraintError(err)
}

func (s *Store) ClaimAgentRestoreRequest(ctx context.Context, agentID string, at time.Time) (AgentRestoreRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRestoreRequest{}, err
	}
	defer tx.Rollback()
	var request AgentRestoreRequest
	var definition, expires, created string
	err = tx.QueryRowContext(ctx, `
		SELECT r.id,r.agent_id,r.definition_json,r.status,r.expires_at,r.created_at
		FROM agent_restore_requests r JOIN agents a ON a.id=r.agent_id
		WHERE r.agent_id=? AND r.status='queued' AND r.expires_at>? AND a.status='online' AND a.revoked_at IS NULL AND a.draining_at IS NULL
		ORDER BY r.created_at LIMIT 1`, agentID, formatTime(at)).Scan(&request.ID, &request.AgentID, &definition, &request.Status, &expires, &created)
	if err != nil {
		return request, err
	}
	request.Definition, request.ExpiresAt, request.CreatedAt = json.RawMessage(definition), mustParseTime(expires), mustParseTime(created)
	result, err := tx.ExecContext(ctx, `UPDATE agent_restore_requests SET status='running' WHERE id=? AND status='queued'`, request.ID)
	if err != nil {
		return request, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return request, sql.ErrNoRows
	}
	request.Status = "running"
	if err := tx.Commit(); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Store) CompleteAgentRestoreRequest(ctx context.Context, id, agentID, status string, result json.RawMessage, at time.Time) error {
	if status != "succeeded" && status != "failed" || !json.Valid(result) {
		return errors.New("invalid Agent restore result")
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE agent_restore_requests SET status=?,result_json=?,completed_at=? WHERE id=? AND agent_id=? AND status='running'`, status, string(result), formatTime(at), id, agentID)
	if err != nil {
		return err
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AgentRestoreRequestStatus(ctx context.Context, id string) (AgentRestoreRequest, error) {
	var request AgentRestoreRequest
	var definition, result, expires, created string
	var completed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,definition_json,status,result_json,expires_at,created_at,completed_at FROM agent_restore_requests WHERE id=?`, id).Scan(&request.ID, &request.AgentID, &definition, &request.Status, &result, &expires, &created, &completed)
	request.Definition, request.Result, request.ExpiresAt, request.CreatedAt = json.RawMessage(definition), json.RawMessage(result), mustParseTime(expires), mustParseTime(created)
	if completed.Valid {
		value := mustParseTime(completed.String)
		request.CompletedAt = &value
	}
	return request, err
}

func (s *Store) ExpireAgentRestoreRequest(ctx context.Context, id, reason string, at time.Time) error {
	result, _ := json.Marshal(map[string]any{"version": 1, "assignmentId": id, "status": "failed", "error": reason})
	updated, err := s.db.ExecContext(ctx, `UPDATE agent_restore_requests SET status='failed',result_json=?,completed_at=? WHERE id=? AND completed_at IS NULL`, string(result), formatTime(at), id)
	if err != nil {
		return err
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateAgentFilesystemRequest(ctx context.Context, request AgentFilesystemRequest) error {
	if !json.Valid(request.Definition) {
		return errors.New("filesystem request definition must be valid JSON")
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM agent_filesystem_requests WHERE expires_at<?`, formatTime(request.CreatedAt.Add(-24*time.Hour)))
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_filesystem_requests(id,agent_id,definition_json,status,expires_at,created_at) VALUES(?,?,?,'queued',?,?)`, request.ID, request.AgentID, string(request.Definition), formatTime(request.ExpiresAt), formatTime(request.CreatedAt))
	return constraintError(err)
}

func (s *Store) ClaimAgentFilesystemRequest(ctx context.Context, agentID string, at time.Time) (AgentFilesystemRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentFilesystemRequest{}, err
	}
	defer tx.Rollback()
	var request AgentFilesystemRequest
	var definition, expires, created string
	err = tx.QueryRowContext(ctx, `
		SELECT r.id,r.agent_id,r.definition_json,r.status,r.expires_at,r.created_at
		FROM agent_filesystem_requests r JOIN agents a ON a.id=r.agent_id
		WHERE r.agent_id=? AND r.status='queued' AND r.expires_at>? AND a.revoked_at IS NULL AND a.draining_at IS NULL
		ORDER BY r.created_at LIMIT 1`, agentID, formatTime(at)).Scan(&request.ID, &request.AgentID, &definition, &request.Status, &expires, &created)
	if err != nil {
		return request, err
	}
	request.Definition, request.ExpiresAt, request.CreatedAt = json.RawMessage(definition), mustParseTime(expires), mustParseTime(created)
	result, err := tx.ExecContext(ctx, `UPDATE agent_filesystem_requests SET status='running' WHERE id=? AND status='queued'`, request.ID)
	if err != nil {
		return request, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return request, sql.ErrNoRows
	}
	request.Status = "running"
	if err := tx.Commit(); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Store) CompleteAgentFilesystemRequest(ctx context.Context, id, agentID, status string, result json.RawMessage, at time.Time) error {
	if status != "succeeded" && status != "failed" || !json.Valid(result) {
		return errors.New("invalid filesystem result")
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE agent_filesystem_requests SET status=?,result_json=?,completed_at=? WHERE id=? AND agent_id=? AND status='running'`, status, string(result), formatTime(at), id, agentID)
	if err != nil {
		return err
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AgentFilesystemRequestStatus(ctx context.Context, id string) (AgentFilesystemRequest, error) {
	var request AgentFilesystemRequest
	var definition, result, expires, created string
	var completed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,definition_json,status,result_json,expires_at,created_at,completed_at FROM agent_filesystem_requests WHERE id=?`, id).Scan(&request.ID, &request.AgentID, &definition, &request.Status, &result, &expires, &created, &completed)
	request.Definition, request.Result, request.ExpiresAt, request.CreatedAt = json.RawMessage(definition), json.RawMessage(result), mustParseTime(expires), mustParseTime(created)
	if completed.Valid {
		value := mustParseTime(completed.String)
		request.CompletedAt = &value
	}
	return request, err
}

func mustParseTime(value string) time.Time { parsed, _ := parseTime(value); return parsed }

func (s *Store) SaveAgent(ctx context.Context, agent AgentRecord) error {
	capabilities, err := json.Marshal(agent.Capabilities)
	if err != nil {
		return err
	}
	if agent.Status == "" {
		agent.Status = "offline"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agents(
			id,remote_host_id,certificate_serial,certificate_not_after,capabilities_json,
			build_version,protocol_min,protocol_max,platform_os,platform_arch,restic_version,rsync_version,service_url,renewal_status,
			status,last_heartbeat_at,created_at,revoked_at,draining_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			remote_host_id=COALESCE(excluded.remote_host_id,agents.remote_host_id),
			certificate_serial=excluded.certificate_serial,certificate_not_after=excluded.certificate_not_after,
			capabilities_json=excluded.capabilities_json,build_version=excluded.build_version,
			protocol_min=excluded.protocol_min,protocol_max=excluded.protocol_max,platform_os=excluded.platform_os,platform_arch=excluded.platform_arch,
			restic_version=excluded.restic_version,rsync_version=excluded.rsync_version,service_url=excluded.service_url,renewal_status=excluded.renewal_status,
			status=excluded.status,last_heartbeat_at=excluded.last_heartbeat_at,revoked_at=excluded.revoked_at,
			stopped_at=NULL,uninstalled_at=NULL,draining_at=excluded.draining_at`,
		agent.ID, nullString(agent.RemoteHostID), agent.CertificateSerial, nullableTime(agent.CertificateNotAfter), string(capabilities),
		agent.BuildVersion, agent.ProtocolMin, agent.ProtocolMax, agent.OS, agent.Arch, agent.ResticVersion, agent.RsyncVersion, agent.ServiceURL, agent.RenewalStatus,
		agent.Status, nullableTime(agent.LastHeartbeatAt), formatTime(agent.CreatedAt), nullableTime(agent.RevokedAt), nullableTime(agent.DrainingAt))
	if err != nil {
		return constraintError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET status='retired',retired_at=? WHERE agent_id=? AND serial<>? AND status IN ('active','pending')`, formatTime(agent.CreatedAt), agent.ID, agent.CertificateSerial); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_certificates(serial,agent_id,not_after,status,issued_at,activated_at)
		VALUES(?,?,?,'active',?,?)
		ON CONFLICT(serial) DO UPDATE SET agent_id=excluded.agent_id,not_after=excluded.not_after,status='active',activated_at=excluded.activated_at,retired_at=NULL`,
		agent.CertificateSerial, agent.ID, nullableTime(agent.CertificateNotAfter), formatTime(agent.CreatedAt), formatTime(agent.CreatedAt)); err != nil {
		return constraintError(err)
	}
	return tx.Commit()
}

// EnrollAgent creates a new identity or replaces one that was explicitly
// revoked/uninstalled. It never lets possession of a fresh enrollment token
// replace an active Agent certificate.
func (s *Store) EnrollAgent(ctx context.Context, agent AgentRecord) error {
	if agent.ID == "" || agent.CertificateSerial == "" || agent.CreatedAt.IsZero() {
		return errors.New("complete Agent enrollment facts are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked, uninstalled sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT revoked_at,uninstalled_at FROM agents WHERE id=?`, agent.ID).Scan(&revoked, &uninstalled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agents(id,certificate_serial,certificate_not_after,capabilities_json,status,created_at)
			VALUES(?,?,?,'[]','offline',?)`, agent.ID, agent.CertificateSerial, nullableTime(agent.CertificateNotAfter), formatTime(agent.CreatedAt))
	case err != nil:
		return err
	case !revoked.Valid && !uninstalled.Valid:
		return errors.New("Agent is already enrolled and active")
	default:
		_, err = tx.ExecContext(ctx, `
			UPDATE agents SET certificate_serial=?,certificate_not_after=?,capabilities_json='[]',
				build_version='',protocol_min=0,protocol_max=0,platform_os='',platform_arch='',restic_version='',rsync_version='',service_url='',renewal_status='',
				status='offline',last_heartbeat_at=NULL,revoked_at=NULL,stopped_at=NULL,uninstalled_at=NULL,draining_at=NULL
			WHERE id=?`, agent.CertificateSerial, nullableTime(agent.CertificateNotAfter), agent.ID)
	}
	if err != nil {
		return constraintError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET status='retired',retired_at=? WHERE agent_id=? AND status IN ('active','pending')`, formatTime(agent.CreatedAt), agent.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_certificates(serial,agent_id,not_after,status,issued_at,activated_at)
		VALUES(?,?,?,'active',?,?)`, agent.CertificateSerial, agent.ID, nullableTime(agent.CertificateNotAfter), formatTime(agent.CreatedAt), formatTime(agent.CreatedAt)); err != nil {
		return constraintError(err)
	}
	return tx.Commit()
}

func (s *Store) MarkAgentStopped(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET status=CASE WHEN revoked_at IS NULL THEN 'offline' ELSE status END,stopped_at=? WHERE id=?`, formatTime(at), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CompleteAgentUninstall(ctx context.Context, id string, at time.Time) error {
	value := formatTime(at)
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET status='revoked',stopped_at=COALESCE(stopped_at,?),uninstalled_at=?,revoked_at=COALESCE(revoked_at,?) WHERE id=?`, value, value, value, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) BindAgentRemoteHost(ctx context.Context, agentID, hostID string) error {
	if agentID == "" || hostID == "" {
		return errors.New("Agent and remote host IDs are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET remote_host_id=NULL WHERE remote_host_id=? AND id<>?`, hostID, agentID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET remote_host_id=? WHERE id=? AND revoked_at IS NULL`, hostID, agentID)
	if err != nil {
		return constraintError(err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) BeginAgentDrain(ctx context.Context, agentID string, at time.Time) error {
	if agentID == "" || at.IsZero() {
		return errors.New("Agent and drain time are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET draining_at=COALESCE(draining_at,?) WHERE id=? AND revoked_at IS NULL`, formatTime(at), agentID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) EndAgentDrain(ctx context.Context, agentID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET draining_at=NULL WHERE id=?`, agentID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// RecoverInterruptedAgentDrains clears process-owned drain markers left by an
// abrupt Service exit. Graceful cancellation already performs the same cleanup
// in the upgrade service before shutdown completes.
func (s *Store) RecoverInterruptedAgentDrains(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET draining_at=NULL WHERE draining_at IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted Agent drains: %w", err)
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) AgentActiveWorkCount(ctx context.Context, agentID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM agent_leases WHERE agent_id=? AND status='running' AND completed_at IS NULL) +
			(SELECT COUNT(*) FROM agent_filesystem_requests WHERE agent_id=? AND status='running' AND completed_at IS NULL) +
			(SELECT COUNT(*) FROM agent_restore_requests WHERE agent_id=? AND status='running' AND completed_at IS NULL)`,
		agentID, agentID, agentID).Scan(&count)
	return count, err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func (s *Store) HeartbeatAgent(ctx context.Context, id string, capabilities []string, at time.Time) error {
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET capabilities_json=?,status='online',last_heartbeat_at=?,stopped_at=NULL,uninstalled_at=NULL WHERE id=? AND revoked_at IS NULL`, string(encoded), formatTime(at), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RecordAgentHeartbeat(ctx context.Context, heartbeat AgentHeartbeat) error {
	if heartbeat.ID == "" || heartbeat.CertificateSerial == "" || heartbeat.ObservedAt.IsZero() {
		return errors.New("complete Agent heartbeat identity is required")
	}
	capabilities, err := json.Marshal(heartbeat.Capabilities)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentSerial, certificateStatus string
	var usable int
	err = tx.QueryRowContext(ctx, `
		SELECT a.certificate_serial,c.status,1
		FROM agents a JOIN agent_certificates c ON c.agent_id=a.id
		WHERE a.id=? AND c.serial=? AND a.revoked_at IS NULL AND c.status IN ('active','pending')
		  AND (c.not_after IS NULL OR c.not_after>?)`,
		heartbeat.ID, heartbeat.CertificateSerial, formatTime(heartbeat.ObservedAt)).Scan(&currentSerial, &certificateStatus, &usable)
	if err != nil || usable != 1 {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	certificateExpiry := nullableNonZeroTime(heartbeat.CertificateNotAfter)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET not_after=COALESCE(?,not_after) WHERE serial=? AND agent_id=?`, certificateExpiry, heartbeat.CertificateSerial, heartbeat.ID); err != nil {
		return err
	}
	if certificateStatus == "pending" && currentSerial != heartbeat.CertificateSerial {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET status='retired',retired_at=? WHERE agent_id=? AND serial<>? AND status='active'`, formatTime(heartbeat.ObservedAt), heartbeat.ID, heartbeat.CertificateSerial); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET status='active',activated_at=?,retired_at=NULL WHERE agent_id=? AND serial=? AND status='pending'`, formatTime(heartbeat.ObservedAt), heartbeat.ID, heartbeat.CertificateSerial); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agents SET certificate_serial=?,certificate_not_after=COALESCE(?,certificate_not_after),capabilities_json=?,
			build_version=?,protocol_min=?,protocol_max=?,platform_os=?,platform_arch=?,restic_version=?,rsync_version=?,service_url=?,renewal_status=?,
			status='online',last_heartbeat_at=?,stopped_at=NULL,uninstalled_at=NULL
		WHERE id=? AND revoked_at IS NULL`,
		heartbeat.CertificateSerial, certificateExpiry, string(capabilities), heartbeat.BuildVersion, heartbeat.ProtocolMin, heartbeat.ProtocolMax,
		heartbeat.OS, heartbeat.Arch, heartbeat.ResticVersion, heartbeat.RsyncVersion, heartbeat.ServiceURL, heartbeat.RenewalStatus,
		formatTime(heartbeat.ObservedAt), heartbeat.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func nullableNonZeroTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func (s *Store) AgentCertificateActive(ctx context.Context, id, serial string) (bool, error) {
	return s.AgentCertificateUsable(ctx, id, serial, time.Now().UTC())
}

func (s *Store) AgentCertificateUsable(ctx context.Context, id, serial string, at time.Time) (bool, error) {
	var present int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM agents a JOIN agent_certificates c ON c.agent_id=a.id
		WHERE a.id=? AND c.serial=? AND a.revoked_at IS NULL AND c.status IN ('active','pending')
		  AND (c.not_after IS NULL OR c.not_after>?)`, id, serial, formatTime(at)).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && present == 1, err
}

func (s *Store) SavePendingAgentCertificate(ctx context.Context, agentID, serial string, notBefore, notAfter, issuedAt time.Time) error {
	if agentID == "" || serial == "" || issuedAt.IsZero() || !notAfter.After(issuedAt) || !notAfter.After(notBefore) {
		return errors.New("valid pending Agent certificate facts are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND revoked_at IS NULL`, agentID).Scan(&present); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_certificates SET status='retired',retired_at=? WHERE agent_id=? AND status='pending'`, formatTime(issuedAt), agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_certificates(serial,agent_id,not_before,not_after,status,issued_at)
		VALUES(?,?,?,?,'pending',?)`, serial, agentID, formatTime(notBefore), formatTime(notAfter), formatTime(issuedAt)); err != nil {
		return constraintError(err)
	}
	return tx.Commit()
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,COALESCE(remote_host_id,''),certificate_serial,certificate_not_after,capabilities_json,
			build_version,protocol_min,protocol_max,platform_os,platform_arch,restic_version,rsync_version,service_url,renewal_status,
			status,last_heartbeat_at,created_at,revoked_at,stopped_at,uninstalled_at,draining_at
		FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []AgentRecord
	for rows.Next() {
		var agent AgentRecord
		var capabilities, created string
		var certificateNotAfter, heartbeat, revoked, stopped, uninstalled, draining sql.NullString
		if err := rows.Scan(
			&agent.ID, &agent.RemoteHostID, &agent.CertificateSerial, &certificateNotAfter, &capabilities,
			&agent.BuildVersion, &agent.ProtocolMin, &agent.ProtocolMax, &agent.OS, &agent.Arch, &agent.ResticVersion, &agent.RsyncVersion, &agent.ServiceURL, &agent.RenewalStatus,
			&agent.Status, &heartbeat, &created, &revoked, &stopped, &uninstalled, &draining,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(capabilities), &agent.Capabilities)
		agent.CreatedAt, _ = parseTime(created)
		if certificateNotAfter.Valid {
			value, _ := parseTime(certificateNotAfter.String)
			agent.CertificateNotAfter = &value
		}
		if heartbeat.Valid {
			value, _ := parseTime(heartbeat.String)
			agent.LastHeartbeatAt = &value
		}
		if revoked.Valid {
			value, _ := parseTime(revoked.String)
			agent.RevokedAt = &value
		}
		if stopped.Valid {
			value, _ := parseTime(stopped.String)
			agent.StoppedAt = &value
		}
		if uninstalled.Valid {
			value, _ := parseTime(uninstalled.String)
			agent.UninstalledAt = &value
		}
		if draining.Valid {
			value, _ := parseTime(draining.String)
			agent.DrainingAt = &value
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *Store) RevokeAgent(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET status='revoked',revoked_at=? WHERE id=? AND revoked_at IS NULL`, formatTime(at), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SaveAgentEnrollmentToken(ctx context.Context, hash []byte, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash,expires_at) VALUES(?,?)`, hash, formatTime(expiresAt))
	return constraintError(err)
}

func (s *Store) ConsumeAgentEnrollmentToken(ctx context.Context, hash []byte, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET consumed_at=? WHERE token_hash=? AND consumed_at IS NULL AND expires_at>?`, formatTime(at), hash, formatTime(at))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateAgentLease(ctx context.Context, lease AgentLease) error {
	if !json.Valid(lease.Definition) {
		return errors.New("agent lease definition must be valid JSON")
	}
	status := lease.Status
	if status == "" {
		status = "queued"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_leases(id,agent_id,task_id,engine,definition_json,status,expires_at) VALUES(?,?,?,?,?,?,?)`, lease.ID, lease.AgentID, lease.TaskID, lease.Engine, string(lease.Definition), status, formatTime(lease.ExpiresAt))
	return constraintError(err)
}

func (s *Store) ClaimAgentLease(ctx context.Context, agentID string, at time.Time) (AgentLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentLease{}, err
	}
	defer tx.Rollback()
	var lease AgentLease
	var definition, expires string
	err = tx.QueryRowContext(ctx, `SELECT l.id,l.agent_id,l.task_id,l.engine,l.definition_json,l.status,l.expires_at FROM agent_leases l JOIN agents a ON a.id=l.agent_id WHERE l.agent_id=? AND a.status='online' AND a.revoked_at IS NULL AND a.draining_at IS NULL AND l.status='queued' AND l.acknowledged_at IS NULL AND l.expires_at>? ORDER BY l.expires_at,l.id LIMIT 1`, agentID, formatTime(at)).Scan(&lease.ID, &lease.AgentID, &lease.TaskID, &lease.Engine, &definition, &lease.Status, &expires)
	if err != nil {
		return AgentLease{}, err
	}
	lease.ExpiresAt, err = parseTime(expires)
	if err != nil {
		return AgentLease{}, err
	}
	lease.Definition = json.RawMessage(definition)
	result, err := tx.ExecContext(ctx, `UPDATE agent_leases SET status='running',acknowledged_at=? WHERE id=? AND acknowledged_at IS NULL`, formatTime(at), lease.ID)
	if err != nil {
		return AgentLease{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return AgentLease{}, sql.ErrNoRows
	}
	acknowledged := at.UTC()
	lease.Status, lease.AcknowledgedAt = "running", &acknowledged
	if err := tx.Commit(); err != nil {
		return AgentLease{}, err
	}
	return lease, nil
}

func (s *Store) CompleteAgentLease(ctx context.Context, leaseID, agentID, status string, resultJSON json.RawMessage, at time.Time) error {
	if status != "succeeded" && status != "failed" {
		return errors.New("agent lease completion status must be succeeded or failed")
	}
	if !json.Valid(resultJSON) {
		return errors.New("agent lease result must be valid JSON")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_leases SET status=?,completed_at=?,result_json=? WHERE id=? AND agent_id=? AND status='running' AND completed_at IS NULL`, status, formatTime(at), string(resultJSON), leaseID, agentID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AgentLeaseStatus(ctx context.Context, leaseID string) (AgentLease, error) {
	var lease AgentLease
	var definition, expires, acknowledged, completed sql.NullString
	var result string
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,task_id,engine,definition_json,status,expires_at,acknowledged_at,completed_at,result_json FROM agent_leases WHERE id=?`, leaseID).Scan(&lease.ID, &lease.AgentID, &lease.TaskID, &lease.Engine, &definition, &lease.Status, &expires, &acknowledged, &completed, &result)
	if err != nil {
		return lease, err
	}
	lease.Definition, lease.Result = json.RawMessage(definition.String), json.RawMessage(result)
	lease.ExpiresAt, _ = parseTime(expires.String)
	if acknowledged.Valid {
		value, _ := parseTime(acknowledged.String)
		lease.AcknowledgedAt = &value
	}
	if completed.Valid {
		value, _ := parseTime(completed.String)
		lease.CompletedAt = &value
	}
	return lease, nil
}

func (s *Store) ExpireAgentLease(ctx context.Context, leaseID, reason string, at time.Time) error {
	resultJSON, _ := json.Marshal(map[string]any{"version": 1, "assignmentId": leaseID, "status": "failed", "error": reason})
	result, err := s.db.ExecContext(ctx, `UPDATE agent_leases SET status='failed',completed_at=?,result_json=? WHERE id=? AND completed_at IS NULL`, formatTime(at), string(resultJSON), leaseID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
