package v3serviceviews

import (
	"context"
	"encoding/json"
)

// metricSourceSeal captures the exact persisted source configuration that
// backs a live metric read. Callers compare seals before and after the read.
// CredentialRef is an opaque Vault reference; never include a seal in errors.
type metricSourceSeal struct {
	BindingID     string
	ConnectorID   string
	ResourceID    string
	Endpoint      string
	AuthMethod    string
	CredentialRef string
	Tools         string
	Enabled       bool
}

func (s *Service) sourceSeal(ctx context.Context, row serviceRow) (metricSourceSeal, error) {
	var zero metricSourceSeal
	if s == nil || s.Store == nil || s.Store.DB == nil {
		return zero, errIncomplete
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT t.id,t.connector_id,t.external_id,
 c.endpoint,c.auth_method,c.credential_ref,c.tools,c.enabled
 FROM v3_connector_targets t JOIN v3_connectors c
 ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2
 AND t.target_kind='metric_resource' AND c.provider='prometheus'
 LIMIT 2`, row.AppID, row.Env)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	var seal metricSourceSeal
	var tools []byte
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&seal.BindingID, &seal.ConnectorID, &seal.ResourceID,
			&seal.Endpoint, &seal.AuthMethod, &seal.CredentialRef, &tools, &seal.Enabled); err != nil {
			return zero, err
		}
	}
	if err = rows.Err(); err != nil {
		return zero, err
	}
	if count != 1 || !seal.Enabled || seal.BindingID == "" || seal.ConnectorID == "" || seal.ResourceID == "" {
		return zero, errIncomplete
	}
	var configured []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err = json.Unmarshal(tools, &configured); err != nil {
		return zero, errIncomplete
	}
	active := false
	for _, tool := range configured {
		if tool.Name == "query_metrics" && tool.Enabled {
			active = true
			break
		}
	}
	if !active {
		return zero, errIncomplete
	}
	seal.Tools = string(tools)
	return seal, nil
}
