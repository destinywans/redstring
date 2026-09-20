package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

type ExperienceEvidence struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source"`
}
type ExperienceInput struct {
	InvestigationID string               `json:"investigation_id"`
	ApplicationID   string               `json:"application_id"`
	EnvironmentID   string               `json:"environment_id"`
	Title           string               `json:"title"`
	Evidence        []ExperienceEvidence `json:"evidence"`
	Recovery        json.RawMessage      `json:"recovery"`
}
type GeneratedExperience struct {
	DraftInput
	RunID string
	Model string
}
type ExperienceGenerator func(context.Context, ExperienceInput) (GeneratedExperience, error)

// GenerateExperienceDraft calls the configured model with committed evidence.
// One lease and one final draft per investigation protect automatic retries and
// manual duplicate requests. A failed/expired lease can be retried explicitly.
// The caller decides the per-environment automatic-generation policy.
func (s *Service) GenerateExperienceDraft(ctx context.Context, inv, user string, automatic bool) (Draft, error) {
	if err := s.authorizeDraft(ctx, "investigation", inv, "operate"); err != nil {
		return Draft{}, err
	}
	if automatic && (s.AutoExperienceEnabled == nil || !s.AutoExperienceEnabled(ctx, inv)) {
		return Draft{}, fmt.Errorf("%w: automatic experience generation is disabled", ErrInvalid)
	}
	if s.ExperienceGenerator == nil {
		return Draft{}, errors.New("experience generation model is not configured")
	}
	repo, ok := s.Repository.(*PGRepository)
	if !ok {
		return Draft{}, errors.New("experience generation requires a durable reservation repository")
	}
	input, err := repo.experienceInput(ctx, inv)
	if err != nil {
		return Draft{}, err
	}
	token := store.ID()
	existing, err := repo.claimExperience(ctx, inv, token)
	if err != nil {
		return Draft{}, err
	}
	if existing != "" {
		return scanDraft(repo.DB.QueryRow(ctx, selectDraft+" WHERE d.id=$1", existing))
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	generated, err := s.ExperienceGenerator(runCtx, input)
	if err == nil {
		err = validateContent(generated.Title, generated.Body, "")
	}
	if err == nil && (generated.Model == "" || generated.RunID == "" || len(generated.EvidenceIDs) == 0 || len(generated.EvidenceIDs) > 100) {
		err = fmt.Errorf("%w: generated draft requires model, run and evidence provenance", ErrInvalid)
	}
	if err == nil {
		err = s.authorizeDraft(runCtx, "investigation", inv, "operate")
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = repo.DB.Exec(cleanup, "UPDATE knowledge_experience_generations SET status='failed',last_error='Generation failed; retry explicitly',updated_at=now() WHERE investigation_id=$1 AND token=$2 AND status='running'", inv, token)
		return Draft{}, err
	}
	return repo.completeExperience(runCtx, input, generated, user, token)
}
func (s *PGRepository) experienceInput(ctx context.Context, inv string) (ExperienceInput, error) {
	var in ExperienceInput
	var status string
	err := s.DB.QueryRow(ctx, "SELECT id,application_id,coalesce(environment_id,''),title,status FROM investigations WHERE id=$1", inv).Scan(&in.InvestigationID, &in.ApplicationID, &in.EnvironmentID, &in.Title, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = store.ErrNotFound
	}
	if err != nil {
		return in, err
	}
	if status != "closed" {
		return in, store.ErrConflict
	}
	// Only a successful, approved recovery establishes the closure basis. AI
	// hypotheses alone must not become an experience draft labeled successful.
	err = s.DB.QueryRow(ctx, "SELECT x.result FROM executions x JOIN action_plans p ON p.id=x.plan_id WHERE p.investigation_id=$1 AND x.status='succeeded' ORDER BY x.updated_at DESC LIMIT 1", inv).Scan(&in.Recovery)
	if errors.Is(err, pgx.ErrNoRows) {
		err = store.ErrConflict
	}
	if err != nil {
		return in, err
	}
	rows, err := s.DB.Query(ctx, "SELECT e.id,e.title,left(e.content,12000),e.source FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE b.investigation_id=$1 ORDER BY e.created_at DESC,e.id LIMIT 40", inv)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	in.Evidence = []ExperienceEvidence{}
	budget := 48000
	for rows.Next() {
		var e ExperienceEvidence
		if err = rows.Scan(&e.ID, &e.Title, &e.Content, &e.Source); err != nil {
			return in, err
		}
		if len(e.Content) > budget {
			continue
		}
		budget -= len(e.Content)
		in.Evidence = append(in.Evidence, e)
	}
	if err = rows.Err(); err != nil {
		return in, err
	}
	if len(in.Evidence) == 0 {
		return in, fmt.Errorf("%w: no committed evidence", ErrInvalid)
	}
	return in, nil
}
func (s *PGRepository) claimExperience(ctx context.Context, inv, token string) (string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO knowledge_experience_generations(investigation_id,status,lease_until,token) VALUES($1,'pending',now(),$2) ON CONFLICT DO NOTHING", inv, token)
	if err != nil {
		return "", err
	}
	var status, draft string
	var live bool
	err = tx.QueryRow(ctx, "SELECT status,coalesce(draft_id,''),lease_until>now() FROM knowledge_experience_generations WHERE investigation_id=$1 FOR UPDATE", inv).Scan(&status, &draft, &live)
	if err != nil {
		return "", err
	}
	if status == "completed" {
		return draft, tx.Commit(ctx)
	}
	if status == "running" && live {
		return "", store.ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE knowledge_experience_generations SET status='running',token=$2,lease_until=now()+interval '3 minutes',last_error='',updated_at=now() WHERE investigation_id=$1", inv, token)
	if err != nil {
		return "", err
	}
	return "", tx.Commit(ctx)
}
func (s *PGRepository) completeExperience(ctx context.Context, in ExperienceInput, g GeneratedExperience, user, token string) (Draft, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, "SELECT status FROM investigations WHERE id=$1 FOR UPDATE", in.InvestigationID).Scan(&status); err != nil {
		return Draft{}, err
	}
	if status != "closed" {
		return Draft{}, store.ErrConflict
	}
	var valid bool
	err = tx.QueryRow(ctx, "SELECT token=$2 AND status='running' AND lease_until>now() FROM knowledge_experience_generations WHERE investigation_id=$1 FOR UPDATE", in.InvestigationID, token).Scan(&valid)
	if err != nil {
		return Draft{}, err
	}
	if !valid {
		return Draft{}, store.ErrConflict
	}
	seen := map[string]bool{}
	available := map[string]bool{}
	for _, e := range in.Evidence {
		available[e.ID] = true
	}
	for _, id := range g.EvidenceIDs {
		if seen[id] || !available[id] {
			return Draft{}, fmt.Errorf("%w: generated evidence must come from supplied input", ErrInvalid)
		}
		seen[id] = true
	}
	if err = validateDraftEvidence(ctx, tx, in.InvestigationID, g.EvidenceIDs); err != nil {
		return Draft{}, err
	}
	id := store.ID()
	ids, _ := json.Marshal(g.EvidenceIDs)
	_, err = tx.Exec(ctx, "INSERT INTO knowledge_drafts(id,investigation_id,application_id,title,body,evidence_ids,created_by,origin,generation_run_id,generation_model) VALUES($1,$2,$3,$4,$5,$6,$7,'agent',$8,$9)", id, in.InvestigationID, in.ApplicationID, g.Title, g.Body, ids, user, g.RunID, g.Model)
	if err != nil {
		return Draft{}, err
	}
	_, err = tx.Exec(ctx, "UPDATE knowledge_experience_generations SET status='completed',draft_id=$2,updated_at=now() WHERE investigation_id=$1", in.InvestigationID, id)
	if err != nil {
		return Draft{}, err
	}
	if err = draftEvent(ctx, tx, in.InvestigationID, "knowledge.drafted", map[string]any{"draft_id": id, "origin": "agent", "generation_run_id": g.RunID, "generation_model": g.Model, "evidence_ids": g.EvidenceIDs}); err != nil {
		return Draft{}, err
	}
	d, err := scanDraft(tx.QueryRow(ctx, selectDraft+" WHERE d.id=$1", id))
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
