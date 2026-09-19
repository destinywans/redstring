package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"github.com/wans/aiops-canvas/internal/core"
	"golang.org/x/crypto/argon2"
	"strings"
)

func PasswordHash(password string) string {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		panic(e)
	}
	key := argon2.IDKey([]byte(password), salt, 2, 64*1024, 2, 32)
	return base64.RawStdEncoding.EncodeToString(salt) + "." + base64.RawStdEncoding.EncodeToString(key)
}
func CheckPassword(password, hash string) bool {
	parts := strings.Split(hash, ".")
	if len(parts) != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[0])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[1])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 2, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}
func (s *Store) Bootstrap(ctx context.Context, email, password string) error {
	var count int
	if e := s.DB.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return nil
	}
	if len(password) < 12 || email == "" {
		return fmt.Errorf("first startup requires AIOPS_ADMIN_EMAIL and AIOPS_ADMIN_PASSWORD of at least 12 characters")
	}
	_, e := s.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,'Administrator','admin',$3) ON CONFLICT(email) DO NOTHING", ID(), strings.ToLower(email), PasswordHash(password))
	return e
}
func (s *Store) Login(ctx context.Context, email, password string) (core.User, string, error) {
	var u core.User
	var hash string
	e := s.DB.QueryRow(ctx, "SELECT id,email,name,role,password_hash FROM users WHERE email=$1 AND NOT disabled", strings.ToLower(email)).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &hash)
	if e != nil || !CheckPassword(password, hash) {
		return u, "", ErrNotFound
	}
	token := ID() + ID()
	_, e = s.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '12 hours')", Hash(token), u.ID)
	return u, token, e
}
func (s *Store) Session(ctx context.Context, token string) (core.User, error) {
	var u core.User
	e := s.DB.QueryRow(ctx, "SELECT u.id,u.email,u.name,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND NOT u.disabled", Hash(token)).Scan(&u.ID, &u.Email, &u.Name, &u.Role)
	return u, e
}
func (s *Store) Applications(ctx context.Context) ([]core.Application, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,name,description,environment FROM applications ORDER BY name")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Application{}
	for rows.Next() {
		var a core.Application
		if e = rows.Scan(&a.ID, &a.Name, &a.Description, &a.Environment); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
