// Package v3configsync promotes reviewed configuration through a dedicated
// GitLab repository. Its first supported object is the sanitized V3 canvas
// template; other object kinds fail explicitly until they have safe contracts.
package v3configsync

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
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
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Repository interface {
	ProjectID() int64
	BranchPrefix() string
	Export(context.Context, string, string, []byte, string) (MR, error)
	ListMerged(context.Context, int) ([]MR, error)
	Merged(context.Context, int64) (MR, error)
	ReadMerged(context.Context, int64, string) (MR, []byte, error)
}

type Service struct {
	DB         *pgxpool.Pool
	Access     Access
	Repository Repository
	// Secret material is supplied by the composition root (for example a
	// Vault-mounted file). Production receives public keys only. An unsigned
	// or untrusted manifest cannot become a production template.
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
		{"POST /api/v1/promotions", "canvas", "change", s.promote},
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
		}
	} else {
		items, err = s.exportItems(r, scope.EnvironmentIDs)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"direction": direction, "items": items})
}

var (
	errResultLimit = errors.New("configuration list exceeds limit")
	errInvalid     = errors.New("invalid configuration promotion request")
	errUntrusted   = errors.New("configuration source is untrusted")
	errUnavailable = errors.New("configuration source unavailable")
	errBusy        = errors.New("configuration export already in progress")
)

func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		problem(w, r, 403, "forbidden", "没有配置提升权限", "Configuration promotion denied")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		problem(w, r, 404, "not_found", "配置项不存在", "Configuration item not found")
	case errors.Is(err, errInvalid):
		problem(w, r, 422, "invalid_promotion", "配置提升请求无效", "Invalid configuration promotion")
	case errors.Is(err, errResultLimit), errors.Is(err, ErrListTooLarge):
		problem(w, r, 413, "scope_too_large", "配置项超出列表上限", "Configuration list exceeds its limit")
	case errors.Is(err, errBusy):
		problem(w, r, 409, "promotion_in_progress", "配置正在导出，请稍后重试", "Configuration export is in progress")
	case errors.Is(err, store.ErrConflict), errors.Is(err, ErrConflict):
		problem(w, r, 409, "promotion_conflict", "配置来源或目标已变化", "Configuration source or target has changed")
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

func (s *Service) exportItems(r *http.Request, envs []string) ([]SyncItem, error) {
	ctx, user := r.Context(), httpapi.CurrentUser(r)
	items := []SyncItem{}
	rows, err := s.DB.Query(ctx, `SELECT e.id,e.environment_id,e.mr_iid,e.payload,COALESCE(u.name,'')
 FROM v3_config_exports e
 LEFT JOIN users u ON u.id=e.actor_id
 WHERE e.actor_id=$1 AND e.environment_id=ANY($2::text[]) AND e.kind='template'
 AND e.project_id=$3 AND e.state='exported'
 ORDER BY e.created_at DESC,e.id DESC LIMIT 101`, user.ID, envs, s.Repository.ProjectID())
	if err != nil {
		return nil, err
	}
	type exportView struct {
		id, env, author string
		iid             int64
		payload         []byte
	}
	candidates := []exportView{}
	for rows.Next() {
		var view exportView
		if err = rows.Scan(&view.id, &view.env, &view.iid, &view.payload, &view.author); err != nil {
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
	if len(candidates) > 100 {
		return nil, errResultLimit
	}
	for _, view := range candidates {
		if err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "canvas", Action: "change", EnvironmentID: view.env}); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			return nil, err
		}
		var manifest Manifest
		if decodeStrict(view.payload, &manifest) != nil || manifest.validate(view.id) != nil {
			return nil, errUntrusted
		}
		items = append(items, SyncItem{ID: "ex-" + view.id, Kind: "template", KindLabel: msg(r, "画布模板", "Canvas template"),
			Title: manifest.Template.Name, MR: fmt.Sprintf("!%d", view.iid), Author: view.author,
			Meta: msg(r, "来源环境：", "Source environment: ") + view.env, Status: "exported"})
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
		id := strings.TrimPrefix(item.SourceBranch, s.Repository.BranchPrefix())
		if !promotionIDRE.MatchString(id) || item.SourceBranch != s.Repository.BranchPrefix()+id {
			return nil, errUntrusted
		}
		var imported bool
		err = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_config_imports
 WHERE project_id=$1 AND mr_iid=$2 AND merge_sha=$3 AND promotion_id=$4)`,
			s.Repository.ProjectID(), item.IID, item.MergeCommitSHA, id).Scan(&imported)
		if err != nil {
			return nil, err
		}
		items = append(items, candidateItem(r, item, imported))
	}
	return items, nil
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

// A merged MR is a review candidate in the list. The full diff, file bytes
// and signed source assertion are checked only when the user imports it.
func candidateItem(r *http.Request, mr MR, imported bool) SyncItem {
	status := "pending"
	if imported {
		status = "imported"
	}
	sha := mr.MergeCommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	return SyncItem{ID: fmt.Sprintf("mr-%d", mr.IID), Kind: "template", KindLabel: msg(r, "画布模板", "Canvas template"),
		Title: mr.Title, MR: fmt.Sprintf("!%d", mr.IID), Author: mr.Author.Name,
		Meta: msg(r, "已合并候选 · 导入时验证 ", "Merged candidate · verify on import ") + sha, Status: status}
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
	if input.Kind != "template" && input.Kind != "templates" {
		problem(w, r, 501, "promotion_kind_unavailable", "该配置类型尚未接入提升流程", "This configuration kind is not connected for promotion")
		return
	}
	if input.ID == "" || len(input.ID) > 200 || strings.TrimSpace(input.ID) != input.ID {
		s.writeError(w, r, errInvalid)
		return
	}
	mr, path, err := s.exportTemplate(r, scope.EnvironmentIDs[0], input.ID)
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
	// The branch name, changed file path and merge commit are checked by the
	// repository adapter. Never fetch from the moving target branch HEAD.
	mr, path, manifest, err := s.readImport(r.Context(), iid)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	_ = path
	if err = s.saveImport(r.Context(), user, mr, manifest); err != nil {
		s.writeError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, 200, mergedItem(r, mr, manifest, true))
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
