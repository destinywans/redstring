package settings

import (
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Store   *store.Store
	Version string
}

func New(db *store.Store, version string) *Service { return &Service{Store: db, Version: version} }

const workspaceColumns = `w.id,w.name,w.description,COALESCE(u.id,''),COALESCE(u.name,''),w.deployment_mode,w.timezone,w.language,w.revision,w.created_at,w.updated_at`

// A fresh install migrates before bootstrap creates its first administrator.
// Resolve a read-only fallback without inventing persisted ownership or dates.
const workspaceQuery = `SELECT ` + workspaceColumns + ` FROM workspace_settings w LEFT JOIN users u ON u.id=COALESCE(w.owner_id,
 (SELECT candidate.id FROM users candidate WHERE candidate.role='admin' AND NOT candidate.disabled AND candidate.deleted_at IS NULL AND candidate.source<>'settings_demo'
 ORDER BY (SELECT min(created_at) FROM management_audit WHERE action='account.created' AND target=candidate.id) NULLS LAST,candidate.id LIMIT 1)) WHERE w.id='default'`

func scanWorkspace(row pgx.Row) (Workspace, error) {
	var v Workspace
	e := row.Scan(&v.ID, &v.Name, &v.Description, &v.OwnerID, &v.OwnerName, &v.DeploymentMode, &v.Timezone, &v.Language, &v.Revision, &v.CreatedAt, &v.UpdatedAt)
	return v, store.ClassifyError(e)
}
func (s *Service) Workspace(ctx context.Context) (Workspace, error) {
	return scanWorkspace(s.Store.DB.QueryRow(ctx, workspaceQuery))
}
func (s *Service) UpdateWorkspace(ctx context.Context, p WorkspacePatch) (Workspace, error) {
	if p.Revision < 1 {
		return Workspace{}, ErrInvalid
	}
	if p.Name != nil {
		v := strings.TrimSpace(*p.Name)
		p.Name = &v
		if v == "" || utf8.RuneCountInString(v) > 120 {
			return Workspace{}, ErrInvalid
		}
	}
	if p.Description != nil && utf8.RuneCountInString(*p.Description) > 200 {
		return Workspace{}, ErrInvalid
	}
	if p.Timezone != nil {
		if *p.Timezone == "" {
			return Workspace{}, ErrInvalid
		}
		if _, e := time.LoadLocation(*p.Timezone); e != nil {
			return Workspace{}, ErrInvalid
		}
	}
	if p.Language != nil && *p.Language != "zh-CN" {
		return Workspace{}, ErrInvalid
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return Workspace{}, e
	}
	defer tx.Rollback(ctx)
	var v Workspace
	e = tx.QueryRow(ctx, `UPDATE workspace_settings SET name=COALESCE($1,name),description=COALESCE($2,description),timezone=COALESCE($3,timezone),language=COALESCE($4,language),revision=revision+1,updated_at=now() WHERE id='default' AND revision=$5 RETURNING id,name,description,COALESCE(owner_id,''),deployment_mode,timezone,language,revision,created_at,updated_at`, p.Name, p.Description, p.Timezone, p.Language, p.Revision).Scan(&v.ID, &v.Name, &v.Description, &v.OwnerID, &v.DeploymentMode, &v.Timezone, &v.Language, &v.Revision, &v.CreatedAt, &v.UpdatedAt)
	if e == pgx.ErrNoRows {
		return v, store.ErrConflict
	}
	if e != nil {
		return v, e
	}
	if v, e = scanWorkspace(tx.QueryRow(ctx, workspaceQuery)); e != nil {
		return v, e
	}
	if e = audit(ctx, tx, "workspace.updated", "default", map[string]any{"revision": v.Revision, "name": v.Name, "description": v.Description, "timezone": v.Timezone, "language": v.Language}); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func audit(ctx context.Context, tx pgx.Tx, action, target string, changes any) error {
	raw, e := json.Marshal(changes)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, store.Principal(ctx).ID, action, target, raw)
	return e
}
func (s *Service) UpdateTeamProfile(ctx context.Context, id string, p TeamProfilePatch) (TeamProfile, error) {
	if p.OwnerID == nil || p.Department == nil {
		return TeamProfile{}, ErrInvalid
	}
	department := strings.TrimSpace(*p.Department)
	if utf8.RuneCountInString(department) > 200 {
		return TeamProfile{}, ErrInvalid
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return TeamProfile{}, e
	}
	defer tx.Rollback(ctx)
	var found string
	if e = tx.QueryRow(ctx, `SELECT id FROM teams WHERE id=$1 FOR UPDATE`, id).Scan(&found); e != nil {
		return TeamProfile{}, store.ClassifyError(e)
	}
	if *p.OwnerID != "" {
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN team_members m ON m.user_id=u.id WHERE m.team_id=$1 AND u.id=$2 AND u.deleted_at IS NULL)`, id, *p.OwnerID).Scan(&valid); e != nil {
			return TeamProfile{}, e
		}
		if !valid {
			return TeamProfile{}, ErrInvalid
		}
	}
	var v TeamProfile
	e = tx.QueryRow(ctx, `INSERT INTO settings_team_profiles(team_id,owner_id,department) VALUES($1,NULLIF($2,''),$3) ON CONFLICT(team_id) DO UPDATE SET owner_id=excluded.owner_id,department=excluded.department,updated_at=now() RETURNING COALESCE(owner_id,''),department,is_mock,created_at,updated_at`, id, *p.OwnerID, department).Scan(&v.OwnerID, &v.Department, &v.IsMock, &v.CreatedAt, &v.UpdatedAt)
	if e != nil {
		return v, store.ClassifyError(e)
	}
	if e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT name FROM users WHERE id=$1),'')`, v.OwnerID).Scan(&v.OwnerName); e != nil {
		return v, e
	}
	if e = audit(ctx, tx, "team.profile.updated", id, p); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}

func (s *Service) Directory(ctx context.Context) (Directory, error) {
	d := Directory{Members: []Member{}, Teams: []Team{}, Roles: []Role{}, Departments: []Department{}, Applications: []Application{}, Grants: []Grant{}, MockNotice: "Mock 为示例成员与团队，示例成员无法登录。"}
	roleRows, err := s.Store.DB.Query(ctx, `SELECT r.id,r.name,r.description,COALESCE((SELECT jsonb_object_agg(p.action,true) FROM rbac_role_permissions rp JOIN rbac_permission_resources p ON p.permission_id=rp.permission_id WHERE rp.role_id=r.id),'{}'),(SELECT count(*) FROM rbac_user_roles ur JOIN users u ON u.id=ur.user_id WHERE ur.role_id=r.id AND u.deleted_at IS NULL) FROM rbac_roles r ORDER BY r.name,r.id`)
	if err != nil {
		return d, err
	}
	for roleRows.Next() {
		var role Role
		var raw []byte
		if err = roleRows.Scan(&role.ID, &role.Name, &role.Description, &raw, &role.MemberCount); err != nil {
			roleRows.Close()
			return d, err
		}
		if err = json.Unmarshal(raw, &role.Permissions); err != nil {
			roleRows.Close()
			return d, err
		}
		role.Scope = "以权限关联的对象范围为准"
		d.Roles = append(d.Roles, role)
	}
	roleRows.Close()
	if err = roleRows.Err(); err != nil {
		return d, err
	}
	members, e := s.Store.Members(ctx, true)
	if e != nil {
		return d, e
	}
	for _, m := range members {
		d.Members = append(d.Members, Member{Member: m, IsMock: m.Source == "settings_demo", TeamIDs: []string{}})
	}
	rows, e := s.Store.DB.Query(ctx, `SELECT u.id,CASE WHEN u.source='settings_demo' AND u.id LIKE 'settings-demo-%'
 THEN (SELECT min(created_at) FROM management_audit WHERE action='settings.demo.seeded' AND target='settings_demo')
 ELSE (SELECT min(created_at) FROM management_audit WHERE action='account.created' AND target=u.id) END FROM users u`)
	if e != nil {
		return d, e
	}
	createdDates := map[string]*time.Time{}
	for rows.Next() {
		var id string
		var created *time.Time
		if e = rows.Scan(&id, &created); e != nil {
			rows.Close()
			return d, e
		}
		createdDates[id] = created
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return d, e
	}
	for i := range d.Members {
		d.Members[i].CreatedAt = createdDates[d.Members[i].ID]
	}
	rows, e = s.Store.DB.Query(ctx, `SELECT t.id,t.name,t.description,COALESCE(u.id,''),COALESCE(u.name,''),COALESCE(p.department,''),COALESCE(p.is_mock,false),p.created_at,p.updated_at FROM teams t LEFT JOIN settings_team_profiles p ON p.team_id=t.id LEFT JOIN users u ON u.id=p.owner_id AND u.deleted_at IS NULL AND EXISTS(SELECT 1 FROM team_members m WHERE m.team_id=t.id AND m.user_id=u.id) ORDER BY t.name`)
	if e != nil {
		return d, e
	}
	for rows.Next() {
		var t Team
		t.MemberIDs = []string{}
		t.Applications = []TeamApplication{}
		if e = rows.Scan(&t.ID, &t.Name, &t.Description, &t.OwnerID, &t.OwnerName, &t.Department, &t.IsMock, &t.CreatedAt, &t.UpdatedAt); e != nil {
			rows.Close()
			return d, e
		}
		d.Teams = append(d.Teams, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return d, e
	}
	rows, e = s.Store.DB.Query(ctx, `SELECT m.team_id,m.user_id FROM team_members m JOIN users u ON u.id=m.user_id AND u.deleted_at IS NULL ORDER BY m.team_id,m.user_id`)
	if e != nil {
		return d, e
	}
	for rows.Next() {
		var tid, uid string
		if e = rows.Scan(&tid, &uid); e != nil {
			rows.Close()
			return d, e
		}
		for i := range d.Teams {
			if d.Teams[i].ID == tid {
				d.Teams[i].MemberIDs = append(d.Teams[i].MemberIDs, uid)
			}
		}
		for i := range d.Members {
			if d.Members[i].ID == uid {
				d.Members[i].TeamIDs = append(d.Members[i].TeamIDs, tid)
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return d, e
	}
	rows, e = s.Store.DB.Query(ctx, `SELECT id,name,key,COALESCE(owner_team_id,'') FROM applications ORDER BY name`)
	if e != nil {
		return d, e
	}
	for rows.Next() {
		var a Application
		var owner string
		if e = rows.Scan(&a.ID, &a.Name, &a.Key, &owner); e != nil {
			rows.Close()
			return d, e
		}
		d.Applications = append(d.Applications, a)
		for i := range d.Teams {
			if d.Teams[i].ID == owner {
				d.Teams[i].Applications = append(d.Teams[i].Applications, TeamApplication{Application: a, Relationship: "owner"})
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return d, e
	}
	rows, e = s.Store.DB.Query(ctx, `SELECT application_id,subject_type,subject_id,permission FROM application_grants ORDER BY application_id,subject_type,subject_id`)
	if e != nil {
		return d, e
	}
	for rows.Next() {
		var g Grant
		if e = rows.Scan(&g.ApplicationID, &g.SubjectType, &g.SubjectID, &g.Permission); e != nil {
			rows.Close()
			return d, e
		}
		d.Grants = append(d.Grants, g)
		if g.SubjectType == "team" {
			for i := range d.Teams {
				if d.Teams[i].ID != g.SubjectID {
					continue
				}
				existing := false
				for j := range d.Teams[i].Applications {
					if d.Teams[i].Applications[j].ID == g.ApplicationID {
						d.Teams[i].Applications[j].Permission = g.Permission
						d.Teams[i].Applications[j].Relationship = "owner_and_grant"
						existing = true
					}
				}
				if !existing {
					for _, a := range d.Applications {
						if a.ID == g.ApplicationID {
							d.Teams[i].Applications = append(d.Teams[i].Applications, TeamApplication{Application: a, Permission: g.Permission, Relationship: "grant"})
						}
					}
				}
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return d, e
	}
	deps := map[string]*Department{}
	department := func(name string) *Department {
		if deps[name] == nil {
			deps[name] = &Department{Name: name}
		}
		return deps[name]
	}
	for _, m := range d.Members {
		if m.DeletedAt != nil {
			continue
		}
		if m.Department != "" {
			department(m.Department).MemberCount++
		}

	}
	for _, t := range d.Teams {
		if t.Department != "" {
			department(t.Department).TeamCount++
		}
	}
	for _, v := range deps {
		d.Departments = append(d.Departments, *v)
	}
	sort.Slice(d.Departments, func(i, j int) bool { return d.Departments[i].Name < d.Departments[j].Name })
	return d, nil
}
