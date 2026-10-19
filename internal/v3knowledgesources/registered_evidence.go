package v3knowledgesources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
	"github.com/destinywans/redstring/internal/v3uploads"
	"github.com/jackc/pgx/v5"
)

// RegisteredEvidence keeps configured sources, completed observations and
// imported content distinct. In particular, a read is not a successful sync
// or a connection-health check, and keyword content is not a vector index.
type RegisteredEvidence struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	EnvironmentID      string          `json:"environment_id"`
	ApplicationID      string          `json:"application_id,omitempty"`
	TeamID             string          `json:"team_id,omitempty"`
	Schedule           string          `json:"schedule"`
	ChunkStrategy      string          `json:"chunk_strategy"`
	RegistrationStatus string          `json:"registration_status"`
	LastRead           *ObservedRead   `json:"last_read"`
	KeywordContent     *KeywordContent `json:"keyword_content"`
	ConnectionStatus   *string         `json:"connection_status"`
	SyncStatus         *string         `json:"sync_status"`
	DocumentCount      *int            `json:"document_count"`
	ChunkCount         *int            `json:"chunk_count"`
	LastSyncAt         *time.Time      `json:"last_sync_at"`
	IndexStatus        *string         `json:"index_status"`
	Error              *string         `json:"error"`
	sourceURL          string
	secretRef          string
	spaces             []string
	bindingFingerprint string
	bindingSnapshot    json.RawMessage
	sourceOwner        string
}

type ObservedRead struct {
	ID            string    `json:"id"`
	ObservedAt    time.Time `json:"observed_at"`
	ObservedCount int       `json:"observed_count"`
}

type KeywordContent struct {
	PersistedDocumentCount int    `json:"persisted_document_count"`
	Version                int    `json:"version"`
	ReadID                 string `json:"read_id"`
}

type registeredEvidenceResponse struct {
	Items                        []RegisteredEvidence `json:"items"`
	Index                        any                  `json:"index"`
	CompleteV3DTO                bool                 `json:"complete_v3_dto"`
	Partial                      bool                 `json:"partial"`
	Reason                       string               `json:"reason"`
	Truncated                    bool                 `json:"truncated"`
	UnknownFields                []string             `json:"unknown_fields"`
	UnconfiguredExternalBindings []string             `json:"unconfigured_external_bindings"`
}

// This describes process configuration, not connection health. A configured
// source type still needs an exact per-registration binding, a successful
// read and separate index evidence. The response omits credentials and URLs.
func (s *Service) unconfiguredExternalBindings() []string {
	configured := map[string]bool{"git_markdown": len(s.gitMarkdownBindings) > 0}
	for _, binding := range s.externalBindings {
		configured[binding.Type] = true
	}
	configured["feishu"] = len(s.feishuBindings) > 0
	missing := make([]string, 0, 4)
	for _, kind := range []string{"git_markdown", "confluence", "notion", "feishu"} {
		if !configured[kind] {
			missing = append(missing, kind)
		}
	}
	return missing
}

const registeredEvidenceCandidates = `SELECT src.id,src.name,src.source_type,src.environment_id,
 COALESCE(src.application_id,''),COALESCE(src.team_id,''),src.schedule,src.chunk,
 read.id,read.observed_at,read.observed_count,read.reader_type,
 keyword.version,git.document_count,git.created_at,external.document_count,external.created_at,
 src.source_url,src.secret_ref,src.spaces,src.binding_fingerprint,src.binding_snapshot,src.created_by
FROM v3_knowledge_source_registrations src
LEFT JOIN LATERAL (
 SELECT r.id,r.observed_at,r.observed_count,r.reader_type
 FROM v3_knowledge_source_reads r
 WHERE r.registration_id=src.id AND r.environment_id=src.environment_id
 ORDER BY r.observed_at DESC,r.id DESC LIMIT 1
) read ON true
LEFT JOIN LATERAL (
 SELECT d.version FROM v3_public_web_documents d
 JOIN v3_knowledge_source_observations o
   ON o.read_id=d.last_read_id AND o.item_hash=d.item_hash
 JOIN v3_public_web_document_revisions rev
   ON rev.registration_id=d.registration_id AND rev.version=d.version
 JOIN v3_knowledge_source_reads revision_read ON revision_read.id=rev.read_id
 JOIN v3_knowledge_source_observations revision_observation
   ON revision_observation.read_id=revision_read.id AND revision_observation.item_hash=d.item_hash
 WHERE src.source_type='web' AND src.secret_ref='' AND src.application_id IS NOT NULL
   AND src.inherit_acl AND src.schedule='manual' AND src.spaces='[]'::jsonb
   AND read.id IS NOT NULL AND read.reader_type='web' AND read.observed_count=1
   AND d.registration_id=src.id AND d.last_read_id=read.id
   AND d.environment_id=src.environment_id AND d.application_id=src.application_id
   AND COALESCE(d.team_id,'')=COALESCE(src.team_id,'')
   AND d.title=src.name AND d.source_url=src.source_url
   AND d.item_hash=encode(sha256(convert_to(src.source_url,'UTF8')),'hex')
   AND d.content_hash=o.content_hash
   AND d.content_hash=encode(sha256(convert_to(d.body,'UTF8')),'hex')
   AND o.environment_id=d.environment_id AND o.application_id=d.application_id
   AND COALESCE(o.team_id,'')=COALESCE(d.team_id,'')
   AND revision_read.registration_id=src.id
   AND revision_read.environment_id=src.environment_id
   AND revision_read.reader_type='web' AND revision_read.observed_count=1
   AND rev.body=d.body AND rev.content_hash=d.content_hash
   AND revision_observation.content_hash=d.content_hash
   AND revision_observation.environment_id=d.environment_id
   AND revision_observation.application_id=d.application_id
   AND COALESCE(revision_observation.team_id,'')=COALESCE(d.team_id,'')
) keyword ON true
LEFT JOIN LATERAL (
 SELECT g.document_count,g.created_at
 FROM v3_git_markdown_syncs g
 WHERE src.source_type='git_markdown' AND src.application_id IS NOT NULL
   AND src.inherit_acl AND src.schedule='manual' AND src.spaces='[]'::jsonb
   AND read.id IS NOT NULL AND read.reader_type='git_markdown'
   AND g.read_id=read.id AND g.registration_id=src.id
   AND g.environment_id=src.environment_id
   AND g.binding_fingerprint=src.binding_fingerprint
   AND g.binding_snapshot=src.binding_snapshot
   AND g.document_count=read.observed_count
   AND g.document_count=(SELECT count(*) FROM v3_knowledge_source_observations o
     WHERE o.read_id=read.id)
   AND g.document_count=(SELECT count(*) FROM v3_git_markdown_documents d
     WHERE d.read_id=read.id)
   AND g.document_count=(SELECT count(*) FROM v3_knowledge_source_observations o
     WHERE o.read_id=read.id AND o.environment_id=src.environment_id
       AND o.application_id=src.application_id
       AND COALESCE(o.team_id,'')=COALESCE(src.team_id,''))
   AND g.document_count=(SELECT count(*) FROM v3_git_markdown_documents d
     JOIN v3_knowledge_source_observations o
       ON o.read_id=d.read_id AND o.item_hash=d.item_hash
     WHERE d.read_id=read.id AND d.environment_id=src.environment_id
       AND d.application_id=src.application_id
       AND COALESCE(d.team_id,'')=COALESCE(src.team_id,'')
       AND o.environment_id=d.environment_id AND o.application_id=d.application_id
       AND COALESCE(o.team_id,'')=COALESCE(d.team_id,'')
       AND d.item_hash=encode(sha256(convert_to(d.path,'UTF8')),'hex')
       AND d.content_hash=o.content_hash
       AND d.content_hash=encode(sha256(convert_to(d.body,'UTF8')),'hex'))
) git ON true
LEFT JOIN LATERAL (
 SELECT x.document_count,x.created_at
 FROM v3_external_source_syncs x
 WHERE src.source_type IN ('confluence','notion','feishu','upload') AND src.application_id IS NOT NULL
   AND src.inherit_acl AND src.schedule='manual'
   AND ((src.source_type='confluence' AND jsonb_array_length(src.spaces)=1)
     OR (src.source_type IN ('notion','feishu','upload') AND src.spaces='[]'::jsonb))
   AND (src.source_type NOT IN ('feishu','upload') OR x.document_count=1)
   AND (src.source_type<>'upload' OR EXISTS (SELECT 1 FROM users owner
     WHERE owner.id=src.created_by AND NOT owner.disabled AND owner.deleted_at IS NULL
       AND owner.source<>'settings_demo'))
   AND read.id IS NOT NULL AND read.reader_type=src.source_type
   AND x.read_id=read.id AND x.registration_id=src.id
   AND x.environment_id=src.environment_id AND x.source_type=src.source_type
   AND x.binding_fingerprint=src.binding_fingerprint
   AND x.binding_snapshot=src.binding_snapshot
   AND x.indexed_chunk_count IS NULL
   AND x.document_count=read.observed_count
   AND x.document_count=(SELECT count(*) FROM v3_knowledge_source_observations o WHERE o.read_id=read.id)
   AND x.document_count=(SELECT count(*) FROM v3_external_source_documents d WHERE d.read_id=read.id)
   AND NOT EXISTS (
    SELECT 1 FROM v3_external_source_documents d
    WHERE d.read_id=read.id AND (
      d.environment_id<>src.environment_id OR d.application_id<>src.application_id
      OR COALESCE(d.team_id,'')<>COALESCE(src.team_id,'')
      OR d.item_hash<>encode(sha256(convert_to(d.external_id,'UTF8')),'hex')
      OR d.content_hash<>encode(sha256(convert_to(d.body,'UTF8')),'hex')
      OR (x.source_type='upload' AND (d.upload_id IS NULL OR d.upload_id<>d.external_id
        OR d.source_version<>(src.binding_snapshot->>'initial_source_version')
        OR NOT EXISTS (SELECT 1 FROM v3_uploads up WHERE up.id=d.upload_id
          AND up.owner_id=src.created_by AND up.environment_id=src.environment_id
          AND up.kind='file' AND up.name=d.title AND up.mime=d.upload_mime
          AND up.size_bytes=octet_length(d.body)
          AND NOT EXISTS (SELECT 1 FROM v3_upload_revocations revoked WHERE revoked.id=up.id)
          AND encode(sha256(up.content),'hex')=d.content_hash)))
      OR (x.source_type<>'upload' AND (d.upload_id IS NOT NULL OR d.upload_mime IS NOT NULL))
      OR NOT EXISTS (SELECT 1 FROM v3_knowledge_source_observations o
       WHERE o.read_id=d.read_id AND o.item_hash=d.item_hash AND o.content_hash=d.content_hash
         AND o.environment_id=d.environment_id AND o.application_id=d.application_id
         AND COALESCE(o.team_id,'')=COALESCE(d.team_id,''))))
) external ON true
WHERE src.environment_id=ANY($1::text[])
 AND (src.application_id IS NULL OR EXISTS (
  SELECT 1 FROM applications a
  JOIN v3_application_environments ae
    ON ae.application_id=a.id AND ae.environment_id=src.environment_id
  JOIN application_environments e
    ON e.application_id=a.id AND e.key=src.environment_id AND NOT e.archived
  WHERE a.id=src.application_id AND NOT a.archived
    AND (src.team_id IS NULL OR a.owner_team_id=src.team_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='environment' AND g.action='view'
    AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
    AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)
      OR (src.application_id IS NOT NULL AND g.scope_kind='application' AND g.scope_id=src.application_id)))
 AND (src.team_id IS NULL OR EXISTS (SELECT 1 FROM v3_effective_role_grants g
  WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
    AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=src.environment_id)
      OR (g.scope_kind='team' AND g.scope_id=src.team_id))))
 AND ($4::text[] IS NULL OR src.id=ANY($4::text[]))
ORDER BY src.created_at DESC,src.id LIMIT 200 OFFSET $3`

func (s *Service) registeredEvidence(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.db == nil || s.access == nil {
		httpapi.Error(w, 503, "knowledge_sources_unavailable", "Knowledge storage or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		registeredEvidenceUnavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	items := make([]RegisteredEvidence, 0)
	truncated := false
	// SQL gates candidates before the limit; the policy is checked again in
	// the same snapshot for every emitted application and team scope.
	for offset := 0; offset <= 10000; offset += 200 {
		batch, readErr := registeredEvidenceBatch(ctx, tx, user, scope.EnvironmentIDs, offset, nil)
		if readErr != nil {
			registeredEvidenceUnavailable(w)
			return
		}
		if offset == 10000 && len(batch) > 0 {
			httpapi.Error(w, 413, "source_registry_scope_too_broad", "Narrow the source registry scope")
			return
		}
		for _, item := range batch {
			if err := s.authorizeRegisteredEvidence(ctx, tx, user, item); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				registeredEvidenceUnavailable(w)
				return
			}
			if requiresExactReaderBinding(item.Kind) && !s.currentExternalBinding(item) {
				continue
			}
			if len(items) == 200 {
				truncated = true
				break
			}
			items = append(items, item)
		}
		if truncated || len(batch) < 200 {
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		registeredEvidenceUnavailable(w)
		return
	}
	// Repeatable Read kept each source and its observation/version proof
	// together, but its authorization snapshot can be old after a concurrent
	// revocation. Re-run the same complete projection for the selected IDs on
	// a fresh statement snapshot. Changed or newly hidden rows fail closed.
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) > 0 {
		// Retain the injected policy's current decision without making it the
		// last check: only the final query also knows whether the application
		// was archived or its owner team changed.
		for _, item := range items {
			if err := s.authorizeRegisteredEvidenceCurrent(ctx, user, item); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					httpapi.Error(w, 409, "source_state_changed", "Source state or access changed; retry")
				} else {
					registeredEvidenceUnavailable(w)
				}
				return
			}
		}
		current, err := registeredEvidenceBatch(ctx, s.db, user, scope.EnvironmentIDs, 0, ids)
		if err != nil {
			registeredEvidenceUnavailable(w)
			return
		}
		if !reflect.DeepEqual(items, current) {
			httpapi.Error(w, 409, "source_state_changed", "Source state or access changed; retry")
			return
		}
		for _, item := range current {
			if requiresExactReaderBinding(item.Kind) && !s.currentExternalBinding(item) {
				httpapi.Error(w, 409, "source_state_changed", "Source state or access changed; retry")
				return
			}
		}
		items = current
	}
	unknownFields := []string{"connection_status", "sync_status", "chunk_count",
		"index_status", "error", "index_settings"}
	for _, item := range items {
		if item.DocumentCount == nil {
			unknownFields = append(unknownFields, "document_count")
			break
		}
	}
	for _, item := range items {
		if item.LastSyncAt == nil {
			unknownFields = append(unknownFields, "last_sync_at")
			break
		}
	}
	if err := s.currentRegisteredEvidenceSession(ctx, r, user); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 409, "source_state_changed", "Source state or access changed; retry")
		} else {
			registeredEvidenceUnavailable(w)
		}
		return
	}
	// The main route has every field in the relaxed evidence DTO. Partial
	// describes the limits of the evidence, not a missing response field.
	completeDTO := r.URL.Path == "/api/v1/knowledge-sources"
	httpapi.WriteJSON(w, 200, registeredEvidenceResponse{Items: items, Index: nil,
		CompleteV3DTO: completeDTO,
		Partial:       true, Reason: "scoped_source_evidence_partial",
		Truncated: truncated, UnknownFields: unknownFields,
		UnconfiguredExternalBindings: s.unconfiguredExternalBindings()})
}

// The final projection checks grants and source state; a logout can also
// happen while that query runs. Recheck the authenticated session immediately
// before disclosing registration metadata.
func (s *Service) currentRegisteredEvidenceSession(ctx context.Context, r *http.Request, user core.User) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrForbidden
	}
	var active bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN sessions sess ON sess.user_id=u.id
 WHERE sess.token_hash=$1 AND u.id=$2 AND sess.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo')`,
		store.Hash(cookie.Value), user.ID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return store.ErrForbidden
	}
	return nil
}

type evidenceRows interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func registeredEvidenceBatch(ctx context.Context, db evidenceRows, user core.User, envs []string, offset int, ids []string) ([]RegisteredEvidence, error) {
	rows, err := db.Query(ctx, registeredEvidenceCandidates, envs, user.ID, offset, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RegisteredEvidence, 0, 200)
	for rows.Next() {
		var item RegisteredEvidence
		var readID, readerType sql.NullString
		var observedAt sql.NullTime
		var observedCount, keywordVersion, gitDocumentCount, externalDocumentCount sql.NullInt64
		var gitSyncedAt, externalSyncedAt sql.NullTime
		var spaces []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.EnvironmentID,
			&item.ApplicationID, &item.TeamID, &item.Schedule, &item.ChunkStrategy,
			&readID, &observedAt, &observedCount, &readerType, &keywordVersion,
			&gitDocumentCount, &gitSyncedAt, &externalDocumentCount, &externalSyncedAt,
			&item.sourceURL, &item.secretRef, &spaces, &item.bindingFingerprint,
			&item.bindingSnapshot, &item.sourceOwner); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(spaces, &item.spaces); err != nil {
			return nil, err
		}
		item.RegistrationStatus = "unverified"
		if readID.Valid && observedAt.Valid && observedCount.Valid && readerType.Valid && readerType.String == item.Kind {
			item.LastRead = &ObservedRead{ID: readID.String, ObservedAt: observedAt.Time, ObservedCount: int(observedCount.Int64)}
			if keywordVersion.Valid && item.Kind == "web" && keywordVersion.Int64 > 0 {
				item.KeywordContent = &KeywordContent{PersistedDocumentCount: 1,
					Version: int(keywordVersion.Int64), ReadID: readID.String}
			}
			if item.Kind == "git_markdown" && gitDocumentCount.Valid && gitSyncedAt.Valid {
				count := int(gitDocumentCount.Int64)
				at := gitSyncedAt.Time
				item.DocumentCount = &count
				item.LastSyncAt = &at
			}
			if isBoundDocumentKind(item.Kind) && externalDocumentCount.Valid && externalSyncedAt.Valid {
				count := int(externalDocumentCount.Int64)
				at := externalSyncedAt.Time
				item.DocumentCount = &count
				item.LastSyncAt = &at
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) currentExternalBinding(item RegisteredEvidence) bool {
	if s == nil || item.bindingFingerprint == "" {
		return false
	}
	if item.Kind == "git_markdown" {
		if len(item.spaces) != 0 || item.ApplicationID == "" || item.Schedule != "manual" {
			return false
		}
		count := 0
		for _, binding := range s.gitMarkdownBindings {
			if !BindingMatchesCreate(binding, item.sourceURL, item.secretRef,
				item.EnvironmentID, item.ApplicationID, item.TeamID) ||
				GitMarkdownBindingFingerprint(binding) != item.bindingFingerprint {
				continue
			}
			expected, err := SnapshotGitMarkdownBinding(binding)
			if err != nil || !sameBindingSnapshot(item.bindingSnapshot, expected) {
				continue
			}
			count++
		}
		return count == 1
	}
	if item.Kind == "upload" {
		if _, ok := v3uploads.ParseSourceUploadURL(item.sourceURL); !ok || item.secretRef != "" ||
			len(item.spaces) != 0 || item.sourceOwner == "" {
			return false
		}
		if _, ok := v3sourceregistry.UploadInitialVersion(item.bindingSnapshot); !ok {
			return false
		}
		return UploadSourceFingerprint(v3sourceregistry.ReadSpec{
			SourceType: "upload", URL: item.sourceURL, EnvironmentID: item.EnvironmentID,
			ApplicationID: item.ApplicationID, TeamID: item.TeamID,
			SourceOwnerID: item.sourceOwner,
		}) == item.bindingFingerprint
	}
	if item.Kind == "feishu" {
		count := 0
		for _, binding := range s.feishuBindings {
			if binding.SourceURL == item.sourceURL && binding.CredentialRef == item.secretRef &&
				binding.EnvironmentID == item.EnvironmentID && binding.ApplicationID == item.ApplicationID &&
				binding.TeamID == item.TeamID && len(item.spaces) == 0 &&
				FeishuBindingFingerprint(binding) == item.bindingFingerprint {
				count++
			}
		}
		return count == 1
	}
	count := 0
	for _, binding := range s.externalBindings {
		if ExternalBindingMatchesCreate(binding, item.Kind, item.sourceURL, item.secretRef, item.EnvironmentID,
			item.ApplicationID, item.TeamID, item.spaces) && ExternalBindingFingerprint(binding) == item.bindingFingerprint {
			count++
		}
	}
	return count == 1
}

func isBoundDocumentKind(kind string) bool {
	switch kind {
	case "confluence", "notion", "feishu", "upload":
		return true
	default:
		return false
	}
}

func requiresExactReaderBinding(kind string) bool {
	return kind == "git_markdown" || isBoundDocumentKind(kind)
}

func sameBindingSnapshot(saved, expected json.RawMessage) bool {
	var savedValue, expectedValue any
	return json.Unmarshal(saved, &savedValue) == nil &&
		json.Unmarshal(expected, &expectedValue) == nil &&
		reflect.DeepEqual(savedValue, expectedValue)
}

func (s *Service) authorizeRegisteredEvidence(ctx context.Context, tx pgx.Tx, user core.User, item RegisteredEvidence) error {
	decision := rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: item.EnvironmentID,
		ApplicationID: item.ApplicationID}
	if err := s.access.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
		return err
	}
	if item.TeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = item.TeamID
		return s.access.AuthorizeWithTx(ctx, tx, user, decision)
	}
	return nil
}

func (s *Service) authorizeRegisteredEvidenceCurrent(ctx context.Context, user core.User, item RegisteredEvidence) error {
	decision := rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: item.EnvironmentID,
		ApplicationID: item.ApplicationID}
	if err := s.access.Authorize(ctx, user, decision); err != nil {
		return err
	}
	if item.TeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = item.TeamID
		return s.access.Authorize(ctx, user, decision)
	}
	return nil
}

func registeredEvidenceUnavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "knowledge_sources_unavailable", "Knowledge source evidence is unavailable")
}
