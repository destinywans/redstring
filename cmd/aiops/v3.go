package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
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
	"github.com/wans/aiops-canvas/internal/v3rollouts"
	"github.com/wans/aiops-canvas/internal/v3ruleassistant"
	"github.com/wans/aiops-canvas/internal/v3security"
	"github.com/wans/aiops-canvas/internal/v3serviceviews"
	"github.com/wans/aiops-canvas/internal/v3settings"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
	"github.com/wans/aiops-canvas/internal/v3tokens"
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

type conversationEnvironmentTxAuthorizer struct {
	policy *rbac.V3Policy
	tx     pgx.Tx
}

func (a conversationEnvironmentTxAuthorizer) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.AuthorizeWithTx(ctx, a.tx, user, decision)
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
	// Pure source/configuration validation must finish before schema writes or
	// route registration. A malformed source must not touch a dependency (or
	// obscure its actual configuration error with a database failure).
	executions := v3executions.New(s.DB, policy)
	if err := executions.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate V3 execution publication: %w", err)
	}
	executions.Register(api)
	if err := configureV3MFA(s, api, *info, os.Getenv("AIOPS_V3_MFA_KEY_FILE")); err != nil {
		return err
	}
	passkeyService, err := configureV3PasskeyService(s, api, *info, os.Getenv("AIOPS_V3_PASSKEY_ENABLED"))
	if err != nil {
		return err
	}
	silenceExecutionSetting := os.Getenv("AIOPS_V3_SILENCE_EXECUTION_ENABLED")
	if silenceExecutionSetting != "" && silenceExecutionSetting != "false" && silenceExecutionSetting != "true" {
		return fmt.Errorf("AIOPS_V3_SILENCE_EXECUTION_ENABLED must be true or false")
	}
	silenceExecutionEnabled := silenceExecutionSetting == "true"
	if silenceExecutionEnabled && passkeyService == nil {
		return fmt.Errorf("V3 silence execution requires proposal passkeys")
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
	memberSources, err := loadV3MemberSources(os.Getenv("AIOPS_V3_MEMBER_SOURCES_CONFIG"), *info, s)
	if err != nil {
		return fmt.Errorf("configure V3 member sources: %w", err)
	}
	admin := v3admin.New(s)
	admin.Invitations = invitations
	admin.MemberSync = memberSources.MemberSync
	admin.MFAEvidence = memberSources.MFAEvidence
	admin.MFAReminders = memberSources.MFAReminders
	admin.Register(api)
	securityAuthority, err := loadV3SecurityAuthority(
		os.Getenv("AIOPS_V3_SECURITY_AUTHORITY_URL"), os.Getenv("AIOPS_V3_SECURITY_AUTHORITY_TOKEN_FILE"))
	if err != nil {
		return fmt.Errorf("configure V3 security authority: %w", err)
	}
	v3security.New(s, info.Domain, securityAuthority).Register(api)
	lowerSources, err := loadV3LowerSources(os.Getenv("AIOPS_V3_LOWER_SOURCES_CONFIG"), cfg.Tier)
	if err != nil {
		return fmt.Errorf("load V3 lower-environment sources: %w", err)
	}
	lowerSources.Register(api)
	auditV3 := v3audit.New(s.DB, cfg.EnvironmentIDs)
	auditV3.Register(api)
	canvasV3 := v3canvas.NewWithRuntime(s, modelRuntime)
	canvasV3.Register(api)
	canvasV3.RegisterPageInteractions(api)
	catalog := v3catalog.New(s, policy, cfg.EnvironmentIDs...)
	catalog.RegisterWithoutServiceReads(api)
	configSync, err := loadV3ConfigSync(os.Getenv("AIOPS_V3_CONFIG_SYNC_CONFIG"), cfg.Tier, s.DB, policy)
	if err != nil {
		return fmt.Errorf("load V3 configuration promotion: %w", err)
	}
	configSync.Register(api)
	proposalService := v3proposals.New(s.DB, policy)
	proposalService.ApprovalPasskeys = passkeyService
	bulkService := v3events.NewBulk(s.DB, policy, nil)
	proposalService.ApprovalEvidence = bulkService
	proposalService.Register(api)
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
	mcpService.AuthorizeRegistrationEndpoint, err = loadV3MCPRegistrationBindings(
		os.Getenv("AIOPS_V3_MCP_REGISTRATION_BINDINGS"), *info)
	if err != nil {
		return fmt.Errorf("configure V3 MCP registration: %w", err)
	}
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
	rulePreview, ruleDraftModel, err := loadV3RuleAssistantSources(
		os.Getenv("AIOPS_V3_RULE_PREVIEW_CONFIG"), os.Getenv("AIOPS_V3_RULE_PREVIEW_ALLOWED_HOSTS"),
		os.Getenv("AIOPS_V3_RULE_DRAFT_MODEL_CONFIG"), os.Getenv("AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS"), *info)
	if err != nil {
		return fmt.Errorf("configure V3 alert rule assistant: %w", err)
	}
	(&v3ruleassistant.Service{DB: s.DB, Access: policy, Preview: rulePreview, Model: ruleDraftModel}).Register(api)
	var alertmanagerSource *v3alertmanager.Client
	var alertmanagerLabels map[string]string
	var alertmanagerBindings map[string]map[string]string
	var alertmanagerPolicyBindings map[string]v3events.GroupPolicyBinding
	if silenceExecutionEnabled {
		alertmanagerSource, alertmanagerLabels, alertmanagerBindings, alertmanagerPolicyBindings, err =
			loadV3AlertmanagerWriteSource(os.Getenv("AIOPS_V3_ALERTMANAGER_CONFIG"), *info, opsConfig, s.DB)
	} else {
		alertmanagerSource, alertmanagerLabels, alertmanagerBindings, alertmanagerPolicyBindings, err =
			loadV3AlertmanagerSourceWithPolicies(os.Getenv("AIOPS_V3_ALERTMANAGER_CONFIG"), *info, opsConfig, s.DB)
	}
	if err != nil {
		return fmt.Errorf("load V3 Alertmanager source: %w", err)
	}
	if silenceExecutionEnabled && alertmanagerSource == nil {
		return fmt.Errorf("V3 silence execution requires a private Alertmanager source")
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
	alertRuleDraftRepository, err := loadV3AlertRuleDraftRepository(configSync, routeDraftRepository, inhibitionRepository,
		os.Getenv("AIOPS_V3_ALERT_RULE_DRAFT_TARGET_BRANCH"),
		os.Getenv("AIOPS_V3_ALERT_RULE_DRAFT_BRANCH_PREFIX"))
	if err != nil {
		return fmt.Errorf("configure V3 alert rule drafts: %w", err)
	}
	alertRuleWriter := v3alertrules.NewWriteService(s.DB, policy, ruleService, alertRuleDraftRepository)
	enableApplier, enableEnvironments, err := loadV3AlertRuleEnableApplier(
		os.Getenv("AIOPS_V3_ALERT_RULE_ENABLE_CONFIG"), *info, ruleSource, ruleBindings)
	if err != nil {
		return fmt.Errorf("configure V3 alert rule enable: %w", err)
	}
	var enableWorker *v3AlertRuleEnableWorker
	if enableApplier != nil {
		if passkeyService == nil {
			return fmt.Errorf("V3 alert rule enable requires proposal passkeys")
		}
		alertRuleWriter.EnableApplier = enableApplier
		alertRuleWriter.EnableApproval = &v3alertrules.AlertRuleApprovalAuthority{}
		enableWorker = newV3AlertRuleEnableWorker(alertRuleWriter, enableEnvironments)
		alertRuleWriter.EnableReady = enableWorker.Ready
		proposalService.AlertRuleEnableReady = enableWorker.Ready
		proposalService.AlertRuleEnableIntent = func(ctx context.Context, tx pgx.Tx, proposalID string) (v3proposals.AlertRuleEnableReview, error) {
			intent, err := v3alertrules.LoadAlertRuleEnableIntentWithTx(ctx, tx, proposalID)
			if err != nil {
				return v3proposals.AlertRuleEnableReview{}, err
			}
			return v3proposals.AlertRuleEnableReview{
				Kind: intent.Kind, EnvironmentID: intent.EnvironmentID,
				ApplicationID: intent.ApplicationID, InvestigationID: intent.InvestigationID,
				InvestigationUpdatedAt: intent.InvestigationUpdatedAt,
			}, nil
		}
	}
	trialRuntime, err := loadV3AlertTrialRuntime(os.Getenv("AIOPS_V3_ALERT_RULE_TRIAL_CONFIG"), cfg,
		ruleSource, ruleBindings, alertRuleWriter)
	if err != nil {
		return fmt.Errorf("configure V3 alert rule trial: %w", err)
	}
	if trialRuntime != nil {
		alertRuleWriter.TrialRuntime = trialRuntime
	}
	alertRuleWriter.Register(api)
	alertRuleWriter.RegisterTrials(api)
	inhibitionHistory, err := loadV3InhibitionHistory(os.Getenv("AIOPS_V3_INHIBITION_HISTORY_CONFIG"),
		*info, alertmanagerPolicyBindings)
	if err != nil {
		return fmt.Errorf("configure V3 inhibition history: %w", err)
	}
	alertmanagerService := &v3alertmanager.Service{
		Source: alertmanagerSource, Catalog: v3alertmanager.DatabaseCatalog{DB: s.DB}, Access: policy,
		EnvironmentLabels: alertmanagerLabels, ApplicationBindings: alertmanagerBindings,
		RouteRepository:      optionalV3AlertmanagerRepository(routeDraftRepository),
		InhibitionRepository: optionalV3AlertmanagerRepository(inhibitionRepository),
		InhibitionHistory:    inhibitionHistory,
	}
	var silenceWorker *v3SilenceWorker
	var silenceLedger *v3alertmanager.DatabaseSilenceLedger
	var silenceCreateReady func(context.Context, string) error
	if silenceExecutionEnabled {
		silenceLedger = v3alertmanager.NewDatabaseSilenceLedger(s.DB, policy, alertmanagerLabels, alertmanagerBindings)
		producer := &v3proposals.SilenceProductProducer{
			Proposals: proposalService, Ledger: silenceLedger, Client: alertmanagerSource,
		}
		executor := &v3alertmanager.SilenceExecutor{
			Ledger: silenceLedger, Client: alertmanagerSource,
			Actions:       &v3proposals.SilenceExecutionActionSource{Proposals: proposalService},
			Approvals:     &v3proposals.SilenceApprovalAuthority{Proposals: proposalService},
			GroupEvidence: bulkService,
		}
		silenceWorker = newV3SilenceWorker(s.DB, executor)
		executor.Worker = silenceWorker
		silenceCreateReady = executor.CreateReady
		proposalService.ApprovalWorkflow = executor
		alertmanagerService.SilenceCreateProposals = producer
		alertmanagerService.SilenceCancelProposals = &v3alertmanager.DatabaseSilenceCancelProduct{
			Ledger: silenceLedger, Source: alertmanagerSource, Review: producer,
		}
		alertmanagerService.SilenceWorkflow = executor
	}
	// PA-08: write bindings approve the rollback executor's Deployments.
	rollbackWriteBindings, err := v3rollouts.ParseWriteBindings(os.Getenv("AIOPS_V3_KUBERNETES_WRITE_BINDINGS"))
	if err != nil {
		return fmt.Errorf("configure V3 Kubernetes write bindings: %w", err)
	}
	if silenceExecutionEnabled || enableWorker != nil || len(rollbackWriteBindings) > 0 {
		proposalService.RegisterApprovals(api)
	}
	if promotionRepository, ok := configSync.Repository.(*v3configsync.GitLabClient); ok && promotionRepository != nil {
		alertmanagerService.ConfigPromotionProjectID = promotionRepository.ProjectID()
		alertmanagerService.ConfigPromotionTargetBranch = promotionRepository.TargetBranch()
		alertmanagerService.ConfigPromotionBranchPrefix = promotionRepository.BranchPrefix()
	}
	alertmanagerService.Register(api)

	personalSettings := v3settings.New(s, modelRuntime)
	personalSettings.MeEvidence = meEvidence
	personalSettings.ProfileInstanceID = meEvidence.MFAInstanceID
	personalSettings.ProfileEvidence, err = loadV3ProfileEvidence(
		os.Getenv("AIOPS_V3_PROFILE_EVIDENCE_URL"), os.Getenv("AIOPS_V3_PROFILE_EVIDENCE_TOKEN_FILE"))
	if err != nil {
		return fmt.Errorf("configure V3 profile evidence: %w", err)
	}
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
	tokens := v3tokens.New(s.DB, policy)
	tokens.Register(api)
	api.V3ConnectorBearerHandler = connector.BearerConnectorList(tokens, cfg.EnvironmentIDs)
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
	sourceCatalog, err := loadSourceCatalog()
	if err != nil {
		return fmt.Errorf("load V3 source catalog: %w", err)
	}
	metricCatalog := sourceCatalog.Queries
	// PA-19 P2: readers keep using the file configuration; compare mode also
	// resolves each read in the registry and logs differences.
	registryMode, err := v3telemetry.ParseMode(os.Getenv("AIOPS_V3_SOURCE_REGISTRY"))
	if err != nil {
		return err
	}
	var sourceCompare v3telemetry.Observer
	if registryMode == v3telemetry.ModeCompare {
		comparer := v3telemetry.NewComparer(s.DB, &v3telemetry.Resolver{Tier: cfg.Tier, Catalog: sourceCatalog,
			Approvals: v3telemetry.NewApprovals(credentialBindings)}, telemetry.Registration(), slog.Default())
		go comparer.Run(ctx)
		sourceCompare = comparer
		slog.Info("source registry compare mode enabled")
	}
	metricReader, err := v3integrations.NewPrometheusReader(s.DB, cfg.Tier, connector.Prober.Resolver, metricCatalog)
	if err != nil {
		return fmt.Errorf("configure V3 Prometheus reader: %w", err)
	}
	// PA-08: Kubernetes rollback. Candidates and proposals read with the
	// connector's approved credential; only the executor reads the write
	// credential named by a write binding. Without bindings the executor does
	// not run, so rollback proposals cannot be approved.
	if len(rollbackWriteBindings) > 0 && (os.Getenv("AIOPS_VAULT_URL") == "" || os.Getenv("AIOPS_VAULT_TOKEN") == "") {
		return errors.New("configure V3 Kubernetes write bindings: AIOPS_VAULT_URL and AIOPS_VAULT_TOKEN are required")
	}
	for _, write := range rollbackWriteBindings {
		for _, read := range credentialBindings {
			if read.CredentialRef == write.CredentialRef {
				return errors.New("configure V3 Kubernetes write bindings: the write credential must not be a connector credential")
			}
		}
	}
	rollouts := &v3rollouts.Service{DB: s.DB, Tier: cfg.Tier, Access: policy,
		Registry:      &v3telemetry.Resolver{Tier: cfg.Tier, Catalog: sourceCatalog, Approvals: v3telemetry.NewApprovals(credentialBindings)},
		Credentials:   credentialResolver,
		Secrets:       v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN")),
		WriteBindings: rollbackWriteBindings, Proposals: proposalService, Metrics: metricReader}
	rollouts.Register(api)
	connector.Workloads = rollouts.ConnectorWorkloads
	executions.Rollout = rollouts.Execution
	proposalService.RollbackReady = rollouts.Ready
	if len(rollbackWriteBindings) > 0 {
		go rollouts.Run(ctx)
		slog.Info("V3 rollback executor enabled", "write_bindings", len(rollbackWriteBindings))
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
	knowledgeV3 := &v3knowledge.Service{DB: s.DB, Knowledge: know, Policy: policy}
	currentToolUser := func(ctx context.Context, id string) (core.User, error) {
		member, err := s.Member(ctx, id)
		if err != nil {
			return core.User{}, err
		}
		if member.DeletedAt != nil || member.Disabled {
			return core.User{}, store.ErrForbidden
		}
		return member.User, nil
	}
	readTools := v3ReadTools{
		DB: s.DB, Models: toolModels, CurrentUser: currentToolUser,
		LookupService: catalog.LookupServiceEvidence, ListServices: catalog.ListServiceEvidence, Access: policy, Telemetry: telemetry,
		SourceCompare: sourceCompare, Workloads: rollouts,
		KnowledgeTool: func(actorID, applicationID, environmentID string) (inference.EvidenceTool, bool) {
			return v3knowledge.NewKnowledgeEvidenceTool(v3knowledge.KnowledgeEvidenceToolOptions{
				Enabled: true, Service: knowledgeV3, ActorID: actorID, ApplicationID: applicationID,
				EnvironmentID: environmentID, CurrentUser: currentToolUser,
			})
		},
	}
	canvasV3.InvestigationToolsForTurn = readTools.ForInvestigation
	chat := v3conversation.New(s.DB, modelRuntime, v3conversation.Options{
		CurrentUser:      httpapi.CurrentUser,
		RefreshSession:   s.Session,
		RefreshSessionTx: s.SessionTx,
		Shutdown:         ctx,
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
		ToolsForTurn: func(ctx context.Context, ownerID, environmentID, modelID string) ([]inference.EvidenceTool, error) {
			return readTools.ForTurn(ctx, ownerID, "", environmentID, modelID)
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
		AllowedEnvironmentsTx: func(ctx context.Context, tx pgx.Tx, user core.User) ([]string, error) {
			return allowedConversationEnvironments(ctx, user, cfg.EnvironmentIDs, conversationEnvironmentTxAuthorizer{policy: policy, tx: tx})
		},
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
	observations.SourceCompare = sourceCompare
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
	if silenceExecutionEnabled {
		groupIdentityBindings, err := loadV3BulkGroupSilenceBindings(
			os.Getenv("AIOPS_V3_BULK_GROUP_SILENCE_CONFIG"), *info, alertmanagerSource,
			alertmanagerLabels, alertmanagerBindings, alertmanagerPolicyBindings, opsConfig)
		if err != nil {
			return fmt.Errorf("configure V3 bulk group silence: %w", err)
		}
		if len(groupIdentityBindings) > 0 {
			// The group transaction and range-coverage callback each need a
			// connection. A one-connection pool cannot execute this path.
			if changeAlertCoverage == nil || s.DB.Config().MaxConns < 2 {
				return fmt.Errorf("V3 bulk group silence requires complete alert-range coverage and at least two DB connections")
			}
			bulkService.Silence = &v3events.BulkSilenceSource{
				Proposals: proposalService, Ledger: silenceLedger, Client: alertmanagerSource,
				CreateReady:         silenceCreateReady,
				ApplicationBindings: alertmanagerBindings, EnvironmentLabels: alertmanagerLabels,
				IngressSources: opsConfig.Sources, GroupIdentityBindings: groupIdentityBindings,
				AlertRangeCovered: changeAlertCoverage,
			}
		}
	}
	traces := v3traces.New(s, policy, telemetry)
	traces.SourceCompare = sourceCompare
	traces.Register(api)
	events := v3events.New(s.DB, func(ctx context.Context, user core.User, app, env, action string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	})
	events.AuthorizeListTx = func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return policy.AuthorizeViewWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}
	events.AuthorizeTeamTx = func(ctx context.Context, tx pgx.Tx, user core.User, team, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, TeamID: team, EnvironmentID: env})
	}
	events.GroupPolicySource = alertmanagerSource
	events.GroupPolicyBindings = alertmanagerPolicyBindings
	// PA-47: a silenced alert's resolved notification is never delivered, so
	// firing incidents from the reviewed sources are reconciled against
	// Alertmanager. AIOPS_V3_ALERT_RECONCILE_SECONDS=0 turns this off.
	if alertmanagerSource != nil && len(alertmanagerPolicyBindings) > 0 {
		interval, err := alertReconcileInterval(os.Getenv("AIOPS_V3_ALERT_RECONCILE_SECONDS"))
		if err != nil {
			return err
		}
		if interval > 0 {
			reconciler := &v3events.AlertReconciler{DB: s.DB, Source: alertmanagerSource,
				Bindings: alertmanagerPolicyBindings, Interval: interval}
			go reconciler.Run(ctx)
		}
	}
	events.RegisterNew(api)
	events.RegisterV3Incidents(api)
	events.RegisterPageOperations(api)
	bulkService.Register(api)
	pushService.Register(api)
	pushService.RegisterPublic(api)
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
	agentService.SetExecutionRuntime(modelRuntime, readTools.ForTurn)
	if err := agentService.RecoverExpiredExecutions(ctx); err != nil {
		return fmt.Errorf("recover V3 Agent executions: %w", err)
	}
	agentService.Register(api)
	if trialRuntime != nil {
		go func() {
			if err := trialRuntime.Run(ctx); err != nil && ctx.Err() == nil {
				log.Printf("V3 alert rule trial worker stopped: %v", err)
			}
		}()
	}
	if silenceWorker != nil {
		go func() {
			if err := silenceWorker.Run(ctx); err != nil && ctx.Err() == nil {
				log.Printf("V3 silence worker stopped: %v", err)
			}
		}()
	}
	if enableWorker != nil {
		go func() {
			if err := enableWorker.Run(ctx); err != nil && ctx.Err() == nil {
				log.Printf("V3 alert rule enable worker stopped: %v", err)
			}
		}()
	}
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

// alertReconcileInterval reads AIOPS_V3_ALERT_RECONCILE_SECONDS: empty is 60,
// 0 disables reconciliation, otherwise 15 to 3600 seconds.
func alertReconcileInterval(value string) (time.Duration, error) {
	if value == "" {
		return time.Minute, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 || (seconds > 0 && (seconds < 15 || seconds > 3600)) {
		return 0, fmt.Errorf("AIOPS_V3_ALERT_RECONCILE_SECONDS must be 0 or 15 to 3600")
	}
	return time.Duration(seconds) * time.Second, nil
}
