package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var errMemberChanged = errors.New("member contact revision changed")
var errMemberSourceManaged = errors.New("member contact source is externally managed")

type memberContactPatch struct {
	ExpectedRevision int64
	NameSet          bool
	Name             string
	PhoneSet         bool
	Phone            *string
}

func respondMember(w http.ResponseWriter, detail memberDetail, err error) {
	if err == nil {
		w.Header().Set("Cache-Control", "private, no-store")
		httpapi.WriteJSON(w, http.StatusOK, detail)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Member not found")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Insufficient permission")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, errMemberChanged):
		httpapi.Error(w, 409, "member_changed", "Member changed; refresh and try again")
	case errors.Is(err, errMemberSourceManaged):
		httpapi.Error(w, 409, "member_source_managed", "Contact fields are managed by the recorded identity source")
	default:
		httpapi.Error(w, 503, "member_unavailable", "Member storage unavailable")
	}
}

func (s *Service) memberDetail(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment to inspect a member")
		return
	}
	detail, err := s.readMember(r.Context(), httpapi.CurrentUser(r), roleSessionToken(r), envs[0], r.PathValue("id"))
	respondMember(w, detail, err)
}

func (s *Service) patchMember(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment to change a member")
		return
	}
	patch, err := decodeMemberContactPatch(w, r)
	if err != nil {
		httpapi.Error(w, 422, "invalid_body", "Invalid member contact patch")
		return
	}
	detail, err := s.writeMember(r.Context(), httpapi.CurrentUser(r), roleSessionToken(r), envs[0], r.PathValue("id"), patch)
	respondMember(w, detail, err)
}

// Decode a small flat object token by token so duplicate keys cannot silently
// replace a previously reviewed field or optimistic-lock revision.
func decodeMemberContactPatch(w http.ResponseWriter, r *http.Request) (memberContactPatch, error) {
	var patch memberContactPatch
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return patch, errors.New("expected object")
	}
	fields := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return patch, err
		}
		key, ok := keyToken.(string)
		if !ok || fields[key] {
			return patch, errors.New("duplicate or invalid key")
		}
		fields[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return patch, err
		}
		switch key {
		case "expected_revision":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || value == "" || value[0] == '0' {
				return patch, errors.New("invalid revision")
			}
			revision, err := strconv.ParseInt(value, 10, 64)
			if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != value {
				return patch, errors.New("invalid revision")
			}
			patch.ExpectedRevision = revision
		case "name":
			if string(raw) == "null" || json.Unmarshal(raw, &patch.Name) != nil {
				return patch, errors.New("invalid name")
			}
			patch.Name = strings.TrimSpace(patch.Name)
			if patch.Name == "" || utf8.RuneCountInString(patch.Name) > 120 {
				return patch, errors.New("invalid name")
			}
			patch.NameSet = true
		case "phone":
			patch.PhoneSet = true
			if string(raw) == "null" {
				patch.Phone = nil
				continue
			}
			var phone string
			if err := json.Unmarshal(raw, &phone); err != nil {
				return patch, errors.New("invalid phone")
			}
			phone = strings.TrimSpace(phone)
			if phone == "" || utf8.RuneCountInString(phone) > 64 {
				return patch, errors.New("invalid phone")
			}
			patch.Phone = &phone
		default:
			return patch, errors.New("unknown key")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return patch, errors.New("incomplete object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return patch, errors.New("extra JSON")
	}
	if !fields["expected_revision"] || (!patch.NameSet && !patch.PhoneSet) {
		return patch, errors.New("revision and changed field required")
	}
	return patch, nil
}

func authorizeMemberRead(ctx context.Context, tx pgx.Tx, st *store.Store, actor core.User, token, environment string) (bool, error) {
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return false, err
	}
	if err := (&rbac.V3Policy{Store: st}).AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{
		Module: "members", Action: "view", EnvironmentID: environment,
	}); err != nil {
		return false, err
	}
	var global bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
 AND module='members' AND action='view' AND scope_kind='global' AND scope_id='*')
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
 AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&global)
	return global, err
}

// memberSnapshot uses the directory's raw environment.view admission rule.
// Disabled targets remain visible; neither their inactive effective grants
// nor a legacy role rank can expand visibility. Role pairs are scoped to the
// selected environment using the same projection as the directory list.
func memberSnapshot(ctx context.Context, tx pgx.Tx, id, environment string, global bool, lock bool) (memberDetail, error) {
	detail := memberDetail{Roles: []memberRef{}, MFA: "unknown", DirectorySyncState: "unknown"}
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF u"
	}
	var departmentID, departmentName *string
	var disabled bool
	var revision int64
	var rolesJSON []byte
	err := tx.QueryRow(ctx, `SELECT u.id,u.name,u.email,t.id,t.name,u.phone,u.disabled,u.v3_identity_kind,u.source,u.v3_member_revision,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',visible.id,'name',visible.name) ORDER BY visible.name,visible.id)
 FROM (SELECT DISTINCT r.id,r.name FROM rbac_roles r
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
  SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id) assigned
 ON assigned.role_id=r.id
 WHERE assigned.user_id=u.id AND EXISTS (
  SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id AND (
   g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=$2)
   OR (g.scope_kind='application' AND EXISTS (
    SELECT 1 FROM v3_application_environments ae
    JOIN applications app ON app.id=ae.application_id AND NOT app.archived
    JOIN application_environments e ON e.application_id=app.id AND e.key=ae.environment_id AND NOT e.archived
    WHERE ae.application_id=g.scope_id AND ae.environment_id=$2))
   OR (g.scope_kind='team' AND EXISTS (
    SELECT 1 FROM applications app
    JOIN v3_application_environments ae ON ae.application_id=app.id
    JOIN application_environments e ON e.application_id=app.id AND e.key=ae.environment_id AND NOT e.archived
    WHERE app.owner_team_id=g.scope_id AND NOT app.archived AND ae.environment_id=$2))
  ))
 ) visible), '[]'::jsonb)
 FROM users u LEFT JOIN teams t ON t.id=u.department_id
 WHERE u.id=$1 AND u.deleted_at IS NULL AND u.source<>'settings_demo'
 AND (($3 AND NOT EXISTS (
  SELECT 1 FROM (
   SELECT ur.user_id,ur.role_id FROM rbac_user_roles ur
   UNION SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id
  ) assigned JOIN v3_role_grants admission ON admission.role_id=assigned.role_id
  WHERE assigned.user_id=u.id AND admission.module='environment' AND admission.action='view'
   AND admission.scope_kind IN ('global','environment')
 )) OR EXISTS (
  SELECT 1 FROM (
   SELECT ur.user_id,ur.role_id FROM rbac_user_roles ur
   UNION SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id
  ) assigned JOIN v3_role_grants admission ON admission.role_id=assigned.role_id
  WHERE assigned.user_id=u.id AND admission.module='environment' AND admission.action='view'
   AND (admission.scope_kind='global' OR (admission.scope_kind='environment' AND admission.scope_id=$2))
 ))`+lockClause, id, environment, global).Scan(
		&detail.ID, &detail.Name, &detail.Email, &departmentID, &departmentName, &detail.Phone,
		&disabled, &detail.IdentityKind, &detail.RecordedSource, &revision, &rolesJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return detail, store.ErrNotFound
	}
	if err != nil {
		return detail, err
	}
	if departmentID != nil {
		detail.Department = &memberRef{ID: *departmentID, Name: *departmentName}
	}
	if err := json.Unmarshal(rolesJSON, &detail.Roles); err != nil {
		return detail, err
	}
	detail.Status = "active"
	if disabled {
		detail.Status = "disabled"
	}
	detail.Revision = strconv.FormatInt(revision, 10)
	return detail, nil
}

func (s *Service) readMember(ctx context.Context, actor core.User, token, environment, id string) (memberDetail, error) {
	var detail memberDetail
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return detail, err
	}
	defer tx.Rollback(ctx)
	if err := memberIdentityLocks(ctx, tx, false); err != nil {
		return detail, err
	}
	global, err := authorizeMemberRead(ctx, tx, s.Store, actor, token, environment)
	if err != nil {
		return detail, err
	}
	detail, err = memberSnapshot(ctx, tx, id, environment, global, false)
	if err != nil {
		return detail, err
	}
	return detail, tx.Commit(ctx)
}

func (s *Service) writeMember(ctx context.Context, actor core.User, token, environment, id string, patch memberContactPatch) (memberDetail, error) {
	var detail memberDetail
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return detail, err
	}
	defer tx.Rollback(ctx)
	if err := memberIdentityLocks(ctx, tx, true); err != nil {
		return detail, err
	}
	global, err := authorizeMemberRead(ctx, tx, s.Store, actor, token, environment)
	if err != nil {
		return detail, err
	}
	var human, globalManage, globalEnvironmentView bool
	err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM users WHERE id=$1 AND v3_identity_kind='human'),
 EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='members' AND action='manage' AND scope_kind='global' AND scope_id='*'),
 EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&human, &globalManage, &globalEnvironmentView)
	if err != nil {
		return detail, err
	}
	if !human || !globalManage || !globalEnvironmentView {
		return detail, store.ErrForbidden
	}
	detail, err = memberSnapshot(ctx, tx, id, environment, global, true)
	if err != nil {
		return detail, err
	}
	if detail.RecordedSource != "local" {
		return detail, errMemberSourceManaged
	}
	if detail.Revision != strconv.FormatInt(patch.ExpectedRevision, 10) {
		return detail, errMemberChanged
	}
	name, phone := detail.Name, detail.Phone
	if patch.NameSet {
		name = patch.Name
	}
	if patch.PhoneSet {
		phone = patch.Phone
	}
	nameChanged := name != detail.Name
	phoneChanged := (phone == nil) != (detail.Phone == nil)
	if phone != nil && detail.Phone != nil {
		phoneChanged = *phone != *detail.Phone
	}
	if nameChanged || phoneChanged {
		if _, err := tx.Exec(ctx, `UPDATE users SET name=$2,phone=$3 WHERE id=$1`, id, name, phone); err != nil {
			return detail, err
		}
		fields := []string{}
		if nameChanged {
			fields = append(fields, "name")
		}
		if phoneChanged {
			fields = append(fields, "phone")
		}
		changes, err := json.Marshal(map[string]any{"fields": fields, "environment": environment})
		if err != nil {
			return detail, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.member.contact.updated',$2,'success',$3)`, actor.ID, id, changes); err != nil {
			return detail, err
		}
		detail, err = memberSnapshot(ctx, tx, id, environment, global, false)
		if err != nil {
			return detail, err
		}
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return detail, err
	}
	return detail, tx.Commit(ctx)
}
