package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"strings"
)

// ClassifyError translates adapter errors to backend-independent repository errors.
func ClassifyError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(e, &pe) && (pe.Code == "23503" || pe.Code == "23505" || pe.Code == "23514") {
		return ErrInvalidReference
	}
	return e
}
func (s *Store) Health(ctx context.Context) error { return s.DB.Ping(ctx) }
func (s *Store) Logout(ctx context.Context, token string) error {
	_, e := s.DB.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", Hash(token))
	return e
}
func (s *Store) Users(ctx context.Context) ([]core.User, error) {
	rows, e := s.DB.Query(ctx, "SELECT id,email,name,role,disabled FROM users ORDER BY email")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.User{}
	for rows.Next() {
		var u core.User
		if e = rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Disabled); e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) CreateUser(ctx context.Context, u core.User, password string) (core.User, error) {
	u.ID = ID()
	u.Email = strings.ToLower(u.Email)
	u.Disabled = false
	_, e := s.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,$3,$4,$5)", u.ID, u.Email, u.Name, u.Role, PasswordHash(password))
	return u, ClassifyError(e)
}
func (s *Store) RegisterApplication(ctx context.Context, a core.Application) (core.Application, error) {
	a.ID = ID()
	_, e := s.DB.Exec(ctx, "INSERT INTO applications(id,name,description,environment) VALUES($1,$2,$3,$4)", a.ID, a.Name, a.Description, a.Environment)
	return a, ClassifyError(e)
}
func (s *Store) InvestigationExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	e := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM investigations WHERE id=$1)", id).Scan(&exists)
	return exists, e
}
func (s *Store) Layout(ctx context.Context, user, inv string) (json.RawMessage, error) {
	exists, e := s.InvestigationExists(ctx, inv)
	if e != nil {
		return nil, e
	}
	if !exists {
		return nil, ErrNotFound
	}
	var b []byte
	e = s.DB.QueryRow(ctx, "SELECT layout FROM layouts WHERE user_id=$1 AND investigation_id=$2", user, inv).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return json.RawMessage("{}"), nil
	}
	return json.RawMessage(b), e
}
func (s *Store) SaveLayout(ctx context.Context, user, inv string, b json.RawMessage) error {
	_, e := s.DB.Exec(ctx, "INSERT INTO layouts(user_id,investigation_id,layout) VALUES($1,$2,$3) ON CONFLICT(user_id,investigation_id) DO UPDATE SET layout=excluded.layout", user, inv, b)
	return ClassifyError(e)
}
func (s *Store) UpdateAccount(ctx context.Context, id string, v AccountUpdate) (core.User, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.User{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018992)"); e != nil {
		return core.User{}, e
	}
	var u core.User
	e = tx.QueryRow(ctx, "SELECT id,email,name,role,disabled FROM users WHERE id=$1 FOR UPDATE", id).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Disabled)
	if e != nil {
		return u, ClassifyError(e)
	}
	oldRole, wasDisabled := u.Role, u.Disabled
	if v.Name != nil {
		u.Name = *v.Name
	}
	if v.Role != nil {
		u.Role = *v.Role
	}
	if v.Disabled != nil {
		u.Disabled = *v.Disabled
	}
	if oldRole == "admin" && !wasDisabled && (u.Role != "admin" || u.Disabled) {
		var others int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND NOT disabled AND id<>$1", u.ID).Scan(&others); e != nil {
			return u, e
		}
		if others == 0 {
			return u, ErrLastAdmin
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE users SET name=$2,role=$3,disabled=$4 WHERE id=$1", u.ID, u.Name, u.Role, u.Disabled); e != nil {
		return u, ClassifyError(e)
	}
	if v.Password != nil {
		if _, e = tx.Exec(ctx, "UPDATE users SET password_hash=$2 WHERE id=$1", u.ID, PasswordHash(*v.Password)); e != nil {
			return u, e
		}
	}
	if v.Password != nil || v.Role != nil || v.Disabled != nil {
		if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", u.ID); e != nil {
			return u, e
		}
	}
	return u, tx.Commit(ctx)
}
