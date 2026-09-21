// Package profile exposes only the authenticated user's personal profile.
package profile

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"strings"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

var ErrInvalid = errors.New("invalid personal profile")
var ErrPassword = errors.New("current password is incorrect")
var ErrNoPassword = errors.New("no local password is configured")

type Service struct{ Store *store.Store }

func New(db *store.Store) *Service { return &Service{Store: db} }

type User struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	Phone     *string `json:"phone"`
	AvatarURL string  `json:"avatar_url"`
	Role      string  `json:"role"`
}
type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Application struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
}
type EffectiveRole struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Source       string `json:"source"`
	DepartmentID string `json:"department_id,omitempty"`
}
type Profile struct {
	Roles             []EffectiveRole `json:"roles"`
	User              User            `json:"user"`
	Departments       []Department    `json:"departments"`
	Permissions       map[string]bool `json:"permissions"`
	Applications      []Application   `json:"applications"`
	CanChangePassword bool            `json:"can_change_password"`
}

func (s *Service) Get(ctx context.Context) (Profile, error) {
	v := Profile{Departments: []Department{}, Applications: []Application{}, Permissions: map[string]bool{"view": false, "investigate": false, "approve": false, "execute": false, "manage": false}}
	id := store.Principal(ctx).ID
	if id == "" {
		return v, store.ErrForbidden
	}
	var avatar []byte
	var passwordHash string
	e := s.Store.DB.QueryRow(ctx, `SELECT id,name,email,phone,avatar_png,role,password_hash FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, id).Scan(&v.User.ID, &v.User.Name, &v.User.Email, &v.User.Phone, &avatar, &v.User.Role, &passwordHash)
	if e != nil {
		return v, store.ClassifyError(e)
	}
	v.CanChangePassword = validPasswordHash(passwordHash)
	if len(avatar) > 0 {
		v.User.AvatarURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(avatar)
	}
	rows, e := s.Store.DB.Query(ctx, `SELECT t.id,t.name FROM teams t JOIN team_members m ON m.team_id=t.id WHERE m.user_id=$1 ORDER BY t.name,t.id`, id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var d Department
		if e = rows.Scan(&d.ID, &d.Name); e != nil {
			rows.Close()
			return v, e
		}
		v.Departments = append(v.Departments, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	principal := core.User{ID: id, Role: v.User.Role}
	ids, e := s.Store.VisibleApplicationIDs(ctx, principal)
	if e != nil {
		return v, e
	}
	rows, e = s.Store.DB.Query(ctx, `SELECT id,key,name FROM applications WHERE id=ANY($1::text[]) ORDER BY key,id`, ids)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var a Application
		if e = rows.Scan(&a.ID, &a.Key, &a.Name); e != nil {
			rows.Close()
			return v, e
		}
		a.Permission = "view"
		v.Applications = append(v.Applications, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}

	for i := range v.Applications {
		if s.Store.AuthorizeApplication(ctx, principal, v.Applications[i].ID, "investigate") == nil {
			v.Applications[i].Permission = "operate"
		}
	}
	for _, action := range []string{"view", "investigate", "approve", "execute"} {
		for _, app := range v.Applications {
			if s.Store.AuthorizeApplication(ctx, principal, app.ID, action) == nil {
				v.Permissions[action] = true
			}
		}
	}
	var manage bool
	if e = s.Store.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions WHERE user_id=$1 AND action='manage' AND resource_id IS NOT NULL)", id).Scan(&manage); e != nil {
		return v, e
	}
	v.Permissions["manage"] = manage
	v.Roles = []EffectiveRole{}
	rows, e = s.Store.DB.Query(ctx, `SELECT r.id,r.name,'direct','' FROM rbac_roles r JOIN rbac_user_roles ur ON ur.role_id=r.id WHERE ur.user_id=$1 UNION ALL SELECT r.id,r.name,'department',u.department_id FROM rbac_roles r JOIN rbac_department_roles dr ON dr.role_id=r.id JOIN users u ON u.department_id=dr.department_id WHERE u.id=$1 ORDER BY 2,3`, id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var role EffectiveRole
		if e = rows.Scan(&role.ID, &role.Name, &role.Source, &role.DepartmentID); e != nil {
			rows.Close()
			return v, e
		}
		v.Roles = append(v.Roles, role)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return v, e
	}

	return v, nil
}

// normalizeAvatar bounds both compressed bytes and decoded pixels, and reencodes
// to PNG to discard untrusted metadata and forbid active formats such as SVG.
func normalizeAvatar(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > base64.StdEncoding.EncodedLen(1<<20)+32 {
		return nil, ErrInvalid
	}
	prefix, payload, ok := strings.Cut(value, ",")
	if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64") {
		return nil, ErrInvalid
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(payload)
	if e != nil || len(raw) > 1<<20 {
		return nil, ErrInvalid
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 || cfg.Width*cfg.Height > 1024*1024 {
		return nil, ErrInvalid
	}
	if (format == "png" && prefix != "data:image/png;base64") || (format == "jpeg" && prefix != "data:image/jpeg;base64") {
		return nil, ErrInvalid
	}
	img, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	if e = png.Encode(&out, img); e != nil {
		return nil, ErrInvalid
	}
	if out.Len() > 4<<20 {
		return nil, ErrInvalid
	}
	return out.Bytes(), nil
}
func (s *Service) SetAvatar(ctx context.Context, value string) (Profile, error) {
	avatar, e := normalizeAvatar(value)
	if e != nil {
		return Profile{}, e
	}
	id := store.Principal(ctx).ID
	if id == "" {
		return Profile{}, store.ErrForbidden
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return Profile{}, e
	}
	defer tx.Rollback(ctx)
	result, e := tx.Exec(ctx, `UPDATE users SET avatar_png=$2 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, id, avatar)
	if e != nil {
		return Profile{}, e
	}
	if result.RowsAffected() != 1 {
		return Profile{}, store.ErrNotFound
	}
	if _, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'profile.avatar.updated',$1,'success',jsonb_build_object('has_avatar',$2::boolean))`, id, len(avatar) > 0); e != nil {
		return Profile{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Profile{}, e
	}
	return s.Get(ctx)
}
func (s *Service) ChangePassword(ctx context.Context, current, next string) error {
	if current == "" || len(current) > 1024 || len(next) < 12 || len(next) > 1024 || current == next {
		return ErrInvalid
	}
	id := store.Principal(ctx).ID
	if id == "" {
		return store.ErrForbidden
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var hash string
	e = tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR UPDATE`, id).Scan(&hash)
	if e != nil {
		return store.ClassifyError(e)
	}
	if !validPasswordHash(hash) {
		return ErrNoPassword
	}
	if !store.CheckPassword(current, hash) {
		return ErrPassword
	}
	if _, e = tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, store.PasswordHash(next)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'profile.password.updated',$1,'success','{"sessions_revoked":true}'::jsonb)`, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Mirror the stored Argon2 salt/key encoding, without deriving a password or
// treating the member's directory source as an authentication provider.
func validPasswordHash(hash string) bool {
	parts := strings.Split(hash, ".")
	if len(parts) != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[0])
	if e != nil || len(salt) != 16 {
		return false
	}
	key, e := base64.RawStdEncoding.DecodeString(parts[1])
	return e == nil && len(key) == 32
}
