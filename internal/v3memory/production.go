package v3memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type productionInput struct {
	DocumentID          string `json:"document_id"`
	ExpectedVersion     int    `json:"expected_version"`
	ExpectedContentHash string `json:"expected_content_hash"`
	ExactExcerpt        string `json:"exact_excerpt"`
}
type conflictInput struct {
	FactA             string `json:"fact_a_id"`
	FactB             string `json:"fact_b_id"`
	ExpectedRevisionA int64  `json:"expected_revision_a"`
	ExpectedRevisionB int64  `json:"expected_revision_b"`
	Reason            string `json:"reason"`
}

// Security-sensitive inputs have one interpretation, including nested JSON.
func decodeProduction(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if err != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	var visit func() error
	visit = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return ErrInvalid
				}
				seen[key] = true
			}
			if err = visit(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err = visit(); err != nil {
		return ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrInvalid
	}
	return nil
}
func productionKey(env, app, team string, in productionInput) string {
	raw, _ := json.Marshal([]any{env, app, team, in.DocumentID, in.ExpectedVersion, in.ExpectedContentHash, in.ExactExcerpt})
	return store.Hash(string(raw))
}
func validProduction(in productionInput, env string) bool {
	if !validCandidate(CandidateInput{in.DocumentID, env, in.ExactExcerpt}) || in.ExpectedVersion < 1 || len(in.ExpectedContentHash) != 64 {
		return false
	}
	for _, c := range in.ExpectedContentHash {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
func productionEnvironment(r *http.Request) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) != 1 || scope.All {
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

// Run after object/catalog locks, matching existing memory governance. This
// checks the exact Cookie, pins grant sources and rejects machine producers.
func productionAuthority(r *http.Request, tx pgx.Tx) error {
	if err := lockWriteAuthority(r.Context(), tx, r); err != nil {
		return err
	}
	var human bool
	if err := tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, httpapi.CurrentUser(r).ID).Scan(&human); err != nil {
		return err
	}
	if !human {
		return store.ErrForbidden
	}
	return nil
}

func (s *Service) produce(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := productionEnvironment(r)
	if !ok {
		failure(w, ErrInvalid)
		return
	}
	var in productionInput
	if err := decodeProduction(w, r, &in); err != nil || !validProduction(in, env) {
		failure(w, ErrInvalid)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "v3-memory-source:"+in.DocumentID); err != nil {
		unavailable(w)
		return
	}
	var app, body, hash, lifecycle string
	var version int
	var metadata []byte
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(d.application_id,''),d.body,d.version,d.content_hash,d.lifecycle,d.metadata
 FROM knowledge_documents d WHERE d.id=$1 AND d.source NOT LIKE 'runbook://v3/%'
 AND NOT EXISTS(SELECT 1 FROM v3_runbooks rb WHERE rb.document_id=d.id) FOR SHARE OF d`, in.DocumentID).Scan(&app, &body, &version, &hash, &lifecycle, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	meta, valid := parseSourceAttribution(metadata)
	// Do not disclose source lifecycle/version/content before scope admission.
	if !valid || !has(meta.EnvironmentIDs, env) || (app == "" && meta.Visibility != "workspace") {
		failure(w, store.ErrNotFound)
		return
	}
	bound, err := lockCurrentBinding(r.Context(), tx, app, meta.TeamID, env)
	if err != nil {
		unavailable(w)
		return
	}
	if err = productionAuthority(r, tx); err != nil {
		failure(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"view", "manage"} {
		if err = s.authorizeTx(r.Context(), tx, user, env, app, meta.TeamID, action); err != nil {
			failure(w, err)
			return
		}
	}
	if !bound || lifecycle != "published" || version != in.ExpectedVersion || hash != in.ExpectedContentHash || hash != store.Hash(body) {
		failure(w, store.ErrConflict)
		return
	}
	if !strings.Contains(body, in.ExactExcerpt) {
		failure(w, ErrInvalid)
		return
	}
	key := productionKey(env, app, meta.TeamID, in)
	current := true
	f := Fact{ID: store.ID(), Fact: in.ExactExcerpt, Source: in.DocumentID, SourceVersion: version, SourceCurrent: &current, State: "candidate", EnvironmentID: env, ApplicationID: app, TeamID: meta.TeamID, Revision: 1}
	tag, err := tx.Exec(r.Context(), `INSERT INTO v3_memory_facts(id,environment_id,application_id,team_id,source_document_id,source_version,source_content_hash,fact,created_by,production_key)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(production_key) WHERE production_key IS NOT NULL DO NOTHING`, f.ID, env, app, meta.TeamID, in.DocumentID, version, hash, in.ExactExcerpt, user.ID, key)
	if err != nil {
		unavailable(w)
		return
	}
	created := tag.RowsAffected() == 1
	// MVCC read avoids taking a fact lock after holding the source row: the
	// existing confirmation path locks fact then source. Never invert that order.
	err = tx.QueryRow(r.Context(), `SELECT id,state,confirmed_at,created_at,revision FROM v3_memory_facts WHERE production_key=$1`, key).Scan(&f.ID, &f.State, &f.Confirmed, &f.CreatedAt, &f.Revision)
	if err != nil {
		unavailable(w)
		return
	}
	if created {
		if err = audit(r.Context(), tx, user.ID, app, "memory.fact.proposed", f.ID, map[string]any{"environment_id": env, "source_document_id": in.DocumentID, "source_version": version, "source_content_hash": hash, "production": "human_exact_excerpt"}); err != nil {
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
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, status, map[string]any{"fact": f, "source_content_hash": hash, "created": created, "production": "human_exact_excerpt"})
}

func (s *Service) recordConflict(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := productionEnvironment(r)
	if !ok {
		failure(w, ErrInvalid)
		return
	}
	var in conflictInput
	if err := decodeProduction(w, r, &in); err != nil || in.FactA == "" || in.FactB == "" || in.FactA == in.FactB || len(in.FactA) > 128 || len(in.FactB) > 128 || strings.TrimSpace(in.FactA) != in.FactA || strings.TrimSpace(in.FactB) != in.FactB || in.ExpectedRevisionA < 1 || in.ExpectedRevisionB < 1 || strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 1000 {
		failure(w, ErrInvalid)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	ids := []string{in.FactA, in.FactB}
	sort.Strings(ids)
	facts := make([]Fact, 0, 2)
	for _, id := range ids {
		var f Fact
		err = tx.QueryRow(r.Context(), `SELECT id,environment_id,application_id,team_id,state,revision FROM v3_memory_facts WHERE id=$1 FOR UPDATE`, id).Scan(&f.ID, &f.EnvironmentID, &f.ApplicationID, &f.TeamID, &f.State, &f.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			failure(w, store.ErrNotFound)
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
		if f.EnvironmentID != env {
			failure(w, store.ErrNotFound)
			return
		}
		facts = append(facts, f)
	}
	bound := true
	for _, f := range facts {
		ok, e := lockCurrentBinding(r.Context(), tx, f.ApplicationID, f.TeamID, env)
		if e != nil {
			unavailable(w)
			return
		}
		bound = bound && ok
	}
	if err = productionAuthority(r, tx); err != nil {
		failure(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	for _, f := range facts {
		for _, action := range []string{"view", "manage"} {
			if err = s.authorizeTx(r.Context(), tx, user, env, f.ApplicationID, f.TeamID, action); err != nil {
				failure(w, err)
				return
			}
		}
	}
	if !bound || facts[0].ApplicationID != facts[1].ApplicationID || facts[0].TeamID != facts[1].TeamID {
		failure(w, store.ErrConflict)
		return
	}
	revisions := map[string]int64{in.FactA: in.ExpectedRevisionA, in.FactB: in.ExpectedRevisionB}
	for _, f := range facts {
		if !active(f.State) || f.Revision != revisions[f.ID] {
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
		current, e := lockCurrentSource(r.Context(), tx, f.ID)
		if e != nil {
			unavailable(w)
			return
		}
		if !current {
			failure(w, store.ErrConflict)
			return
		}
	}
	c := Conflict{ID: store.ID(), FactAID: ids[0], FactBID: ids[1], State: "open", EnvironmentID: env, ApplicationID: facts[0].ApplicationID, TeamID: facts[0].TeamID}
	var existingReason string
	err = tx.QueryRow(r.Context(), `SELECT id,fact_a_id,fact_b_id,reason FROM v3_memory_conflicts WHERE state='open' AND LEAST(fact_a_id,fact_b_id)=$1 AND GREATEST(fact_a_id,fact_b_id)=$2 FOR UPDATE`, ids[0], ids[1]).Scan(&c.ID, &c.FactAID, &c.FactBID, &existingReason)
	created := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !created {
		unavailable(w)
		return
	}
	if !created && existingReason != in.Reason {
		failure(w, store.ErrConflict)
		return
	}
	if created {
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_memory_conflicts(id,environment_id,application_id,team_id,fact_a_id,fact_b_id,created_by,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, c.ID, env, c.ApplicationID, c.TeamID, c.FactAID, c.FactBID, user.ID, in.Reason)
		if err != nil {
			unavailable(w)
			return
		}
		if err = audit(r.Context(), tx, user.ID, c.ApplicationID, "memory.conflict.recorded", c.ID, map[string]any{"environment_id": env, "fact_a_id": c.FactAID, "fact_b_id": c.FactBID, "reason": in.Reason, "assertion": "human"}); err != nil {
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
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, status, map[string]any{"conflict": c, "created": created, "reason": in.Reason, "assertion": "human"})
}
