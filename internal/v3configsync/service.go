// Package v3configsync promotes reviewed configuration through a dedicated
// GitLab repository. Its signed manifests carry bounded configuration only;
// importing an object never executes a Runbook, applies a rule, or runs an Agent.
package v3configsync

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3templatecapacity"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Repository interface {
	ProjectID() int64
	BranchPrefix() string
	Export(context.Context, string, string, []byte, string) (MR, error)
	LookupPromotionMR(context.Context, int64) (MR, error)
	ListMerged(context.Context, int) ([]MR, error)
	Merged(context.Context, int64) (MR, error)
	ReadMerged(context.Context, int64, string) (MR, []byte, error)
	ReadMergedManifest(context.Context, int64) (MR, string, []byte, error)
}

type Service struct {
	DB         *pgxpool.Pool
	Access     Access
	Repository Repository
	AlertRules *AlertRuleResolver
	// Secret material is supplied by the composition root (for example a
	// Vault-mounted file). Production receives public keys only. An unsigned
	// or untrusted manifest cannot become production configuration.
	SourceInstanceID string
	SigningKey       ed25519.PrivateKey
	TrustedSources   map[string]ed25519.PublicKey
}

func New(db *pgxpool.Pool, access Access, repo Repository) *Service {
	return &Service{DB: db, Access: access, Repository: repo}
}

func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, module, action string
		handler                 http.HandlerFunc
	}{
		{"GET /api/v1/config-sync", "workspace", "view", s.list},
		{"POST /api/v1/promotions", "environment", "view", s.promote},
		{"POST /api/v1/config-sync/{id}/import", "workspace", "manage", s.importMerged},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, route.module, route.action)
	}
}

type SyncItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	KindLabel string `json:"kind_label"`
	Title     string `json:"title"`
	MR        string `json:"mr"`
	Author    string `json:"author"`
	Meta      string `json:"meta"`
	Status    string `json:"status"`
	// Reason says why a production candidate is verifying or unverifiable:
	// not_ready, gitlab_unavailable or untrusted (PA-103).
	Reason string `json:"reason,omitempty"`
}

func msg(r *http.Request, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		return en
	}
	return zh
}

func problem(w http.ResponseWriter, r *http.Request, status int, code, zh, en string) {
	httpapi.Error(w, status, code, msg(r, zh, en))
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.DB == nil || s.Access == nil || s.Repository == nil {
		problem(w, r, 501, "config_repository_unavailable", "配置仓库未接入", "Configuration repository is not connected")
		return false
	}
	if s.Repository.ProjectID() <= 0 || s.Repository.BranchPrefix() == "" {
		problem(w, r, 501, "config_repository_unavailable", "配置仓库配置无效", "Configuration repository is not configured")
		return false
	}
	if scope, ok := httpapi.V3ScopeFromContext(r.Context()); ok {
		if scope.Tier == "production" && len(s.TrustedSources) == 0 ||
			scope.Tier == "nonprod" && (!sourceInstanceRE.MatchString(s.SourceInstanceID) || len(s.SigningKey) != ed25519.PrivateKeySize) {
			problem(w, r, 501, "config_source_trust_unavailable", "配置来源信任尚未接入", "Configuration source trust is not connected")
			return false
		}
	}
	return true
}

func scopeFor(r *http.Request, write bool) (httpapi.V3EnvironmentScope, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" ||
		(write && (scope.All || len(scope.EnvironmentIDs) != 1)) {
		return httpapi.V3EnvironmentScope{}, false
	}
	return scope, true
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w, r) {
		return
	}
	scope, ok := scopeFor(r, false)
	if !ok {
		problem(w, r, 422, "environment_required", "请选择环境", "Select an environment")
		return
	}
	var items []SyncItem
	var err error
	direction := "export"
	if scope.Tier == "production" {
		direction = "import"
		if err = s.authorizeGlobalManager(r.Context(), httpapi.CurrentUser(r), "prod", nil); err == nil {
			items, err = s.importItems(r)
			if err == nil {
				// GitLab reads can outlive a global-manager grant. Recheck the
				// current policy before returning any imported titles or metadata.
				err = s.authorizeGlobalManager(r.Context(), httpapi.CurrentUser(r), "prod", nil)
			}
		}
	} else {
		items, err = s.exportItems(r, scope.EnvironmentIDs)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err = s.authorizeCurrentListSession(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"direction": direction, "items": items})
}

// The environment middleware authenticates at request entry, before remote
// GitLab reads. Recheck the exact Cookie session and account immediately before
// returning either list; a revoked or expired session must not receive items.
func (s *Service) authorizeCurrentListSession(r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return errListSessionRevoked
	}
	var active bool
	err = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions sess
		JOIN users u ON u.id=sess.user_id
		WHERE sess.token_hash=$1 AND sess.user_id=$2 AND sess.expires_at>clock_timestamp()
		AND NOT u.disabled AND u.deleted_at IS NULL)`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return errListSessionRevoked
	}
	return nil
}

var (
	errResultLimit           = errors.New("configuration list exceeds limit")
	errInvalid               = errors.New("invalid configuration promotion request")
	errUntrusted             = errors.New("configuration source is untrusted")
	errUnavailable           = errors.New("configuration source unavailable")
	errBusy                  = errors.New("configuration export already in progress")
	errListSessionRevoked    = errors.New("configuration list session revoked")
	errKindSourceUnavailable = errors.New("configuration kind source unavailable")
)

// configRefusal is a source object the export contract cannot carry. It is
// refused before anything is reserved or written, with a code naming why
// (PA-96, PA-97), not reported as an untrusted merged source.
type configRefusal struct{ code, zh, en string }

func (e *configRefusal) Error() string { return "configuration source not exportable: " + e.code }

var (
	errTemplateDescription   = &configRefusal{"template_has_description", "模板带说明文字，配置提升只携带结构", "The template has a description; promotion carries structure only"}
	errTemplateContent       = &configRefusal{"template_retained_content", "模板保留了节点标题、步骤或内容，配置提升只携带结构", "The template keeps node titles, steps or content; promotion carries structure only"}
	errTemplateRunbooks      = &configRefusal{"template_private_runbooks", "模板关联了本实例的 Runbook，无法随配置提升", "The template links this instance's Runbooks, which cannot be promoted with it"}
	errTemplateStructure     = &configRefusal{"template_structure_invalid", "模板结构不在配置提升支持的范围内", "The template structure is outside what promotion supports"}
	errAlertRuleExpression   = &configRefusal{"alert_rule_expression_unsafe", "告警表达式含不能提升的字符串字面量或字符", "The alert expression has string literals or characters that cannot be promoted"}
	errAlertRuleLabels       = &configRefusal{"alert_rule_label_not_exportable", "告警规则带有不能提升的标签或级别", "The alert rule has labels or a severity that cannot be promoted"}
	errAlertRuleShape        = &configRefusal{"alert_rule_not_exportable", "告警规则名称、分组或持续时间超出配置提升的范围", "The alert rule name, group or duration is outside what promotion supports"}
	errAgentArtifactUnpinned = &configRefusal{"artifact_ref_not_pinned", "Agent 稳定版本的制品引用没有固定到 @sha256 摘要", "The Agent's stable version artifact is not pinned to an @sha256 digest"}
	// PA-100: instances map applications by key.
	errApplicationKeyMissing    = &configRefusal{"application_key_missing", "应用没有可用于跨实例对应的 key", "The application has no key to match it across instances"}
	errApplicationNotRegistered = &configRefusal{"application_not_registered", "生产实例没有登记这个应用 key 的 prod 环境", "Production has no application with this key registered in prod"}
	errReexportRequired         = &configRefusal{"promotion_reexport_required", "这次提升按旧格式导出，没有应用 key；请在非生产重新导出", "This promotion was exported without an application key; export it again from non-production"}
)

func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var refused *configRefusal
	switch {
	case errors.As(err, &refused):
		problem(w, r, 422, refused.code, refused.zh, refused.en)
	case errors.Is(err, errListSessionRevoked):
		problem(w, r, 401, "unauthorized", "请重新登录", "Sign in again")
	case errors.Is(err, errKindSourceUnavailable):
		problem(w, r, 501, "promotion_source_unavailable", "配置来源未接入", "Configuration source is not connected")
	case errors.Is(err, store.ErrForbidden):
		problem(w, r, 403, "forbidden", "没有配置提升权限", "Configuration promotion denied")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		problem(w, r, 404, "not_found", "配置项不存在", "Configuration item not found")
	case errors.Is(err, errInvalid):
		problem(w, r, 422, "invalid_promotion", "配置提升请求无效", "Invalid configuration promotion")
	case errors.Is(err, v3templatecapacity.ErrFull):
		problem(w, r, 422, "template_scope_too_broad", "画布模板列表超出上限", "Canvas template list exceeds its limit")
	case errors.Is(err, errResultLimit), errors.Is(err, ErrListTooLarge):
		problem(w, r, 413, "scope_too_large", "配置项超出列表上限", "Configuration list exceeds its limit")
	case errors.Is(err, errBusy):
		problem(w, r, 409, "promotion_in_progress", "配置正在导出，请稍后重试", "Configuration export is in progress")
	case errors.Is(err, store.ErrConflict), errors.Is(err, ErrConflict):
		problem(w, r, 409, "promotion_conflict", "配置来源或目标已变化", "Configuration source or target has changed")
	case errors.Is(err, ErrGitLabUnavailable):
		problem(w, r, 503, "gitlab_unavailable", "GitLab 暂时无法读取，请稍后重试", "GitLab could not be read; retry shortly")
	case errors.Is(err, ErrMRNotPrepared):
		problem(w, r, 503, "merge_request_not_ready", "GitLab 仍在处理这次合并，请稍后重试", "GitLab is still processing this merge; retry shortly")
	case errors.Is(err, ErrMRLinkMismatch):
		// The MR may exist; the export stays uncertain and a retry after
		// fixing gitlab.web_url reuses it (PA-98).
		problem(w, r, 503, "gitlab_web_url_mismatch", "GitLab 返回的合并请求链接不在配置的网页地址下，请检查 gitlab.web_url；导出记为结果未知，修正后重试会复用同一个合并请求",
			"GitLab returned a merge request link outside the configured web address; check gitlab.web_url. The export is recorded as unknown and a retry reuses the same merge request")
	case errors.Is(err, errUntrusted), errors.Is(err, ErrUntrusted), errors.Is(err, errInvalidManifest):
		problem(w, r, 503, "config_source_untrusted", "无法验证已合并配置来源", "Merged configuration provenance cannot be verified")
	default:
		problem(w, r, 503, "config_sync_unavailable", "配置同步暂不可用", "Configuration sync is unavailable")
	}
}

func (s *Service) authorizeGlobalManager(ctx context.Context, user core.User, env string, tx pgx.Tx) error {
	decision := rbac.V3Decision{Module: "workspace", Action: "manage", EnvironmentID: env}
	var err error
	if tx != nil {
		err = s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	} else {
		err = s.Access.Authorize(ctx, user, decision)
	}
	if err != nil {
		return err
	}
	const globalSQL = `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
 JOIN rbac_platform_administrators a ON a.user_id=g.user_id
 WHERE g.user_id=$1 AND g.module='workspace' AND g.action='manage'
 AND g.scope_kind='global' AND g.scope_id='*' AND g.v3_identity_kind='human' AND g.role_kind<>'machine')`
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, globalSQL, user.ID)
	} else {
		row = s.DB.QueryRow(ctx, globalSQL, user.ID)
	}
	var allowed bool
	if err = row.Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return store.ErrForbidden
	}
	return nil
}

type exportView struct {
	id, kind, sourceID, env, applicationID, sourceVersion, digest, author string
	branch, path                                                          string
	iid                                                                   int64
	payload                                                               []byte
}

// A persisted export may outlive grants, application bindings and Runbook
// publication. Re-evaluate its current domain read scope before showing it.
func (s *Service) exportAllowed(ctx context.Context, user core.User, view exportView) (bool, error) {
	module, action, ok := configKindAccess(view.kind)
	if !ok {
		return false, errUntrusted
	}
	ownerTeam := ""
	if view.applicationID != "" {
		err := s.DB.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`, view.applicationID, view.env).Scan(&ownerTeam)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	if view.kind == "runbook" {
		return s.runbookExportAllowed(ctx, user, view, ownerTeam)
	}
	decision := rbac.V3Decision{Module: module, Action: action, EnvironmentID: view.env,
		ApplicationID: view.applicationID}
	err := s.Access.Authorize(ctx, user, decision)
	if errors.Is(err, store.ErrForbidden) && view.kind == "alert_rule" && ownerTeam != "" {
		decision.ApplicationID = ""
		decision.TeamID = ownerTeam
		err = s.Access.Authorize(ctx, user, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) runbookExportAllowed(ctx context.Context, user core.User, view exportView, ownerTeam string) (bool, error) {
	version, err := strconv.Atoi(view.sourceVersion)
	if err != nil || version <= 0 {
		return false, nil
	}
	var lifecycle, maintainer, teamID string
	var teamScoped bool
	err = s.DB.QueryRow(ctx, `SELECT d.lifecycle,COALESCE(d.metadata->>'maintainer_id',''),
 d.metadata ? 'team_id',COALESCE(d.metadata->>'team_id','')
 FROM knowledge_documents d WHERE d.id=$1 AND d.version >= $2 AND COALESCE(d.application_id,'')=$3
 AND d.source LIKE 'runbook://%'
 AND EXISTS(SELECT 1 FROM knowledge_revisions revision
   WHERE revision.document_id=d.id AND revision.version=$2)
 AND jsonb_typeof(d.metadata)='object' AND NOT (d.metadata ? 'source_registration_id')
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
   THEN jsonb_array_length(d.metadata->'environment_ids')>0
     AND d.metadata->'environment_ids' ? $4
     AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item
       WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='')
   ELSE false END
 AND (NOT (d.metadata ? 'team_id') OR
   (jsonb_typeof(d.metadata->'team_id')='string'
    AND btrim(d.metadata->>'team_id')<>'' AND d.metadata->>'team_id'=$5))`,
		view.sourceID, version, view.applicationID, view.env, ownerTeam).
		Scan(&lifecycle, &maintainer, &teamScoped, &teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	action := "view"
	switch lifecycle {
	case "published":
	case "draft":
		if maintainer != user.ID {
			return false, nil
		}
		action = "manage"
	case "review":
		action = "manage"
	default:
		return false, nil
	}
	err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: action,
		EnvironmentID: view.env, ApplicationID: view.applicationID})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if teamScoped {
		err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: action,
			EnvironmentID: view.env, TeamID: teamID})
		if errors.Is(err, store.ErrForbidden) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Service) exportItems(r *http.Request, envs []string) ([]SyncItem, error) {
	ctx, user := r.Context(), httpapi.CurrentUser(r)
	items := []SyncItem{}
	rows, err := s.DB.Query(ctx, `SELECT e.id,e.kind,e.object_id,e.environment_id,
 COALESCE(e.application_id,''),COALESCE(e.source_version,''),e.source_digest,
 COALESCE(e.mr_iid,0),e.payload,COALESCE(u.name,''),e.branch,e.path
 FROM v3_config_exports e
 LEFT JOIN users u ON u.id=e.actor_id
 WHERE e.actor_id=$1 AND e.environment_id=ANY($2::text[])
 AND e.project_id=$3 AND e.state='exported'
 ORDER BY e.created_at DESC,e.id DESC LIMIT 1001`, user.ID, envs, s.Repository.ProjectID())
	if err != nil {
		return nil, err
	}
	candidates := []exportView{}
	for rows.Next() {
		var view exportView
		if err = rows.Scan(&view.id, &view.kind, &view.sourceID, &view.env,
			&view.applicationID, &view.sourceVersion, &view.digest, &view.iid, &view.payload,
			&view.author, &view.branch, &view.path); err != nil {
			break
		}
		candidates = append(candidates, view)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > 1000 {
		return nil, errResultLimit
	}
	publicKey := s.SigningKey.Public().(ed25519.PublicKey)
	trusted := map[string]ed25519.PublicKey{s.SourceInstanceID: publicKey}
	visible := make([]exportView, 0, len(candidates))
	for _, view := range candidates {
		allowed, authErr := s.exportAllowed(ctx, user, view)
		if authErr != nil {
			return nil, authErr
		}
		if !allowed {
			continue
		}
		summary, err := readPromotionSummary(view.payload, view.id, trusted)
		if err != nil || summary.Kind != view.kind || summary.SourceID != view.sourceID ||
			summary.SourceEnvironment != view.env || summary.SourceInstance != s.SourceInstanceID ||
			summary.SourceDigest != view.digest || summary.ApplicationID != view.applicationID ||
			summary.SourceVersion != view.sourceVersion || view.iid <= 0 {
			return nil, errUntrusted
		}
		expectedPath, err := promotionPathForKind(summary.Kind, view.id)
		if err != nil || view.path != expectedPath ||
			view.branch != s.Repository.BranchPrefix()+view.id {
			return nil, errUntrusted
		}
		meta := msg(r, "来源环境：", "Source environment: ") + view.env
		if summary.SourceVersion != "" {
			meta += msg(r, " · 来源版本：", " · Source version: ") + summary.SourceVersion
		}
		mr, err := s.Repository.LookupPromotionMR(ctx, view.iid)
		if err != nil {
			return nil, err
		}
		if mr.IID != view.iid || mr.SourceBranch != s.Repository.BranchPrefix()+view.id ||
			mr.SourceProjectID != s.Repository.ProjectID() || mr.TargetProjectID != s.Repository.ProjectID() {
			return nil, errUntrusted
		}
		status := "exported"
		if mr.State == "merged" {
			mergedMR, path, raw, readErr := s.Repository.ReadMergedManifest(ctx, view.iid)
			if readErr != nil {
				return nil, readErr
			}
			mergedSummary, verifyErr := readPromotionSummary(raw, view.id, trusted)
			if verifyErr != nil || path != expectedPath || mergedSummary != summary ||
				mergedMR.IID != mr.IID || mergedMR.State != "merged" ||
				mergedMR.MergeCommitSHA != mr.MergeCommitSHA ||
				mergedMR.SourceBranch != mr.SourceBranch ||
				mergedMR.SourceProjectID != mr.SourceProjectID ||
				mergedMR.TargetProjectID != mr.TargetProjectID {
				return nil, errUntrusted
			}
			status = "merged"
		} else if mr.State != "opened" {
			return nil, errUntrusted
		}
		allowed, authErr = s.exportAllowed(ctx, user, view)
		if authErr != nil {
			return nil, authErr
		}
		if !allowed {
			return nil, errUnavailable
		}
		items = append(items, SyncItem{ID: "ex-" + view.id, Kind: summary.Kind,
			KindLabel: configKindLabel(r, summary.Kind), Title: summary.Title,
			MR: fmt.Sprintf("!%d", view.iid), Author: view.author, Meta: meta, Status: status})
		if len(items) > 100 {
			return nil, errResultLimit
		}
		visible = append(visible, view)
	}
	// Remote verification can take several seconds per MR. A final pass avoids
	// returning a result assembled across a grant or binding revocation.
	for _, view := range visible {
		allowed, authErr := s.exportAllowed(ctx, user, view)
		if authErr != nil {
			return nil, authErr
		}
		if !allowed {
			return nil, errUnavailable
		}
	}
	return items, nil
}

func (s *Service) importItems(r *http.Request) ([]SyncItem, error) {
	merged, err := s.Repository.ListMerged(r.Context(), 100)
	if err != nil {
		return nil, err
	}
	if len(merged) > 100 {
		return nil, errResultLimit
	}
	items := make([]SyncItem, 0, len(merged))
	for _, item := range merged {
		listed, err := s.importItem(r, item)
		if err != nil {
			return nil, err
		}
		items = append(items, listed)
	}
	return items, nil
}

// importItem verifies one merged candidate. A candidate that cannot be
// verified is listed by itself as verifying (GitLab still processing the
// merge) or unverifiable, with the reason, and logged with its step, so one
// candidate never hides the others (PA-103); nothing is read from it or
// claimed for it. The list fails only when it cannot be built: GitLab cannot
// enumerate the candidates, there are too many, or this instance's own
// ledger cannot be read.
func (s *Service) importItem(r *http.Request, item MR) (SyncItem, error) {
	sha := item.MergeCommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	listed := SyncItem{ID: fmt.Sprintf("mr-%d", item.IID), Kind: "unknown", MR: fmt.Sprintf("!%d", item.IID),
		Author: item.Author.Name, Meta: msg(r, "已合并提交 ", "Merged commit ") + sha}
	unverified := func(step string, err error) (SyncItem, error) {
		var check *MergedCheckError
		status := 0
		if errors.As(err, &check) {
			step, status = check.Step, check.Status
		}
		switch {
		case errors.Is(err, ErrMRNotPrepared):
			// Just merged: GitLab is still processing it.
			listed.Status, listed.Reason = "verifying", "not_ready"
			listed.KindLabel = msg(r, "核验中", "Verifying")
			listed.Title = msg(r, "GitLab 仍在处理这次合并，稍后可核验", "GitLab is still processing this merge; it can be verified shortly")
		case errors.Is(err, ErrGitLabUnavailable):
			listed.Status, listed.Reason = "unverifiable", "gitlab_unavailable"
			listed.KindLabel = msg(r, "无法核验", "Unverifiable")
			listed.Title = msg(r, "GitLab 暂时无法读取，这一条稍后刷新再核验", "GitLab could not be read; refresh shortly to verify this one")
		default:
			listed.Status, listed.Reason = "unverifiable", "untrusted"
			listed.KindLabel = msg(r, "无法核验", "Unverifiable")
			listed.Title = msg(r, "合并记录与签名或审核不符，不能导入", "The merge does not match its signature or review; it cannot be imported")
		}
		slog.Warn("V3 config sync merged candidate not verified", "mr_iid", item.IID, "step", step,
			"gitlab_status", status, "reason", listed.Reason)
		return listed, nil
	}
	id := strings.TrimPrefix(item.SourceBranch, s.Repository.BranchPrefix())
	if !promotionIDRE.MatchString(id) || item.SourceBranch != s.Repository.BranchPrefix()+id {
		return unverified("branch", errUntrusted)
	}
	verifiedMR, path, raw, err := s.Repository.ReadMergedManifest(r.Context(), item.IID)
	if err != nil {
		return unverified("merge_request", err)
	}
	if verifiedMR.IID != item.IID || verifiedMR.MergeCommitSHA != item.MergeCommitSHA ||
		verifiedMR.SourceBranch != item.SourceBranch || verifiedMR.SourceProjectID != s.Repository.ProjectID() ||
		verifiedMR.TargetProjectID != s.Repository.ProjectID() {
		return unverified("merge_request", errUntrusted)
	}
	summary, err := readPromotionSummary(raw, id, s.TrustedSources)
	if err != nil {
		return unverified("manifest", errUntrusted)
	}
	expectedPath, err := promotionPathForKind(summary.Kind, id)
	if err != nil || path != expectedPath {
		return unverified("manifest_path", errUntrusted)
	}
	var importSHA, importID, importKind, importTarget, importActor string
	imported := false
	err = s.DB.QueryRow(r.Context(), `SELECT merge_sha,promotion_id,kind,target_id,actor_id FROM v3_config_imports
 WHERE project_id=$1 AND mr_iid=$2`, s.Repository.ProjectID(), item.IID).
		Scan(&importSHA, &importID, &importKind, &importTarget, &importActor)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	} else if err != nil {
		return SyncItem{}, err
	} else {
		imported = true
	}
	if imported && (importSHA != item.MergeCommitSHA || importID != id || importKind != summary.Kind) {
		return unverified("import_ledger", errUntrusted)
	}
	status := "merged"
	if imported {
		if summary.Kind == "template" {
			if importTarget != "CTP-"+id {
				return unverified("imported_target", errUntrusted)
			}
			err = s.verifyImportedTemplate(r.Context(), raw, verifiedMR, importTarget, importActor)
		} else {
			err = s.verifyImportedKind(r.Context(), raw, verifiedMR, summary.Kind, importTarget, importActor)
		}
		if err != nil {
			if !errors.Is(err, errUntrusted) && !errors.Is(err, ErrUntrusted) && !errors.Is(err, errInvalidManifest) {
				return SyncItem{}, err
			}
			return unverified("imported_target", err)
		}
		status = "imported"
	}
	return SyncItem{ID: fmt.Sprintf("mr-%d", verifiedMR.IID), Kind: summary.Kind,
		KindLabel: configKindLabel(r, summary.Kind), Title: summary.Title,
		MR: fmt.Sprintf("!%d", verifiedMR.IID), Author: verifiedMR.Author.Name,
		Meta: msg(r, "已合并提交 ", "Merged commit ") + sha, Status: status}, nil
}

// The template writer commits its target, ledger, and success audit together.
// Check all three before the read API reports that production has imported it.
func (s *Service) verifyImportedTemplate(ctx context.Context, raw []byte, mr MR, targetID, actorID string) error {
	var manifest Manifest
	if decodeStrict(raw, &manifest) != nil {
		return errUntrusted
	}
	nodes, err := json.Marshal(manifest.Template.Nodes)
	if err != nil {
		return errUntrusted
	}
	edges, err := json.Marshal(manifest.Template.Edges)
	if err != nil {
		return errUntrusted
	}
	var targetExists, auditExists bool
	err = s.DB.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM v3_canvas_templates WHERE id=$1 AND kind=$2 AND name=$3
 AND nodes=$4::jsonb AND edges=$5::jsonb AND owner_id IS NULL AND environment_id IS NULL
 AND description='' AND NOT is_default),
 EXISTS(SELECT 1 FROM management_audit WHERE actor_id=$6
 AND action='v3.config_import.completed' AND target=$1 AND outcome='success'
 AND changes->>'environment_id'='prod' AND changes->>'kind'='template'
 AND changes->>'project_id'=$7 AND changes->>'mr_iid'=$8
 AND changes->>'merge_sha'=$9 AND changes->>'promotion_id'=$10)`,
		targetID, manifest.Template.Kind, manifest.Template.Name, nodes, edges, actorID,
		strconv.FormatInt(s.Repository.ProjectID(), 10), strconv.FormatInt(mr.IID, 10),
		mr.MergeCommitSHA, manifest.PromotionID).Scan(&targetExists, &auditExists)
	if err != nil {
		return err
	}
	if !targetExists || !auditExists {
		return errUntrusted
	}
	return nil
}

func configKindAccess(kind string) (module, action string, ok bool) {
	switch kind {
	case "template":
		return "canvas", "change", true
	case "runbook":
		return "knowledge", "view", true
	case "alert_rule":
		return "alert_rules", "view", true
	case "agent_version":
		return "agents", "view", true
	default:
		return "", "", false
	}
}

func configKindLabel(r *http.Request, kind string) string {
	switch kind {
	case "template":
		return msg(r, "画布模板", "Canvas template")
	case "runbook":
		return "Runbook"
	case "alert_rule":
		return msg(r, "告警规则", "Alert rule")
	case "agent_version":
		return msg(r, "Agent 版本", "Agent version")
	default:
		return ""
	}
}

func mergedItem(r *http.Request, mr MR, manifest Manifest, imported bool) SyncItem {
	status := "pending"
	if imported {
		status = "imported"
	}
	sha := mr.MergeCommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	return SyncItem{ID: fmt.Sprintf("mr-%d", mr.IID), Kind: "template", KindLabel: msg(r, "画布模板", "Canvas template"),
		Title: manifest.Template.Name, MR: fmt.Sprintf("!%d", mr.IID), Author: mr.Author.Name,
		Meta: msg(r, "已合并提交 ", "Merged commit ") + sha, Status: status}
}

type promoteInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (s *Service) promote(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFor(r, true)
	if !ok {
		problem(w, r, 409, "env_view_only", "请选择单个非生产环境", "Select one non-production environment")
		return
	}
	if scope.Tier == "production" {
		problem(w, r, 409, "already_production", "生产实例不能导出配置", "Production cannot export configuration")
		return
	}
	if !s.ready(w, r) {
		return
	}
	var input promoteInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	kind, supported := normalizePromotionKind(input.Kind)
	if !supported {
		problem(w, r, 501, "promotion_kind_unavailable", "该配置类型尚未接入提升流程", "This configuration kind is not connected for promotion")
		return
	}
	if input.ID == "" || len(input.ID) > 200 || strings.TrimSpace(input.ID) != input.ID {
		s.writeError(w, r, errInvalid)
		return
	}
	var mr MR
	var path string
	var err error
	if kind == "template" {
		mr, path, err = s.exportTemplate(r, scope.EnvironmentIDs[0], input.ID)
	} else {
		mr, path, err = s.exportKind(r, kind, scope.EnvironmentIDs[0], input.ID)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"mr": fmt.Sprintf("!%d", mr.IID), "path": path})
}

func (s *Service) importMerged(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFor(r, true)
	if !ok {
		problem(w, r, 409, "env_view_only", "请选择生产环境", "Select the production environment")
		return
	}
	if scope.Tier != "production" || scope.EnvironmentIDs[0] != "prod" {
		problem(w, r, 409, "production_only", "仅生产实例可导入已合并配置", "Only production can import merged configuration")
		return
	}
	if !s.ready(w, r) {
		return
	}
	iid, err := parseMRID(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeGlobalManager(r.Context(), user, "prod", nil); err != nil {
		s.writeError(w, r, err)
		return
	}
	// The adapter verifies review, exact path and immutable merge commit before
	// any production target is changed. The signed kind selects the importer.
	mr, raw, summary, err := s.readAnyImport(r.Context(), iid)
	if err != nil {
		var check *MergedCheckError
		if errors.As(err, &check) {
			slog.Warn("V3 config sync merged MR not verified for import", "mr_iid", iid, "step", check.Step,
				"gitlab_status", check.Status, "gitlab_unavailable", errors.Is(err, ErrGitLabUnavailable))
		}
		s.writeError(w, r, err)
		return
	}
	if err = s.authorizeCurrentListSession(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if summary.Kind == "template" {
		var manifest Manifest
		if decodeStrict(raw, &manifest) != nil || manifest.verifyTrusted(manifest.PromotionID, s.TrustedSources) != nil {
			s.writeError(w, r, errUntrusted)
			return
		}
		if err = s.saveImport(r.Context(), user, mr, manifest); err != nil {
			s.writeError(w, r, err)
			return
		}
		httpapi.WriteJSON(w, 200, mergedItem(r, mr, manifest, true))
		return
	}
	var manifest readManifestV2
	if !noDuplicateKeys(raw) || decodeStrict(raw, &manifest) != nil {
		s.writeError(w, r, errUntrusted)
		return
	}
	if _, err = manifest.verify(manifest.PromotionID, s.TrustedSources); err != nil {
		s.writeError(w, r, errUntrusted)
		return
	}
	if err = s.saveKindImport(r.Context(), user, mr, manifest); err != nil {
		s.writeError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, importedKindItem(r, mr, summary))
}

func parseMRID(id string) (int64, error) {
	if !strings.HasPrefix(id, "mr-") || len(id) > 22 {
		return 0, errInvalid
	}
	iid, err := strconv.ParseInt(strings.TrimPrefix(id, "mr-"), 10, 64)
	if err != nil || iid <= 0 {
		return 0, errInvalid
	}
	return iid, nil
}

func (s *Service) readImport(ctx context.Context, iid int64) (MR, string, Manifest, error) {
	// Resolve the MR first so the path is entirely derived from a configured
	// branch prefix and a bounded generated ID, never from URL or request input.
	candidate, err := s.Repository.Merged(ctx, iid)
	if err != nil {
		return MR{}, "", Manifest{}, err
	}
	id := strings.TrimPrefix(candidate.SourceBranch, s.Repository.BranchPrefix())
	if !promotionIDRE.MatchString(id) || candidate.SourceBranch != s.Repository.BranchPrefix()+id {
		return MR{}, "", Manifest{}, errUntrusted
	}
	path := promotionPath(id)
	mr, raw, err := s.Repository.ReadMerged(ctx, iid, path)
	if err != nil {
		return MR{}, "", Manifest{}, err
	}
	var manifest Manifest
	if err = decodeStrict(raw, &manifest); err != nil || manifest.verifyTrusted(id, s.TrustedSources) != nil {
		return MR{}, "", Manifest{}, errUntrusted
	}
	return mr, path, manifest, nil
}

func (s *Service) saveImport(ctx context.Context, user core.User, mr MR, manifest Manifest) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Match the existing role/account writer lock order while the import and
	// success audit are committed together. A stale browser permission cannot
	// authorize a production-wide template after revocation.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
	}
	// Repeat clicks for one MR must converge on the same import row.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018999)`); err != nil {
		return err
	}
	var active string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, user.ID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrForbidden
		}
		return err
	}
	if err = s.authorizeGlobalManager(ctx, user, "prod", tx); err != nil {
		return err
	}
	targetID := "CTP-" + manifest.PromotionID
	nodes, err := json.Marshal(manifest.Template.Nodes)
	if err != nil {
		return err
	}
	edges, err := json.Marshal(manifest.Template.Edges)
	if err != nil {
		return err
	}
	var existingSHA, existingPromotion, existingTarget string
	err = tx.QueryRow(ctx, `SELECT merge_sha,promotion_id,target_id FROM v3_config_imports
 WHERE project_id=$1 AND mr_iid=$2 FOR UPDATE`, s.Repository.ProjectID(), mr.IID).
		Scan(&existingSHA, &existingPromotion, &existingTarget)
	if err == nil {
		if existingSHA != mr.MergeCommitSHA || existingPromotion != manifest.PromotionID || existingTarget != targetID {
			return store.ErrConflict
		}
		var unchanged bool
		err = tx.QueryRow(ctx, `SELECT kind=$2 AND name=$3 AND nodes=$4::jsonb AND edges=$5::jsonb
 AND owner_id IS NULL AND environment_id IS NULL AND description='' AND NOT is_default
 FROM v3_canvas_templates WHERE id=$1 FOR SHARE`, targetID, manifest.Template.Kind,
			manifest.Template.Name, nodes, edges).Scan(&unchanged)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !unchanged {
			return store.ErrConflict
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// The MR idempotency path above remains valid at capacity. New system
	// templates must fit every user's list, including concurrent private saves.
	if err = v3templatecapacity.Lock(ctx, tx); err != nil {
		return err
	}
	if err = v3templatecapacity.CheckSystem(ctx, tx); err != nil {
		return err
	}
	// Imported templates are shared system definitions only after a trusted
	// GitLab merge and an explicit production administrator import. The
	// allowlisted manifest cannot carry a lower-environment owner or content.
	_, err = tx.Exec(ctx, `INSERT INTO v3_canvas_templates
 (id,owner_id,environment_id,kind,name,description,nodes,edges,is_default)
 VALUES($1,NULL,NULL,$2,$3,'',$4::jsonb,$5::jsonb,false)`, targetID,
		manifest.Template.Kind, manifest.Template.Name, nodes, edges)
	if err != nil {
		if importUniqueConflict(err, "v3_canvas_templates") {
			return store.ErrConflict
		}
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_config_imports
 (id,project_id,mr_iid,merge_sha,promotion_id,kind,target_id,actor_id)
 VALUES($1,$2,$3,$4,$5,'template',$6,$7)`, store.ID(), s.Repository.ProjectID(),
		mr.IID, mr.MergeCommitSHA, manifest.PromotionID, targetID, user.ID)
	if err != nil {
		if importUniqueConflict(err, "v3_config_imports") {
			return store.ErrConflict
		}
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.config_import.completed',$2,'success',jsonb_build_object(
 'environment_id','prod','kind','template','project_id',$3::bigint,'mr_iid',$4::bigint,
 'merge_sha',$5::text,'promotion_id',$6::text))`, user.ID, targetID,
		s.Repository.ProjectID(), mr.IID, mr.MergeCommitSHA, manifest.PromotionID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func importUniqueConflict(err error, table string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.TableName == table || strings.HasPrefix(pgErr.ConstraintName, table+"_"))
}

// Keep time imported for the export lease without relying on the remote clock.
