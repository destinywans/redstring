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
	BindingFingerprint                                               string
	Spaces                                                           []string
}

// Observation contains only bounded lineage evidence. ACLVerified means the
// reader could access the item under its configured source access. A public
// Web reader proves only an unauthenticated server-side read; this flag does
// not grant product users access to a body or prove universal public access.
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

// ContentReadResult is a separate, opt-in result for a single bounded public
// Web page. Generic observations must never carry bodies or private titles.
type ContentReadResult struct {
	Observation Observation
	Body        string
}

type ContentReader interface {
	ReadContent(context.Context, ReadSpec) (ContentReadResult, error)
}

// GitMarkdownFile carries one bounded body tied to an observation from the
// same pinned repository commit. Its path is relative to the configured repo.
type GitMarkdownFile struct {
	Observation Observation
	Path        string
	Body        string
}

type GitMarkdownReadResult struct {
	CommitSHA string
	Files     []GitMarkdownFile
	Complete  bool
}

type GitMarkdownContentReader interface {
	ReadGitMarkdown(context.Context, ReadSpec) (GitMarkdownReadResult, error)
}

// RegistrationBinding verifies that a client-proposed registration matches a
// server-owned source configuration before any saved row is created.
type RegistrationBindingEvidence interface {
	BindingEvidenceForCreate(ReadSpec) (string, json.RawMessage, bool)
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
	BindingFingerprint                                                   string
	BindingSnapshot                                                      json.RawMessage
}

func loadRegistration(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id, env string, lock bool) (registration, error) {
	query := `SELECT id,environment_id,COALESCE(application_id,''),COALESCE(team_id,''),name,source_type,
source_url,secret_ref,spaces,schedule,chunk,inherit_acl,COALESCE(binding_fingerprint,''),
COALESCE(binding_snapshot,'{}'::jsonb) FROM v3_knowledge_source_registrations
WHERE id=$1 AND environment_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var v registration
	var spaces []byte
	err := q.QueryRow(ctx, query, id, env).Scan(&v.ID, &v.EnvironmentID, &v.ApplicationID, &v.TeamID,
		&v.Name, &v.Type, &v.URL, &v.SecretRef, &spaces, &v.Schedule, &v.Chunk, &v.InheritACL,
		&v.BindingFingerprint, &v.BindingSnapshot)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(spaces, &v.Spaces); err != nil {
		return v, err
	}
	return v, nil
}

func (s *Service) activeRegistration(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, v registration, lock bool) (bool, error) {
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
	err := q.QueryRow(ctx, query, v.ApplicationID, v.EnvironmentID, v.TeamID).Scan(&activeID)
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

var errSourceSyncSession = errors.New("source sync session expired")

// Revalidate the active session and serialize grant revocation with the write
// transaction after the upstream read. The HTTP session validated before the
// slow network operation is not sufficient authority for persistence.
func lockSourceSyncAuthority(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errSourceSyncSession
	}
	var actor string
	err = tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN sessions s ON s.user_id=u.id
 WHERE s.token_hash=$1 AND u.id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo'
 FOR SHARE OF u,s`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSourceSyncSession
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func currentSourceSyncSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errSourceSyncSession
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return errSourceSyncSession
	}
	return err
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
	if initial.Type == "web" && initial.SecretRef == "" &&
		(initial.Schedule != "manual" || len(initial.Spaces) != 0) {
		httpapi.Error(w, 503, "source_config_invalid", "Saved public Web scope is invalid")
		return
	}
	if initial.Type == "git_markdown" &&
		(initial.ApplicationID == "" || initial.Schedule != "manual" || len(initial.Spaces) != 0 ||
			len(initial.BindingFingerprint) != 64 || len(initial.BindingSnapshot) == 0 ||
			string(initial.BindingSnapshot) == "{}") {
		httpapi.Error(w, 503, "source_config_invalid", "Saved Git Markdown scope is invalid")
		return
	}
	reader := s.Readers[initial.Type]
	if reader == nil {
		httpapi.Error(w, 501, "source_reader_unavailable", "No read-only reader is configured for this source type")
		return
	}
	if initial.Type == "web" && initial.SecretRef == "" && initial.ApplicationID == "" {
		httpapi.Error(w, 422, "source_application_required", "Public Web observation requires a registered application")
		return
	}
	active, err := s.activeRegistration(ctx, s.DB, initial, false)
	if err != nil {
		unavailable(w)
		return
	}
	if !active {
		httpapi.Error(w, 409, "source_scope_changed", "Source application or team is no longer active")
		return
	}
	spec := ReadSpec{SourceType: initial.Type, URL: initial.URL, SecretRef: initial.SecretRef,
		EnvironmentID: initial.EnvironmentID, ApplicationID: initial.ApplicationID, TeamID: initial.TeamID,
		BindingFingerprint: initial.BindingFingerprint, Spaces: append([]string(nil), initial.Spaces...)}
	var result ReadResult
	var content *ContentReadResult
	var gitContent *GitMarkdownReadResult
	if initial.Type == "web" && initial.SecretRef == "" {
		contentReader, available := reader.(ContentReader)
		if !available {
			httpapi.Error(w, 501, "source_content_reader_unavailable", "No bounded content reader is configured for this public Web source")
			return
		}
		var fetched ContentReadResult
		fetched, err = contentReader.ReadContent(ctx, spec)
		if err == nil && !validPublicWebContent(initial, fetched) {
			httpapi.Error(w, 422, "source_content_invalid", "Public Web content or lineage could not be verified")
			return
		}
		result = ReadResult{Items: []Observation{fetched.Observation}, Complete: true}
		content = &fetched
	} else if initial.Type == "git_markdown" {
		contentReader, available := reader.(GitMarkdownContentReader)
		if !available {
			httpapi.Error(w, 501, "source_content_reader_unavailable", "No bounded Git Markdown reader is configured")
			return
		}
		fetched, readErr := contentReader.ReadGitMarkdown(ctx, spec)
		err = readErr
		if err == nil && !validGitMarkdownContent(initial, fetched) {
			httpapi.Error(w, 422, "source_content_invalid", "Git Markdown content or lineage could not be verified")
			return
		}
		if err == nil {
			items := make([]Observation, len(fetched.Files))
			for i, file := range fetched.Files {
				items[i] = file.Observation
			}
			result = ReadResult{Items: items, Complete: fetched.Complete}
			gitContent = &fetched
		}
	} else {
		result, err = reader.Read(ctx, spec)
	}
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
	if err = lockSourceSyncAuthority(ctx, tx, r); errors.Is(err, errSourceSyncSession) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
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
	active, err = s.activeRegistration(ctx, tx, current, true)
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
	_, err = tx.Exec(ctx, `INSERT INTO v3_knowledge_source_reads(id,registration_id,environment_id,observed_count,reader_type,requested_by,observed_at)
VALUES($1,$2,$3,$4,$5,$6,clock_timestamp())`, readID, id, current.EnvironmentID, len(items), current.Type, user.ID)
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
	contentVersion := 0
	if content != nil {
		contentVersion, _, err = importPublicWebContent(ctx, tx, current, readID, user.ID, *content)
		if errors.Is(err, errContentScopeConflict) {
			httpapi.Error(w, 409, "source_content_scope_changed", "Public Web content belongs to a different source scope")
			return
		}
		if err != nil {
			unavailable(w)
			return
		}
	}
	if gitContent != nil {
		if err = importGitMarkdownContent(ctx, tx, current, readID, *gitContent); err != nil {
			unavailable(w)
			return
		}
	}
	gitSHA := ""
	if gitContent != nil {
		gitSHA = gitContent.CommitSHA
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.knowledge_source.read',$3,'success',
 jsonb_build_object('environment_id',$4::text,'source_type',$5::text,'observed_count',$6::integer,
'keyword_content_persisted',$7::boolean,'content_version',NULLIF($8::integer,0))
 || CASE WHEN $9::text<>'' THEN jsonb_build_object('git_commit_sha',$9::text) ELSE '{}'::jsonb END)`,
		user.ID, current.ApplicationID, id, current.EnvironmentID, current.Type, len(items), content != nil, contentVersion, gitSHA)
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Source read audit is unavailable")
		return
	}
	if err = currentSourceSyncSession(ctx, tx, r); errors.Is(err, errSourceSyncSession) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		unavailable(w)
		return
	}
	response := map[string]any{"read_id": readID, "observed_count": len(items),
		"index_state": "not_proven", "complete_v3_dto": false}
	if content != nil {
		response["keyword_content_persisted"] = true
		response["content_version"] = contentVersion
	}
	if gitContent != nil {
		response["commit_sha"] = gitContent.CommitSHA
		response["document_count"] = len(gitContent.Files)
	}
	httpapi.WriteJSON(w, 200, response)
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
		if v.Type == "web" && v.SecretRef == "" {
			var version int
			if v.ApplicationID != "" && v.InheritACL && v.Schedule == "manual" && len(v.Spaces) == 0 {
				readErr = tx.QueryRow(ctx, `SELECT d.version FROM v3_public_web_documents d
 JOIN v3_knowledge_source_observations o ON o.read_id=d.last_read_id AND o.item_hash=d.item_hash
 JOIN v3_knowledge_source_reads rr ON rr.id=d.last_read_id
 JOIN v3_public_web_document_revisions rev ON rev.registration_id=d.registration_id AND rev.version=d.version
 JOIN v3_knowledge_source_reads revision_read ON revision_read.id=rev.read_id
 JOIN v3_knowledge_source_observations revision_observation
   ON revision_observation.read_id=revision_read.id AND revision_observation.item_hash=d.item_hash
 WHERE d.registration_id=$1 AND d.environment_id=$2 AND d.last_read_id=$3
 AND d.application_id=$4 AND COALESCE(d.team_id,'')=$5
 AND d.title=$6 AND d.source_url=$7 AND d.item_hash=$8
 AND d.content_hash=o.content_hash
 AND d.content_hash=encode(sha256(convert_to(d.body,'UTF8')),'hex')
 AND o.environment_id=d.environment_id AND o.application_id=d.application_id
	AND COALESCE(o.team_id,'')=COALESCE(d.team_id,'')
 AND rr.registration_id=d.registration_id AND rr.environment_id=d.environment_id
 AND rr.reader_type='web' AND rr.observed_count=1
 AND rev.body=d.body AND rev.content_hash=d.content_hash
 AND revision_read.registration_id=d.registration_id AND revision_read.environment_id=d.environment_id
 AND revision_read.reader_type='web' AND revision_read.observed_count=1
 AND revision_observation.item_hash=d.item_hash AND revision_observation.content_hash=d.content_hash
 AND revision_observation.environment_id=d.environment_id
 AND revision_observation.application_id=d.application_id
 AND COALESCE(revision_observation.team_id,'')=COALESCE(d.team_id,'')`,
					id, env, readID, v.ApplicationID, v.TeamID, v.Name, v.URL, hashID(v.URL)).Scan(&version)
			} else {
				readErr = pgx.ErrNoRows
			}
			if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
				unavailable(w)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				unavailable(w)
				return
			}
			var keyword any
			if readErr == nil {
				keyword = map[string]any{"content_persisted": true, "version": version}
			}
			httpapi.WriteJSON(w, 200, map[string]any{"id": id,
				"last_read": map[string]any{"id": readID, "observed_at": at, "observed_count": count},
				"keyword":   keyword, "index": nil, "reason": "semantic_index_unavailable", "complete_v3_dto": false})
			return
		}
		if v.Type == "git_markdown" {
			var commitSHA string
			var documentCount int
			readErr = tx.QueryRow(ctx, `SELECT s.commit_sha,s.document_count
FROM v3_git_markdown_syncs s
JOIN v3_knowledge_source_reads rr ON rr.id=s.read_id
WHERE s.read_id=$1 AND s.registration_id=$2 AND s.environment_id=$3
 AND s.binding_fingerprint=$7 AND s.binding_snapshot=$8::jsonb
 AND rr.registration_id=s.registration_id AND rr.environment_id=s.environment_id
 AND rr.reader_type='git_markdown' AND rr.observed_count=s.document_count
 AND s.document_count=$4 AND s.indexed_chunk_count IS NULL
 AND (SELECT count(*) FROM v3_git_markdown_documents d WHERE d.read_id=s.read_id)=s.document_count
 AND (SELECT count(*) FROM v3_knowledge_source_observations o WHERE o.read_id=s.read_id)=s.document_count
 AND NOT EXISTS (
  SELECT 1 FROM v3_git_markdown_documents d
  WHERE d.read_id=s.read_id AND (
   d.environment_id<>s.environment_id OR d.application_id<>$5
   OR COALESCE(d.team_id,'')<>$6
   OR d.item_hash<>encode(sha256(convert_to(d.path,'UTF8')),'hex')
   OR d.content_hash<>encode(sha256(convert_to(d.body,'UTF8')),'hex')
   OR NOT EXISTS (SELECT 1 FROM v3_knowledge_source_observations o
    WHERE o.read_id=s.read_id AND o.item_hash=d.item_hash
     AND o.content_hash=d.content_hash AND o.environment_id=d.environment_id
     AND o.application_id=d.application_id
     AND COALESCE(o.team_id,'')=COALESCE(d.team_id,''))))`,
				readID, id, env, count, v.ApplicationID, v.TeamID,
				v.BindingFingerprint, string(v.BindingSnapshot)).Scan(&commitSHA, &documentCount)
			if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
				unavailable(w)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				unavailable(w)
				return
			}
			var git any
			if readErr == nil {
				git = map[string]any{"commit_sha": commitSHA, "document_count": documentCount,
					"content_persisted": true}
			}
			httpapi.WriteJSON(w, 200, map[string]any{"id": id,
				"last_read":    map[string]any{"id": readID, "observed_at": at, "observed_count": count},
				"git_markdown": git, "index": nil, "reason": "semantic_index_unavailable", "complete_v3_dto": false})
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
