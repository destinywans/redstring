package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
	"strings"
)

var ErrForbidden = errors.New("application permission denied")

type principalKey struct{}
type environmentKey struct{}

func WithPrincipal(ctx context.Context, u core.User) context.Context {
	return context.WithValue(ctx, principalKey{}, u)
}
func Principal(ctx context.Context) core.User {
	u, _ := ctx.Value(principalKey{}).(core.User)
	return u
}
func WithEnvironment(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, environmentKey{}, id)
}
func EnvironmentID(ctx context.Context) string {
	id, _ := ctx.Value(environmentKey{}).(string)
	return id
}

type PlatformRepository interface {
	AuthorizeApplication(context.Context, core.User, string, string) error
	VisibleApplicationIDs(context.Context, core.User) ([]string, error)
	Application(context.Context, string) (core.Application, error)
	UpdateApplication(context.Context, core.Application) (core.Application, error)
	Environments(context.Context, string) ([]core.Environment, error)
	SaveEnvironment(context.Context, core.Environment) (core.Environment, error)
	ResourceApplication(context.Context, string, string) (string, error)
	Teams(context.Context) ([]core.Team, error)
	SaveTeam(context.Context, core.Team) (core.Team, error)
	TeamMembers(context.Context, string) ([]string, error)
	SetTeamMembers(context.Context, string, []string) error
	Grants(context.Context, string) ([]core.Grant, error)
	SetGrants(context.Context, string, []core.Grant) error
	Audit(context.Context, string, string, string, string, any) error
	AuditEntries(context.Context) ([]map[string]any, error)
}

func (s *Store) AuthorizeApplication(ctx context.Context, u core.User, app, permission string) error {
	return s.authorizeApplicationRBAC(ctx, u, app, permission)
}

func (s *Store) VisibleApplicationIDs(ctx context.Context, u core.User) ([]string, error) {
	rows, e := s.DB.Query(ctx, "SELECT id FROM applications ORDER BY id")
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := []string{}
	for _, id := range ids {
		e = s.AuthorizeApplication(ctx, u, id, "view")
		if e == nil {
			out = append(out, id)
		} else if !errors.Is(e, ErrForbidden) {
			return nil, e
		}
	}
	return out, nil
}

const appColumns = "id,name,description,environment,key,COALESCE(owner_team_id,''),COALESCE(maintainer_id,''),tags,repository_url,runbook_url,archived,profile,COALESCE((SELECT name FROM users WHERE users.id=applications.maintainer_id),''),COALESCE((SELECT name FROM teams WHERE teams.id=applications.owner_team_id),'')"

func scanApplication(row pgx.Row) (core.Application, error) {
	var a core.Application
	var tags, profile []byte
	e := row.Scan(&a.ID, &a.Name, &a.Description, &a.Environment, &a.Key, &a.OwnerTeamID, &a.MaintainerID, &tags, &a.RepositoryURL, &a.RunbookURL, &a.Archived, &profile, &a.MaintainerName, &a.OwnerTeamName)
	if e == nil {
		e = json.Unmarshal(tags, &a.Tags)
		if e == nil {
			e = json.Unmarshal(profile, &a.Profile)
		}
	}
	return a, ClassifyError(e)
}
func (s *Store) Application(ctx context.Context, id string) (core.Application, error) {
	a, e := scanApplication(s.DB.QueryRow(ctx, "SELECT "+appColumns+" FROM applications WHERE id=$1", id))
	if e == nil {
		a.Environments, e = s.Environments(ctx, id)
	}
	return a, e
}
func (s *Store) UpdateApplication(ctx context.Context, a core.Application) (core.Application, error) {
	if err := ValidateApplication(a); err != nil {
		return a, err
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	tags, _ := json.Marshal(a.Tags)
	profile, _ := json.Marshal(a.Profile)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	previous, err := scanApplication(tx.QueryRow(ctx, "SELECT "+appColumns+" FROM applications WHERE id=$1 FOR UPDATE", a.ID))
	if err != nil {
		return a, err
	}
	tag, e := tx.Exec(ctx, `UPDATE applications SET name=$2,description=$3,key=$4,owner_team_id=NULLIF($5,''),maintainer_id=NULLIF($6,''),tags=$7,repository_url=$8,runbook_url=$9,archived=$10,profile=$11 WHERE id=$1`, a.ID, a.Name, a.Description, a.Key, a.OwnerTeamID, a.MaintainerID, tags, a.RepositoryURL, a.RunbookURL, a.Archived, profile)
	if e != nil {
		return a, ClassifyError(e)
	}
	if tag.RowsAffected() != 1 {
		return a, ErrNotFound
	}
	if e = auditTx(ctx, tx, a.ID, "application.updated", a.ID, map[string]any{"before": map[string]any{"name": previous.Name, "key": previous.Key, "archived": previous.Archived, "owner_team_id": previous.OwnerTeamID, "maintainer_id": previous.MaintainerID, "tags": previous.Tags, "profile": previous.Profile}, "after": map[string]any{"name": a.Name, "key": a.Key, "archived": a.Archived, "owner_team_id": a.OwnerTeamID, "maintainer_id": a.MaintainerID, "tags": a.Tags, "profile": a.Profile}, "description_changed": previous.Description != a.Description, "repository_url_changed": previous.RepositoryURL != a.RepositoryURL, "runbook_url_changed": previous.RunbookURL != a.RunbookURL}); e != nil {
		return a, e
	}
	if e = tx.Commit(ctx); e != nil {
		return a, e
	}
	return s.Application(ctx, a.ID)
}
func (s *Store) Environments(ctx context.Context, app string) ([]core.Environment, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,application_id,key,name,archived,binding_revision,bindings,configuration,configuration_revision FROM application_environments WHERE application_id=$1 ORDER BY key", app)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Environment{}
	for rows.Next() {
		var v core.Environment
		var raw, config []byte
		if e = rows.Scan(&v.ID, &v.ApplicationID, &v.Key, &v.Name, &v.Archived, &v.BindingRevision, &raw, &config, &v.ConfigurationRevision); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Bindings); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(config, &v.Configuration); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveEnvironment(ctx context.Context, v core.Environment) (core.Environment, error) {
	if !catalogKey.MatchString(v.Key) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 240 {
		return v, ErrInvalidReference
	}
	if err := validateConfiguration(v.Configuration); err != nil {
		return v, err
	}
	if v.Bindings == nil {
		v.Bindings = []core.Binding{}
	}
	raw, _ := json.Marshal(v.Bindings)
	var previousConfiguration core.EnvironmentConfiguration
	config, _ := json.Marshal(v.Configuration)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if v.ID == "" {
		v.ID = ID()
		v.BindingRevision = 1
		v.ConfigurationRevision = 1
		if v.Configuration == nil {
			v.Configuration = &core.EnvironmentConfiguration{}
			config, _ = json.Marshal(v.Configuration)
		}
		_, e = tx.Exec(ctx, "INSERT INTO application_environments(id,application_id,key,name,archived,bindings,configuration) VALUES($1,$2,$3,$4,$5,$6,$7)", v.ID, v.ApplicationID, v.Key, v.Name, v.Archived, raw, config)
	} else {
		var oldConfig []byte
		var oldConfigRevision int64
		e = tx.QueryRow(ctx, "SELECT configuration,configuration_revision FROM application_environments WHERE id=$1 AND application_id=$2 FOR UPDATE", v.ID, v.ApplicationID).Scan(&oldConfig, &oldConfigRevision)
		if e != nil {
			return v, ClassifyError(e)
		}
		if e = json.Unmarshal(oldConfig, &previousConfiguration); e != nil {
			return v, e
		}
		if v.Configuration == nil {
			v.Configuration = &previousConfiguration
			v.ConfigurationRevision = oldConfigRevision
			config = oldConfig
		} else if v.ConfigurationRevision != oldConfigRevision {
			return v, ErrConflict
		}
		e = tx.QueryRow(ctx, `UPDATE application_environments SET key=$2,name=$3,archived=$4,bindings=$5,
   binding_revision=binding_revision+CASE WHEN key IS DISTINCT FROM $2 OR archived IS DISTINCT FROM $4 OR bindings IS DISTINCT FROM $5::jsonb THEN 1 ELSE 0 END,
   configuration=$8,configuration_revision=configuration_revision+CASE WHEN configuration IS DISTINCT FROM $8::jsonb THEN 1 ELSE 0 END
   WHERE id=$1 AND application_id=$6 AND binding_revision=$7 RETURNING binding_revision,configuration_revision`, v.ID, v.Key, v.Name, v.Archived, raw, v.ApplicationID, v.BindingRevision, config).Scan(&v.BindingRevision, &v.ConfigurationRevision)
		if errors.Is(e, pgx.ErrNoRows) {
			return v, ErrConflict
		}
	}
	if e != nil {
		return v, ClassifyError(e)
	}
	if e = auditTx(ctx, tx, v.ApplicationID, "environment.updated", v.ID, map[string]any{"binding_revision": v.BindingRevision, "archived": v.Archived, "key": v.Key, "name": v.Name, "bindings": v.Bindings, "configuration_revision": v.ConfigurationRevision, "configuration_before": previousConfiguration, "configuration_after": v.Configuration}); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func (s *Store) ResolveEnvironment(ctx context.Context, app, id string) (core.Environment, error) {
	envs, e := s.Environments(ctx, app)
	if e != nil {
		return core.Environment{}, e
	}
	var found []core.Environment
	for _, v := range envs {
		if !v.Archived && (id == "" || v.ID == id) {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		return core.Environment{}, ErrInvalidReference
	}
	return found[0], nil
}
func (s *Store) ResourceApplication(ctx context.Context, kind, id string) (string, error) {
	q := map[string]string{
		"applications":   "SELECT id FROM applications WHERE id=$1",
		"environments":   "SELECT application_id FROM application_environments WHERE id=$1",
		"investigations": "SELECT application_id FROM investigations WHERE id=$1",
		"branches":       "SELECT i.application_id FROM investigations i JOIN branches b ON b.investigation_id=i.id WHERE b.id=$1",
		"runs":           "SELECT i.application_id FROM investigations i JOIN branches b ON b.investigation_id=i.id JOIN runs r ON r.branch_id=b.id WHERE r.id=$1",
		"action-plans":   "SELECT i.application_id FROM investigations i JOIN action_plans p ON p.investigation_id=i.id WHERE p.id=$1",
		"executions":     "SELECT i.application_id FROM investigations i JOIN action_plans p ON p.investigation_id=i.id JOIN executions x ON x.plan_id=p.id WHERE x.id=$1",
	}[kind]
	if q == "" {
		return "", ErrInvalidReference
	}
	var app string
	e := s.DB.QueryRow(ctx, q, id).Scan(&app)
	return app, ClassifyError(e)
}
func (s *Store) Teams(ctx context.Context) ([]core.Team, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,name,description FROM teams ORDER BY name")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Team{}
	for rows.Next() {
		var v core.Team
		if e = rows.Scan(&v.ID, &v.Name, &v.Description); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveTeam(ctx context.Context, v core.Team) (core.Team, error) {
	if strings.TrimSpace(v.Name) == "" {
		return v, ErrInvalidReference
	}
	if v.ID == "" {
		v.ID = ID()
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "INSERT INTO teams(id,name,description) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description", v.ID, v.Name, v.Description)
	if e != nil {
		return v, ClassifyError(e)
	}
	if e = auditTx(ctx, tx, "", "team.updated", v.ID, nil); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func (s *Store) TeamMembers(ctx context.Context, id string) ([]string, error) {
	rows, e := s.DB.Query(ctx, "SELECT user_id FROM team_members WHERE team_id=$1 ORDER BY user_id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	v := []string{}
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			return nil, e
		}
		v = append(v, x)
	}
	return v, rows.Err()
}
func (s *Store) SetTeamMembers(ctx context.Context, id string, ids []string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Serialize membership changes with role-to-department grants. Checking only
	// the destination would miss a transfer away from a privileged department.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018995)"); e != nil {
		return e
	}
	var grantsAccess bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_department_roles WHERE department_id=$1 OR department_id IN (SELECT team_id FROM team_members WHERE user_id=ANY($2::text[])))`, id, ids).Scan(&grantsAccess); e != nil {
		return e
	}
	if grantsAccess {
		if e = authorizeIdentityGrant(ctx, tx); e != nil {
			return e
		}
	}
	var found string
	if e = tx.QueryRow(ctx, "SELECT id FROM teams WHERE id=$1 FOR UPDATE", id).Scan(&found); e != nil {
		return ClassifyError(e)
	}
	if _, e = tx.Exec(ctx, "DELETE FROM team_members WHERE team_id=$1", id); e != nil {
		return e
	}
	for _, uid := range ids {
		if _, e = tx.Exec(ctx, "INSERT INTO team_members(team_id,user_id) VALUES($1,$2)", id, uid); e != nil {
			return ClassifyError(e)
		}
	}
	if e = ProtectLastRBACManager(ctx, tx); e != nil {
		return e
	}
	if e = auditTx(ctx, tx, "", "team.members.updated", id, map[string]any{"user_ids": ids}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Grants(ctx context.Context, id string) ([]core.Grant, error) {
	rows, e := s.DB.Query(ctx, "SELECT subject_type,subject_id,permission FROM application_grants WHERE application_id=$1 ORDER BY subject_type,subject_id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	v := []core.Grant{}
	for rows.Next() {
		var x core.Grant
		if e = rows.Scan(&x.SubjectType, &x.SubjectID, &x.Permission); e != nil {
			return nil, e
		}
		v = append(v, x)
	}
	return v, rows.Err()
}
func (s *Store) SetGrants(ctx context.Context, id string, grants []core.Grant) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var app string
	if e = tx.QueryRow(ctx, "SELECT id FROM applications WHERE id=$1 FOR UPDATE", id).Scan(&app); e != nil {
		return ClassifyError(e)
	}
	if _, e = tx.Exec(ctx, "DELETE FROM application_grants WHERE application_id=$1", id); e != nil {
		return e
	}
	for _, g := range grants {
		table := "users"
		if g.SubjectType == "team" {
			table = "teams"
		} else if g.SubjectType != "user" {
			return ErrInvalidReference
		}
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", g.SubjectID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrInvalidReference
		}
		if _, e = tx.Exec(ctx, "INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES($1,$2,$3,$4)", id, g.SubjectType, g.SubjectID, g.Permission); e != nil {
			return ClassifyError(e)
		}
	}
	if e = auditTx(ctx, tx, id, "application.grants.updated", id, grants); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func auditTx(ctx context.Context, tx pgx.Tx, app, action, target string, changes any) error {
	raw, e := json.Marshal(changes)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,$3,$4,'success',$5)", Principal(ctx).ID, app, action, target, raw)
	return e
}
func (s *Store) Audit(ctx context.Context, app, action, target, outcome string, changes any) error {
	raw, e := json.Marshal(changes)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, "INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,$3,$4,$5,$6)", Principal(ctx).ID, app, action, target, outcome, raw)
	return e
}
func (s *Store) AuditEntries(ctx context.Context) ([]map[string]any, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,actor_id,actor_kind,application_id,action,target,outcome,changes,created_at FROM management_audit ORDER BY id DESC LIMIT 250")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		values, e := rows.Values()
		if e != nil {
			return nil, e
		}
		v := map[string]any{}
		for i, k := range []string{"id", "actor_id", "actor_kind", "application_id", "action", "target", "outcome", "changes", "created_at"} {
			v[k] = values[i]
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ActiveUser(ctx context.Context, id string) (core.User, error) {
	var u core.User
	e := s.DB.QueryRow(ctx, "SELECT u.id,u.email,u.name,"+sessionLegacyRole+",u.disabled FROM users u WHERE u.id=$1 AND NOT u.disabled", id).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Disabled)
	return u, ClassifyError(e)
}
