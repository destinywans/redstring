package knowledge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Draft is not a searchable source. Only explicit confirmation of a closed
// investigation creates the immutable version referenced by DocumentID.
type Draft struct {
	Origin          string     `json:"origin"`
	GenerationRunID string     `json:"generation_run_id,omitempty"`
	GenerationModel string     `json:"generation_model,omitempty"`
	ID              string     `json:"id"`
	InvestigationID string     `json:"investigation_id"`
	ApplicationID   string     `json:"application_id"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	EvidenceIDs     []string   `json:"evidence_ids"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	ConfirmedBy     string     `json:"confirmed_by,omitempty"`
	ConfirmedAt     *time.Time `json:"confirmed_at,omitempty"`
	DocumentID      string     `json:"document_id,omitempty"`
}
type DraftInput struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// DraftRepository validates evidence provenance and commits confirmation and its
// source document together. The boolean reports whether this call created the
// document, allowing the caller to avoid duplicate embedding work on retries.
type DraftRepository interface {
	CreateDraft(context.Context, string, DraftInput, string) (Draft, error)
	ListDrafts(context.Context, string) ([]Draft, error)
	ConfirmDraft(context.Context, string, string) (string, bool, error)
}

func (s *Service) CreateDraft(ctx context.Context, inv string, in DraftInput, user string) (Draft, error) {
	if err := s.authorizeDraft(ctx, "investigation", inv, "operate"); err != nil {
		return Draft{}, err
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	if in.Title == "" || in.Body == "" || len(in.Title) > 200 || len(in.Body) > 128*1024 || len(in.EvidenceIDs) < 1 || len(in.EvidenceIDs) > 100 {
		return Draft{}, fmt.Errorf("%w: title, body and 1 to 100 evidence_ids are required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, id := range in.EvidenceIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return Draft{}, fmt.Errorf("%w: evidence_ids must be nonempty and unique", ErrInvalid)
		}
		seen[id] = true
	}
	return s.Repository.CreateDraft(ctx, inv, in, user)
}
func (s *Service) Drafts(ctx context.Context, inv string) ([]Draft, error) {
	if err := s.authorizeDraft(ctx, "investigation", inv, "view"); err != nil {
		return nil, err
	}
	return s.Repository.ListDrafts(ctx, inv)
}
func (s *Service) ConfirmDraft(ctx context.Context, id, user string) (Document, error) {
	if err := s.authorizeDraft(ctx, "draft", id, "operate"); err != nil {
		return Document{}, err
	}
	document, created, err := s.Repository.ConfirmDraft(ctx, id, user)
	if err != nil {
		return Document{}, err
	}
	if created {
		// Publication is an explicit human decision already committed with provenance.
		// Embedding availability cannot undo it. Reindex can retry this same source.
		_ = s.Index(ctx, document)
	}
	return s.Get(ctx, document)
}

var _ DraftRepository = (*PGRepository)(nil)

func (s *Service) authorizeDraft(ctx context.Context, kind, id, permission string) error {
	r, ok := s.Repository.(interface {
		DraftApplication(context.Context, string, string) (string, error)
	})
	if !ok {
		if s.Authorize != nil {
			return ErrForbidden
		}
		return nil
	}
	app, err := r.DraftApplication(ctx, kind, id)
	if err != nil {
		return err
	}
	return s.authorize(ctx, app, permission)
}
