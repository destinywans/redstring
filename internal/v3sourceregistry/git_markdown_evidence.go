package v3sourceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
)

type gitMarkdownEvidenceState struct {
	readID        string
	observedCount int
	observedAt    time.Time
	commitSHA     string
	documentCount int
	proofPresent  bool
}

func (s *Service) gitMarkdownEvidenceBindingCurrent(v registration) bool {
	if v.Type != "git_markdown" || v.ApplicationID == "" || !v.InheritACL ||
		v.Schedule != "manual" || len(v.Spaces) != 0 || len(v.BindingFingerprint) != 64 {
		return false
	}
	binding, ok := s.Readers[v.Type].(RegistrationBindingEvidence)
	if !ok {
		return false
	}
	fingerprint, snapshot, matched := binding.BindingEvidenceForCreate(ReadSpec{
		SourceType: v.Type, URL: v.URL, SecretRef: v.SecretRef,
		EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
		TeamID: v.TeamID, Spaces: v.Spaces, SourceOwnerID: v.CreatedBy,
	})
	if !matched || fingerprint != v.BindingFingerprint {
		return false
	}
	var savedValue, currentValue any
	return json.Unmarshal(v.BindingSnapshot, &savedValue) == nil &&
		json.Unmarshal(snapshot, &currentValue) == nil &&
		reflect.DeepEqual(savedValue, currentValue)
}

// A completed read is evidence of Git content only when the pinned commit,
// private bodies, observations and saved binding still describe the same set.
func gitMarkdownEvidenceProof(ctx context.Context, tx pgx.Tx, source registration,
	readID string, observedCount int) (string, int, error) {
	var commitSHA string
	var documentCount int
	err := tx.QueryRow(ctx, `SELECT s.commit_sha,s.document_count
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
		readID, source.ID, source.EnvironmentID, observedCount,
		source.ApplicationID, source.TeamID, source.BindingFingerprint,
		string(source.BindingSnapshot)).Scan(&commitSHA, &documentCount)
	return commitSHA, documentCount, err
}

// The first read uses a Repeatable Read snapshot. Before disclosing its Git
// metadata, recheck the current session, authorization, binding and proof.
func (s *Service) recheckGitMarkdownEvidence(ctx context.Context, r *http.Request,
	user core.User, original registration, expected gitMarkdownEvidenceState) error {
	if err := s.authorized(ctx, user, "view", original.EnvironmentID,
		original.ApplicationID, original.TeamID); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := currentExternalEvidenceSession(ctx, tx, r, user); err != nil {
		return err
	}
	current, err := loadRegistration(ctx, tx, original.ID, original.EnvironmentID, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !reflect.DeepEqual(original, current) {
		return errExternalEvidenceChanged
	}
	if err != nil {
		return err
	}
	active, err := s.activeRegistration(ctx, tx, current, false)
	if err != nil {
		return err
	}
	if !active || !s.gitMarkdownEvidenceBindingCurrent(current) {
		return errExternalEvidenceChanged
	}
	if err := s.authorizedTx(ctx, tx, user, "view", current.EnvironmentID,
		current.ApplicationID, current.TeamID); err != nil {
		return err
	}
	var readID string
	var count int
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT id,observed_count,observed_at FROM v3_knowledge_source_reads
 WHERE registration_id=$1 AND environment_id=$2 ORDER BY observed_at DESC,id DESC LIMIT 1`,
		current.ID, current.EnvironmentID).Scan(&readID, &count, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		if expected.readID != "" {
			return errExternalEvidenceChanged
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if readID != expected.readID || count != expected.observedCount ||
		!at.Equal(expected.observedAt) {
		return errExternalEvidenceChanged
	}
	sha, documents, err := gitMarkdownEvidenceProof(ctx, tx, current, readID, count)
	if errors.Is(err, pgx.ErrNoRows) {
		if expected.proofPresent {
			return errExternalEvidenceChanged
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if !expected.proofPresent || sha != expected.commitSHA ||
		documents != expected.documentCount {
		return errExternalEvidenceChanged
	}
	return tx.Commit(ctx)
}
