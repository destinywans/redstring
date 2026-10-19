package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
	"strings"
)

// ErrSessionRevoked means the request's session or user is no longer active.
var ErrSessionRevoked = errors.New("session revoked")

// LockActiveSession rechecks the current request's raw Cookie token and
// authenticated actor within tx. Call it after domain lock waits, immediately
// before the final authorization check and write or commit. It holds both the
// user and session rows until tx ends, so logout or user disable cannot commit
// concurrently. Lock the user before the session, matching account updates
// that update users before deleting their sessions. Expiry is checked at the
// session query's wall-clock instant; callers
// with long transactions must check it again before committing if needed.
func LockActiveSession(ctx context.Context, tx pgx.Tx, token, actorID string) error {
	if token == "" || actorID == "" {
		return ErrSessionRevoked
	}
	var activeID string
	err := tx.QueryRow(ctx, `SELECT id FROM users
		WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
		FOR SHARE`, actorID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT user_id FROM sessions
		WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp()
		FOR SHARE`, Hash(token), actorID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	return err
}

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

// Legacy-only modules use this rank. A deleted base role cannot retain implicit
// admin/operator privileges; explicit RBAC grants continue to be checked separately.
const sessionLegacyRole = `CASE WHEN EXISTS(SELECT 1 FROM rbac_platform_administrators a WHERE a.user_id=u.id) THEN 'admin' WHEN EXISTS(SELECT 1 FROM rbac_user_roles ur WHERE ur.user_id=u.id AND ur.role_id=u.role) THEN u.role ELSE 'viewer' END`

func (s *Store) Login(ctx context.Context, email, password string) (core.User, string, error) {
	var u core.User
	var hash string
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return u, "", e
	}
	defer tx.Rollback(ctx)
	// Serialize login with password changes: a revoked password cannot create
	// a session after the password-changing transaction has invalidated sessions.
	e = tx.QueryRow(ctx, "SELECT u.id,u.email,u.name,"+sessionLegacyRole+",u.password_hash FROM users u WHERE u.email=$1 AND NOT u.disabled AND u.deleted_at IS NULL FOR UPDATE OF u", strings.ToLower(email)).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &hash)
	if e != nil || !CheckPassword(password, hash) {
		return u, "", ErrNotFound
	}
	token := ID() + ID()
	if _, e = tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '12 hours')", Hash(token), u.ID); e != nil {
		return u, "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return u, "", e
	}
	return u, token, nil
}
func (s *Store) Session(ctx context.Context, token string) (core.User, error) {
	var u core.User
	e := s.DB.QueryRow(ctx, "SELECT u.id,u.email,u.name,"+sessionLegacyRole+" FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND NOT u.disabled AND u.deleted_at IS NULL", Hash(token)).Scan(&u.ID, &u.Email, &u.Name, &u.Role)
	return u, e
}

// SessionTx retains the ordinary identity projection on the caller's connection.
// Wall-clock expiry must remain fresh even after waits inside a transaction.
// It takes no additional row locks and never borrows the pool.
func (s *Store) SessionTx(ctx context.Context, tx pgx.Tx, token string) (core.User, error) {
	if tx == nil {
		return core.User{}, ErrSessionRevoked
	}
	var u core.User
	err := tx.QueryRow(ctx, "SELECT u.id,u.email,u.name,"+sessionLegacyRole+" FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND NOT u.disabled AND u.deleted_at IS NULL", Hash(token)).Scan(&u.ID, &u.Email, &u.Name, &u.Role)
	return u, err
}
func (s *Store) Applications(ctx context.Context) ([]core.Application, error) {
	rows, e := s.DB.Query(ctx, "SELECT "+appColumns+" FROM applications ORDER BY name")
	if e != nil {
		return nil, e
	}
	out := []core.Application{}
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for i := range out {
		out[i].Environments, e = s.Environments(ctx, out[i].ID)
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
