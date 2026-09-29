package v3changeview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	CommitSHA, PlanDigest, DiffDigest                                         string
	DiffPath, DiffMinus, DiffPlus                                             string
	RiskTitle, RiskMeta, RiskText                                             string
	AlertText                                                                 string
	ReviewerID, ChainHash                                                     string
	ReviewedAt, RunUpdatedAt, IncidentUpdatedAt                               time.Time
}

// AuditReviewSource reads an immutable, chained, deliberately published audit
// assertion written by the human review endpoint.
type AuditReviewSource struct{ DB *pgxpool.Pool }

type auditReviewPayload struct {
	SchemaVersion      int       `json:"schema_version"`
	DisplayApproved    bool      `json:"display_approved"`
	SupersedesReviewID string    `json:"supersedes_review_id"`
	EnvironmentID      string    `json:"environment_id"`
	RunDigest          string    `json:"run_digest"`
	RunUpdatedAt       time.Time `json:"run_updated_at"`
	ArtifactDigest     string    `json:"artifact_digest"`
	ExecutionID        string    `json:"execution_id"`
	PlanDigest         string    `json:"plan_digest"`
	CommitSHA          string    `json:"commit_sha"`
	IncidentID         string    `json:"incident_id"`
	IncidentUpdatedAt  time.Time `json:"incident_updated_at"`
	DiffDigest         string    `json:"diff_digest"`
	Diff               struct {
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
 ORDER BY m.chain_seq DESC LIMIT 2`, appID, runID)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	type record struct {
		id        int64
		actorID   string
		createdAt time.Time
		raw       []byte
		chainHash string
	}
	var records []record
	for rows.Next() {
		var item record
		if err = rows.Scan(&item.id, &item.actorID, &item.createdAt, &item.raw, &item.chainHash); err != nil {
			return ReviewEvidence{}, err
		}
		records = append(records, item)
	}
	if err = rows.Err(); err != nil {
		return ReviewEvidence{}, err
	}
	if len(records) == 0 || records[0].chainHash == "" || records[0].actorID == "" {
		return ReviewEvidence{}, errIncomplete
	}
	latest := records[0]
	zero.ReviewerID, zero.ReviewedAt, zero.ChainHash = latest.actorID, latest.createdAt, latest.chainHash
	var payload auditReviewPayload
	if err = json.Unmarshal(latest.raw, &payload); err != nil {
		return ReviewEvidence{}, errIncomplete
	}
	if (len(records) == 1 && payload.SupersedesReviewID != "") ||
		(len(records) == 2 && payload.SupersedesReviewID != strconv.FormatInt(records[1].id, 10)) {
		return ReviewEvidence{}, errIncomplete
	}
	if payload.SchemaVersion != 1 || !payload.DisplayApproved || payload.EnvironmentID != envID ||
		payload.RunDigest != digest ||
		payload.RunUpdatedAt.IsZero() || payload.IncidentUpdatedAt.IsZero() ||
		!reviewText(payload.ArtifactDigest, 128) ||
		!reviewText(payload.ExecutionID, 128) || !reviewText(payload.IncidentID, 128) ||
		!reviewText(payload.PlanDigest, 128) || !validCommitSHA(payload.CommitSHA) ||
		!reviewText(payload.Diff.Path, 300) || !reviewText(payload.Diff.Minus, 1000) ||
		!reviewText(payload.Diff.Plus, 1000) || !reviewText(payload.Risk.Title, 200) ||
		!reviewText(payload.Risk.Meta, 300) || !reviewText(payload.Risk.Text, 2000) ||
		!reviewText(payload.AlertText, 500) || payload.DiffDigest != reviewedDiffDigest(payload.Diff.Path, payload.Diff.Minus, payload.Diff.Plus) ||
		zero.ReviewedAt.After(time.Now().Add(5*time.Minute)) {
		return ReviewEvidence{}, errIncomplete
	}
	zero.ID = strconv.FormatInt(latest.id, 10)
	zero.RunID, zero.EnvironmentID, zero.Digest, zero.ArtifactDigest = runID, envID, payload.RunDigest, payload.ArtifactDigest
	zero.ExecutionID, zero.IncidentID = payload.ExecutionID, payload.IncidentID
	zero.CommitSHA, zero.PlanDigest, zero.DiffDigest = payload.CommitSHA, payload.PlanDigest, payload.DiffDigest
	zero.RunUpdatedAt, zero.IncidentUpdatedAt = payload.RunUpdatedAt, payload.IncidentUpdatedAt
	zero.DiffPath, zero.DiffMinus, zero.DiffPlus = payload.Diff.Path, payload.Diff.Minus, payload.Diff.Plus
	zero.RiskTitle, zero.RiskMeta, zero.RiskText = payload.Risk.Title, payload.Risk.Meta, payload.Risk.Text
	zero.AlertText = payload.AlertText
	return zero, nil
}

func reviewText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max &&
		!strings.ContainsAny(value, "\x00\r")
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return validHexValue(value)
}

func validSHA256Hex(value string) bool {
	return len(value) == 64 && validHexValue(value)
}

func validHexValue(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func reviewedDiffDigest(path, minus, plus string) string {
	raw, _ := json.Marshal(struct {
		Path  string `json:"path"`
		Minus string `json:"minus"`
		Plus  string `json:"plus"`
	}{path, minus, plus})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateReview(e ReviewEvidence, runID, envID, digest string) error {
	if e.RunID != runID || e.EnvironmentID != envID || e.Digest != digest || e.ID == "" || e.ReviewerID == "" || e.ChainHash == "" ||
		e.ReviewedAt.IsZero() || e.RunUpdatedAt.IsZero() || e.IncidentUpdatedAt.IsZero() ||
		!reviewText(e.ArtifactDigest, 128) || !reviewText(e.ExecutionID, 128) || !reviewText(e.IncidentID, 128) ||
		!reviewText(e.PlanDigest, 128) || !validCommitSHA(e.CommitSHA) ||
		!reviewText(e.DiffPath, 300) || !reviewText(e.DiffMinus, 1000) || !reviewText(e.DiffPlus, 1000) ||
		!reviewText(e.RiskTitle, 200) || !reviewText(e.RiskMeta, 300) || !reviewText(e.RiskText, 2000) ||
		!reviewText(e.AlertText, 500) || e.DiffDigest != reviewedDiffDigest(e.DiffPath, e.DiffMinus, e.DiffPlus) {
		return fmt.Errorf("%w: review", errIncomplete)
	}
	return nil
}
