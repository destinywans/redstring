package v3sourceregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

const maxObservedItems = 500

// ReadSpec is supplied only to a reader explicitly installed for SourceType.
// SecretRef is an opaque Vault reference, not a resolved credential. Readers
// must use read-only upstream methods and must not log the spec or response.
type ReadSpec struct {
	SourceType, URL, SecretRef, EnvironmentID, ApplicationID, TeamID string
	Spaces                                                           []string
}

// Observation contains only bounded lineage evidence. A reader must verify
// that an item was readable under the source ACL before setting ACLVerified.
// The body and title must never be returned in this type.
type Observation struct {
	ExternalID, ContentHash, EnvironmentID, ApplicationID, TeamID string
	ACLVerified                                                   bool
}

// Complete means the reader exhausted the configured source scope. Partial
// pages cannot be saved as a successful source read or a truthful zero count.
type ReadResult struct {
	Items    []Observation
	Complete bool
}

type Reader interface {
	Read(context.Context, ReadSpec) (ReadResult, error)
}

// ConnectionTester is optional because an ordinary read may require a saved
// registration's spaces and ACL scope that the unsaved test request lacks.
type ConnectionTester interface {
	Probe(context.Context, ReadSpec) error
}

type registration struct {
	ID, EnvironmentID, ApplicationID, TeamID, Name, Type, URL, SecretRef string
	Schedule, Chunk                                                      string
	Spaces                                                               []string
	InheritACL                                                           bool
}

func loadRegistration(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id, env string, lock bool) (registration, error) {
	query := `SELECT id,environment_id,COALESCE(application_id,''),COALESCE(team_id,''),name,source_type,
source_url,secret_ref,spaces,schedule,chunk,inherit_acl FROM v3_knowledge_source_registrations
WHERE id=$1 AND environment_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var v registration
	var spaces []byte
	err := q.QueryRow(ctx, query, id, env).Scan(&v.ID, &v.EnvironmentID, &v.ApplicationID, &v.TeamID,
		&v.Name, &v.Type, &v.URL, &v.SecretRef, &spaces, &v.Schedule, &v.Chunk, &v.InheritACL)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(spaces, &v.Spaces); err != nil {
		return v, err
	}
	return v, nil
}

func (s *Service) activeRegistration(ctx context.Context, tx pgx.Tx, v registration, lock bool) (bool, error) {
	if v.ApplicationID == "" {
		return true, nil
	}
	var activeID string
	query := `SELECT a.id FROM applications a
JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=$2
JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
WHERE a.id=$1 AND NOT a.archived AND ($3='' OR a.owner_team_id=$3)`
	if lock {
		query += " FOR SHARE OF a,ae,e"
	}
	err := tx.QueryRow(ctx, query, v.ApplicationID, v.EnvironmentID, v.TeamID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return activeID != "", err
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func validObservation(v registration, o Observation) bool {
	if !o.ACLVerified || o.ExternalID == "" || len(o.ExternalID) > 1000 ||
		o.EnvironmentID != v.EnvironmentID || o.ApplicationID != v.ApplicationID || o.TeamID != v.TeamID ||
		len(o.ContentHash) != 64 {
		return false
	}
	_, err := hex.DecodeString(o.ContentHash)
	return err == nil && strings.ToLower(o.ContentHash) == o.ContentHash
}

func (s *Service) sync(w http.ResponseWriter, r *http.Request) {
	user, envs, ok := s.ready(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 100 {
		httpapi.Error(w, 422, "invalid_source_id", "Invalid source ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	initial, err := loadRegistration(ctx, s.DB, id, envs[0], false)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "source_not_found", "Source not found")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = s.authorized(ctx, user, "manage", initial.EnvironmentID, initial.ApplicationID, initial.TeamID); err != nil {
		// Hide object existence when it is outside the caller's grant.
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "source_not_found", "Source not found")
		} else {
			authorizationError(w, err)
		}
		return
	}
	if !initial.InheritACL {
		httpapi.Error(w, 422, "source_acl_unverified", "Source ACL inheritance is required for observed sync")
		return
	}
	if err := validateConnection(initial.Type, initial.URL, initial.SecretRef); err != nil {
		httpapi.Error(w, 503, "source_config_invalid", "Saved source configuration is invalid")
		return
	}
	reader := s.Readers[initial.Type]
	if reader == nil {
		httpapi.Error(w, 501, "source_reader_unavailable", "No read-only reader is configured for this source type")
		return
	}
	result, err := reader.Read(ctx, ReadSpec{SourceType: initial.Type, URL: initial.URL, SecretRef: initial.SecretRef,
		EnvironmentID: initial.EnvironmentID, ApplicationID: initial.ApplicationID, TeamID: initial.TeamID,
		Spaces: append([]string(nil), initial.Spaces...)})
	if err != nil {
		httpapi.Error(w, 503, "source_read_failed", "Source read failed")
		return
	}
	if !result.Complete {
		httpapi.Error(w, 503, "source_read_incomplete", "Source reader did not complete the configured scope")
		return
	}
	items := result.Items
	if len(items) > maxObservedItems {
		httpapi.Error(w, 422, "source_result_limit", "Source returned too many items for one read")
		return
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !validObservation(initial, item) {
			httpapi.Error(w, 422, "source_acl_unverified", "Source item scope or ACL could not be verified")
			return
		}
		fingerprint := hashID(item.ExternalID)
		if seen[fingerprint] {
			httpapi.Error(w, 422, "source_duplicate_item", "Source returned a duplicate item")
			return
		}
		seen[fingerprint] = true
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	current, err := loadRegistration(ctx, tx, id, envs[0], true)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "source_changed", "Source changed during read")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !reflect.DeepEqual(initial, current) {
		httpapi.Error(w, 409, "source_changed", "Source changed during read")
		return
	}
	active, err := s.activeRegistration(ctx, tx, current, true)
	if err != nil {
		unavailable(w)
		return
	}
	if !active {
		httpapi.Error(w, 409, "source_scope_changed", "Source application or team is no longer active")
		return
	}
	if err = s.authorizedTx(ctx, tx, user, "manage", current.EnvironmentID, current.ApplicationID, current.TeamID); err != nil {
		authorizationError(w, err)
		return
	}
	readID := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO v3_knowledge_source_reads(id,registration_id,environment_id,observed_count,reader_type,requested_by)
VALUES($1,$2,$3,$4,$5,$6)`, readID, id, current.EnvironmentID, len(items), current.Type, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	for _, item := range items {
		_, err = tx.Exec(ctx, `INSERT INTO v3_knowledge_source_observations
(read_id,item_hash,content_hash,environment_id,application_id,team_id)
VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''))`, readID, hashID(item.ExternalID), item.ContentHash,
			item.EnvironmentID, item.ApplicationID, item.TeamID)
		if err != nil {
			unavailable(w)
			return
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
VALUES($1,$2,'v3.knowledge_source.read',$3,'success',
jsonb_build_object('environment_id',$4::text,'source_type',$5::text,'observed_count',$6::integer))`,
		user.ID, current.ApplicationID, id, current.EnvironmentID, current.Type, len(items))
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Source read audit is unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"read_id": readID, "observed_count": len(items),
		"index_state": "not_proven", "complete_v3_dto": false})
}

func (s *Service) evidence(w http.ResponseWriter, r *http.Request) {
	user, envs, ok := s.ready(w, r, false)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 100 {
		httpapi.Error(w, 422, "invalid_source_id", "Invalid source ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	for _, env := range envs {
		v, readErr := loadRegistration(ctx, tx, id, env, false)
		if errors.Is(readErr, pgx.ErrNoRows) {
			continue
		}
		if readErr != nil {
			unavailable(w)
			return
		}
		active, activeErr := s.activeRegistration(ctx, tx, v, false)
		if activeErr != nil {
			unavailable(w)
			return
		}
		if !active {
			continue
		}
		if authErr := s.authorizedTx(ctx, tx, user, "view", env, v.ApplicationID, v.TeamID); authErr != nil {
			if errors.Is(authErr, store.ErrForbidden) {
				continue
			}
			authorizationError(w, authErr)
			return
		}
		var readID string
		var count int
		var at time.Time
		readErr = tx.QueryRow(ctx, `SELECT id,observed_count,observed_at FROM v3_knowledge_source_reads
WHERE registration_id=$1 AND environment_id=$2 ORDER BY observed_at DESC,id DESC LIMIT 1`, id, env).Scan(&readID, &count, &at)
		if errors.Is(readErr, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				unavailable(w)
				return
			}
			httpapi.WriteJSON(w, 200, map[string]any{"id": id, "last_read": nil, "index": nil,
				"reason": "no_observed_read", "complete_v3_dto": false})
			return
		}
		if readErr != nil {
			unavailable(w)
			return
		}
		// Only matching, ready generations attributed to an observed item count.
		// An untagged old index must not be projected onto this source.
		var indexedDocs, chunks int
		readErr = tx.QueryRow(ctx, `SELECT count(DISTINCT d.id),count(c.ordinal)
FROM v3_knowledge_source_observations o
JOIN knowledge_documents d ON d.metadata->>'source_registration_id'=$1
 AND d.metadata->>'source_observation_hash'=o.item_hash
 AND d.content_hash=o.content_hash
 AND d.metadata->'environment_ids' ? $2
 AND COALESCE(d.application_id,'')=COALESCE(o.application_id,'')
 AND COALESCE(d.metadata->>'team_id','')=COALESCE(o.team_id,'')
 AND d.lifecycle='published'
JOIN knowledge_generations g ON g.id=d.active_generation AND g.document_version=d.version AND g.status='ready'
JOIN knowledge_chunks c ON c.generation_id=g.id
WHERE o.read_id=$3 AND o.environment_id=$2`, id, env, readID).Scan(&indexedDocs, &chunks)
		if readErr != nil {
			unavailable(w)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			unavailable(w)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"id": id,
			"last_read": map[string]any{"id": readID, "observed_at": at, "observed_count": count},
			"index": map[string]any{"attributed_ready_documents": indexedDocs, "attributed_ready_chunks": chunks,
				"complete": false, "reason": "source_importer_unavailable"}, "complete_v3_dto": false})
		return
	}
	httpapi.Error(w, 404, "source_not_found", "Source not found")
}
