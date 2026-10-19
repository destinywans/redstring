package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/store"
)

var ErrForbidden = errors.New("knowledge access denied")

// Metadata is versioned together with source content. Applicability never grants
// access: shared applications must be explicitly authorized by the writer.
type Metadata struct {
	MaintainerID         string   `json:"maintainer_id"`
	TeamID               string   `json:"team_id,omitempty"`
	Origin               string   `json:"origin"`
	ActorID              string   `json:"actor_id"`
	GenerationRunID      string   `json:"generation_run_id,omitempty"`
	GenerationModel      string   `json:"generation_model,omitempty"`
	EvidenceIDs          []string `json:"evidence_ids,omitempty"`
	EnvironmentIDs       []string `json:"environment_ids,omitempty"`
	ResourceCategories   []string `json:"resource_categories,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	SharedApplicationIDs []string `json:"shared_application_ids,omitempty"`
	Visibility           string   `json:"visibility"`
}
type Principal struct{ ID, Role string }
type principalKey struct{}

func WithPrincipal(ctx context.Context, id, role string) context.Context {
	return context.WithValue(ctx, principalKey{}, Principal{id, role})
}
func PrincipalFromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	if p.ID == "" {
		u := store.Principal(ctx)
		p = Principal{u.ID, u.Role}
	}
	return p
}

type GovernanceRepository interface {
	UpdateDocument(context.Context, string, UpdateInput, string) (Document, error)
	TransitionDocument(context.Context, string, int, string, string) (Document, error)
	Revisions(context.Context, string) ([]Document, error)
	Revision(context.Context, string, int) (Document, error)
	KeywordSearch(context.Context, string, string, string, int) ([]Hit, error)
	ValidateMetadata(context.Context, string, Metadata) error
}
type UpdateInput struct {
	ExpectedVersion int      `json:"expected_version"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Source          string   `json:"source"`
	Metadata        Metadata `json:"metadata"`
}
type TransitionInput struct {
	ExpectedVersion int    `json:"expected_version"`
	State           string `json:"state"`
}
type Template struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func Templates() []Template {
	return []Template{
		{"blank", "空白 Runbook", ""},
		{"investigation", "通用故障调查", "# 适用范围\n\n## 症状与触发条件\n\n## 前置条件\n\n## 取证步骤\n\n## 假设与排除依据\n\n## 操作与审批\n\n## 风险与回滚\n\n## 恢复验证\n\n## 参考资料\n"},
		{"change-recovery", "变更与恢复", "# 适用范围\n\n## 变更目标与前置条件\n\n## 影响范围与风险\n\n## 变更前取证\n\n## 操作步骤与人工审批\n\n## 回滚条件与步骤\n\n## 恢复验证与观察窗口\n\n## 参考资料\n"},
	}
}
func (s *Service) governance() (GovernanceRepository, error) {
	r, ok := s.Repository.(GovernanceRepository)
	if !ok {
		return nil, errors.New("repository does not support knowledge governance")
	}
	return r, nil
}
func (s *Service) authorize(ctx context.Context, app, permission string) error {
	if s.Authorize == nil {
		return nil
	} // Composition supplies authorization; portable repositories may be used alone.
	p := PrincipalFromContext(ctx)
	if p.ID == "" {
		return ErrForbidden
	}
	if app == "" {
		if permission == "view" || p.Role == "admin" {
			return nil
		}
		return ErrForbidden
	}
	if err := s.Authorize(ctx, p.ID, app, permission); err != nil {
		return ErrForbidden
	}
	return nil
}
func (s *Service) access(ctx context.Context, d Document, permission string) error {
	if permission != "view" {
		return s.authorize(ctx, d.ApplicationID, permission)
	}
	if d.Metadata.Visibility == "workspace" {
		return s.authorize(ctx, "", "view")
	}
	if s.authorize(ctx, d.ApplicationID, "view") == nil && d.ApplicationID != "" {
		return nil
	}
	for _, app := range d.Metadata.SharedApplicationIDs {
		if s.authorize(ctx, app, "view") == nil {
			return nil
		}
	}
	if s.Authorize == nil {
		return nil
	}
	return ErrForbidden
}
func (s *Service) normalizeMetadata(ctx context.Context, app, user string, m Metadata) (Metadata, error) {
	if m.MaintainerID == "" {
		m.MaintainerID = user
	}
	if m.Visibility == "" {
		m.Visibility = "application"
	}
	if s.Authorize != nil && app != "" {
		if err := s.Authorize(ctx, m.MaintainerID, app, "operate"); err != nil {
			return m, fmt.Errorf("%w: maintainer needs application operate permission", ErrInvalid)
		}
	}
	if m.Visibility != "application" && m.Visibility != "workspace" {
		return m, fmt.Errorf("%w: unsupported visibility", ErrInvalid)
	}
	if app == "" && m.Visibility != "workspace" {
		return m, fmt.Errorf("%w: application_id is required for private knowledge", ErrInvalid)
	}
	if m.Visibility == "workspace" {
		if s.Authorize != nil && PrincipalFromContext(ctx).Role != "admin" {
			return m, ErrForbidden
		}
	}
	if len(m.Tags) > 32 || len(m.EnvironmentIDs) > 32 || len(m.SharedApplicationIDs) > 32 || len(m.ResourceCategories) > 32 {
		return m, fmt.Errorf("%w: metadata lists are limited to 32 entries", ErrInvalid)
	}
	for _, list := range [][]string{m.Tags, m.EnvironmentIDs, m.SharedApplicationIDs, m.ResourceCategories} {
		for _, v := range list {
			if strings.TrimSpace(v) == "" || len(v) > 200 {
				return m, fmt.Errorf("%w: invalid metadata entry", ErrInvalid)
			}
		}
	}
	for _, shared := range m.SharedApplicationIDs {
		if err := s.authorize(ctx, shared, "operate"); err != nil {
			return m, err
		}
	}
	if r, ok := s.Repository.(GovernanceRepository); ok {
		if err := r.ValidateMetadata(ctx, app, m); err != nil {
			return m, err
		}
	}
	return m, nil
}
func validateContent(title, body, source string) error {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" || len(title) > 200 || len(body) > 128*1024 || len(source) > 2048 {
		return fmt.Errorf("%w: title and body required; body limit 128 KiB", ErrInvalid)
	}
	return nil
}
func (s *Service) Update(ctx context.Context, id string, in UpdateInput, user string) (Document, error) {
	d, err := s.legacyGet(ctx, id)
	if err != nil {
		return d, err
	}
	if err = s.access(ctx, d, "operate"); err != nil {
		return Document{}, err
	}
	if err = validateContent(in.Title, in.Body, in.Source); err != nil {
		return Document{}, err
	}
	// Clients cannot forge Agent provenance or rewrite the original creator.
	in.Metadata.Origin = d.Metadata.Origin
	in.Metadata.ActorID = d.Metadata.ActorID
	in.Metadata.GenerationRunID = d.Metadata.GenerationRunID
	in.Metadata.GenerationModel = d.Metadata.GenerationModel
	in.Metadata.EvidenceIDs = d.Metadata.EvidenceIDs
	in.Metadata, err = s.normalizeMetadata(ctx, d.ApplicationID, user, in.Metadata)
	if err != nil {
		return Document{}, err
	}
	repo, err := s.governance()
	if err != nil {
		return Document{}, err
	}
	return repo.UpdateDocument(ctx, id, in, user)
}
func (s *Service) Transition(ctx context.Context, id string, in TransitionInput, user string) (Document, error) {
	d, err := s.legacyGet(ctx, id)
	if err != nil {
		return d, err
	}
	if err = s.access(ctx, d, "operate"); err != nil {
		return Document{}, err
	}
	if d.Metadata.Visibility == "workspace" && s.Authorize != nil && PrincipalFromContext(ctx).Role != "admin" {
		return Document{}, ErrForbidden
	}
	allowed := map[string]map[string]bool{"draft": {"review": true, "archived": true}, "review": {"draft": true, "published": true, "archived": true}, "published": {"archived": true}, "archived": {"draft": true}}
	if !allowed[d.Lifecycle][in.State] {
		return Document{}, fmt.Errorf("%w: invalid lifecycle transition", ErrInvalid)
	}
	// Publication is only reachable through authenticated human HTTP calls; Agent
	// tools expose Search only, and automatic draft generation never calls this.
	if user == "" {
		return Document{}, ErrForbidden
	}
	repo, err := s.governance()
	if err != nil {
		return Document{}, err
	}
	d, err = repo.TransitionDocument(ctx, id, in.ExpectedVersion, in.State, user)
	if err == nil && in.State == "published" {
		_ = s.Index(ctx, id)
		d, err = s.Get(ctx, id)
	}
	return d, err
}
func (s *Service) Revisions(ctx context.Context, id string) ([]Document, error) {
	if _, err := s.legacyGet(ctx, id); err != nil {
		return nil, err
	}
	r, err := s.governance()
	if err != nil {
		return nil, err
	}
	ds, err := r.Revisions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []Document{}
	for _, d := range ds {
		if s.access(ctx, d, "view") == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *Service) Revision(ctx context.Context, id string, version int) (Document, error) {
	if _, err := s.legacyGet(ctx, id); err != nil {
		return Document{}, err
	}
	r, err := s.governance()
	if err != nil {
		return Document{}, err
	}
	d, err := r.Revision(ctx, id, version)
	if err == nil {
		err = s.access(ctx, d, "view")
	}
	return d, err
}

type Export struct {
	Format     string     `json:"format"`
	DocumentID string     `json:"document_id"`
	ExportedAt time.Time  `json:"exported_at"`
	Revisions  []Document `json:"revisions"`
	Document   Document   `json:"document"`
}

func (s *Service) Export(ctx context.Context, id string) (Export, error) {
	d, err := s.legacyGet(ctx, id)
	if err != nil {
		return Export{}, err
	}
	ds, err := s.Revisions(ctx, id)
	return Export{Format: "aiops-knowledge-v1", DocumentID: id, ExportedAt: time.Now().UTC(), Revisions: ds, Document: d}, err
}
