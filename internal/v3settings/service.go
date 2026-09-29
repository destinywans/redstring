// Package v3settings serves V3 account and workspace settings from durable
// records. It never treats a UI fixture as the state of MFA or on-call systems.
package v3settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct {
	Store   *store.Store
	Runtime inference.Runner
	// ToolModels is the operator-enabled V3 read-tool allowlist. Legacy model
	// capabilities remain on Runtime and are not changed by this catalog.
	ToolModels map[string]bool
}

func New(s *store.Store, runtime inference.Runner) *Service {
	return &Service{Store: s, Runtime: runtime}
}

type Preferences struct {
	Theme        string `json:"theme"`
	Locale       string `json:"locale"`
	Density      string `json:"density"`
	ReduceMotion bool   `json:"reduce_motion"`
}

type preferencesPatch struct {
	Theme        *string `json:"theme"`
	Locale       *string `json:"locale"`
	Density      *string `json:"density"`
	ReduceMotion *bool   `json:"reduce_motion"`
}

type workspace struct {
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Timezone      string `json:"timezone"`
	Locale        string `json:"locale"`
	WeekStart     string `json:"week_start"`
	RetentionDays int    `json:"retention_days"`
	DataRegion    string `json:"data_region"`
	Brand         struct {
		Name    string  `json:"name"`
		Logo    *string `json:"logo"`
		Pending bool    `json:"pending"`
	} `json:"brand"`
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
var errInvalid = errors.New("invalid V3 settings")
var errIdentityUnavailable = errors.New("verified profile and security evidence are unavailable")
var errModelCatalogUnavailable = errors.New("model protocol cannot be represented by the V3 catalog")

func validTimezone(s string) bool { _, e := time.LoadLocation(s); return s != "" && e == nil }
func respond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		httpapi.WriteJSON(w, http.StatusOK, v)
		return
	}
	if errors.Is(err, errInvalid) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_settings", "Check the submitted settings")
		return
	}
	if errors.Is(err, errIdentityUnavailable) {
		httpapi.Error(w, http.StatusServiceUnavailable, "identity_evidence_unavailable", "Profile and security evidence are not connected")
		return
	}
	if errors.Is(err, errModelCatalogUnavailable) {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_catalog_unavailable", "A configured model protocol is not supported by the V3 catalog")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrNotFound) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Record not found")
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Permission denied")
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "settings_unavailable", "Settings storage unavailable")
}

func (s *Service) prefs(ctx context.Context, id string) (Preferences, error) {
	p := Preferences{}
	err := s.Store.DB.QueryRow(ctx, `SELECT theme,locale,density,reduce_motion FROM v3_user_settings WHERE user_id=$1`, id).Scan(&p.Theme, &p.Locale, &p.Density, &p.ReduceMotion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preferences{Theme: "system", Locale: "zh-CN", Density: "comfortable"}, nil
	}
	return p, err
}

func (s *Service) savePrefs(ctx context.Context, id string, patch preferencesPatch) (Preferences, error) {
	if patch.Theme != nil && *patch.Theme != "light" && *patch.Theme != "dark" && *patch.Theme != "system" ||
		patch.Locale != nil && *patch.Locale != "zh-CN" && *patch.Locale != "en-US" ||
		patch.Density != nil && *patch.Density != "comfortable" && *patch.Density != "compact" {
		return Preferences{}, errInvalid
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer tx.Rollback(ctx)
	// The upsert takes the user's settings row lock. COALESCE applies only
	// submitted fields, so concurrent patches to distinct fields do not erase
	// each other through a stale read-modify-write snapshot.
	p := Preferences{}
	err = tx.QueryRow(ctx, `INSERT INTO v3_user_settings(user_id,theme,locale,density,reduce_motion)
	 VALUES($1,COALESCE($2,'system'),COALESCE($3,'zh-CN'),COALESCE($4,'comfortable'),COALESCE($5,false))
	 ON CONFLICT(user_id) DO UPDATE SET theme=COALESCE($2,v3_user_settings.theme),
	 locale=COALESCE($3,v3_user_settings.locale),density=COALESCE($4,v3_user_settings.density),
	 reduce_motion=COALESCE($5,v3_user_settings.reduce_motion),updated_at=now()
	 RETURNING theme,locale,density,reduce_motion`, id, patch.Theme, patch.Locale, patch.Density, patch.ReduceMotion).Scan(&p.Theme, &p.Locale, &p.Density, &p.ReduceMotion)
	if err != nil {
		return Preferences{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.preferences.updated',$1,'success',$2)`, id, mustJSON(patch))
	if err != nil {
		return Preferences{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return p, nil
}

func decodePreferencesPatch(w http.ResponseWriter, r *http.Request) (preferencesPatch, bool) {
	var fields map[string]json.RawMessage
	if !httpapi.Decode(w, r, &fields) {
		return preferencesPatch{}, false
	}
	if fields == nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Expected a preferences object")
		return preferencesPatch{}, false
	}
	var patch preferencesPatch
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Preference values cannot be null")
			return preferencesPatch{}, false
		}
		var err error
		switch name {
		case "theme":
			err = json.Unmarshal(value, &patch.Theme)
		case "locale":
			err = json.Unmarshal(value, &patch.Locale)
		case "density":
			err = json.Unmarshal(value, &patch.Density)
		case "reduce_motion":
			err = json.Unmarshal(value, &patch.ReduceMotion)
		default:
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Unknown preference field")
			return preferencesPatch{}, false
		}
		if err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", "Invalid preference value type")
			return preferencesPatch{}, false
		}
	}
	return patch, true
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

type workspaceReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readWorkspace(ctx context.Context, q workspaceReader) (workspace, error) {
	v := workspace{}
	err := q.QueryRow(ctx, `SELECT name,v3_slug,timezone,language,v3_week_start,v3_retention_days,v3_data_region,v3_brand_name,v3_brand_logo,v3_brand_pending FROM workspace_settings WHERE id='default'`).Scan(&v.Name, &v.Slug, &v.Timezone, &v.Locale, &v.WeekStart, &v.RetentionDays, &v.DataRegion, &v.Brand.Name, &v.Brand.Logo, &v.Brand.Pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspace{}, errWorkspaceUnavailable
	}
	if err != nil {
		return workspace{}, err
	}
	if !validStoredWorkspace(v) {
		return workspace{}, errWorkspaceUnavailable
	}
	return v, nil
}

func (s *Service) getWorkspace(ctx context.Context) (workspace, error) {
	return readWorkspace(ctx, s.Store.DB)
}

func (s *Service) getAuthorizedWorkspace(ctx context.Context, user core.User, scope httpapi.V3EnvironmentScope) (workspace, error) {
	if user.ID == "" || len(scope.EnvironmentIDs) == 0 {
		return workspace{}, store.ErrForbidden
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return workspace{}, err
	}
	defer tx.Rollback(ctx)
	// Account and role writers take these locks exclusively before changing
	// authority. Shared read locks keep concurrent reads independent while a
	// queued revocation must complete before this read checks permissions.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return workspace{}, err
		}
	}
	var activeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, user.ID).Scan(&activeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workspace{}, store.ErrForbidden
		}
		return workspace{}, err
	}
	policy := &rbac.V3Policy{Store: s.Store}
	allowed := false
	for _, environmentID := range scope.EnvironmentIDs {
		err := policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "workspace", Action: "view", EnvironmentID: environmentID})
		if err == nil {
			allowed = true
			break
		}
		if !errors.Is(err, store.ErrForbidden) {
			return workspace{}, err
		}
	}
	if !allowed {
		return workspace{}, store.ErrForbidden
	}
	v, err := readWorkspace(ctx, tx)
	if err != nil {
		return workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace{}, err
	}
	return v, nil
}

func (s *Service) putWorkspace(ctx context.Context, user core.User, environmentID string, w workspace) (workspace, error) {
	if !validEditableWorkspace(w) {
		return workspace{}, errInvalid
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return workspace{}, err
	}
	defer tx.Rollback(ctx)
	// Account, department, legacy role association, and V3 matrix writers use
	// these locks. Take them in the existing account-writer order before reading
	// authority, then retain them through the workspace update and audit.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return workspace{}, err
		}
	}
	var activeID string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, user.ID).Scan(&activeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workspace{}, store.ErrForbidden
		}
		return workspace{}, err
	}
	// Workspace settings apply to the whole instance. A scoped environment
	// grant can pass the route gate, but must not mutate instance-wide state.
	// The contract's administrator is the effective platform administrator,
	// not any member delegated one V3 manage cell.
	if err = (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "workspace", Action: "manage", EnvironmentID: environmentID}); err != nil {
		return workspace{}, err
	}
	var globalManage bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
 JOIN rbac_platform_administrators a ON a.user_id=g.user_id
 WHERE g.user_id=$1 AND g.module='workspace' AND g.action='manage' AND g.scope_kind='global' AND g.scope_id='*')`, user.ID).Scan(&globalManage); err != nil {
		return workspace{}, err
	}
	if !globalManage {
		return workspace{}, store.ErrForbidden
	}
	// Read the immutable deployment fields under the same row lock as the
	// update. A concurrent branding/deployment change must not be overwritten
	// or validated against an earlier snapshot.
	current := workspace{}
	err = tx.QueryRow(ctx, `SELECT name,v3_slug,timezone,language,v3_week_start,v3_retention_days,v3_data_region,v3_brand_name,v3_brand_logo,v3_brand_pending FROM workspace_settings WHERE id='default' FOR UPDATE`).Scan(&current.Name, &current.Slug, &current.Timezone, &current.Locale, &current.WeekStart, &current.RetentionDays, &current.DataRegion, &current.Brand.Name, &current.Brand.Logo, &current.Brand.Pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspace{}, errWorkspaceUnavailable
	}
	if err != nil {
		return workspace{}, err
	}
	if !validStoredWorkspace(current) {
		return workspace{}, errWorkspaceUnavailable
	}
	// Data residency and branding assets are deployment choices, not editable text.
	if w.DataRegion != current.DataRegion || w.Brand.Name != current.Brand.Name || !equalStringPointers(w.Brand.Logo, current.Brand.Logo) || w.Brand.Pending != current.Brand.Pending {
		return workspace{}, errInvalid
	}
	var saved workspace
	err = tx.QueryRow(ctx, `UPDATE workspace_settings SET name=$1,v3_slug=$2,timezone=$3,language=$4,v3_week_start=$5,v3_retention_days=$6,revision=revision+1,updated_at=now() WHERE id='default'
 RETURNING name,v3_slug,timezone,language,v3_week_start,v3_retention_days,v3_data_region,v3_brand_name,v3_brand_logo,v3_brand_pending`, w.Name, w.Slug, w.Timezone, w.Locale, w.WeekStart, w.RetentionDays).Scan(&saved.Name, &saved.Slug, &saved.Timezone, &saved.Locale, &saved.WeekStart, &saved.RetentionDays, &saved.DataRegion, &saved.Brand.Name, &saved.Brand.Logo, &saved.Brand.Pending)
	if err != nil {
		return workspace{}, err
	}
	if !validStoredWorkspace(saved) {
		return workspace{}, errWorkspaceUnavailable
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.workspace.updated','default','success',$2)`, user.ID, mustJSON(map[string]any{"name": saved.Name, "slug": saved.Slug, "timezone": saved.Timezone, "locale": saved.Locale, "week_start": saved.WeekStart, "retention_days": saved.RetentionDays}))
	if err != nil {
		return workspace{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return workspace{}, err
	}
	return saved, nil
}

func equalStringPointers(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Register installs the account routes that have durable semantics. Callers
// register /instance separately because it is public bootstrap configuration.
func (s *Service) Register(api *httpapi.Server) {
	s.registerProfile(api)
	s.registerAI(api)
	// The current identity store has no MFA enrollment or live on-call source.
	// Both are required by Me, so these routes fail explicitly until their
	// sources are integrated. PATCH must not commit a partial update first.
	api.Register("GET /api/v1/me", "viewer", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, errIdentityUnavailable)
	})
	api.Register("PATCH /api/v1/me", "viewer", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, errIdentityUnavailable)
	})
	api.Register("PATCH /api/v1/me/preferences", "viewer", func(w http.ResponseWriter, r *http.Request) {
		p, ok := decodePreferencesPatch(w, r)
		if !ok {
			return
		}
		v, e := s.savePrefs(r.Context(), httpapi.CurrentUser(r).ID, p)
		respond(w, v, e)
	})
	api.Register("GET /api/v1/workspace", "viewer", func(w http.ResponseWriter, r *http.Request) {
		scope, ok := httpapi.V3ScopeFromContext(r.Context())
		if !ok {
			httpapi.Error(w, http.StatusServiceUnavailable, "environment_unavailable", localize(r, "工作区读取需要已授权环境", "Workspace read requires an authorized environment"))
			return
		}
		v, e := s.getAuthorizedWorkspace(r.Context(), httpapi.CurrentUser(r), scope)
		respondWorkspace(w, r, v, e)
	})
	api.Register("PUT /api/v1/workspace", "admin", func(w http.ResponseWriter, r *http.Request) {
		v, ok := decodeWorkspace(w, r)
		if !ok {
			return
		}
		scope, ok := httpapi.V3ScopeFromContext(r.Context())
		if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
			httpapi.Error(w, http.StatusServiceUnavailable, "environment_unavailable", localize(r, "工作区更新需要一个已授权环境", "Workspace write requires one authorized environment"))
			return
		}
		updated, e := s.putWorkspace(r.Context(), httpapi.CurrentUser(r), scope.EnvironmentIDs[0], v)
		respondWorkspace(w, r, updated, e)
	})
	for _, pattern := range []string{
		"GET /api/v1/me", "PATCH /api/v1/me", "PATCH /api/v1/me/preferences",
		"GET /api/v1/me/profile", "PUT /api/v1/me/profile", "DELETE /api/v1/me/sessions/{id}",
		"GET /api/v1/me/ai-preferences", "PUT /api/v1/me/ai-preferences",
		"GET /api/v1/me/notification-settings", "PUT /api/v1/me/notification-settings",
	} {
		api.MarkV3Route(pattern, "", "")
	}
	// httpapi owns the /models handler and delegates to ModelsHandler. Only its
	// V3 policy mark belongs here; registering the route again would collide.
	api.MarkV3Route("GET /api/v1/models", "models", "view")
	api.MarkV3Route("GET /api/v1/workspace", "workspace", "view")
	api.MarkV3Route("PUT /api/v1/workspace", "workspace", "manage")
}
