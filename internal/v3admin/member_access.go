package v3admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var errAccessInvalid = errors.New("invalid access request")
var errAccessChanged = errors.New("access snapshot changed")
var errRoleExists = errors.New("role already exists with different content")
var errLoginNotReady = errors.New("member has no login credential")
var accessIDPattern = regexp.MustCompile(`^custom-[a-z0-9][a-z0-9_-]{0,56}$`)

type accessScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type createRoleInput struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Scopes      []accessScope `json:"scopes"`
	Matrix      []matrixRow   `json:"matrix"`
}
type accessRole struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	Origin         string  `json:"origin"`
	MatrixRevision int64   `json:"matrix_revision"`
	Grants         []grant `json:"grants"`
}
type memberAccess struct {
	ID           string       `json:"id"`
	Enabled      bool         `json:"enabled"`
	IdentityKind string       `json:"identity_kind"`
	Source       string       `json:"source"`
	LoginReady   bool         `json:"local_login_ready"`
	Roles        []accessRole `json:"roles"`
	Revision     string       `json:"revision"`
}
type memberAccessInput struct {
	ExpectedRevision string            `json:"expected_revision"`
	RoleIDs          []string          `json:"role_ids"`
	RoleRevisions    map[string]string `json:"role_revisions"`
	Enabled          *bool             `json:"enabled"`
}

func accessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAccessInvalid):
		httpapi.Error(w, 422, "invalid_access", "Use explicit scopes, known roles and a complete access request")
	case errors.Is(err, errAccessChanged):
		httpapi.Error(w, 409, "access_changed", "Access changed; read the current snapshot before saving")
	case errors.Is(err, errRoleExists):
		httpapi.Error(w, 409, "role_exists", "Role ID or name already belongs to a different role")
	case errors.Is(err, errLoginNotReady):
		httpapi.Error(w, 409, "login_not_ready", "Member has no login credential")
	case errors.Is(err, store.ErrLastRBACManager), errors.Is(err, store.ErrLastAdmin):
		httpapi.Error(w, 409, "last_administrator", "At least one active platform administrator with login credentials is required")
	default:
		respond(w, nil, err)
	}
}

// Parse twice: tokens reject duplicate keys at every depth; the typed decoder
// rejects unknown fields. Authorization payloads must have one interpretation.
func decodeAccess(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		return errAccessInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	var value func() error
	value = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, compound := tok.(json.Delim)
		if !compound {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errAccessInvalid
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errAccessInvalid
				}
				seen[name] = true
			}
			if err := value(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return errAccessInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return errAccessInvalid
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errAccessInvalid
	}
	return nil
}

// The selected environment is admission to this administrative operation, not
// a filter on the assignment. Global authority is required because roles may
// contain grants outside that environment. Never infer authority from rank.
func (s *Service) authorizeAccess(ctx context.Context, tx pgx.Tx, actor core.User, token, env string, members bool) error {
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return err
	}
	modules := []string{"roles"}
	if members {
		modules = append(modules, "members")
	}
	for _, module := range modules {
		if err := (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: module, Action: "manage", EnvironmentID: env}); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND v3_identity_kind='human' AND module=$2 AND action='manage' AND scope_kind='global' AND scope_id='*') AND EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID, module).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return store.ErrForbidden
		}
	}
	return nil
}

func normalizeRoleInput(in *createRoleInput, env string) ([]grant, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if !accessIDPattern.MatchString(in.ID) || in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || utf8.RuneCountInString(in.Description) > 2000 || len(in.Scopes) != 1 {
		return nil, errAccessInvalid
	}
	scope := in.Scopes[0]
	if scope.Kind == "global" {
		if scope.ID != "*" {
			return nil, errAccessInvalid
		}
	} else if scope.Kind != "environment" || scope.ID != env {
		return nil, errAccessInvalid
	}
	matrix, err := canonicalMatrix(in.Matrix)
	if err != nil {
		return nil, err
	}
	grants := []grant{{Module: "environment", Action: "view", ScopeKind: scope.Kind, ScopeID: scope.ID, Assurance: "standard"}}
	for _, entry := range matrixCatalog {
		for col, action := range matrixActions {
			if matrix[entry.Code][col] == 0 {
				continue
			}
			assurance := "standard"
			if action == "approve" {
				assurance = "passkey"
			}
			for _, module := range entry.Modules {
				grants = append(grants, grant{Module: module, Action: action, ScopeKind: scope.Kind, ScopeID: scope.ID, Assurance: assurance})
			}
		}
	}
	sort.Slice(grants, func(i, j int) bool { return grantKey(grants[i]) < grantKey(grants[j]) })
	return grants, nil
}
func grantKey(g grant) string {
	return g.Module + "\x00" + g.Action + "\x00" + g.ScopeKind + "\x00" + g.ScopeID
}

func (s *Service) createRole(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment")
		return
	}
	var in createRoleInput
	if err := decodeAccess(w, r, &in); err != nil {
		accessError(w, err)
		return
	}
	grants, err := normalizeRoleInput(&in, envs[0])
	if err != nil {
		accessError(w, err)
		return
	}
	created, revision, err := s.createRoleRecord(r.Context(), httpapi.CurrentUser(r), roleSessionToken(r), envs[0], in, grants)
	if err != nil {
		accessError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, status, map[string]any{"id": in.ID, "created": created, "kind": "business", "scopes": in.Scopes, "matrix_revision": strconv.FormatInt(revision, 10)})
}
func (s *Service) createRoleRecord(ctx context.Context, actor core.User, token, env string, in createRoleInput, grants []grant) (bool, int64, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return false, 0, err
	}
	if err = s.authorizeAccess(ctx, tx, actor, token, env, false); err != nil {
		return false, 0, err
	}
	var name, description, kind string
	var builtin bool
	var revision int64
	err = tx.QueryRow(ctx, `SELECT name,description,v3_kind,builtin,v3_matrix_revision FROM rbac_roles WHERE id=$1 FOR UPDATE`, in.ID).Scan(&name, &description, &kind, &builtin, &revision)
	if err == nil {
		existing, e := loadGrants(ctx, tx, in.ID)
		if e != nil {
			return false, 0, e
		}
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(grants)
		if builtin || kind != "business" || name != in.Name || description != in.Description || !bytes.Equal(a, b) {
			return false, 0, errRoleExists
		}
		if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
			return false, 0, err
		}
		return false, revision, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO rbac_roles(id,name,description,v3_kind,builtin) VALUES($1,$2,$3,'business',false)`, in.ID, in.Name, in.Description); err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return false, 0, errRoleExists
		}
		return false, 0, err
	}
	for _, g := range grants {
		if _, err = tx.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES($1,$2,$3,$4,$5,$6)`, in.ID, g.Module, g.Action, g.ScopeKind, g.ScopeID, g.Assurance); err != nil {
			return false, 0, err
		}
	}
	if err = auditAccess(ctx, tx, actor.ID, "v3.role.created", in.ID, map[string]any{"kind": "business", "grants": grants}); err != nil {
		return false, 0, err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return false, 0, err
	}
	return true, 1, tx.Commit(ctx)
}
func auditAccess(ctx context.Context, tx pgx.Tx, actor, action, target string, changes any) error {
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, actor, action, target, data)
	return err
}

func readAccessSnapshot(ctx context.Context, tx pgx.Tx, id string) (memberAccess, error) {
	out := memberAccess{Roles: []accessRole{}}
	err := tx.QueryRow(ctx, `SELECT id,NOT disabled,v3_identity_kind,source,password_hash<>'' FROM users WHERE id=$1 AND deleted_at IS NULL AND source<>'settings_demo' FOR UPDATE`, id).Scan(&out.ID, &out.Enabled, &out.IdentityKind, &out.Source, &out.LoginReady)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.name,r.v3_kind,a.origin,r.v3_matrix_revision FROM (
 SELECT role_id,origin FROM rbac_user_roles WHERE user_id=$1 UNION ALL
 SELECT dr.role_id,'department' FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id WHERE u.id=$1
 ) a JOIN rbac_roles r ON r.id=a.role_id ORDER BY r.id,a.origin`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var role accessRole
		if err = rows.Scan(&role.ID, &role.Name, &role.Kind, &role.Origin, &role.MatrixRevision); err != nil {
			break
		}
		out.Roles = append(out.Roles, role)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	for i := range out.Roles {
		out.Roles[i].Grants, err = loadGrants(ctx, tx, out.Roles[i].ID)
		if err != nil {
			return out, err
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	out.Revision = hex.EncodeToString(sum[:])
	return out, nil
}

func (s *Service) getMemberAccess(w http.ResponseWriter, r *http.Request) {
	s.memberAccessHTTP(w, r, false)
}
func (s *Service) putMemberAccess(w http.ResponseWriter, r *http.Request) {
	s.memberAccessHTTP(w, r, true)
}
func (s *Service) memberAccessHTTP(w http.ResponseWriter, r *http.Request, write bool) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment")
		return
	}
	var in memberAccessInput
	if write {
		if err := decodeAccess(w, r, &in); err != nil {
			accessError(w, err)
			return
		}
		if err := validateAccessInput(&in); err != nil {
			accessError(w, err)
			return
		}
	}
	out, err := s.memberAccessRecord(r.Context(), httpapi.CurrentUser(r), roleSessionToken(r), envs[0], r.PathValue("id"), in, write)
	if err != nil {
		accessError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, out)
}
func validateAccessInput(in *memberAccessInput) error {
	revision, err := hex.DecodeString(in.ExpectedRevision)
	if err != nil || len(revision) != 32 || strings.ToLower(in.ExpectedRevision) != in.ExpectedRevision || in.Enabled == nil || in.RoleIDs == nil || len(in.RoleIDs) > 64 || len(in.RoleRevisions) != len(in.RoleIDs) {
		return errAccessInvalid
	}
	seen := map[string]bool{}
	for _, id := range in.RoleIDs {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 128 || seen[id] {
			return errAccessInvalid
		}
		seen[id] = true
		revision, err := strconv.ParseInt(in.RoleRevisions[id], 10, 64)
		if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != in.RoleRevisions[id] {
			return errAccessInvalid
		}
	}
	sort.Strings(in.RoleIDs)
	return nil
}

func (s *Service) memberAccessRecord(ctx context.Context, actor core.User, token, env, id string, in memberAccessInput, write bool) (memberAccess, error) {
	var out memberAccess
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// A single exclusive order avoids read lock upgrades when the snapshot locks
	// the actor/target row, and shares the revocation boundary with legacy writers.
	if err = memberIdentityLocks(ctx, tx, true); err != nil {
		return out, err
	}
	if err = s.authorizeAccess(ctx, tx, actor, token, env, true); err != nil {
		return out, err
	}
	out, err = readAccessSnapshot(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if !write {
		if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if out.Revision != in.ExpectedRevision {
		return out, errAccessChanged
	}
	if out.IdentityKind != "human" || id == actor.ID && !*in.Enabled {
		return out, errAccessInvalid
	}
	if *in.Enabled && !out.LoginReady && out.Source != "feishu" && out.Source != "ldap" {
		return out, errLoginNotReady
	}
	before := out
	var privileged, platformAdmin bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators WHERE user_id=$1),
 EXISTS(SELECT 1 FROM (SELECT role_id FROM rbac_user_roles WHERE user_id=$2 UNION SELECT dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id WHERE u.id=$2) a WHERE a.role_id='admin' OR EXISTS(SELECT 1 FROM rbac_role_permissions p JOIN rbac_permission_resources pr ON pr.permission_id=p.permission_id WHERE p.role_id=a.role_id AND pr.action<>'view'))`, actor.ID, id).Scan(&platformAdmin, &privileged); err != nil {
		return out, err
	}
	if privileged && !platformAdmin {
		return out, store.ErrForbidden
	}
	// Direct assignments are replaced; department and legacy associations stay
	// visible and intact. Do not convert inherited roles into editable bindings.
	for _, roleID := range in.RoleIDs {
		var valid, legacy bool
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT r.v3_kind<>'machine' AND NOT EXISTS(SELECT 1 FROM rbac_user_roles WHERE user_id=$2 AND role_id=$1 AND origin='legacy'), r.v3_matrix_revision, r.id='admin' OR EXISTS(SELECT 1 FROM rbac_role_permissions p WHERE p.role_id=r.id) FROM rbac_roles r WHERE id=$1`, roleID, id).Scan(&valid, &revision, &legacy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, errAccessInvalid
			}
			return out, err
		}
		if !valid {
			return out, errAccessInvalid
		}
		if strconv.FormatInt(revision, 10) != in.RoleRevisions[roleID] {
			return out, errAccessChanged
		}
		if legacy && !platformAdmin {
			return out, store.ErrForbidden
		}
	}
	oldDirect := []string{}
	for _, role := range out.Roles {
		if role.Origin == "direct" {
			oldDirect = append(oldDirect, role.ID)
		}
	}
	sort.Strings(oldDirect)
	same := strings.Join(oldDirect, "\x00") == strings.Join(in.RoleIDs, "\x00") && out.Enabled == *in.Enabled
	if same {
		if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND origin='direct'`, id); err != nil {
		return out, err
	}
	for _, roleID := range in.RoleIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id,origin) VALUES($1,$2,'direct')`, id, roleID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET disabled=$2 WHERE id=$1`, id, !*in.Enabled); err != nil {
		return out, err
	}
	if *in.Enabled {
		var admitted bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='environment' AND action='view' AND scope_kind IN ('global','environment'))`, id).Scan(&admitted); err != nil {
			return out, err
		}
		if !admitted {
			return out, errAccessInvalid
		}
	}
	if err = store.ProtectLastRBACManager(ctx, tx); err != nil {
		return out, err
	}
	var manager bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g JOIN users u ON u.id=g.user_id WHERE g.v3_identity_kind='human' AND g.module='roles' AND g.action='manage' AND g.scope_kind='global' AND g.scope_id='*' AND u.password_hash<>'' AND EXISTS(SELECT 1 FROM v3_effective_role_grants e WHERE e.user_id=g.user_id AND e.module='environment' AND e.action='view' AND e.scope_kind='global' AND e.scope_id='*'))`).Scan(&manager); err != nil {
		return out, err
	}
	if !manager {
		return out, errLastRoleManager
	}
	out, err = readAccessSnapshot(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if err = auditAccess(ctx, tx, actor.ID, "v3.member.access.updated", id, map[string]any{"before": before, "after": out}); err != nil {
		return out, err
	}
	// Recheck the exact authorizing session before intentional session revocation,
	// including self-demotion. Rechecking after deletion would roll back the edit.
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
