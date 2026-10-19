package v3catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
)

// GET /services/{id}/external-targets/options (PA-19 P3 stage 2 step 2):
// what the service mapping editor may offer for one service environment,
// per mapping system. Read-only; options never assert live data.

type mappingOptionCurrent struct {
	TargetID    *string `json:"target_id"`
	ConnectorID string  `json:"connector_id"`
	ResourceID  string  `json:"resource_id"`
}

type mappingOptionConnector struct {
	ConnectorID string   `json:"connector_id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Ready       bool     `json:"ready"`
	NotReady    []string `json:"not_ready"`
	Resources   []string `json:"resources"`
}

type mappingOptionSystem struct {
	SystemID          string                   `json:"system_id"`
	Editable          bool                     `json:"editable"`
	UnavailableReason *string                  `json:"unavailable_reason"`
	Current           *mappingOptionCurrent    `json:"current"`
	Options           []mappingOptionConnector `json:"options"`
}

// mappingSystemProviders are the connector providers a system can use.
var mappingSystemProviders = map[string][]string{
	"metrics":  {"prometheus"},
	"logs":     {"loki", "elasticsearch"},
	"traces":   {"tempo"},
	"workload": {v3telemetry.WorkloadProvider},
	"code":     {"gitlab"},
}

func optionServiceID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != id {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Service) mappingOptions(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "view")
	if !ok {
		return
	}
	if s.Registry == nil {
		httpapi.Error(w, 503, "catalog_unavailable", "The connector registry is not configured")
		return
	}
	app, env, valid := optionServiceID(r.PathValue("id"))
	inScope := false
	for _, e := range envs {
		inScope = inScope || e == env
	}
	if !valid || !inScope {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	access, ok := s.transactionAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	defer tx.Rollback(ctx)
	appID, teamID, _, raw, _, err := mappingSaveTarget(ctx, tx, app, env, false)
	if err != nil {
		mappingSaveTargetError(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	if err := authorizeMappingService(ctx, access, tx, user, appID, env, teamID, "view"); err != nil {
		mappingSavePolicyError(w, err)
		return
	}
	if err := authorizeMappingConnector(ctx, access, tx, user, env, "view"); err != nil {
		policyError(w, err)
		return
	}
	canChange := true
	for _, check := range []func() error{
		func() error { return authorizeMappingService(ctx, access, tx, user, appID, env, teamID, "change") },
		func() error { return authorizeMappingConnector(ctx, access, tx, user, env, "manage") },
	} {
		if err := check(); errors.Is(err, store.ErrForbidden) {
			canChange = false
		} else if err != nil {
			policyError(w, err)
			return
		}
	}
	var legacy []core.Binding
	if err := json.Unmarshal(raw, &legacy); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
		return
	}
	systems, err := s.optionSystems(ctx, tx, appID, env, legacy, canChange)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping options unavailable")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping options unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"service_id": r.PathValue("id"), "environment_id": env, "systems": systems})
}

func (s *Service) optionSystems(ctx context.Context, tx pgx.Tx, app, env string, legacy []core.Binding, canChange bool) ([]mappingOptionSystem, error) {
	type connectorRow struct {
		name  string
		state v3telemetry.ConnectorState
	}
	rows, err := tx.Query(ctx, `SELECT id,provider,name,endpoint,auth_method,credential_ref,enabled,tools,options
 FROM v3_connectors WHERE environment_id=$1 ORDER BY name,id LIMIT 501`, env)
	if err != nil {
		return nil, err
	}
	connectors := []connectorRow{}
	for rows.Next() {
		var c connectorRow
		if err = rows.Scan(&c.state.ID, &c.state.Provider, &c.name, &c.state.Endpoint, &c.state.AuthMethod, &c.state.CredentialRef,
			&c.state.Enabled, &c.state.Tools, &c.state.Options); err != nil {
			break
		}
		connectors = append(connectors, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(connectors) > 500 {
		return nil, errMappingTargetLimit
	}
	targets, err := mappingRegistryTargets(ctx, tx, []string{env}, app)
	if err != nil {
		return nil, err
	}
	serviceTargets := targets[app+"\x00"+env]
	// GitLab projects bound to this service, by connector.
	projects := map[string][]string{}
	rows, err = tx.Query(ctx, `SELECT t.id,t.connector_id,t.external_id FROM v3_connector_targets t
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind='project' ORDER BY t.external_id LIMIT 101`, app, env)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, connector, project string
		if err = rows.Scan(&id, &connector, &project); err != nil {
			break
		}
		projects[connector] = append(projects[connector], project)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}

	out := make([]mappingOptionSystem, 0, len(mappingSystems))
	for _, system := range mappingSystems {
		item := mappingOptionSystem{SystemID: system.ID, Options: []mappingOptionConnector{}}
		reason := ""
		switch {
		case legacyOnlySystem(system.ID):
			reason = "system_not_supported"
		case registrySystem(system.ID):
			var current []registryTarget
			for _, target := range serviceTargets {
				if target.System == system.ID {
					current = append(current, target)
				}
			}
			if len(current) > 1 {
				reason = "ambiguous_targets"
			}
			if len(current) == 1 {
				id := current[0].TargetID
				item.Current = &mappingOptionCurrent{TargetID: &id, ConnectorID: current[0].ConnectorID, ResourceID: current[0].Resource}
			}
		}
		if reason == "" && !canChange {
			reason = "forbidden"
		}
		item.Editable = reason == ""
		if reason != "" {
			item.UnavailableReason = &reason
		}
		// The confirmed code project (D20) is a target too.
		if system.ID == "code" {
			for _, target := range serviceTargets {
				if target.System == "code" {
					id := target.TargetID
					item.Current = &mappingOptionCurrent{TargetID: &id, ConnectorID: target.ConnectorID, ResourceID: target.Resource}
				}
			}
		}
		// The read-only systems show their stored binding.
		if !projectedSystem(system.ID) {
			for _, binding := range legacy {
				if mappingKind(binding.Kind) != system.ID {
					continue
				}
				item.Current = &mappingOptionCurrent{ConnectorID: binding.ConnectorID, ResourceID: binding.Resource}
				break
			}
		}
		for _, c := range connectors {
			if !providerFor(system.ID, c.state.Provider) {
				continue
			}
			notReady := s.Registry.Readiness(env, c.state)
			if notReady == nil {
				notReady = []string{}
			}
			option := mappingOptionConnector{ConnectorID: c.state.ID, Name: c.name, Provider: c.state.Provider,
				Ready: len(notReady) == 0, NotReady: notReady, Resources: []string{}}
			if system.ID == "code" {
				option.Resources = append(option.Resources, projects[c.state.ID]...)
				sort.Strings(option.Resources)
			} else {
				option.Resources = s.Registry.Catalog.ResourceIDs(c.state.ID, app, env, c.state.Provider)
			}
			item.Options = append(item.Options, option)
		}
		out = append(out, item)
	}
	return out, nil
}

func providerFor(systemID, provider string) bool {
	for _, p := range mappingSystemProviders[systemID] {
		if p == provider {
			return true
		}
	}
	return false
}
