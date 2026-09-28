// Package v3memory governs source-backed facts without inventing model metrics.
package v3memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}
type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }
func (s *Service) ready() bool                     { return s != nil && s.DB != nil && s.Access != nil }

// Fact is a partial projection. Missing recall/confidence/expiry values are
// unknown, never implied to be zero, low or non-expiring.
type Fact struct {
	ID            string     `json:"id"`
	Fact          string     `json:"fact"`
	Source        string     `json:"source"`
	SourceVersion int        `json:"source_version"`
	SourceCurrent *bool      `json:"source_current,omitempty"`
	State         string     `json:"state"`
	EnvironmentID string     `json:"environment_id"`
	ApplicationID string     `json:"application_id,omitempty"`
	TeamID        string     `json:"team_id,omitempty"`
	Confirmed     *time.Time `json:"confirmed"`
	CreatedAt     time.Time  `json:"created_at"`
	Revision      int64      `json:"revision"`
}
type Conflict struct {
	ID            string `json:"id"`
	FactAID       string `json:"fact_a_id"`
	FactBID       string `json:"fact_b_id"`
	State         string `json:"state"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id,omitempty"`
	TeamID        string `json:"team_id,omitempty"`
}
type CandidateInput struct{ DocumentID, EnvironmentID, ExactExcerpt string }

var ErrInvalid = errors.New("invalid memory input")

func active(state string) bool { return state == "candidate" || state == "confirmed" }
func validCandidate(in CandidateInput) bool {
	return in.DocumentID != "" && len(in.DocumentID) <= 128 && strings.TrimSpace(in.DocumentID) == in.DocumentID &&
		in.EnvironmentID != "" && in.EnvironmentID != "all" && len(in.EnvironmentID) <= 128 && strings.TrimSpace(in.EnvironmentID) == in.EnvironmentID &&
		strings.TrimSpace(in.ExactExcerpt) == in.ExactExcerpt && len([]rune(in.ExactExcerpt)) > 0 && len([]rune(in.ExactExcerpt)) <= 4000
}
func transition(state, action string) (string, bool) {
	switch action {
	case "confirm":
		return "confirmed", state == "candidate"
	case "reject":
		return "rejected", state == "candidate"
	case "expire":
		return "expired", active(state)
	default:
		return "", false
	}
}
func has(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}
func decision(env, app, team, action string) rbac.V3Decision {
	return rbac.V3Decision{Module: "knowledge", Action: action, EnvironmentID: env, ApplicationID: app, TeamID: team}
}

// Both dimensions must be granted when a fact is bound to an application and team.
func (s *Service) authorize(ctx context.Context, user core.User, env, app, team, action string) error {
	if app != "" {
		if err := s.Access.Authorize(ctx, user, decision(env, app, "", action)); err != nil {
			return err
		}
	}
	if team != "" {
		if err := s.Access.Authorize(ctx, user, decision(env, "", team, action)); err != nil {
			return err
		}
	}
	if app == "" && team == "" {
		return s.Access.Authorize(ctx, user, decision(env, "", "", action))
	}
	return nil
}
func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, user core.User, env, app, team, action string) error {
	if app != "" {
		if err := s.Access.AuthorizeWithTx(ctx, tx, user, decision(env, app, "", action)); err != nil {
			return err
		}
	}
	if team != "" {
		if err := s.Access.AuthorizeWithTx(ctx, tx, user, decision(env, "", team, action)); err != nil {
			return err
		}
	}
	if app == "" && team == "" {
		return s.Access.AuthorizeWithTx(ctx, tx, user, decision(env, "", "", action))
	}
	return nil
}
func audit(ctx context.Context, tx pgx.Tx, actor, app, action, target string, changes any) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,'success',$5)`, actor, app, action, target, raw)
	return err
}

// ProposeFromDocument only accepts an exact excerpt of a published knowledge
// version with an explicit environment attribution. No model extraction is
// trusted as a fact. The caller supplies a human-selected excerpt.
func (s *Service) ProposeFromDocument(ctx context.Context, user core.User, in CandidateInput) (Fact, error) {
	if !s.ready() {
		return Fact{}, errors.New("memory unavailable")
	}
	if !validCandidate(in) {
		return Fact{}, ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback(ctx)
	var app, body, hash, lifecycle string
	var version int
	var metadata []byte
	err = tx.QueryRow(ctx, `SELECT COALESCE(application_id,''),body,version,content_hash,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR SHARE`, in.DocumentID).Scan(&app, &body, &version, &hash, &lifecycle, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return Fact{}, store.ErrNotFound
	}
	if err != nil {
		return Fact{}, err
	}
	var meta struct {
		EnvironmentIDs []string `json:"environment_ids"`
		TeamID         string   `json:"team_id"`
	}
	if err = json.Unmarshal(metadata, &meta); err != nil {
		return Fact{}, err
	}
	if lifecycle != "published" || !has(meta.EnvironmentIDs, in.EnvironmentID) || !strings.Contains(body, in.ExactExcerpt) {
		return Fact{}, ErrInvalid
	}
	if app != "" {
		var mapped bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_application_environments WHERE application_id=$1 AND environment_id=$2)`, app, in.EnvironmentID).Scan(&mapped)
		if err != nil {
			return Fact{}, err
		}
		if !mapped {
			return Fact{}, ErrInvalid
		}
	}
	if err = s.authorizeTx(ctx, tx, user, in.EnvironmentID, app, meta.TeamID, "manage"); err != nil {
		return Fact{}, err
	}
	current := true
	f := Fact{ID: store.ID(), Fact: in.ExactExcerpt, Source: in.DocumentID, SourceVersion: version, SourceCurrent: &current, State: "candidate", EnvironmentID: in.EnvironmentID, ApplicationID: app, TeamID: meta.TeamID, Revision: 1}
	err = tx.QueryRow(ctx, `INSERT INTO v3_memory_facts(id,environment_id,application_id,team_id,source_document_id,source_version,source_content_hash,fact,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING created_at`, f.ID, f.EnvironmentID, f.ApplicationID, f.TeamID, f.Source, f.SourceVersion, hash, f.Fact, user.ID).Scan(&f.CreatedAt)
	if err != nil {
		return Fact{}, err
	}
	if err = audit(ctx, tx, user.ID, app, "memory.fact.proposed", f.ID, map[string]any{"environment_id": in.EnvironmentID, "source_document_id": in.DocumentID, "source_version": version}); err != nil {
		return Fact{}, err
	}
	return f, tx.Commit(ctx)
}

// RecordConflict registers a human-asserted contradiction in one exact scope.
func (s *Service) RecordConflict(ctx context.Context, user core.User, a, b string) (Conflict, error) {
	if !s.ready() {
		return Conflict{}, errors.New("memory unavailable")
	}
	if a == "" || b == "" || a == b {
		return Conflict{}, ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Conflict{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,environment_id,application_id,team_id,state FROM v3_memory_facts
 WHERE id=ANY($1::text[]) ORDER BY id FOR UPDATE`, []string{a, b})
	if err != nil {
		return Conflict{}, err
	}
	type side struct{ id, env, app, team, state string }
	sides := []side{}
	for rows.Next() {
		var v side
		if err = rows.Scan(&v.id, &v.env, &v.app, &v.team, &v.state); err != nil {
			break
		}
		sides = append(sides, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return Conflict{}, err
	}
	if len(sides) != 2 {
		return Conflict{}, store.ErrNotFound
	}
	if sides[0].env != sides[1].env || sides[0].app != sides[1].app || sides[0].team != sides[1].team ||
		!active(sides[0].state) || !active(sides[1].state) {
		return Conflict{}, ErrInvalid
	}
	if err = s.authorizeTx(ctx, tx, user, sides[0].env, sides[0].app, sides[0].team, "manage"); err != nil {
		return Conflict{}, err
	}
	for _, id := range []string{a, b} {
		current, sourceErr := lockCurrentSource(ctx, tx, id)
		if sourceErr != nil {
			return Conflict{}, sourceErr
		}
		if !current {
			return Conflict{}, store.ErrConflict
		}
	}
	c := Conflict{ID: store.ID(), FactAID: a, FactBID: b, State: "open", EnvironmentID: sides[0].env, ApplicationID: sides[0].app, TeamID: sides[0].team}
	_, err = tx.Exec(ctx, `INSERT INTO v3_memory_conflicts(id,environment_id,application_id,team_id,fact_a_id,fact_b_id,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, c.ID, c.EnvironmentID, c.ApplicationID, c.TeamID, a, b, user.ID)
	if err != nil {
		return Conflict{}, err
	}
	if err = audit(ctx, tx, user.ID, c.ApplicationID, "memory.conflict.recorded", c.ID, map[string]any{"environment_id": c.EnvironmentID, "fact_a_id": a, "fact_b_id": b}); err != nil {
		return Conflict{}, err
	}
	return c, tx.Commit(ctx)
}

func (s *Service) Register(api *httpapi.Server) {
	routes := []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/memory", "view", s.view},
		{"GET /api/v1/memory/facts", "view", s.list},
		{"POST /api/v1/memory/facts/{id}/{action}", "manage", s.decide},
		{"POST /api/v1/memory/conflicts/resolve", "manage", s.resolve},
	}
	for _, r := range routes {
		api.Register(r.pattern, "viewer", r.handler)
		if r.pattern == "GET /api/v1/memory" {
			api.MarkV3Route(r.pattern, "knowledge", r.action)
		} else {
			api.MarkV3ObjectRoute(r.pattern, "knowledge", r.action)
		}
	}
}
