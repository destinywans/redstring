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

	"github.com/wans/aiops-canvas/internal/store"
)

var ErrInvalid = errors.New("invalid personal profile")
var ErrPassword = errors.New("current password is incorrect")
var ErrPasswordRequirements = errors.New("new password does not meet requirements")
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
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
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
	IsPlatformAdmin   bool            `json:"is_platform_admin"`
}

func (s *Service) Get(ctx context.Context) (Profile, error) {
	v := Profile{Departments: []Department{}, Applications: []Application{}, Permissions: map[string]bool{"view": false, "update": false, "approve": false, "delete": false}}
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
	rows, e := s.Store.DB.Query(ctx, `SELECT t.id,t.name FROM teams t JOIN users u ON u.department_id=t.id WHERE u.id=$1 ORDER BY t.name,t.id`, id)
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
	rows, e = s.Store.DB.Query(ctx, `SELECT DISTINCT a.id,a.key,a.name FROM applications a
 JOIN cmdb_resource_applications l ON l.application_id=a.id
 JOIN rbac_effective_permissions p ON p.resource_id=l.resource_id
 WHERE p.user_id=$1 AND p.action='view' ORDER BY a.key,a.id`, id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var a Application
		if e = rows.Scan(&a.ID, &a.Key, &a.Name); e != nil {
			rows.Close()
			return v, e
		}
		v.Applications = append(v.Applications, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}

	for _, action := range []string{"view", "update", "approve", "delete"} {
		var allowed bool
		if e = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions WHERE user_id=$1 AND action=$2)`, id, action).Scan(&allowed); e != nil {
			return v, e
		}
		v.Permissions[action] = allowed
	}
	if e = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators WHERE user_id=$1)`, id).Scan(&v.IsPlatformAdmin); e != nil {
		return v, e
	}
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
	if current == "" || len(current) > 1024 || current == next {
		return ErrInvalid
	}
	if !validNewPassword(next) {
		return ErrPasswordRequirements
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
