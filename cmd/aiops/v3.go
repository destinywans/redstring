package main

import (
	"bytes"
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
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3admin"
	"github.com/wans/aiops-canvas/internal/v3agents"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3alertrules"
	"github.com/wans/aiops-canvas/internal/v3audit"
	"github.com/wans/aiops-canvas/internal/v3canvas"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3changes"
	"github.com/wans/aiops-canvas/internal/v3changeview"
	"github.com/wans/aiops-canvas/internal/v3configsync"
	"github.com/wans/aiops-canvas/internal/v3conversation"
	"github.com/wans/aiops-canvas/internal/v3coverage"
	"github.com/wans/aiops-canvas/internal/v3discovery"
	"github.com/wans/aiops-canvas/internal/v3events"
	"github.com/wans/aiops-canvas/internal/v3executions"
	"github.com/wans/aiops-canvas/internal/v3home"
	"github.com/wans/aiops-canvas/internal/v3identity"
	"github.com/wans/aiops-canvas/internal/v3ingestpull"
	"github.com/wans/aiops-canvas/internal/v3ingestpush"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3knowledge"
	"github.com/wans/aiops-canvas/internal/v3knowledgesources"
	"github.com/wans/aiops-canvas/internal/v3mcp"
	"github.com/wans/aiops-canvas/internal/v3memory"
	"github.com/wans/aiops-canvas/internal/v3models"
	"github.com/wans/aiops-canvas/internal/v3observe"
	"github.com/wans/aiops-canvas/internal/v3postmortems"
	"github.com/wans/aiops-canvas/internal/v3proposals"
	"github.com/wans/aiops-canvas/internal/v3resilience"
	"github.com/wans/aiops-canvas/internal/v3serviceviews"
	"github.com/wans/aiops-canvas/internal/v3settings"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3topology"
	"github.com/wans/aiops-canvas/internal/v3traces"
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

type conversationEnvironmentAuthorizer interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

// The conversation list contains owner-scoped chats and canvases. Return the
// union of their current view environments; the route and object readers still
// enforce their own operation and source-specific permissions.
func allowedConversationEnvironments(ctx context.Context, user core.User, environmentIDs []string, policy conversationEnvironmentAuthorizer) ([]string, error) {
	allowed := make([]string, 0, len(environmentIDs))
	for _, id := range environmentIDs {
		for _, module := range []string{"conversation", "canvas"} {
			err := policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: id})
			if err == nil {
				allowed = append(allowed, id)
				break
			}
			if !errors.Is(err, store.ErrForbidden) {
				return nil, err
			}
		}
	}
	return allowed, nil
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

func loadV3PushSources(path string) (v3ingestpush.Config, error) {
	var cfg v3ingestpush.Config
	if path == "" {
		return cfg, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	const maxBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return cfg, err
	}
	if len(data) > maxBytes || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return cfg, fmt.Errorf("V3 Push sources configuration must be one JSON object of at most %d bytes", maxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("V3 Push sources configuration must contain one JSON object")
	}
	return cfg, nil
}

func registerV3Pull(api *httpapi.Server, s *store.Store, cfg httpapi.V3InstanceConfig,
	resolver *v3integrations.BoundResolver, pullCfg v3ingestpull.Config) error {
	service, err := v3ingestpull.New(s.DB, cfg.Tier, resolver, pullCfg, nil)
	if err != nil {
		return fmt.Errorf("configure V3 Pull ingress: %w", err)
	}
	service.Register(api)
	return nil
}

// A configured V3 binding snapshot is shared by every model-facing V3 service.
// Without a file, retain the existing V3 runtime during rollout.
func configuredV3ModelRuntime(path string, cfg httpapi.V3InstanceConfig, runtime inference.Runner) (inference.Runner, *v3models.Bindings, error) {
	bindings, err := v3models.LoadBindings(path, cfg.Tier, cfg.EnvironmentIDs, runtime)
	if err != nil {
		return nil, nil, fmt.Errorf("load V3 model bindings: %w", err)
	}
	if bindings == nil {
		return runtime, nil, nil
	}
	return v3models.NewBoundRunner(runtime, bindings), bindings, nil
}

// configureV3 enables the new backend only with explicit instance
// configuration. A partial deployment cannot silently claim production scope.
func configureV3(ctx context.Context, s *store.Store, runtime inference.Runner, api *httpapi.Server, telemetry *connectors.Telemetry, know *knowledge.Service, info *v3Instance, cfg httpapi.V3InstanceConfig, opsConfig operations.Config) error {
	return configureV3WithNativeSources(ctx, s, runtime, api, telemetry, know, info, cfg, opsConfig, nil)
}

func configureV3WithNativeSources(ctx context.Context, s *store.Store, runtime inference.Runner, api *httpapi.Server, telemetry *connectors.Telemetry, know *knowledge.Service, info *v3Instance, cfg httpapi.V3InstanceConfig, opsConfig operations.Config, gitLabs []delivery.GitLabConfig) error {
	if info == nil {
		return nil
	}
	modelRuntime, modelBindings, err := configuredV3ModelRuntime(os.Getenv("AIOPS_V3_MODEL_BINDINGS_FILE"), cfg, runtime)
	if err != nil {
		return err
	}
	meEvidence, err := configureV3MeEvidence(*info, os.Getenv("AIOPS_V3_ONCALL_CURRENT_URL"), os.Getenv("AIOPS_V3_ONCALL_TOKEN_FILE"))
	if err != nil {
		return err
	}
	policy := &rbac.V3Policy{Store: s}
	executions := v3executions.New(s.DB, policy)
	if err := executions.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate V3 execution publication: %w", err)
	}
	executions.Register(api)
	gitMarkdownBindings, err := v3knowledgesources.ParseGitMarkdownBindings(os.Getenv("AIOPS_V3_GIT_MARKDOWN_BINDINGS"))
	if err != nil {
		return fmt.Errorf("configure V3 Git Markdown bindings: %w", err)
	}
	activeGitMarkdownBindings := make([]v3knowledgesources.GitMarkdownBinding, 0, len(gitMarkdownBindings))
	for _, binding := range gitMarkdownBindings {
		if binding.Tier == cfg.Tier {
			activeGitMarkdownBindings = append(activeGitMarkdownBindings, binding)
		}
	}
	externalBindings, err := v3knowledgesources.ParseExternalBindings(os.Getenv("AIOPS_V3_EXTERNAL_SOURCE_BINDINGS"))
	if err != nil {
		return fmt.Errorf("configure V3 Confluence/Notion bindings: %w", err)
	}
	activeExternalBindings := make([]v3knowledgesources.ExternalBinding, 0, len(externalBindings))
	for _, binding := range externalBindings {
		if binding.Tier == cfg.Tier {
			activeExternalBindings = append(activeExternalBindings, binding)
		}
	}
	feishuBindings, err := v3knowledgesources.ParseFeishuBindings(os.Getenv("AIOPS_V3_FEISHU_SOURCE_BINDINGS"))
	if err != nil {
		return fmt.Errorf("configure V3 Feishu Docx bindings: %w", err)
	}
	activeFeishuBindings := make([]v3knowledgesources.FeishuBinding, 0, len(feishuBindings))
	for _, binding := range feishuBindings {
		if binding.Tier == cfg.Tier {
			activeFeishuBindings = append(activeFeishuBindings, binding)
		}
	}
	pullCfg, err := v3ingestpull.LoadConfig(os.Getenv("AIOPS_V3_PULL_SOURCES_CONFIG"))
	if err != nil {
		return fmt.Errorf("load V3 Pull sources: %w", err)
	}
	sourceRegistry, err := configuredV3SourceRegistryWithAll(os.Getenv("AIOPS_V3_PUBLIC_WEB_READER_ENABLED"), s, policy,
		activeGitMarkdownBindings, activeExternalBindings, activeFeishuBindings)
	if err != nil {
		return err
	}
	pushCfg, err := loadV3PushSources(os.Getenv("AIOPS_V3_PUSH_SOURCES_CONFIG"))
	if err != nil {
		return fmt.Errorf("load V3 Push sources: %w", err)
	}
	pushService, err := v3ingestpush.New(s.DB, cfg, pushCfg, policy)
	if err != nil {
		return fmt.Errorf("configure V3 Push ingress: %w", err)
	}
	if err := pushService.SetNativeWebhookSources(opsConfig.Sources, gitLabs); err != nil {
		return fmt.Errorf("configure V3 native Webhook overview: %w", err)
	}
	if err := api.ConfigureV3Environment(cfg, policy, s.Session, func(ctx context.Context, u core.User, module, action, environmentID string) error {
		return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
	}); err != nil {
		return err
	}
	if err := configureV3MFA(s, api, *info, os.Getenv("AIOPS_V3_MFA_KEY_FILE")); err != nil {
		return err
	}
	if err := configureV3Passkey(s, api, *info, os.Getenv("AIOPS_V3_PASSKEY_ENABLED")); err != nil {
		return err
	}
	api.RegisterPublic("GET /api/v1/instance", func(w http.ResponseWriter, _ *http.Request) { httpapi.WriteJSON(w, http.StatusOK, info) })
	if issuer, clientID, redirect := os.Getenv("AIOPS_V3_OIDC_ISSUER"), os.Getenv("AIOPS_V3_OIDC_CLIENT_ID"), os.Getenv("AIOPS_V3_OIDC_REDIRECT_URL"); issuer != "" || clientID != "" || redirect != "" || os.Getenv("AIOPS_V3_OIDC_CLIENT_SECRET") != "" {
		identity, err := v3identity.New(ctx, s, v3identity.Config{
			Issuer: issuer, ClientID: clientID, ClientSecret: os.Getenv("AIOPS_V3_OIDC_CLIENT_SECRET"),
			RedirectURL: redirect, PublicOrigin: api.AllowedOrigin, Tier: cfg.Tier,
			SecureCookies:          api.SecureCookies,
			AllowLoopbackHTTP:      os.Getenv("AIOPS_V3_OIDC_ALLOW_LOOPBACK_HTTP") == "true",
			AllowedEndpointOrigins: oidcEndpointOrigins(os.Getenv("AIOPS_V3_OIDC_ENDPOINT_ORIGINS")),
		}, nil)
		if err != nil {
			return fmt.Errorf("configure OIDC: %w", err)
		}
		identity.Register(api)
	}
	invitations, err := configureV3Invitations(*info, api)
	if err != nil {
		return fmt.Errorf("configure V3 member invitations: %w", err)
	}
	admin := v3admin.New(s)
	admin.Invitations = invitations
	admin.Register(api)
	lowerSources, err := loadV3LowerSources(os.Getenv("AIOPS_V3_LOWER_SOURCES_CONFIG"), cfg.Tier)
	if err != nil {
		return fmt.Errorf("load V3 lower-environment sources: %w", err)
	}
	lowerSources.Register(api)
	auditV3 := v3audit.New(s.DB, cfg.EnvironmentIDs)
	auditV3.Register(api)
	v3canvas.NewWithRuntime(s, modelRuntime).Register(api)
	catalog := v3catalog.New(s, policy, cfg.EnvironmentIDs...)
	catalog.RegisterWithoutServiceReads(api)
	configSync, err := loadV3ConfigSync(os.Getenv("AIOPS_V3_CONFIG_SYNC_CONFIG"), cfg.Tier, s.DB, policy)
	if err != nil {
		return fmt.Errorf("load V3 configuration promotion: %w", err)
	}
	configSync.Register(api)
	v3proposals.New(s.DB, policy).Register(api)
	discovery := v3discovery.New(s.DB, policy)
	textCommands, err := loadV3TextCommands(os.Getenv("AIOPS_V3_TEXT_COMMANDS_CONFIG"), cfg.EnvironmentIDs)
	if err != nil {
		return fmt.Errorf("load V3 text commands: %w", err)
	}
	if err := discovery.ConfigureTextCommands(textCommands); err != nil {
		return fmt.Errorf("configure V3 text commands: %w", err)
	}
	discovery.Register(api)
	v3coverage.New(s.DB, policy).Register(api)
	v3home.New(s.DB, policy).Register(api)
	mcpService := v3mcp.New(s.DB, policy)
	credentialRoot := os.Getenv("AIOPS_MCP_CREDENTIAL_ROOT")
	mcpService.DiscoverTools = func(ctx context.Context, config aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return aiextensions.DiscoverTools(ctx, credentialRoot, config)
	}
	mcpService.DiscoverCatalog = func(ctx context.Context, config aiextensions.MCPConfig) (aiextensions.MCPCatalog, error) {
		return aiextensions.DiscoverCatalog(ctx, credentialRoot, config)
	}
	mcpService.Register(api)
	resilienceReader, err := configureV3ResilienceReader(
		os.Getenv("AIOPS_V3_RESILIENCE_SOURCE_URL"), os.Getenv("AIOPS_V3_RESILIENCE_TOKEN_FILE"))
	if err != nil {
		return fmt.Errorf("configure V3 resilience source: %w", err)
	}
	v3resilience.New(s.DB, policy, resilienceReader).Register(api)
	ruleSource, ruleBindings, err := loadV3AlertRuleSource(os.Getenv("AIOPS_V3_ALERT_RULES_CONFIG"), *info)
	if err != nil {
		return fmt.Errorf("load V3 alert rule source: %w", err)
	}
	ruleService, err := v3alertrules.New(v3alertrules.DatabaseCatalog{DB: s.DB}, policy, ruleSource, ruleBindings)
	if err != nil {
		return fmt.Errorf("bind V3 alert rule groups: %w", err)
	}
	if ruleSource != nil && len(ruleBindings) > 0 {
		configSync.AlertRules, err = v3configsync.NewAlertRuleResolver(ruleSource, ruleBindings)
		if err != nil {
			return fmt.Errorf("bind V3 configuration promotion alert rules: %w", err)
		}
	}
	ruleService.Register(api)
	alertmanagerSource, alertmanagerLabels, alertmanagerBindings, alertmanagerPolicyBindings, err :=
		loadV3AlertmanagerSourceWithPolicies(os.Getenv("AIOPS_V3_ALERTMANAGER_CONFIG"), *info, opsConfig, s.DB)
	if err != nil {
		return fmt.Errorf("load V3 Alertmanager source: %w", err)
	}
	routeDraftRepository, err := loadV3NotificationRouteRepository(configSync,
		os.Getenv("AIOPS_V3_NOTIFICATION_ROUTE_TARGET_BRANCH"),
		os.Getenv("AIOPS_V3_NOTIFICATION_ROUTE_BRANCH_PREFIX"))
	if err != nil {
		return fmt.Errorf("configure V3 notification route drafts: %w", err)
	}
	inhibitionRepository, err := loadV3InhibitionRepository(configSync, routeDraftRepository,
		os.Getenv("AIOPS_V3_INHIBITION_TARGET_BRANCH"),
		os.Getenv("AIOPS_V3_INHIBITION_BRANCH_PREFIX"))
	if err != nil {
		return fmt.Errorf("configure V3 inhibition drafts: %w", err)
	}
	inhibitionHistory, err := loadV3InhibitionHistory(os.Getenv("AIOPS_V3_INHIBITION_HISTORY_CONFIG"),
		*info, alertmanagerPolicyBindings)
	if err != nil {
		return fmt.Errorf("configure V3 inhibition history: %w", err)
	}
	alertmanagerService := &v3alertmanager.Service{
		Source: alertmanagerSource, Catalog: v3alertmanager.DatabaseCatalog{DB: s.DB}, Access: policy,
		EnvironmentLabels: alertmanagerLabels, ApplicationBindings: alertmanagerBindings,
		RouteRepository: routeDraftRepository, InhibitionRepository: inhibitionRepository,
		InhibitionHistory: inhibitionHistory,
	}
	if promotionRepository, ok := configSync.Repository.(*v3configsync.GitLabClient); ok && promotionRepository != nil {
		alertmanagerService.ConfigPromotionProjectID = promotionRepository.ProjectID()
		alertmanagerService.ConfigPromotionTargetBranch = promotionRepository.TargetBranch()
		alertmanagerService.ConfigPromotionBranchPrefix = promotionRepository.BranchPrefix()
	}
	alertmanagerService.Register(api)

	personalSettings := v3settings.New(s, modelRuntime)
	personalSettings.MeEvidence = meEvidence
	personalSettings.Register(api)
	api.V3ModelsHandler = personalSettings.ModelsHandler

	credentialBindings, err := v3integrations.ParseCredentialBindings(os.Getenv("AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS"))
	if err != nil {
		return fmt.Errorf("configure V3 connector credentials: %w", err)
	}
	// A process Vault token can read more than one secret. Only server-managed
	// bindings may send a credential to an exact registered connector endpoint.
	credentialResolver := v3integrations.NewBoundResolver(
		v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")), credentialBindings)
	connector := v3integrations.New(s.DB, cfg.Tier, credentialResolver)
	connector.Register(api)
	if err := registerV3Pull(api, s, cfg, credentialResolver, pullCfg); err != nil {
		return err
	}
	v3changes.New(s.DB, cfg.Tier, policy, connector.Prober.Resolver).Register(api)
	uploads := v3uploads.New(s.DB)
	uploads.ReadAuthorizer = func(ctx context.Context, user core.User, environmentID string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: environmentID})
	}
	uploads.Environments = make(map[string]bool, len(cfg.EnvironmentIDs))
	for _, environmentID := range cfg.EnvironmentIDs {
		uploads.Environments[environmentID] = true
	}
	uploads.Register(api)
	uploads.RegisterRevocation(api)

	allowedConversationEnvs := func(ctx context.Context, u core.User) ([]string, error) {
		return allowedConversationEnvironments(ctx, u, cfg.EnvironmentIDs, policy)
	}
	metricCatalog, err := v3integrations.LoadPrometheusQueryCatalog(os.Getenv("AIOPS_V3_PROMETHEUS_QUERIES_CONFIG"))
	if err != nil {
		return fmt.Errorf("load V3 Prometheus query catalog: %w", err)
	}
	metricReader, err := v3integrations.NewPrometheusReader(s.DB, cfg.Tier, connector.Prober.Resolver, metricCatalog)
	if err != nil {
		return fmt.Errorf("configure V3 Prometheus reader: %w", err)
	}
	topology := v3topology.New(s.DB, policy)
	for key := range metricCatalog {
		if key.QueryID == "topology_v1" {
			topology = v3topology.NewWithMetrics(s.DB, policy, metricReader)
			break
		}
	}
	topology.Register(api)
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
	modelAdmin := v3models.New(s.DB, modelRuntime, policy)
	modelAdmin.ToolModels = toolModels
	modelAdmin.Register(api)
	configuredConversationEnv := func(environmentID string) bool {
		for _, id := range cfg.EnvironmentIDs {
			if id == environmentID {
				return true
			}
		}
		return false
	}
	chat := v3conversation.New(s.DB, modelRuntime, v3conversation.Options{
		CurrentUser:    httpapi.CurrentUser,
		RefreshSession: s.Session,
		Shutdown:       ctx,
		AuthorizeMetricsSource: func(ctx context.Context, ownerID, environmentID string, source v3conversation.MetricsSource) error {
			return metricReader.AuthorizeMetricSourceVersion(ctx, ownerID, environmentID, source.ApplicationID,
				source.BindingID, source.ConnectorID, source.ResourceID, source.QueryID, source.Fingerprint)
		},
		AuthorizeMetricsSourceTx: func(ctx context.Context, tx pgx.Tx, ownerID, environmentID string, source v3conversation.MetricsSource) error {
			return metricReader.AuthorizeMetricSourceVersionTx(ctx, tx, ownerID, environmentID, source.ApplicationID,
				source.BindingID, source.ConnectorID, source.ResourceID, source.QueryID, source.Fingerprint)
		},
		DefaultModel: func(ctx context.Context, ownerID, environmentID string) (string, error) {
			var model string
			err := s.DB.QueryRow(ctx, `SELECT COALESCE(ai_preferences #>> '{default,model}','') FROM v3_user_settings WHERE user_id=$1`, ownerID).Scan(&model)
			if errors.Is(err, pgx.ErrNoRows) {
				model = ""
			} else if err != nil {
				return "", err
			}
			if modelBindings != nil && !modelBindings.Allowed(environmentID, model) {
				return modelBindings.DefaultModel(environmentID), nil
			}
			return model, nil
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
			tools := []inference.EvidenceTool{{
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
			}}
			if telemetry == nil {
				return tools, nil
			}
			metricTool, ok := v3conversation.NewServiceMetricsTool(v3conversation.ServiceMetricsToolOptions{
				DB: s.DB, OwnerID: ownerID, EnvironmentID: environmentID,
				CurrentUser: func(ctx context.Context, id string) (core.User, error) {
					member, err := s.Member(ctx, id)
					if err != nil {
						return core.User{}, err
					}
					if member.DeletedAt != nil || member.Disabled {
						return core.User{}, store.ErrForbidden
					}
					return member.User, nil
				},
				LookupService: catalog.LookupServiceEvidence, Telemetry: telemetry,
			})
			if ok {
				tools = append(tools, metricTool)
			}
			logTool, ok := v3conversation.NewServiceLogsTool(v3conversation.ServiceLogsToolOptions{
				Enabled: true, DB: s.DB, OwnerID: ownerID, EnvironmentID: environmentID,
				CurrentUser: func(ctx context.Context, id string) (core.User, error) {
					member, err := s.Member(ctx, id)
					if err != nil {
						return core.User{}, err
					}
					if member.DeletedAt != nil || member.Disabled {
						return core.User{}, store.ErrForbidden
					}
					return member.User, nil
				},
				LookupService: catalog.LookupServiceEvidence, Access: policy, Telemetry: telemetry,
			})
			if ok {
				tools = append(tools, logTool)
			}
			return tools, nil
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
	chat.RegisterV3(api)
	observations := v3observe.New(s, policy, telemetry)
	observations.MetricReader = metricReader
	observations.MetricsConversation = chat
	observations.Register(api)
	alertCoverage, err := v3serviceviews.LoadAlertCoverage(os.Getenv("AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG"), opsConfig, cfg, s.DB)
	if err != nil {
		return fmt.Errorf("configure V3 service alert coverage: %w", err)
	}
	serviceViews := v3serviceviews.New(s, policy, metricReader, alertCoverage)
	serviceViews.Register(api)
	serviceViews.RegisterWatched(api)
	changeAlertCoverage, err := v3serviceviews.LoadAlertRangeCoverage(os.Getenv("AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG"), opsConfig, cfg, s.DB)
	if err != nil {
		return fmt.Errorf("configure V3 change alert coverage: %w", err)
	}
	v3changeview.New(s, policy, v3changeview.AuditReviewSource{DB: s.DB}, v3changeview.AlertCovered(changeAlertCoverage)).Register(api)
	v3traces.New(s, policy, telemetry).Register(api)
	events := v3events.New(s.DB, func(ctx context.Context, user core.User, app, env, action string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	})
	events.AuthorizeTeamTx = func(ctx context.Context, tx pgx.Tx, user core.User, team, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, TeamID: team, EnvironmentID: env})
	}
	events.GroupPolicySource = alertmanagerSource
	events.GroupPolicyBindings = alertmanagerPolicyBindings
	events.RegisterNew(api)
	events.RegisterV3Incidents(api)
	pushService.Register(api)
	pushService.RegisterPublic(api)
	knowledgeV3 := &v3knowledge.Service{DB: s.DB, Knowledge: know, Policy: policy}
	knowledgeV3.Register(api)
	knowledgeV3.RegisterSearch(api)
	v3postmortems.New(s.DB, policy).Register(api)
	v3knowledgesources.NewWithAllSourceBindings(s.DB, policy, activeGitMarkdownBindings,
		activeExternalBindings, activeFeishuBindings).Register(api)
	sourceRegistry.Register(api)
	v3memory.New(s.DB, policy).Register(api)
	agentService := v3agents.New(s.DB, policy)
	promptTrialAdapter, err := loadV3PromptTrialAdapter(
		os.Getenv("AIOPS_V3_PROMPT_TRIAL_MODEL_CONFIG"),
		os.Getenv("AIOPS_V3_PROMPT_TRIAL_ALLOWED_HOSTS"), *info)
	if err != nil {
		return fmt.Errorf("configure V3 Prompt trial: %w", err)
	}
	agentService.SetPromptTrialModelAdapter(promptTrialAdapter)
	agentService.Register(api)
	go auditV3.CleanupExpired(ctx)
	return nil
}

func configuredV3SourceRegistry(raw string, s *store.Store, policy *rbac.V3Policy) (*v3sourceregistry.Service, error) {
	return configuredV3SourceRegistryWithGit(raw, s, policy, nil)
}

func configuredV3SourceRegistryWithGit(raw string, s *store.Store, policy *rbac.V3Policy,
	gitMarkdownBindings []v3knowledgesources.GitMarkdownBinding) (*v3sourceregistry.Service, error) {
	return configuredV3SourceRegistryWithSources(raw, s, policy, gitMarkdownBindings, nil)
}

func configuredV3SourceRegistryWithSources(raw string, s *store.Store, policy *rbac.V3Policy,
	gitMarkdownBindings []v3knowledgesources.GitMarkdownBinding,
	externalBindings []v3knowledgesources.ExternalBinding) (*v3sourceregistry.Service, error) {
	return configuredV3SourceRegistryWithAll(raw, s, policy, gitMarkdownBindings, externalBindings, nil)
}

func configuredV3SourceRegistryWithAll(raw string, s *store.Store, policy *rbac.V3Policy,
	gitMarkdownBindings []v3knowledgesources.GitMarkdownBinding,
	externalBindings []v3knowledgesources.ExternalBinding,
	feishuBindings []v3knowledgesources.FeishuBinding) (*v3sourceregistry.Service, error) {
	readers := map[string]v3sourceregistry.Reader{}
	readers["upload"] = v3knowledgesources.NewUploadReader(s.DB)
	switch raw {
	case "", "false":
	case "true":
		readers["web"] = v3knowledgesources.PublicWebObservationReader{Fetcher: v3knowledgesources.WebFetcher{}}
	default:
		return nil, fmt.Errorf("AIOPS_V3_PUBLIC_WEB_READER_ENABLED must be true or false")
	}
	if len(gitMarkdownBindings) > 0 {
		credentialBindings := make([]v3integrations.CredentialBinding, 0, len(gitMarkdownBindings))
		for _, binding := range gitMarkdownBindings {
			credentialBindings = append(credentialBindings, v3integrations.CredentialBinding{
				Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
				ConnectorID: binding.ConnectorID, Provider: "gitlab",
				Endpoint: binding.Endpoint, CredentialRef: binding.CredentialRef,
			})
		}
		resolver := v3integrations.NewBoundResolver(
			v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")),
			credentialBindings)
		reader, err := v3knowledgesources.NewGitMarkdownReader(gitMarkdownBindings, resolver, nil)
		if err != nil {
			return nil, fmt.Errorf("configure V3 Git Markdown reader: %w", err)
		}
		readers["git_markdown"] = reader
	}
	if len(externalBindings) > 0 {
		credentialBindings := make([]v3integrations.CredentialBinding, 0, len(externalBindings))
		for _, binding := range externalBindings {
			credentialBindings = append(credentialBindings, v3integrations.CredentialBinding{
				Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
				ConnectorID: binding.ConnectorID, Provider: binding.Type,
				Endpoint: binding.Endpoint, CredentialRef: binding.CredentialRef,
			})
		}
		resolver := v3integrations.NewBoundResolver(
			v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")),
			credentialBindings)
		for _, kind := range []string{"confluence", "notion"} {
			var selected []v3knowledgesources.ExternalBinding
			for _, binding := range externalBindings {
				if binding.Type == kind {
					selected = append(selected, binding)
				}
			}
			if len(selected) == 0 {
				continue
			}
			reader, err := v3knowledgesources.NewExternalReader(kind, selected, resolver, nil)
			if err != nil {
				return nil, fmt.Errorf("configure V3 %s source reader: %w", kind, err)
			}
			readers[kind] = reader
		}
	}
	if len(feishuBindings) > 0 {
		credentialBindings := make([]v3integrations.CredentialBinding, 0, len(feishuBindings))
		for _, binding := range feishuBindings {
			credentialBindings = append(credentialBindings, v3integrations.CredentialBinding{
				Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
				ConnectorID: binding.ConnectorID, Provider: "feishu",
				Endpoint: binding.Endpoint, CredentialRef: binding.CredentialRef,
			})
		}
		resolver := v3integrations.NewBoundResolver(
			v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")),
			credentialBindings)
		reader, err := v3knowledgesources.NewFeishuReader(feishuBindings, resolver, nil)
		if err != nil {
			return nil, fmt.Errorf("configure V3 Feishu Docx reader: %w", err)
		}
		readers["feishu"] = reader
	}
	return v3sourceregistry.NewWithReaders(s.DB, policy, readers), nil
}

func oidcEndpointOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
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
