package v3executions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

func reportDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func noPublicationBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil {
		httpapi.Error(w, 400, "invalid_body", "Could not read publication request")
		return false
	}
	if len(raw) > 1024 {
		httpapi.Error(w, 413, "body_too_large", "Publication request body is too large")
		return false
	}
	if strings.TrimSpace(string(raw)) != "" {
		httpapi.Error(w, 422, "unexpected_body", "Publication uses the stored report")
		return false
	}
	return true
}

func reportNotReady(w http.ResponseWriter) {
	httpapi.Error(w, 409, "report_not_ready", "The approved execution report is not ready for local publication")
}

func writePublication(w http.ResponseWriter, id string, p publicationRecord) {
	httpapi.WriteJSON(w, 200, map[string]any{
		"id": id, "status": "published", "destination": "local",
		"published_at": p.PublishedAt.UTC().Format(time.RFC3339),
		"published_by": p.PublishedBy, "report_digest": p.Digest,
	})
}

// Publish records a local publication marker for the immutable source report.
// The unique execution key serializes concurrent requests. Only the winner
// writes audit evidence; a retry reads the committed marker and returns it.
func (s *Service) Publish(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r, true)
	if !ok || !noPublicationBody(w, r) {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	x, err := loadExecution(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, x.EnvironmentKey) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	// Read the source under row locks before checking current identity and grants.
	// CloseInvestigation and worker transitions cannot change the locked rows.
	a, err := loadApproval(r.Context(), tx, x.PlanID)
	if err != nil {
		unavailable(w)
		return
	}
	report, err := loadReport(r.Context(), tx, x)
	if err != nil {
		unavailable(w)
		return
	}
	token, err := lockSession(r.Context(), tx, r, true)
	if !handleSessionError(w, err) {
		return
	}
	user := httpapi.CurrentUser(r)
	if err = lockGrantSources(r.Context(), tx, user.ID); !handleAccessError(w, err) {
		return
	}
	for _, permission := range [][2]string{{"proposals", "view"}, {"knowledge", "manage"}} {
		err = s.authorizeTx(r.Context(), tx, user, x.ApplicationID,
			x.OwnerTeam, x.EnvironmentKey, permission[0], permission[1])
		if !handleAccessError(w, err) {
			return
		}
	}
	if x.Status != "succeeded" || !validReport(x, a, report) {
		reportNotReady(w)
		return
	}
	observation, err := decodeObservation(x.Result)
	if err != nil {
		unavailable(w)
		return
	}
	succeededEvent, closedEvent, err := loadEventProof(r.Context(), tx, x)
	if err != nil {
		unavailable(w)
		return
	}
	if !verifiedRecovery(x, observation) || !succeededEvent || !closedEvent {
		reportNotReady(w)
		return
	}
	digest := reportDigest(report.Raw)
	publication, err := loadPublication(r.Context(), tx, x.ID)
	if err != nil {
		unavailable(w)
		return
	}
	if publication != nil && publication.Digest != digest {
		reportNotReady(w)
		return
	}
	if publication == nil {
		var inserted publicationRecord
		err = tx.QueryRow(r.Context(), `INSERT INTO v3_execution_report_publications
 (execution_id,investigation_id,report_digest,published_by)
 VALUES($1,$2,$3,$4) ON CONFLICT(execution_id) DO NOTHING
 RETURNING report_digest,published_by,published_at`,
			x.ID, x.InvestigationID, digest, user.ID).
			Scan(&inserted.Digest, &inserted.PublishedBy, &inserted.PublishedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			publication, err = loadPublication(r.Context(), tx, x.ID)
			if err != nil || publication == nil {
				unavailable(w)
				return
			}
			if publication.Digest != digest {
				reportNotReady(w)
				return
			}
		} else if err != nil {
			unavailable(w)
			return
		} else {
			publication = &inserted
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.execution.report.published',$3,'success',
 jsonb_build_object('investigation_id',$4::text,'environment_id',$5::text,
  'plan_id',$6::text,'report_digest',$7::text))`,
				user.ID, x.ApplicationID, x.ID, x.InvestigationID,
				x.EnvironmentKey, x.PlanID, digest)
			if err != nil {
				unavailable(w)
				return
			}
		}
	}
	if err = sessionStillActive(r.Context(), tx, token, user.ID); !handleSessionError(w, err) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	writePublication(w, x.ID, *publication)
}
