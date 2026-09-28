package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3admin"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3alertrules"
	"github.com/wans/aiops-canvas/internal/v3audit"
	"github.com/wans/aiops-canvas/internal/v3canvas"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3conversation"
	"github.com/wans/aiops-canvas/internal/v3discovery"
	"github.com/wans/aiops-canvas/internal/v3events"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3knowledge"
	"github.com/wans/aiops-canvas/internal/v3knowledgesources"
	"github.com/wans/aiops-canvas/internal/v3mcp"
	"github.com/wans/aiops-canvas/internal/v3observe"
	"github.com/wans/aiops-canvas/internal/v3postmortems"
	"github.com/wans/aiops-canvas/internal/v3proposals"
	"github.com/wans/aiops-canvas/internal/v3settings"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3topology"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

type v3Instance struct {
	Tier         string `json:"tier"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	Environments []struct {
		ID       string   `json:"id"`
		Label    string   `json:"label"`
		Kind     string   `json:"kind"`
		Tone     string   `json:"tone"`
		Clusters []string `json:"clusters"`
		Note     string   `json:"note,omitempty"`
	} `json:"environments"`
	DefaultEnv string `json:"default_env"`
	Other      *struct {
		Label  string `json:"label"`
		URL    string `json:"url"`
		Domain string `json:"domain"`
	} `json:"other,omitempty"`
}

func loadV3Instance(path string) (v3Instance, httpapi.V3InstanceConfig, error) {
	var info v3Instance
	file, err := os.Open(path)
	if err != nil {
		return info, httpapi.V3InstanceConfig{}, err
	}
	defer file.Close()
	dec := json.NewDecoder(io.LimitReader(file, 64<<10))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&info); err != nil {
		return info, httpapi.V3InstanceConfig{}, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return info, httpapi.V3InstanceConfig{}, fmt.Errorf("instance configuration must contain one JSON object")
	}
	if strings.TrimSpace(info.Name) == "" || strings.TrimSpace(info.Domain) == "" || len(info.Environments) == 0 {
		return info, httpapi.V3InstanceConfig{}, fmt.Errorf("instance name, domain and environments are required")
	}
	ids := make([]string, 0, len(info.Environments))
	seen := map[string]bool{}
	for _, env := range info.Environments {
		if env.ID == "" || env.ID == "all" || strings.TrimSpace(env.ID) != env.ID || seen[env.ID] ||
			strings.TrimSpace(env.Label) == "" || (env.Kind != "standing" && env.Kind != "feature") || (env.Tone != "prod" && env.Tone != "stg" && env.Tone != "dev") {
			return info, httpapi.V3InstanceConfig{}, fmt.Errorf("invalid V3 environment metadata")
		}
		seen[env.ID] = true
		ids = append(ids, env.ID)
	}
	if (info.Tier == "production" && (len(ids) != 1 || ids[0] != "prod" || info.DefaultEnv != "prod")) ||
		(info.Tier == "nonprod" && (seen["prod"] || !seen[info.DefaultEnv])) ||
		(info.Tier != "production" && info.Tier != "nonprod") {
		return info, httpapi.V3InstanceConfig{}, fmt.Errorf("invalid V3 instance tier or environment boundary")
	}
	return info, httpapi.V3InstanceConfig{Tier: info.Tier, DefaultEnv: info.DefaultEnv, EnvironmentIDs: ids}, nil
}

// configureV3 enables the new backend only with explicit instance
// configuration. A partial deployment cannot silently claim production scope.
func configureV3(ctx context.Context, s *store.Store, runtime inference.Runner, api *httpapi.Server, telemetry *connectors.Telemetry, know *knowledge.Service, info *v3Instance, cfg httpapi.V3InstanceConfig) error {
	if info == nil {
		return nil
	}
	policy := &rbac.V3Policy{Store: s}
	if err := api.ConfigureV3Environment(cfg, policy, s.Session, func(ctx context.Context, u core.User, module, action, environmentID string) error {
		return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
	}); err != nil {
		return err
	}
	api.RegisterPublic("GET /api/v1/instance", func(w http.ResponseWriter, _ *http.Request) { httpapi.WriteJSON(w, http.StatusOK, info) })
	v3admin.New(s).Register(api)
	lowerSources, err := loadV3LowerSources(os.Getenv("AIOPS_V3_LOWER_SOURCES_CONFIG"), cfg.Tier)
	if err != nil {
		return fmt.Errorf("load V3 lower-environment sources: %w", err)
	}
	lowerSources.Register(api)
	api.MarkV3Route("GET /api/v1/integrations/lower-sources", "connectors", "view")
	auditV3 := v3audit.New(s.DB, cfg.EnvironmentIDs)
	auditV3.Register(api)
	v3canvas.New(s).Register(api)
	catalog := v3catalog.New(s, policy, cfg.EnvironmentIDs...)
	catalog.Register(api)
	v3proposals.New(s.DB, policy).Register(api)
	api.MarkV3Route("POST /api/v1/services", "services", "change")
	api.MarkV3ObjectRoute("GET /api/v1/services", "services", "view")
	api.MarkV3ObjectRoute("GET /api/v1/services/{id}", "services", "view")
	api.MarkV3ObjectRoute("GET /api/v1/service-mappings", "services", "view")
	v3discovery.New(s.DB, policy).Register(api)
	v3mcp.New(s.DB, policy).Register(api)
	v3topology.New(s.DB, policy).Register(api)
	ruleSource, ruleBindings, err := loadV3AlertRuleSource(os.Getenv("AIOPS_V3_ALERT_RULES_CONFIG"), *info)
	if err != nil {
		return fmt.Errorf("load V3 alert rule source: %w", err)
	}
	ruleService, err := v3alertrules.New(v3alertrules.DatabaseCatalog{DB: s.DB}, policy, ruleSource, ruleBindings)
	if err != nil {
		return fmt.Errorf("bind V3 alert rule groups: %w", err)
	}
	ruleService.Register(api)
	alertmanagerSource, alertmanagerLabels, err := loadV3AlertmanagerSource(os.Getenv("AIOPS_V3_ALERTMANAGER_CONFIG"), *info)
	if err != nil {
		return fmt.Errorf("load V3 Alertmanager source: %w", err)
	}
	(&v3alertmanager.Service{Source: alertmanagerSource, EnvironmentLabels: alertmanagerLabels}).Register(api)

	personalSettings := v3settings.New(s, runtime)
	personalSettings.Register(api)
	api.V3ModelsHandler = personalSettings.ModelsHandler
	for _, pattern := range []string{"GET /api/v1/me", "PATCH /api/v1/me", "PATCH /api/v1/me/preferences", "GET /api/v1/me/profile", "PUT /api/v1/me/profile", "DELETE /api/v1/me/sessions/{id}", "GET /api/v1/me/ai-preferences", "PUT /api/v1/me/ai-preferences", "GET /api/v1/me/notification-settings", "PUT /api/v1/me/notification-settings"} {
		api.MarkV3Route(pattern, "", "")
	}
	api.MarkV3Route("GET /api/v1/models", "models", "view")
	api.MarkV3Route("GET /api/v1/workspace", "workspace", "view")
	api.MarkV3Route("PUT /api/v1/workspace", "workspace", "manage")

	connector := v3integrations.New(s.DB, cfg.Tier, v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")))
	connector.Register(api)
	for _, pattern := range []string{"GET /api/v1/connector-types/{type}", "GET /api/v1/connectors", "GET /api/v1/connectors/{id}"} {
		api.MarkV3Route(pattern, "connectors", "view")
	}
	for _, pattern := range []string{"POST /api/v1/connectors", "PATCH /api/v1/connectors/{id}", "POST /api/v1/connectors/{id}/test"} {
		api.MarkV3Route(pattern, "connectors", "manage")
	}
	uploads := v3uploads.New(s.DB)
	uploads.ReadAuthorizer = func(ctx context.Context, user core.User, environmentID string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: environmentID})
	}
	uploads.Environments = make(map[string]bool, len(cfg.EnvironmentIDs))
	for _, environmentID := range cfg.EnvironmentIDs {
		uploads.Environments[environmentID] = true
	}
	uploads.Register(api)

	allowedConversationEnvs := func(ctx context.Context, u core.User) ([]string, error) {
		allowed := []string{}
		for _, id := range cfg.EnvironmentIDs {
			if policy.Authorize(ctx, u, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: id}) == nil {
				allowed = append(allowed, id)
			}
		}
		return allowed, nil
	}
	// Function-calling support is model-specific. Only models explicitly
	// verified by the operator are offered V3 read tools; an empty allowlist
	// leaves ordinary conversations available without tool schemas.
	toolModels := map[string]bool{}
	configuredModels := map[string]bool{}
	for _, model := range runtime.Models() {
		configuredModels[model.ID] = model.Tools
	}
	for _, modelID := range strings.Split(os.Getenv("AIOPS_V3_TOOL_MODELS"), ",") {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if !configuredModels[modelID] {
			return fmt.Errorf("V3 tool model %q is not configured with tool support", modelID)
		}
		toolModels[modelID] = true
	}
	personalSettings.ToolModels = toolModels
	configuredConversationEnv := func(environmentID string) bool {
		for _, id := range cfg.EnvironmentIDs {
			if id == environmentID {
				return true
			}
		}
		return false
	}
	chat := v3conversation.New(s.DB, runtime, v3conversation.Options{
		CurrentUser:    httpapi.CurrentUser,
		RefreshSession: s.Session,
		Shutdown:       ctx,
		DefaultModel: func(ctx context.Context, ownerID string) (string, error) {
			var model string
			err := s.DB.QueryRow(ctx, `SELECT COALESCE(ai_preferences #>> '{default,model}','') FROM v3_user_settings WHERE user_id=$1`, ownerID).Scan(&model)
			if errors.Is(err, pgx.ErrNoRows) {
				return "", nil
			}
			return model, err
		},
		AuthorizeBackgroundTurn: func(ctx context.Context, ownerID, environmentID string) error {
			if !configuredConversationEnv(environmentID) {
				return store.ErrForbidden
			}
			member, err := s.Member(ctx, ownerID)
			if err != nil {
				return err
			}
			if member.DeletedAt != nil || member.Disabled {
				return store.ErrForbidden
			}
			return policy.Authorize(ctx, member.User, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: environmentID})
		},
		AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, ownerID, environmentID string) error {
			if !configuredConversationEnv(environmentID) {
				return store.ErrForbidden
			}
			member, err := s.MemberTx(ctx, tx, ownerID)
			if err != nil {
				return err
			}
			if member.DeletedAt != nil || member.Disabled {
				return store.ErrForbidden
			}
			return policy.AuthorizeWithTx(ctx, tx, member.User, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: environmentID})
		},
		ToolsForTurn: func(_ context.Context, ownerID, environmentID, modelID string) ([]inference.EvidenceTool, error) {
			if !toolModels[modelID] {
				return nil, nil
			}
			return []inference.EvidenceTool{{
				Name:        "read_service_profile",
				Description: "Read one service's registered profile by exact service key in this conversation's environment. Health is unavailable unless independently observed.",
				Read: func(ctx context.Context, key string) (inference.Evidence, error) {
					member, err := s.Member(ctx, ownerID)
					if err != nil || member.DeletedAt != nil || member.Disabled {
						return inference.Evidence{}, errors.New("service profile unavailable")
					}
					profile, err := catalog.LookupServiceEvidence(ctx, member.User, environmentID, key)
					if err != nil {
						// Keep hidden and missing services indistinguishable to the model.
						return inference.Evidence{}, errors.New("service profile unavailable")
					}
					content, err := json.Marshal(profile)
					if err != nil {
						return inference.Evidence{}, errors.New("service profile unavailable")
					}
					return inference.Evidence{Kind: "service_profile", Title: "Service profile: " + profile.Name, Content: string(content), Source: "service_catalog", TargetID: profile.ID}, nil
				},
			}}, nil
		},
		Environment: func(r *http.Request) (string, error) {
			if cfg.Tier == "production" {
				return cfg.DefaultEnv, nil
			}
			if scope, ok := httpapi.V3ScopeFromContext(r.Context()); ok {
				if scope.All {
					return "all", nil
				}
				if len(scope.EnvironmentIDs) == 1 {
					return scope.EnvironmentIDs[0], nil
				}
			}
			return "", v3conversation.ErrEnvironment
		},
		AllowedEnvironments: allowedConversationEnvs,
		ValidateAttachments: uploads.ValidateAttachments,
	})
	if err := chat.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover V3 conversation runs: %w", err)
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := chat.RecoverInterrupted(sweepCtx); err != nil && ctx.Err() == nil {
					log.Printf("V3 reply recovery sweep: %v", err)
				}
				cancel()
			}
		}
	}()
	chat.Register(api.Register)
	for _, pattern := range []string{"GET /api/v1/conversations", "GET /api/v1/conversations/{id}"} {
		api.MarkV3Route(pattern, "conversation", "view")
	}
	for _, pattern := range []string{"POST /api/v1/conversations", "POST /api/v1/conversations/{id}/messages"} {
		api.MarkV3Route(pattern, "conversation", "change")
	}
	// EventSource cannot carry the custom environment header; the handler checks
	// the persisted conversation scope and current authorization for every read.
	api.MarkV3Route("GET /api/v1/conversations/{id}/events", "", "")
	observations := v3observe.New(s, policy, telemetry)
	observations.Register(api)
	api.MarkV3Route("GET /api/v1/services/discover", "services", "view")
	api.MarkV3ObjectRoute("GET /api/v1/metrics/dashboard", "services", "view")
	api.MarkV3ObjectRoute("GET /api/v1/logs", "logs", "view")
	api.MarkV3ObjectRoute("GET /api/v1/traces", "traces", "view")
	api.MarkV3ObjectRoute("GET /api/v1/traces/{id}", "traces", "view")
	events := v3events.New(s.DB, func(ctx context.Context, user core.User, app, env, action string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	})
	events.RegisterNew(api)
	api.Register("GET /api/v1/incidents", "viewer", events.Incidents)
	api.MarkV3ObjectRoute("GET /api/v1/incidents", "events", "view")
	api.Register("GET /api/v1/incidents/{id}", "viewer", events.Incident)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/{id}", "events", "view")
	knowledgeV3 := &v3knowledge.Service{DB: s.DB, Knowledge: know, Policy: policy}
	knowledgeV3.Register(api)
	knowledgeV3.RegisterSearch(api)
	v3postmortems.New(s.DB, policy).Register(api)
	v3knowledgesources.New(s.DB, policy).Register(api)
	v3sourceregistry.New(s.DB, policy).Register(api)
	go auditV3.CleanupExpired(ctx)
	return nil
}

func bindV3Instance(ctx context.Context, s *store.Store, info v3Instance) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('aiops_v3_instance_boundary'))`); err != nil {
		return err
	}
	// This small boundary table is intentionally established before any product
	// migration or Bootstrap. A mispointed instance cannot mutate an already
	// bound production/non-production database before discovering the mismatch.
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS v3_instance_boundary (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 tier text NOT NULL CHECK (tier IN ('production','nonprod')),
 domain text NOT NULL CHECK (domain <> ''),
 bound_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO v3_instance_boundary(singleton,tier,domain)
 VALUES(true,$1,$2) ON CONFLICT(singleton) DO NOTHING`, info.Tier, info.Domain); err != nil {
		return err
	}
	var tier, domain string
	if err = tx.QueryRow(ctx, `SELECT tier,domain FROM v3_instance_boundary WHERE singleton=true FOR UPDATE`).Scan(&tier, &domain); err != nil {
		return err
	}
	if tier != info.Tier || domain != info.Domain {
		return fmt.Errorf("database is bound to another V3 tier or domain")
	}
	return tx.Commit(ctx)
}

// rejectLegacyOnV3Database prevents an omitted instance config from reverting
// an already-bound V3 database to legacy routes, authorization and workers.
// A new or previously migrated legacy database has no binding row and remains
// eligible for the legacy startup path.
func rejectLegacyOnV3Database(ctx context.Context, s *store.Store) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('aiops_v3_instance_boundary'))`); err != nil {
		return err
	}
	var present bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('v3_instance_boundary') IS NOT NULL`).Scan(&present); err != nil {
		return err
	}
	if present {
		var bound bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_instance_boundary WHERE singleton=true)`).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return fmt.Errorf("database is bound to a V3 instance; AIOPS_V3_INSTANCE_CONFIG is required")
		}
	}
	return tx.Commit(ctx)
}
