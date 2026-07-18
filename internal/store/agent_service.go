package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type AgentServiceSettings struct {
	Enabled        bool
	ListenHost     string
	Port           int
	AdvertisedHost string
	TLSNames       []string
}

func (s *Store) LoadAgentServiceSettings(ctx context.Context) (AgentServiceSettings, bool, error) {
	var settings AgentServiceSettings
	var enabled int
	var names string
	err := s.db.QueryRowContext(ctx, `SELECT enabled,listen_host,port,advertised_host,tls_names_json FROM agent_service_settings WHERE id=1`).Scan(
		&enabled, &settings.ListenHost, &settings.Port, &settings.AdvertisedHost, &names,
	)
	if err == sql.ErrNoRows {
		return AgentServiceSettings{}, false, nil
	}
	if err != nil {
		return AgentServiceSettings{}, false, fmt.Errorf("load Agent service settings: %w", err)
	}
	if err := json.Unmarshal([]byte(names), &settings.TLSNames); err != nil {
		return AgentServiceSettings{}, false, fmt.Errorf("decode Agent service TLS names: %w", err)
	}
	settings.Enabled = enabled != 0
	return settings, true, nil
}

func (s *Store) SaveAgentServiceSettings(ctx context.Context, settings AgentServiceSettings, now time.Time) error {
	names, err := json.Marshal(settings.TLSNames)
	if err != nil {
		return fmt.Errorf("encode Agent service TLS names: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_service_settings(id,enabled,listen_host,port,advertised_host,tls_names_json,updated_at)
		VALUES(1,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			enabled=excluded.enabled,
			listen_host=excluded.listen_host,
			port=excluded.port,
			advertised_host=excluded.advertised_host,
			tls_names_json=excluded.tls_names_json,
			updated_at=excluded.updated_at
	`, settings.Enabled, settings.ListenHost, settings.Port, settings.AdvertisedHost, string(names), formatTime(now))
	if err != nil {
		return fmt.Errorf("save Agent service settings: %w", err)
	}
	return nil
}
