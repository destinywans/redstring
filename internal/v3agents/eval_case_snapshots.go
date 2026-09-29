package v3agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

// EvalCaseView is the complete V3 case DTO. Only a trusted evaluator that has
// frozen the source input and recorded both model runs and scoring may submit
// one. Public HTTP requests cannot write this payload.
type EvalCaseView struct {
	Queue    string             `json:"queue"`
	Alert    EvalCaseAlert      `json:"alert"`
	Evidence []EvalCaseEvidence `json:"evidence"`
	Expected EvalCaseExpected   `json:"expected"`
	Output   EvalCaseOutput     `json:"output"`
	Baseline EvalCaseBaseline   `json:"baseline"`
	Auto     EvalCaseAuto       `json:"auto"`
}

type EvalCaseAlert struct {
	Sev    string          `json:"sev"`
	Title  string          `json:"title"`
	Labels []EvalCaseLabel `json:"labels"`
}
type EvalCaseLabel struct {
	K string `json:"k"`
	V string `json:"v"`
}
type EvalCaseEvidence struct {
	Brand string `json:"brand"`
	Title string `json:"title"`
	Sub   string `json:"sub"`
}
type EvalCaseExpected struct {
	Cause  string `json:"cause"`
	Advice string `json:"advice"`
}
type EvalCaseOutput struct {
	Version  string   `json:"version"`
	Meta     string   `json:"meta"`
	Tools    []string `json:"tools"`
	Markdown string   `json:"markdown"`
}
type EvalCaseBaseline struct {
	Version string `json:"version"`
	Score   string `json:"score"`
	Text    string `json:"text"`
}
type EvalCaseAuto struct {
	Score string   `json:"score"`
	Note  string   `json:"note"`
	Rules []string `json:"rules"`
}

type EvalCaseSnapshot struct {
	CaseID       string
	ProducerRef  string
	InputRef     string
	ExpectedRef  string
	OutputRef    string
	BaselineRef  string
	EvaluatorRef string
	View         EvalCaseView
}

func validEvalCaseView(v EvalCaseView) bool {
	if !bounded(v.Queue, 240) || !bounded(v.Alert.Title, 500) ||
		(v.Alert.Sev != "P1" && v.Alert.Sev != "P2" && v.Alert.Sev != "P3" && v.Alert.Sev != "P4") ||
		v.Alert.Labels == nil || len(v.Alert.Labels) > 64 || len(v.Evidence) == 0 || len(v.Evidence) > 64 ||
		!bounded(v.Expected.Cause, 20000) || !bounded(v.Expected.Advice, 20000) ||
		!safeCode(v.Output.Version, 80) || !bounded(v.Output.Meta, 1000) ||
		!bounded(v.Output.Markdown, 100000) || v.Output.Tools == nil || len(v.Output.Tools) > 64 ||
		!safeCode(v.Baseline.Version, 80) || !bounded(v.Baseline.Score, 80) ||
		!bounded(v.Baseline.Text, 100000) || !bounded(v.Auto.Score, 80) ||
		!bounded(v.Auto.Note, 2000) || v.Auto.Rules == nil || len(v.Auto.Rules) > 64 {
		return false
	}
	for _, label := range v.Alert.Labels {
		if !bounded(label.K, 160) || !bounded(label.V, 1000) {
			return false
		}
	}
	for _, evidence := range v.Evidence {
		if !safeCode(evidence.Brand, 80) || !bounded(evidence.Title, 500) || !bounded(evidence.Sub, 2000) {
			return false
		}
	}
	for _, tool := range v.Output.Tools {
		if !safeCode(tool, 160) {
			return false
		}
	}
	for _, rule := range v.Auto.Rules {
		if !bounded(rule, 1000) {
			return false
		}
	}
	return true
}

func validEvalEvidenceRef(ref string) bool {
	return safeCode(ref, 500) && !strings.HasPrefix(strings.ToLower(ref), "vault:")
}

// RecordEvalCaseSnapshot appends a complete report from a trusted evaluator.
// The caller is the trusted evaluator integration, not a user-supplied HTTP
// request. The caller reports opaque references for the input, expected answer,
// two model outputs and evaluator result. This service does not fetch those
// external artifacts; it freezes the reported rendering and its fingerprint.
func (s *Service) RecordEvalCaseSnapshot(ctx context.Context, snapshot EvalCaseSnapshot) error {
	if s == nil || s.DB == nil || !bounded(snapshot.CaseID, 128) ||
		!validEvalEvidenceRef(snapshot.ProducerRef) || !validEvalEvidenceRef(snapshot.InputRef) ||
		!validEvalEvidenceRef(snapshot.ExpectedRef) || !validEvalEvidenceRef(snapshot.OutputRef) ||
		!validEvalEvidenceRef(snapshot.BaselineRef) || !validEvalEvidenceRef(snapshot.EvaluatorRef) ||
		!validEvalCaseView(snapshot.View) {
		return errors.New("invalid evaluation case snapshot")
	}
	payload, err := json.Marshal(snapshot.View)
	if err != nil || len(payload) > 262144 {
		return errors.New("evaluation case snapshot too large")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(payload))
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCaseTx(ctx, tx, snapshot.CaseID, "FOR UPDATE")
	if err != nil {
		return err
	}
	if c.ExpectedRef == "" || c.ExpectedRef != snapshot.ExpectedRef {
		return errors.New("evaluation case expected source mismatch")
	}
	a, err := lockEvalAgent(ctx, tx, c.AgentID)
	if err != nil {
		return err
	}
	if c.OriginApp != a.App || c.OriginEnv != a.Env {
		return errors.New("evaluation case origin scope changed")
	}
	if err = lockActiveEvalScope(ctx, tx, a); err != nil {
		return err
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_agent_versions
 WHERE agent_id=$1 AND version=ANY($2)`, a.ID,
		[]string{snapshot.View.Output.Version, snapshot.View.Baseline.Version}).Scan(&count)
	if err != nil {
		return err
	}
	if snapshot.View.Output.Version == snapshot.View.Baseline.Version || count != 2 {
		return errors.New("evaluation case references unknown or identical agent versions")
	}
	// The case row lock serializes trusted writers. A per-case revision gives
	// latest-snapshot reads a stable order even if wall-clock time changes.
	var revision int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1
 FROM v3_agent_eval_case_snapshots WHERE case_id=$1`, c.ID).Scan(&revision)
	if err != nil {
		return err
	}
	id := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_case_snapshots
	 (id,case_id,agent_id,revision,producer_ref,input_ref,expected_ref,output_ref,baseline_ref,evaluator_ref,payload,payload_sha256)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, c.ID, a.ID, revision,
		snapshot.ProducerRef, snapshot.InputRef, snapshot.ExpectedRef,
		snapshot.OutputRef, snapshot.BaselineRef, snapshot.EvaluatorRef, payload, fingerprint)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.eval.case.snapshot", c.ID,
		map[string]any{"environment_id": a.Env, "snapshot_id": id,
			"snapshot_revision": revision, "producer_ref": snapshot.ProducerRef,
			"payload_sha256": fingerprint}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func loadCaseTx(ctx context.Context, tx pgx.Tx, id, lock string) (caseRecord, error) {
	if lock != "FOR SHARE" && lock != "FOR UPDATE" {
		return caseRecord{}, fmt.Errorf("invalid case lock")
	}
	var c caseRecord
	err := tx.QueryRow(ctx, `SELECT id,agent_id,title,expected_ref,
 COALESCE(origin_application_id,''),COALESCE(origin_environment_id,'')
 FROM v3_agent_eval_cases WHERE id=$1 `+lock, id).
		Scan(&c.ID, &c.AgentID, &c.Title, &c.ExpectedRef, &c.OriginApp, &c.OriginEnv)
	return c, err
}

func loadEvalCaseSnapshot(ctx context.Context, tx pgx.Tx, c caseRecord) (*EvalCaseView, string, string, error) {
	var payload []byte
	var id, agentID, expectedRef, fingerprint string
	err := tx.QueryRow(ctx, `SELECT id,agent_id,expected_ref,payload,payload_sha256
 FROM v3_agent_eval_case_snapshots WHERE case_id=$1
 ORDER BY revision DESC LIMIT 1 FOR SHARE`, c.ID).
		Scan(&id, &agentID, &expectedRef, &payload, &fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	if agentID != c.AgentID || expectedRef != c.ExpectedRef || expectedRef == "" {
		return nil, "", "", errors.New("evaluation case snapshot provenance mismatch")
	}
	var view EvalCaseView
	if err = json.Unmarshal(payload, &view); err != nil || !validEvalCaseView(view) {
		return nil, "", "", errors.New("invalid stored evaluation case snapshot")
	}
	canonical, err := json.Marshal(view)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(canonical)) != fingerprint {
		return nil, "", "", errors.New("evaluation case snapshot fingerprint mismatch")
	}
	return &view, id, fingerprint, nil
}
