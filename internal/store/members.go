package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
)

var ErrMemberInvalid = errors.New("invalid member data")
var ErrMemberPreviewStale = errors.New("member import preview is stale")
var ErrMemberImportConflict = errors.New("member import contains conflicts")

type Member struct {
	core.User
	Department string     `json:"department"`
	JobTitle   string     `json:"job_title"`
	Source     string     `json:"source"`
	ExternalID string     `json:"external_id"`
	DeletedAt  *time.Time `json:"deleted_at"`
}
type MemberCreate struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Password   string `json:"password"`
	Department string `json:"department"`
	JobTitle   string `json:"job_title"`
}
type MemberUpdate struct {
	AccountUpdate
	Email      *string `json:"email"`
	Department *string `json:"department"`
	JobTitle   *string `json:"job_title"`
}
type MemberImportRow struct {
	ExternalID string `json:"external_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Department string `json:"department"`
	JobTitle   string `json:"job_title"`
}
type MemberImportRequest struct {
	Source      string            `json:"source"`
	Rows        []MemberImportRow `json:"rows"`
	Fingerprint string            `json:"fingerprint,omitempty"`
}
type MemberImportItem struct {
	ExternalID string `json:"external_id"`
	Email      string `json:"email"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
}
type MemberImportPreview struct {
	Fingerprint string             `json:"fingerprint"`
	Items       []MemberImportItem `json:"items"`
	CanImport   bool               `json:"can_import"`
}
type MemberImportResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}
type MemberRepository interface {
	Members(context.Context, bool) ([]Member, error)
	Member(context.Context, string) (Member, error)
	CreateMember(context.Context, MemberCreate) (Member, error)
	UpdateMember(context.Context, string, MemberUpdate) (Member, error)
	DeleteMember(context.Context, string) error
	PreviewMemberImport(context.Context, MemberImportRequest) (MemberImportPreview, error)
	ImportMembers(context.Context, MemberImportRequest) (MemberImportResult, error)
}

const memberColumns = "id,email,name,role,disabled,department,job_title,source,external_id,deleted_at"

func scanMember(row pgx.Row) (Member, error) {
	var m Member
	e := row.Scan(&m.ID, &m.Email, &m.Name, &m.Role, &m.Disabled, &m.Department, &m.JobTitle, &m.Source, &m.ExternalID, &m.DeletedAt)
	return m, ClassifyError(e)
}
func (s *Store) Members(ctx context.Context, includeDeleted bool) ([]Member, error) {
	rows, e := s.DB.Query(ctx, "SELECT "+memberColumns+" FROM users WHERE ($1 OR deleted_at IS NULL) ORDER BY email", includeDeleted)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) Member(ctx context.Context, id string) (Member, error) {
	return scanMember(s.DB.QueryRow(ctx, "SELECT "+memberColumns+" FROM users WHERE id=$1", id))
}

// MemberTx reads member state on the caller's transaction connection. It is
// used by reply recovery while that transaction holds the reply row lock.
func (s *Store) MemberTx(ctx context.Context, tx pgx.Tx, id string) (Member, error) {
	if tx == nil {
		return Member{}, ErrForbidden
	}
	return scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM users WHERE id=$1", id))
}
func validMember(email, name, department, job string) bool {
	a, e := mail.ParseAddress(email)
	return e == nil && a.Address == email && len(email) <= 320 && strings.Contains(email, "@") && strings.TrimSpace(name) != "" && len(name) <= 200 && len(department) <= 200 && len(job) <= 200
}
func validRole(role string) bool { return role == "admin" || role == "operator" || role == "viewer" }

// All account mutations share the last-administrator lock, including directory imports.
func lockMembers(ctx context.Context, tx pgx.Tx) error {
	for _, key := range []int64{752018992, 752018995} {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
			return err
		}
	}
	return nil
}

// New local and directory members inherit only defaults backed by personal
// fields. The workspace week start has no per-member setting or calendar
// consumer yet, so it is intentionally not represented here.
func newMemberWorkspaceDefaults(ctx context.Context, tx pgx.Tx) (timezone, locale string, err error) {
	err = tx.QueryRow(ctx, `SELECT timezone,language FROM workspace_settings WHERE id='default' FOR SHARE`).Scan(&timezone, &locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("workspace defaults unavailable: %w", ErrNotFound)
	}
	if err != nil {
		return "", "", err
	}
	if _, e := time.LoadLocation(timezone); timezone == "" || e != nil || locale != "zh-CN" && locale != "en-US" {
		return "", "", ErrMemberInvalid
	}
	return timezone, locale, nil
}
func (s *Store) CreateMember(ctx context.Context, v MemberCreate) (Member, error) {
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	v.Name = strings.TrimSpace(v.Name)
	if !validMember(v.Email, v.Name, v.Department, v.JobTitle) || !validRole(v.Role) || len(v.Password) < 12 || len(v.Password) > 1024 {
		return Member{}, ErrMemberInvalid
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Member{}, e
	}
	defer tx.Rollback(ctx)
	if e = lockMembers(ctx, tx); e != nil {
		return Member{}, e
	}
	var grantsPrivilegedRole bool
	if e = requireBaseRole(ctx, tx, v.Role); e != nil {
		return Member{}, e
	}
	if e = tx.QueryRow(ctx, `SELECT $1<>'viewer' OR EXISTS(SELECT 1 FROM rbac_role_permissions rp JOIN rbac_permission_resources p ON p.permission_id=rp.permission_id WHERE rp.role_id=$1 AND NOT (p.action='view' AND p.scope_type='resource'))`, v.Role).Scan(&grantsPrivilegedRole); e != nil {
		return Member{}, e
	}
	if grantsPrivilegedRole {
		if e = authorizeIdentityGrant(ctx, tx); e != nil {
			return Member{}, e
		}
	}
	timezone, locale, e := newMemberWorkspaceDefaults(ctx, tx)
	if e != nil {
		return Member{}, e
	}
	id := ID()
	_, e = tx.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash,department,job_title,v3_timezone) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, v.Email, v.Name, v.Role, PasswordHash(v.Password), v.Department, v.JobTitle, timezone)
	if e != nil {
		return Member{}, ClassifyError(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,locale) VALUES($1,$2)`, id, locale); e != nil {
		return Member{}, e
	}
	if e = auditTx(ctx, tx, "", "account.created", id, map[string]string{"role": v.Role, "source": "local"}); e != nil {
		return Member{}, e
	}
	m, e := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM users WHERE id=$1", id))
	if e != nil {
		return m, e
	}
	return m, tx.Commit(ctx)
}
func (s *Store) UpdateMember(ctx context.Context, id string, v MemberUpdate) (Member, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Member{}, e
	}
	defer tx.Rollback(ctx)
	if e = lockMembers(ctx, tx); e != nil {
		return Member{}, e
	}
	m, e := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id))
	if e != nil {
		return m, e
	}
	oldRole, oldDisabled := m.Role, m.Disabled
	if v.Role != nil {
		if e = requireBaseRole(ctx, tx, *v.Role); e != nil {
			return m, e
		}
	}
	if v.Role != nil && *v.Role != m.Role {
		if e = authorizeIdentityGrant(ctx, tx); e != nil {
			return m, e
		}
	}
	if v.Password != nil || v.Email != nil || v.Disabled != nil {
		var protected bool
		if e = privilegedIdentity(ctx, tx, id).Scan(&protected); e != nil {
			return m, e
		}
		if protected {
			if e = authorizeIdentityGrant(ctx, tx); e != nil {
				return m, e
			}
		}
	}

	if v.Email != nil && m.Source != "local" && strings.ToLower(strings.TrimSpace(*v.Email)) != m.Email {
		return m, fmt.Errorf("%w: imported member emails must be changed through a directory import", ErrMemberInvalid)
	}
	if v.Name != nil {
		m.Name = strings.TrimSpace(*v.Name)
	}
	if v.Email != nil {
		m.Email = strings.ToLower(strings.TrimSpace(*v.Email))
	}
	if v.Role != nil {
		m.Role = *v.Role
	}
	if v.Disabled != nil {
		m.Disabled = *v.Disabled
	}
	if v.Department != nil {
		m.Department = *v.Department
	}
	if v.JobTitle != nil {
		m.JobTitle = *v.JobTitle
	}
	if !validMember(m.Email, m.Name, m.Department, m.JobTitle) || !validRole(m.Role) || (v.Password != nil && (len(*v.Password) < 12 || len(*v.Password) > 1024)) {
		return m, ErrMemberInvalid
	}
	if oldRole == "admin" && !oldDisabled && (m.Role != "admin" || m.Disabled) {
		if e = protectLastAdmin(ctx, tx, id); e != nil {
			return m, e
		}
	}
	var passwordHash string
	if e = tx.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1", id).Scan(&passwordHash); e != nil {
		return m, e
	}
	if v.Password != nil {
		passwordHash = PasswordHash(*v.Password)
	}
	if !m.Disabled && passwordHash == "" {
		return m, fmt.Errorf("%w: set a password before enabling this member", ErrMemberInvalid)
	}
	_, e = tx.Exec(ctx, "UPDATE users SET email=$2,name=$3,role=$4,disabled=$5,department=$6,job_title=$7,password_hash=$8 WHERE id=$1", id, m.Email, m.Name, m.Role, m.Disabled, m.Department, m.JobTitle, passwordHash)
	if e != nil {
		return m, ClassifyError(e)
	}
	if v.Password != nil || v.Role != nil || v.Disabled != nil || v.Email != nil {
		if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); e != nil {
			return m, e
		}
	}
	if v.Role != nil || v.Disabled != nil {
		if e = ProtectLastRBACManager(ctx, tx); e != nil {
			return m, e
		}
	}
	if e = auditTx(ctx, tx, "", "account.updated", id, map[string]any{"role": m.Role, "disabled": m.Disabled, "password_changed": v.Password != nil, "profile_changed": v.Name != nil || v.Email != nil || v.Department != nil || v.JobTitle != nil}); e != nil {
		return m, e
	}
	return m, tx.Commit(ctx)
}
func protectLastAdmin(ctx context.Context, tx pgx.Tx, id string) error {
	var n int
	e := tx.QueryRow(ctx, "SELECT count(*) FROM rbac_platform_administrators a JOIN users u ON u.id=a.user_id WHERE u.password_hash<>'' AND u.id<>$1", id).Scan(&n)
	if e != nil {
		return e
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}
func (s *Store) DeleteMember(ctx context.Context, id string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockMembers(ctx, tx); e != nil {
		return e
	}
	m, e := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM users WHERE id=$1 FOR UPDATE", id))
	if e != nil {
		return e
	}
	var protected bool
	if e = privilegedIdentity(ctx, tx, id).Scan(&protected); e != nil {
		return e
	}
	if protected {
		if e = authorizeIdentityGrant(ctx, tx); e != nil {
			return e
		}
	}
	if m.DeletedAt != nil {
		return nil
	}
	if m.Role == "admin" && !m.Disabled {
		if e = protectLastAdmin(ctx, tx, id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE users SET disabled=true,deleted_at=now(),password_hash='' WHERE id=$1", id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); e != nil {
		return e
	}
	if e = ProtectLastRBACManager(ctx, tx); e != nil {
		return e
	}
	if e = auditTx(ctx, tx, "", "account.deleted", id, map[string]string{"source": m.Source}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func normalizeImport(v MemberImportRequest) (MemberImportRequest, error) {
	v.Source = strings.TrimSpace(v.Source)
	if v.Source == "" || strings.EqualFold(v.Source, "local") || len(v.Source) > 100 || len(v.Rows) == 0 || len(v.Rows) > 200 {
		return v, ErrMemberInvalid
	}
	for _, c := range v.Source {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return v, ErrMemberInvalid
		}
	}
	for i := range v.Rows {
		r := &v.Rows[i]
		r.Email = strings.ToLower(strings.TrimSpace(r.Email))
		r.Name = strings.TrimSpace(r.Name)
		r.ExternalID = strings.TrimSpace(r.ExternalID)
		if r.ExternalID == "" || len(r.ExternalID) > 200 || !validMember(r.Email, r.Name, r.Department, r.JobTitle) {
			return v, fmt.Errorf("%w: row %d", ErrMemberInvalid, i+1)
		}
	}
	return v, nil
}

// Fingerprints include normalized input and the directory snapshot. Confirmation
// re-evaluates under the same lock as CRUD, so a reviewed import cannot race an edit.
func previewMembers(ctx context.Context, tx pgx.Tx, v MemberImportRequest) (MemberImportPreview, error) {
	rows, e := tx.Query(ctx, "SELECT "+memberColumns+" FROM users ORDER BY id")
	if e != nil {
		return MemberImportPreview{}, e
	}
	all := []Member{}
	byExternal := map[string]Member{}
	byEmail := map[string]Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return MemberImportPreview{}, e
		}
		all = append(all, m)
		byEmail[m.Email] = m
		if m.Source == v.Source {
			byExternal[m.ExternalID] = m
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return MemberImportPreview{}, e
	}
	input := v
	input.Fingerprint = ""
	b, _ := json.Marshal(struct {
		Input     MemberImportRequest
		Directory []Member
	}{input, all})
	digest := sha256.Sum256(b)
	out := MemberImportPreview{Fingerprint: hex.EncodeToString(digest[:]), Items: []MemberImportItem{}, CanImport: true}
	ids, emails := map[string]int{}, map[string]int{}
	for _, r := range v.Rows {
		ids[r.ExternalID]++
		emails[r.Email]++
	}
	for _, r := range v.Rows {
		item := MemberImportItem{ExternalID: r.ExternalID, Email: r.Email, Action: "create"}
		m, exists := byExternal[r.ExternalID]
		if exists {
			item.Action = "update"
		}
		switch {
		case ids[r.ExternalID] > 1 || emails[r.Email] > 1:
			item.Reason = "Duplicate external identity or email in import"
		case exists && m.DeletedAt != nil:
			item.Reason = "External identity belongs to a deleted member"
		default:
			if owner, ok := byEmail[r.Email]; ok && (!exists || owner.ID != m.ID) {
				item.Reason = "Email belongs to another identity; automatic takeover is forbidden"
			}
		}
		if item.Reason != "" {
			item.Action = "conflict"
			out.CanImport = false
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (s *Store) PreviewMemberImport(ctx context.Context, v MemberImportRequest) (MemberImportPreview, error) {
	v, e := normalizeImport(v)
	if e != nil {
		return MemberImportPreview{}, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return MemberImportPreview{}, e
	}
	defer tx.Rollback(ctx)
	if e = lockMembers(ctx, tx); e != nil {
		return MemberImportPreview{}, e
	}
	return previewMembers(ctx, tx, v)
}
func (s *Store) ImportMembers(ctx context.Context, v MemberImportRequest) (MemberImportResult, error) {
	out := MemberImportResult{}
	v, e := normalizeImport(v)
	if e != nil {
		return out, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = lockMembers(ctx, tx); e != nil {
		return out, e
	}
	p, e := previewMembers(ctx, tx, v)
	if e != nil {
		return out, e
	}
	if v.Fingerprint == "" || v.Fingerprint != p.Fingerprint {
		return out, ErrMemberPreviewStale
	}
	if !p.CanImport {
		return out, ErrMemberImportConflict
	}
	var timezone, locale string
	for i, r := range v.Rows {
		if p.Items[i].Action == "create" {
			if timezone == "" {
				timezone, locale, e = newMemberWorkspaceDefaults(ctx, tx)
				if e != nil {
					return MemberImportResult{}, e
				}
			}
			id := ID()
			_, e = tx.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash,disabled,department,job_title,source,external_id,v3_timezone) VALUES($1,$2,$3,'viewer','',true,$4,$5,$6,$7,$8)", id, r.Email, r.Name, r.Department, r.JobTitle, v.Source, r.ExternalID, timezone)
			if e == nil {
				_, e = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,locale) VALUES($1,$2)`, id, locale)
			}
			out.Created++
		} else {
			_, e = tx.Exec(ctx, "UPDATE users SET email=$3,name=$4,department=$5,job_title=$6 WHERE source=$1 AND external_id=$2 AND deleted_at IS NULL", v.Source, r.ExternalID, r.Email, r.Name, r.Department, r.JobTitle)
			out.Updated++
			if e == nil {
				_, e = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE source=$1 AND external_id=$2)", v.Source, r.ExternalID)
			}
		}
		if e != nil {
			return MemberImportResult{}, ClassifyError(e)
		}
	}
	if e = auditTx(ctx, tx, "", "account.directory_imported", v.Source, map[string]any{"source": v.Source, "created": out.Created, "updated": out.Updated, "fingerprint": p.Fingerprint}); e != nil {
		return MemberImportResult{}, e
	}
	return out, tx.Commit(ctx)
}

// Account administration does not implicitly grant the right to change access
// or take over a privileged identity by replacing its credentials.
func authorizeIdentityGrant(ctx context.Context, tx pgx.Tx) error {
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators WHERE user_id=$1)`, Principal(ctx).ID).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func privilegedIdentity(ctx context.Context, tx pgx.Tx, id string) pgx.Row {
	// Include additional grants even for disabled accounts. Built-in viewer is now
	// editable and cannot be assumed to remain read-only when protecting credentials.
	return tx.QueryRow(ctx, `SELECT role<>'viewer' OR EXISTS(SELECT 1 FROM rbac_user_roles WHERE user_id=$1 AND role_id<>'viewer') OR EXISTS(SELECT 1 FROM rbac_department_roles WHERE department_id=users.department_id) OR EXISTS(SELECT 1 FROM rbac_role_permissions rp JOIN rbac_permission_resources p ON p.permission_id=rp.permission_id WHERE NOT (p.action='view' AND p.scope_type='resource') AND (rp.role_id=users.role OR rp.role_id IN (SELECT role_id FROM rbac_user_roles WHERE user_id=$1))) FROM users WHERE id=$1`, id)
}
