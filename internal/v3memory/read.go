package v3memory

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errMemoryReadUnavailable = errors.New("memory read unavailable")
var errMemoryFactsLimit = errors.New("memory facts exceed bounded view")
var errMemoryConflictsLimit = errors.New("memory conflicts exceed bounded view")

type memoryApplication struct {
	ID   string
	Key  string
	Name string
}

type memoryConflictRow struct {
	ID            string
	FactAID       string
	FactBID       string
	EnvironmentID string
	ApplicationID string
	TeamID        string
	CreatedAt     time.Time
}

type memorySnapshot struct {
	Facts        []Fact
	Conflicts    []memoryConflictRow
	Applications map[string]memoryApplication
	// SourceTitles names the source documents of Facts; a blank title is left out.
	SourceTitles map[string]string
}

func readFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMemorySession):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, errMemoryFactsLimit):
		httpapi.Error(w, 413, "scope_too_large", "Memory facts exceed the bounded view")
	case errors.Is(err, errMemoryConflictsLimit):
		httpapi.Error(w, 413, "scope_too_large", "Memory conflicts exceed the bounded view")
	default:
		unavailable(w)
	}
}

// The first pass locks matching fact rows in ID order, as conflict governance
// does. It is a bounded candidate set, not the response: source and catalog
// state can still change before their rows are pinned below.
const memoryReadFacts = factSelect + ` f JOIN knowledge_documents d ON d.id=f.source_document_id
WHERE f.environment_id=ANY($1::text[])
 AND (cardinality($4::text[])=0 OR f.source_document_id=ANY($4))
 AND (cardinality($5::text[])=0 OR 'unknown'=ANY($5))
 AND ($3::boolean OR f.state IN ('candidate','confirmed'))
 AND ($3::boolean OR NOT EXISTS(SELECT 1 FROM v3_memory_policies p WHERE p.environment_id=f.environment_id AND COALESCE(f.confirmed_at,f.created_at)+p.ttl_days*interval '1 day'<=clock_timestamp()))
 AND d.lifecycle='published' AND d.version=f.source_version AND d.content_hash=f.source_content_hash
 AND d.source NOT LIKE 'runbook://v3/%'
 AND NOT EXISTS (SELECT 1 FROM v3_runbooks vr WHERE vr.document_id=d.id)
 AND COALESCE(d.application_id,'')=f.application_id
 AND jsonb_typeof(d.metadata)='object' AND NOT (d.metadata ? 'source_registration_id')
 AND COALESCE(d.metadata->>'team_id','')=f.team_id
 AND (NOT (d.metadata ? 'team_id') OR
   (jsonb_typeof(d.metadata->'team_id')='string' AND btrim(d.metadata->>'team_id')<>''
    AND btrim(d.metadata->>'team_id')=d.metadata->>'team_id'))
 AND (NOT (d.metadata ? 'visibility') OR jsonb_typeof(d.metadata->'visibility') IN ('string','null'))
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array' THEN
   jsonb_array_length(d.metadata->'environment_ids')>0
   AND d.metadata->'environment_ids' ? f.environment_id
   AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item
     WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')=''
       OR btrim(env_item #>> '{}')<>(env_item #>> '{}') OR (env_item #>> '{}')='all')
   ELSE false END
 AND position(f.fact in d.body)>0
 AND ((f.application_id<>'' AND EXISTS (SELECT 1 FROM applications a
   JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=f.environment_id
   JOIN application_environments e ON e.application_id=a.id AND e.key=f.environment_id
   WHERE a.id=f.application_id AND NOT a.archived AND NOT e.archived
   AND (f.team_id='' OR a.owner_team_id=f.team_id)))
   OR (f.application_id='' AND d.application_id IS NULL AND f.team_id=''
   AND d.metadata->>'visibility'='workspace' AND NOT (d.metadata ? 'team_id')))
 AND (f.application_id='' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id)
      OR (g.scope_kind='application' AND g.scope_id=f.application_id))
 ))
 AND (f.team_id='' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id)
      OR (g.scope_kind='team' AND g.scope_id=f.team_id))
 ))
 AND (f.application_id<>'' OR f.team_id<>'' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id))
 ))
ORDER BY f.id LIMIT 501 FOR SHARE OF f`

// readSnapshot supplies both /memory and /memory/facts. App/environment
// bindings, the exact session and grant sources, and current published
// documents are held until commit; a revoke that commits first is observed.
func (s *Service) readSnapshot(r *http.Request, includeInactive, includeView bool) (memorySnapshot, error) {
	result := memorySnapshot{Facts: []Fact{}, Conflicts: []memoryConflictRow{}, Applications: map[string]memoryApplication{}, SourceTitles: map[string]string{}}
	if !s.ready() {
		return result, errMemoryReadUnavailable
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		return result, errMemoryReadUnavailable
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return result, err
	}
	defer tx.Rollback(r.Context())
	sources, confidence := []string{}, []string{}
	if includeView {
		var valid bool
		sources, confidence, valid = memoryFacets(r)
		if !valid {
			return result, ErrInvalid
		}
		sources = append([]string{}, sources...)
		confidence = append([]string{}, confidence...)
	}
	rows, err := tx.Query(r.Context(), memoryReadFacts, scope.EnvironmentIDs, user.ID, includeInactive, sources, confidence)
	if err != nil {
		return result, err
	}
	candidates := []Fact{}
	for rows.Next() {
		fact, scanErr := scanFact(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		candidates = append(candidates, fact)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(candidates) > 500 {
		return result, errMemoryFactsLimit
	}

	// Lock each concrete binding once in a stable order before locking the
	// account/grant sources. Governance writes take the same catalog-before-
	// grant order. An archived or transferred application is omitted.
	type binding struct{ app, team, env string }
	bindings := map[binding]bool{}
	keys := make([]binding, 0, len(candidates))
	for _, fact := range candidates {
		key := binding{fact.ApplicationID, fact.TeamID, fact.EnvironmentID}
		if _, exists := bindings[key]; !exists {
			bindings[key] = false
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].app != keys[j].app {
			return keys[i].app < keys[j].app
		}
		if keys[i].env != keys[j].env {
			return keys[i].env < keys[j].env
		}
		return keys[i].team < keys[j].team
	})
	for _, key := range keys {
		current, bindErr := lockCurrentBinding(r.Context(), tx, key.app, key.team, key.env)
		if bindErr != nil {
			return result, bindErr
		}
		bindings[key] = current
	}
	if err = lockWriteAuthority(r.Context(), tx, r); err != nil {
		return result, err
	}
	for _, fact := range candidates {
		if !bindings[binding{fact.ApplicationID, fact.TeamID, fact.EnvironmentID}] {
			continue
		}
		err = s.authorizeTx(r.Context(), tx, user, fact.EnvironmentID, fact.ApplicationID, fact.TeamID, "view")
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return result, err
		}
		current, sourceErr := lockCurrentSource(r.Context(), tx, fact.ID)
		if sourceErr != nil {
			return result, sourceErr
		}
		if !current {
			continue
		}
		eligible, policyErr := factWithinPolicy(r.Context(), tx, fact.ID)
		if policyErr != nil {
			return result, policyErr
		}
		if !includeInactive && !eligible {
			continue
		}
		fact.SourceCurrent = &current
		result.Facts = append(result.Facts, fact)
	}
	sort.Slice(result.Facts, func(i, j int) bool {
		if !result.Facts[i].CreatedAt.Equal(result.Facts[j].CreatedAt) {
			return result.Facts[i].CreatedAt.After(result.Facts[j].CreatedAt)
		}
		return result.Facts[i].ID > result.Facts[j].ID
	})
	if includeView {
		if err = loadMemoryViewMetadata(r, tx, &result); err != nil {
			return result, err
		}
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		return result, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return result, err
	}
	return result, nil
}

func loadMemoryViewMetadata(r *http.Request, tx pgx.Tx, snapshot *memorySnapshot) error {
	ids, appIDs, sourceIDs := make([]string, 0, len(snapshot.Facts)), map[string]bool{}, map[string]bool{}
	for _, fact := range snapshot.Facts {
		ids = append(ids, fact.ID)
		sourceIDs[fact.Source] = true
		if fact.ApplicationID != "" {
			appIDs[fact.ApplicationID] = true
		}
	}
	if err := loadMemorySourceTitles(r, tx, sourceIDs, snapshot); err != nil {
		return err
	}
	if len(ids) > 0 {
		rows, err := tx.Query(r.Context(), `SELECT c.id,c.fact_a_id,c.fact_b_id,c.environment_id,c.application_id,c.team_id,c.created_at
FROM v3_memory_conflicts c
JOIN v3_memory_facts a ON a.id=c.fact_a_id
JOIN v3_memory_facts b ON b.id=c.fact_b_id
WHERE c.state='open' AND c.fact_a_id=ANY($1::text[]) AND c.fact_b_id=ANY($1::text[])
 AND a.state IN ('candidate','confirmed') AND b.state IN ('candidate','confirmed')
 AND a.environment_id=c.environment_id AND b.environment_id=c.environment_id
 AND a.application_id=c.application_id AND b.application_id=c.application_id
 AND a.team_id=c.team_id AND b.team_id=c.team_id
ORDER BY c.created_at DESC,c.id DESC LIMIT 101 FOR SHARE OF c`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var conflict memoryConflictRow
			if err = rows.Scan(&conflict.ID, &conflict.FactAID, &conflict.FactBID, &conflict.EnvironmentID,
				&conflict.ApplicationID, &conflict.TeamID, &conflict.CreatedAt); err != nil {
				break
			}
			snapshot.Conflicts = append(snapshot.Conflicts, conflict)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		if len(snapshot.Conflicts) > 100 {
			return errMemoryConflictsLimit
		}
	}
	if len(appIDs) == 0 {
		return nil
	}
	apps := make([]string, 0, len(appIDs))
	for id := range appIDs {
		apps = append(apps, id)
	}
	sort.Strings(apps)
	rows, err := tx.Query(r.Context(), `SELECT id,key,name FROM applications
WHERE id=ANY($1::text[]) AND NOT archived ORDER BY id`, apps)
	if err != nil {
		return err
	}
	for rows.Next() {
		var app memoryApplication
		if err = rows.Scan(&app.ID, &app.Key, &app.Name); err != nil {
			break
		}
		snapshot.Applications[app.ID] = app
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(snapshot.Applications) != len(apps) {
		return errMemoryReadUnavailable
	}
	return nil
}

// loadMemorySourceTitles names the facts' source documents (PA-166). Each
// current source was already locked by lockCurrentSource, so the title is
// the one of the version the fact is anchored to.
func loadMemorySourceTitles(r *http.Request, tx pgx.Tx, sourceIDs map[string]bool, snapshot *memorySnapshot) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(sourceIDs))
	for id := range sourceIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows, err := tx.Query(r.Context(), `SELECT id,title FROM knowledge_documents WHERE id=ANY($1::text[]) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, title string
		if err = rows.Scan(&id, &title); err != nil {
			break
		}
		if title = strings.TrimSpace(title); title != "" {
			snapshot.SourceTitles[id] = title
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}
