package v3memory

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
)

func memorySingleEnvironment(w http.ResponseWriter, r *http.Request) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if ok && (scope.All || len(scope.EnvironmentIDs) > 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment")
		return "", false
	}
	env, ok := productionEnvironment(r)
	if !ok {
		failure(w, ErrInvalid)
	}
	return env, ok
}

// An edit preserves exact document/version/hash ownership. The new text must
// still be verbatim evidence; arbitrary assertions cannot bypass production.
func (s *Service) editFact(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := memorySingleEnvironment(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision int64  `json:"expected_revision"`
		Fact     string `json:"fact"`
	}
	if err := decodeProduction(w, r, &in); err != nil || in.Revision < 1 || !validCandidate(CandidateInput{"source", env, in.Fact}) {
		failure(w, ErrInvalid)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	f, err := scanFact(tx.QueryRow(r.Context(), factSelect+` f LEFT JOIN knowledge_documents d ON d.id=f.source_document_id WHERE f.id=$1 FOR UPDATE OF f`, r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && f.EnvironmentID != env {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	bound, err := lockCurrentBinding(r.Context(), tx, f.ApplicationID, f.TeamID, f.EnvironmentID)
	if err != nil {
		unavailable(w)
		return
	}
	if err = productionAuthority(r, tx); err == nil {
		for _, action := range []string{"view", "manage"} {
			err = s.authorizeTx(r.Context(), tx, httpapi.CurrentUser(r), env, f.ApplicationID, f.TeamID, action)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		failure(w, err)
		return
	}
	if !bound || !active(f.State) || f.Revision != in.Revision {
		failure(w, store.ErrConflict)
		return
	}
	eligible, policyErr := factWithinPolicy(r.Context(), tx, f.ID)
	if policyErr != nil {
		unavailable(w)
		return
	}
	if !eligible {
		failure(w, store.ErrConflict)
		return
	}
	current, err := lockCurrentSource(r.Context(), tx, f.ID)
	if err != nil {
		unavailable(w)
		return
	}
	if !current {
		failure(w, store.ErrConflict)
		return
	}
	var present bool
	var sourceHash, sourceBody string
	err = tx.QueryRow(r.Context(), `SELECT position($2 in body)>0,content_hash,body FROM knowledge_documents WHERE id=$1 FOR SHARE`, f.Source, in.Fact).Scan(&present, &sourceHash, &sourceBody)
	if err != nil {
		unavailable(w)
		return
	}
	if sourceHash != store.Hash(sourceBody) {
		failure(w, store.ErrConflict)
		return
	}
	if !present {
		failure(w, ErrInvalid)
		return
	}
	var hasConflict bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_memory_conflicts WHERE state='open' AND (fact_a_id=$1 OR fact_b_id=$1))`, f.ID).Scan(&hasConflict); err != nil {
		unavailable(w)
		return
	}
	if hasConflict {
		failure(w, store.ErrConflict)
		return
	}
	// Keep the semantic key bound to the edited excerpt. A retry of this exact
	// current source excerpt returns this fact; the original excerpt may be
	// proposed again as a distinct candidate after its text was replaced.
	nextKey := productionKey(env, f.ApplicationID, f.TeamID, productionInput{DocumentID: f.Source, ExpectedVersion: f.SourceVersion, ExpectedContentHash: sourceHash, ExactExcerpt: in.Fact})
	_, err = tx.Exec(r.Context(), `UPDATE v3_memory_facts SET fact=$2,state='candidate',revision=revision+1,confirmed_by=NULL,confirmed_at=NULL,decided_by=NULL,decided_at=NULL,production_key=$3 WHERE id=$1`, f.ID, in.Fact, nextKey)
	var constraintErr *pgconn.PgError
	if errors.As(err, &constraintErr) && constraintErr.Code == "23505" {
		failure(w, store.ErrConflict)
		return
	}
	if err == nil {
		err = audit(r.Context(), tx, httpapi.CurrentUser(r).ID, f.ApplicationID, "memory.fact.edited", f.ID, map[string]any{"environment_id": env, "source_document_id": f.Source, "source_version": f.SourceVersion, "from_revision": f.Revision, "to_revision": f.Revision + 1})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	f.Fact = in.Fact
	f.State = "candidate"
	f.Revision++
	f.Confirmed = nil
	f.SourceCurrent = &current
	httpapi.WriteJSON(w, 200, f)
}

type memoryPolicy struct {
	TTLDays   int    `json:"ttl_days"`
	Proposers string `json:"proposers"`
	Revision  int64  `json:"revision"`
}

func validPolicy(p memoryPolicy) bool {
	return (p.TTLDays == 30 || p.TTLDays == 90 || p.TTLDays == 180) && (p.Proposers == "human" || p.Proposers == "human_ai")
}
func (s *Service) policy(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := memorySingleEnvironment(w, r)
	if !ok {
		return
	}
	write := r.Method == http.MethodPut
	var in struct {
		Revision  int64  `json:"expected_revision"`
		TTLDays   int    `json:"ttl_days"`
		Proposers string `json:"proposers"`
	}
	if write {
		if err := decodeProduction(w, r, &in); err != nil || in.Revision < 0 || !validPolicy(memoryPolicy{TTLDays: in.TTLDays, Proposers: in.Proposers}) {
			failure(w, ErrInvalid)
			return
		}
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	// Advisory lock also serializes first-write CAS when no policy row exists.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "v3-memory-policy:"+env); err != nil {
		unavailable(w)
		return
	}
	if err = productionAuthority(r, tx); err != nil {
		failure(w, err)
		return
	}
	action := "view"
	if write {
		action = "manage"
	}
	if err = s.authorizeTx(r.Context(), tx, httpapi.CurrentUser(r), env, "", "", action); err != nil {
		failure(w, err)
		return
	}
	p := memoryPolicy{TTLDays: 90, Proposers: "human", Revision: 0}
	err = tx.QueryRow(r.Context(), `SELECT ttl_days,proposers,revision FROM v3_memory_policies WHERE environment_id=$1 FOR UPDATE`, env).Scan(&p.TTLDays, &p.Proposers, &p.Revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		unavailable(w)
		return
	}
	if write {
		if p.Revision != in.Revision {
			failure(w, store.ErrConflict)
			return
		}
		p = memoryPolicy{TTLDays: in.TTLDays, Proposers: in.Proposers, Revision: p.Revision + 1}
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_memory_policies(environment_id,ttl_days,proposers,revision,updated_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(environment_id) DO UPDATE SET ttl_days=EXCLUDED.ttl_days,proposers=EXCLUDED.proposers,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()`, env, p.TTLDays, p.Proposers, p.Revision, httpapi.CurrentUser(r).ID)
		if err == nil {
			err = audit(r.Context(), tx, httpapi.CurrentUser(r).ID, "", "memory.policy.updated", env, map[string]any{"environment_id": env, "revision": p.Revision, "ttl_days": p.TTLDays, "proposers": p.Proposers, "ai_production_available": false})
		}
		if err != nil {
			unavailable(w)
			return
		}
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	effect := "unconfigured_no_expiry"
	if p.Revision > 0 {
		effect = "ttl_enforced_on_read_and_confirmation"
	}
	httpapi.WriteJSON(w, 200, map[string]any{"configured": p.Revision > 0, "ttl_days": p.TTLDays, "proposers": p.Proposers, "revision": p.Revision, "automatic_expiry_available": true, "ai_production_available": false, "policy_effect": effect})
}
func memoryFacets(r *http.Request) ([]string, []string, bool) {
	out := [][]string{}
	for _, key := range []string{"source", "confidence"} {
		vs, err := v3pagequery.Values(r.URL.Query(), key)
		if err != nil {
			return nil, nil, false
		}
		for _, v := range vs {
			if len(v) > 128 {
				return nil, nil, false
			}
			if key == "confidence" && v != "high" && v != "medium" && v != "low" && v != "unknown" {
				return nil, nil, false
			}
		}
		out = append(out, vs)
	}
	return out[0], out[1], true
}

// TTL is derived from durable original timestamps, including historical facts.
// Confirmed records start at confirmed_at; candidates start at created_at.
// A policy never rewrites history or silently refreshes an old fact's lifetime.
func factWithinPolicy(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var eligible bool
	var env string
	if err := tx.QueryRow(ctx, `SELECT environment_id FROM v3_memory_facts WHERE id=$1`, id).Scan(&env); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "v3-memory-policy:"+env); err != nil {
		return false, err
	}
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM v3_memory_policies p WHERE p.environment_id=f.environment_id AND COALESCE(f.confirmed_at,f.created_at)+p.ttl_days*interval '1 day'<=clock_timestamp()) FROM v3_memory_facts f WHERE f.id=$1`, id).Scan(&eligible)
	return eligible, err
}
