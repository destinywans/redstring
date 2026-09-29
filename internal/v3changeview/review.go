package v3changeview

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewSource supplies explicit, human-approved assertions for the frozen
// detail fields that delivery and incident records cannot infer. The caller
// independently verifies the run, deployment and incident identities.
type ReviewSource interface {
	Read(context.Context, string, string, string, string) (ReviewEvidence, error)
}

type ReviewEvidence struct {
	ID, RunID, EnvironmentID, Digest, ArtifactDigest, ExecutionID, IncidentID string
	DiffPath, DiffMinus, DiffPlus                                             string
	RiskTitle, RiskMeta, RiskText                                             string
	AlertText                                                                 string
	ReviewerID, ChainHash                                                     string
	ReviewedAt                                                                time.Time
}

// AuditReviewSource reads an immutable, chained, deliberately published audit
// assertion. No producer for change.review.accepted is currently registered;
// until one is reviewed and wired, the normal request has no detail evidence.
type AuditReviewSource struct{ DB *pgxpool.Pool }

type auditReviewPayload struct {
	SchemaVersion   int    `json:"schema_version"`
	DisplayApproved bool   `json:"display_approved"`
	EnvironmentID   string `json:"environment_id"`
	RunDigest       string `json:"run_digest"`
	ArtifactDigest  string `json:"artifact_digest"`
	ExecutionID     string `json:"execution_id"`
	IncidentID      string `json:"incident_id"`
	Diff            struct {
		Path  string `json:"path"`
		Minus string `json:"minus"`
		Plus  string `json:"plus"`
	} `json:"diff"`
	Risk struct {
		Title string `json:"title"`
		Meta  string `json:"meta"`
		Text  string `json:"text"`
	} `json:"risk"`
	AlertText string `json:"alert_text"`
}

func (a AuditReviewSource) Read(ctx context.Context, appID, envID, runID, digest string) (ReviewEvidence, error) {
	var zero ReviewEvidence
	if a.DB == nil {
		return zero, errIncomplete
	}
	rows, err := a.DB.Query(ctx, `SELECT m.id,m.actor_id,m.created_at,m.changes,m.chain_hash
 FROM management_audit m JOIN users u ON u.id=m.actor_id
 WHERE m.application_id=$1 AND m.target=$2 AND m.action='change.review.accepted'
 AND m.outcome='success' AND m.actor_kind='user'
 ORDER BY m.id DESC LIMIT 2`, appID, runID)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	var id int64
	var raw []byte
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&id, &zero.ReviewerID, &zero.ReviewedAt, &raw, &zero.ChainHash); err != nil {
			return ReviewEvidence{}, err
		}
	}
	if err = rows.Err(); err != nil {
		return ReviewEvidence{}, err
	}
	if count != 1 || zero.ChainHash == "" || zero.ReviewerID == "" {
		return ReviewEvidence{}, errIncomplete
	}
	var payload auditReviewPayload
	if err = json.Unmarshal(raw, &payload); err != nil {
		return ReviewEvidence{}, errIncomplete
	}
	if payload.SchemaVersion != 1 || !payload.DisplayApproved || payload.EnvironmentID != envID ||
		payload.RunDigest != digest ||
		!reviewText(payload.ArtifactDigest, 128) ||
		!reviewText(payload.ExecutionID, 128) || !reviewText(payload.IncidentID, 128) ||
		!reviewText(payload.Diff.Path, 300) || !reviewText(payload.Diff.Minus, 1000) ||
		!reviewText(payload.Diff.Plus, 1000) || !reviewText(payload.Risk.Title, 200) ||
		!reviewText(payload.Risk.Meta, 300) || !reviewText(payload.Risk.Text, 2000) ||
		!reviewText(payload.AlertText, 500) || zero.ReviewedAt.After(time.Now().Add(5*time.Minute)) {
		return ReviewEvidence{}, errIncomplete
	}
	zero.ID = strconv.FormatInt(id, 10)
	zero.RunID, zero.EnvironmentID, zero.Digest, zero.ArtifactDigest = runID, envID, payload.RunDigest, payload.ArtifactDigest
	zero.ExecutionID, zero.IncidentID = payload.ExecutionID, payload.IncidentID
	zero.DiffPath, zero.DiffMinus, zero.DiffPlus = payload.Diff.Path, payload.Diff.Minus, payload.Diff.Plus
	zero.RiskTitle, zero.RiskMeta, zero.RiskText = payload.Risk.Title, payload.Risk.Meta, payload.Risk.Text
	zero.AlertText = payload.AlertText
	return zero, nil
}

func reviewText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max &&
		!strings.ContainsAny(value, "\x00\r")
}

func validateReview(e ReviewEvidence, runID, envID, digest string) error {
	if e.RunID != runID || e.EnvironmentID != envID || e.Digest != digest || e.ID == "" || e.ReviewerID == "" || e.ChainHash == "" ||
		e.ReviewedAt.IsZero() || !reviewText(e.ArtifactDigest, 128) || !reviewText(e.ExecutionID, 128) || !reviewText(e.IncidentID, 128) ||
		!reviewText(e.DiffPath, 300) || !reviewText(e.DiffMinus, 1000) || !reviewText(e.DiffPlus, 1000) ||
		!reviewText(e.RiskTitle, 200) || !reviewText(e.RiskMeta, 300) || !reviewText(e.RiskText, 2000) ||
		!reviewText(e.AlertText, 500) {
		return fmt.Errorf("%w: review", errIncomplete)
	}
	return nil
}
