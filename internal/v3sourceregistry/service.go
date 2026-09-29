// Package v3sourceregistry stores scoped knowledge source configuration.
// Registration is not connection validation, ingestion, or ACL enforcement.
package v3sourceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

type Service struct {
	DB      *pgxpool.Pool
	Access  Access
	Readers map[string]Reader
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

// NewWithReaders enables only explicitly configured source-specific readers.
// The default constructor deliberately leaves external reads unavailable.
func NewWithReaders(db *pgxpool.Pool, access Access, readers map[string]Reader) *Service {
	configured := make(map[string]Reader, len(readers))
	for kind, reader := range readers {
		configured[kind] = reader
	}
	return &Service{DB: db, Access: access, Readers: configured}
}

// Auxiliary routes deliberately avoid the complete V3 KnowledgeSource DTO.
// The opt-in public Web reader persists one scoped Markdown page for V3
// keyword retrieval; scheduling, other source importers and vector indexing
// remain unavailable.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
		action  string
	}{
		{"GET /api/v1/knowledge-sources/registrations", s.list, "view"},
		{"POST /api/v1/knowledge-sources/registrations", s.create, "manage"},
		{"POST /api/v1/knowledge-sources/test", s.testConnection, "manage"},
		{"POST /api/v1/knowledge-sources/registrations/{id}/sync", s.sync, "manage"},
		{"GET /api/v1/knowledge-sources/registrations/{id}/evidence", s.evidence, "view"},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, "knowledge", route.action)
	}
}

type request struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	URL           string   `json:"url"`
	SecretRef     string   `json:"secret_ref"`
	Spaces        []string `json:"spaces"`
	Schedule      string   `json:"schedule"`
	Chunk         string   `json:"chunk"`
	InheritACL    *bool    `json:"inherit_acl"`
	ApplicationID string   `json:"application_id"`
	TeamID        string   `json:"team_id"`
}

type connectionRequest struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	SecretRef string `json:"secret_ref"`
}

var vaultReference = regexp.MustCompile(`^vault:[A-Za-z0-9_-]+(?:/[A-Za-z0-9_.-]+)+$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validateConnection(kind, address, secretRef string) error {
	switch kind {
	case "confluence", "notion", "feishu", "git", "web", "upload":
	default:
		return errors.New("unsupported source type")
	}
	if len(address) > 2000 || strings.TrimSpace(address) != address {
		return errors.New("invalid source URL")
	}
	if kind == "web" && secretRef == "" {
		if _, _, _, err := ParsePublicWebURL(address); err != nil {
			return err
		}
		return nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("source URL must be HTTPS without credentials, query or fragment")
	}
	// Other source types, including private Web endpoints, still require an
	// opaque Vault ref.
	if len(secretRef) > 300 || !vaultReference.MatchString(secretRef) {
		return errors.New("secret_ref must be a Vault reference")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(secretRef, "vault:"), "/") {
		if segment == "." || segment == ".." {
			return errors.New("invalid Vault path")
		}
	}
	return nil
}

func validateCreate(v request) error {
	if err := validateConnection(v.Type, v.URL, v.SecretRef); err != nil {
		return err
	}
	if v.Name == "" || strings.TrimSpace(v.Name) != v.Name || len([]rune(v.Name)) > 160 {
		return errors.New("invalid source name")
	}
	if v.InheritACL == nil {
		return errors.New("inherit_acl is required")
	}
	if v.Schedule != "6h" && v.Schedule != "24h" && v.Schedule != "manual" {
		return errors.New("invalid schedule")
	}
	if v.Chunk != "heading" && v.Chunk != "fixed" {
		return errors.New("invalid chunk strategy")
	}
	if len(v.Spaces) > 100 {
		return errors.New("too many spaces")
	}
	if v.Type == "web" && v.SecretRef == "" &&
		(len(v.Spaces) != 0 || v.Schedule != "manual" || !*v.InheritACL) {
		return errors.New("public Web Markdown requires one page, manual sync and inherited source access")
	}
	seen := map[string]bool{}
	for _, space := range v.Spaces {
		if !identifier.MatchString(space) || seen[space] {
			return errors.New("invalid or repeated space")
		}
		seen[space] = true
	}
	if (v.ApplicationID != "" && !identifier.MatchString(v.ApplicationID)) ||
		(v.TeamID != "" && !identifier.MatchString(v.TeamID)) {
		return errors.New("invalid application or team")
	}
	if v.TeamID != "" && v.ApplicationID == "" {
		return errors.New("team scope requires an application")
	}
	return nil
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request, write bool) (core.User, []string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "source_registry_unavailable", "Source registry or policy is unavailable")
		return core.User{}, nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return core.User{}, nil, false
	}
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment for a source registration")
		return core.User{}, nil, false
	}
	return user, scope.EnvironmentIDs, true
}

func (s *Service) authorized(ctx context.Context, user core.User, action, env, app, team string) error {
	if err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: action,
		EnvironmentID: env, ApplicationID: app}); err != nil {
		return err
	}
	if team != "" {
		return s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: action,
			EnvironmentID: env, TeamID: team})
	}
	return nil
}

func (s *Service) authorizedTx(ctx context.Context, tx pgx.Tx, user core.User, action, env, app, team string) error {
	if err := s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "knowledge", Action: action,
		EnvironmentID: env, ApplicationID: app}); err != nil {
		return err
	}
	if team != "" {
		return s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "knowledge", Action: action,
			EnvironmentID: env, TeamID: team})
	}
	return nil
}

func authorizationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Knowledge permission required")
	} else {
		httpapi.Error(w, 503, "authorization_unavailable", "Knowledge authorization is unavailable")
	}
}

// Test fails closed until a source-specific read-only prober is installed.
// The result is a point-in-time probe, not a persisted sync or index status.
func (s *Service) testConnection(w http.ResponseWriter, r *http.Request) {
	user, envs, ok := s.ready(w, r, true)
	if !ok {
		return
	}
	if err := s.authorized(r.Context(), user, "manage", envs[0], "", ""); err != nil {
		authorizationError(w, err)
		return
	}
	var v connectionRequest
	if !httpapi.Decode(w, r, &v) {
		return
	}
	if err := validateConnection(v.Type, v.URL, v.SecretRef); err != nil {
		httpapi.Error(w, 422, "invalid_source_connection", err.Error())
		return
	}
	prober, ok := s.Readers[v.Type].(ConnectionTester)
	if !ok {
		httpapi.Error(w, 501, "source_connection_test_unavailable", "No read-only connection prober is configured for this source type")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := prober.Probe(ctx, ReadSpec{SourceType: v.Type, URL: v.URL, SecretRef: v.SecretRef, EnvironmentID: envs[0]}); err != nil {
		httpapi.Error(w, 503, "source_connection_failed", "Read-only source connection failed")
		return
	}
	if err := s.authorized(ctx, user, "manage", envs[0], "", ""); err != nil {
		authorizationError(w, err)
		return
	}
	result := map[string]any{"ok": true, "message": "Read-only source access succeeded",
		"connected": true, "observed_at": time.Now().UTC(),
		"persistent": false, "scope_verified": false}
	if v.Type == "web" && v.SecretRef == "" {
		result["spaces"] = []any{}
	}
	httpapi.WriteJSON(w, 200, result)
}

type item struct {
	ID                 string    `json:"id"`
	EnvironmentID      string    `json:"environment_id"`
	ApplicationID      string    `json:"application_id,omitempty"`
	TeamID             string    `json:"team_id,omitempty"`
	Name               string    `json:"name"`
	Type               string    `json:"type"`
	URL                string    `json:"url"`
	Spaces             []string  `json:"spaces"`
	Schedule           string    `json:"schedule"`
	Chunk              string    `json:"chunk"`
	InheritACL         bool      `json:"inherit_acl"`
	RegistrationStatus string    `json:"registration_status"`
	CreatedAt          time.Time `json:"created_at"`
}

const registrationCandidates = `SELECT id,environment_id,COALESCE(application_id,''),COALESCE(team_id,''),
 name,source_type,source_url,spaces,schedule,chunk,inherit_acl,created_at
 FROM v3_knowledge_source_registrations src WHERE environment_id=ANY($1::text[])
 AND (application_id IS NULL OR EXISTS (
  SELECT 1 FROM applications a
  JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=src.environment_id
  JOIN application_environments e ON e.application_id=a.id AND e.key=src.environment_id AND NOT e.archived
  WHERE a.id=src.application_id AND NOT a.archived
   AND (src.team_id IS NULL OR a.owner_team_id=src.team_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='environment' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)
     OR (src.application_id IS NOT NULL AND g.scope_kind='application' AND g.scope_id=src.application_id)))
 AND (src.team_id IS NULL OR EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)
     OR (g.scope_kind='team' AND g.scope_id=src.team_id))))
 ORDER BY created_at DESC,id LIMIT 200 OFFSET $3`

type candidate struct {
	item
	spaces []byte
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	user, envs, ok := s.ready(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "source_registry_unavailable", "Source registry is unavailable")
		return
	}
	defer tx.Rollback(ctx)
	items := []item{}
	truncated := false
	// The candidate cap bounds a hostile or unexpectedly broad scope. Hidden
	// registrations do not consume the response limit or signal truncation.
	for offset := 0; offset <= 10000; offset += 200 {
		rows, queryErr := tx.Query(ctx, registrationCandidates, envs, user.ID, offset)
		if queryErr != nil {
			httpapi.Error(w, 503, "source_registry_unavailable", "Source registry is unavailable")
			return
		}
		batch := make([]candidate, 0, 200)
		for rows.Next() {
			var v candidate
			if queryErr = rows.Scan(&v.ID, &v.EnvironmentID, &v.ApplicationID, &v.TeamID,
				&v.Name, &v.Type, &v.URL, &v.spaces, &v.Schedule, &v.Chunk, &v.InheritACL, &v.CreatedAt); queryErr != nil {
				break
			}
			batch = append(batch, v)
		}
		if queryErr == nil {
			queryErr = rows.Err()
		}
		rows.Close()
		if queryErr != nil {
			httpapi.Error(w, 503, "source_registry_unavailable", "Source registry is unavailable")
			return
		}
		if offset == 10000 && len(batch) > 0 {
			httpapi.Error(w, 422, "source_registry_scope_too_broad", "Narrow the source registry scope")
			return
		}
		for _, v := range batch {
			if err := s.authorizedTx(ctx, tx, user, "view", v.EnvironmentID, v.ApplicationID, v.TeamID); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				authorizationError(w, err)
				return
			}
			if err := json.Unmarshal(v.spaces, &v.Spaces); err != nil {
				httpapi.Error(w, 503, "source_registry_unavailable", "Source registry is inconsistent")
				return
			}
			v.RegistrationStatus = "unverified"
			if len(items) == 200 {
				truncated = true
				break
			}
			items = append(items, v.item)
		}
		if truncated || len(batch) < 200 {
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "source_registry_unavailable", "Source registry is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "index": nil,
		"complete_v3_dto": false, "truncated": truncated,
		"unavailable_fields": []string{"connection_status", "sync_status", "document_count", "chunk_count", "last_sync", "index_settings"}})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	user, envs, ok := s.ready(w, r, true)
	if !ok {
		return
	}
	var v request
	if !httpapi.Decode(w, r, &v) {
		return
	}
	if err := validateCreate(v); err != nil {
		httpapi.Error(w, 422, "invalid_source_registration", err.Error())
		return
	}
	env := envs[0]
	spaces, _ := json.Marshal(v.Spaces)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if v.ApplicationID != "" {
		var activeID string
		err = tx.QueryRow(r.Context(), `SELECT a.id FROM applications a
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
	 WHERE a.id=$1 AND NOT a.archived AND ($3='' OR a.owner_team_id=$3)
 FOR SHARE OF a,ae,e`, v.ApplicationID, env, v.TeamID).Scan(&activeID)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 422, "invalid_source_scope", "Application/team is not active in the selected environment")
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
	}
	if err := s.authorizedTx(r.Context(), tx, user, "manage", env, v.ApplicationID, v.TeamID); err != nil {
		authorizationError(w, err)
		return
	}
	id := uuid.NewString()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,team_id,name,source_type,source_url,secret_ref,
  spaces,schedule,chunk,inherit_acl,created_by)
 VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, env, v.ApplicationID, v.TeamID, v.Name, v.Type, v.URL, v.SecretRef,
		spaces, v.Schedule, v.Chunk, *v.InheritACL, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.knowledge_source.registered',$3,'success',
 jsonb_build_object('environment_id',$4::text,'source_type',$5::text,'team_id',$6::text))`,
		user.ID, v.ApplicationID, id, env, v.Type, v.TeamID)
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Source registration audit is unavailable")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id, "registration_status": "unverified"})
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "source_registry_unavailable", "Source registration could not be saved")
}
