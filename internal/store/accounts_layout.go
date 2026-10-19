package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	members, err := s.Members(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]core.User, 0, len(members))
	for _, member := range members {
		out = append(out, member.User)
	}
	return out, nil
}
func (s *Store) CreateUser(ctx context.Context, u core.User, password string) (core.User, error) {
	member, err := s.CreateMember(ctx, MemberCreate{Email: u.Email, Name: u.Name, Role: u.Role, Password: password})
	return member.User, err
}
func (s *Store) RegisterApplication(ctx context.Context, a core.Application) (core.Application, error) {
	a.ID = ID()
	if a.Key == "" {
		a.Key = a.ID
	}
	if a.Environment == "" {
		a.Environment = "development"
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if e := ValidateApplication(a); e != nil {
		return a, e
	}
	profile, _ := json.Marshal(a.Profile)
	tags, _ := json.Marshal(a.Tags)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "INSERT INTO applications(id,name,description,environment,key,owner_team_id,maintainer_id,tags,repository_url,runbook_url,profile) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11)", a.ID, a.Name, a.Description, a.Environment, a.Key, a.OwnerTeamID, a.MaintainerID, tags, a.RepositoryURL, a.RunbookURL, profile)
	if e != nil {
		return a, ClassifyError(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO application_environments(id,application_id,key,name) VALUES($1,$2,$3,$3)", a.ID+"-default", a.ID, a.Environment)
	if e != nil {
		return a, e
	}
	if e = auditTx(ctx, tx, a.ID, "application.created", a.ID, map[string]string{"key": a.Key}); e != nil {
		return a, e
	}
	if e = tx.Commit(ctx); e != nil {
		return a, e
	}
	return s.Application(ctx, a.ID)
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
	member, err := s.UpdateMember(ctx, id, MemberUpdate{AccountUpdate: v})
	return member.User, err
}
