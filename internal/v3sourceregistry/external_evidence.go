package v3sourceregistry

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errExternalEvidenceChanged = errors.New("external source evidence or access changed")

func externalEvidenceRecheckError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) || errors.Is(err, errExternalEvidenceChanged) {
		httpapi.Error(w, 409, "source_state_changed", "Source state or access changed; retry")
		return
	}
	httpapi.Error(w, 503, "source_registry_unavailable", "Source evidence is unavailable")
}

type externalEvidenceState struct {
	readID        string
	observedCount int
	observedAt    time.Time
	documentCount int
	proofPresent  bool
}

func (s *Service) externalBindingCurrent(v registration) bool {
	binding, configured := s.Readers[v.Type].(RegistrationBindingEvidence)
	if !configured {
		return false
	}
	fingerprint, _, matched := binding.BindingEvidenceForCreate(ReadSpec{
		SourceType: v.Type, URL: v.URL, SecretRef: v.SecretRef,
		EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
		TeamID: v.TeamID, Spaces: v.Spaces,
	})
	return matched && fingerprint == v.BindingFingerprint
}

func currentExternalEvidenceSession(ctx context.Context, tx pgx.Tx, r *http.Request, user core.User) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errExternalEvidenceChanged
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN sessions s ON s.user_id=u.id
 WHERE s.token_hash=$1 AND u.id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo')`,
		store.Hash(cookie.Value), user.ID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return errExternalEvidenceChanged
	}
	return nil
}

func externalEvidenceDocumentCount(ctx context.Context, tx pgx.Tx, v registration,
	readID string, observedCount int) (int, error) {
	if v.ApplicationID == "" || !v.InheritACL || v.Schedule != "manual" ||
		!validExternalSpaces(v.Type, v.Spaces) || len(v.BindingFingerprint) != 64 {
		return 0, pgx.ErrNoRows
	}
	var documentCount int
	err := tx.QueryRow(ctx, `SELECT s.document_count
FROM v3_external_source_syncs s
JOIN v3_knowledge_source_reads rr ON rr.id=s.read_id
WHERE s.read_id=$1 AND s.registration_id=$2 AND s.environment_id=$3
 AND s.source_type=$4 AND s.binding_fingerprint=$7 AND s.binding_snapshot=$8::jsonb
 AND rr.registration_id=s.registration_id AND rr.environment_id=s.environment_id
 AND rr.reader_type=s.source_type AND rr.observed_count=s.document_count
 AND s.document_count=$5 AND s.indexed_chunk_count IS NULL
 AND (SELECT count(*) FROM v3_external_source_documents d WHERE d.read_id=s.read_id)=s.document_count
 AND (SELECT count(*) FROM v3_knowledge_source_observations o WHERE o.read_id=s.read_id)=s.document_count
 AND NOT EXISTS (
  SELECT 1 FROM v3_external_source_documents d
  WHERE d.read_id=s.read_id AND (
   d.environment_id<>s.environment_id OR d.application_id<>$6
   OR COALESCE(d.team_id,'')<>$9
   OR d.item_hash<>encode(sha256(convert_to(d.external_id,'UTF8')),'hex')
   OR d.content_hash<>encode(sha256(convert_to(d.body,'UTF8')),'hex')
   OR NOT EXISTS (SELECT 1 FROM v3_knowledge_source_observations o
    WHERE o.read_id=s.read_id AND o.item_hash=d.item_hash
     AND o.content_hash=d.content_hash AND o.environment_id=d.environment_id
     AND o.application_id=d.application_id
     AND COALESCE(o.team_id,'')=COALESCE(d.team_id,''))))`,
		readID, v.ID, v.EnvironmentID, v.Type, observedCount, v.ApplicationID,
		v.BindingFingerprint, string(v.BindingSnapshot), v.TeamID).Scan(&documentCount)
	return documentCount, err
}

// The initial Repeatable Read snapshot is internally consistent but can be
// stale by response time. Re-run scope, grants, binding and latest read/proof
// against a new transaction before disclosing any external-source metadata.
func (s *Service) recheckExternalEvidence(ctx context.Context, r *http.Request,
	user core.User, original registration, expected externalEvidenceState) error {
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
	if !active || !s.externalBindingCurrent(current) {
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
	documentCount, err := externalEvidenceDocumentCount(ctx, tx, current, readID, count)
	if errors.Is(err, pgx.ErrNoRows) {
		if expected.proofPresent {
			return errExternalEvidenceChanged
		}
		return tx.Commit(ctx)
	}
	if err == nil && (!expected.proofPresent || documentCount != expected.documentCount) {
		return errExternalEvidenceChanged
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
