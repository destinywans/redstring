// Package v3proposals owns the V3 review record and discussion. It has no
// approval or execution API; historical action_plans are a different model.
package v3proposals

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

// Register only exposes review operations. The object route marker leaves the
// exact persisted application check to this service after resolving the ID.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/proposals/{id}", "view", s.Detail},
		{"POST /api/v1/proposals/{id}/comments", "change", s.Comment},
		{"POST /api/v1/proposals/{id}/reject", "manage", s.Reject},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, "proposals", route.action)
	}
}

type Diff struct {
	Path  string   `json:"path"`
	Lines []string `json:"lines"`
}
type Evidence struct {
	N      int       `json:"n"`
	Icon   string    `json:"icon"`
	Title  string    `json:"title"`
	Desc   string    `json:"desc"`
	Series []float64 `json:"series,omitempty"`
}
type Content struct {
	Title            string     `json:"title"`
	Risk             string     `json:"risk"`
	Impact           string     `json:"impact"`
	Reversible       bool       `json:"reversible"`
	ProposedBy       string     `json:"proposed_by"`
	InvestigationID  string     `json:"investigation_id"`
	Target           string     `json:"target"`
	Diff             Diff       `json:"diff"`
	Method           string     `json:"method"`
	Evidence         []Evidence `json:"evidence"`
	Verification     []string   `json:"verification"`
	VerificationNote string     `json:"verification_note"`
	Checklist        []string   `json:"checklist"`
	Requirement      string     `json:"requirement"`
}
type CommentItem struct {
	Author string `json:"author"`
	AI     bool   `json:"ai,omitempty"`
	Time   string `json:"time"`
	Text   string `json:"text"`
}
type Proposal struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ProposedAt string `json:"proposed_at"`
	Content
	Discussion []CommentItem `json:"discussion"`
}

// NewProposal is accepted only from an in-process trusted producer. The
// producer must supply sourced review evidence; this is not a browser route.
type NewProposal struct {
	ApplicationID string
	EnvironmentID string
	CreatedBy     string
	CreatorKind   string // user or agent
	Content
}

func bounded(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && len([]rune(s)) <= max
}

func validContent(c Content) bool {
	if !bounded(c.Title, 300) || !bounded(c.Impact, 1000) || !bounded(c.ProposedBy, 300) ||
		!bounded(c.InvestigationID, 128) || !bounded(c.Target, 1000) ||
		!bounded(c.Diff.Path, 1000) || !bounded(c.Method, 4000) ||
		!bounded(c.VerificationNote, 2000) || !bounded(c.Requirement, 2000) ||
		(c.Risk != "low" && c.Risk != "medium" && c.Risk != "high") ||
		len(c.Diff.Lines) == 0 || len(c.Diff.Lines) > 200 ||
		len(c.Evidence) == 0 || len(c.Evidence) > 50 ||
		len(c.Verification) == 0 || len(c.Verification) > 50 ||
		len(c.Checklist) == 0 || len(c.Checklist) > 50 {
		return false
	}
	for _, line := range c.Diff.Lines {
		if strings.TrimSpace(line) == "" || len([]rune(line)) > 2000 {
			return false
		}
	}
	for _, e := range c.Evidence {
		if e.N < 1 || !bounded(e.Icon, 80) || !bounded(e.Title, 500) || !bounded(e.Desc, 2000) || len(e.Series) > 200 {
			return false
		}
	}
	for _, s := range append(append([]string{}, c.Verification...), c.Checklist...) {
		if !bounded(s, 2000) {
			return false
		}
	}
	return true
}

var ErrInvalidProposal = errors.New("invalid V3 proposal")

// Create requires an existing open investigation in the same active
// application and mapped environment. It stores no executable plan.
func (s *Service) Create(ctx context.Context, in NewProposal) (string, error) {
	if s == nil || s.DB == nil || !validContent(in.Content) ||
		!bounded(in.ApplicationID, 128) || !bounded(in.EnvironmentID, 128) ||
		!bounded(in.CreatedBy, 128) || (in.CreatorKind != "user" && in.CreatorKind != "agent") {
		return "", ErrInvalidProposal
	}
	raw, err := json.Marshal(in.Content)
	if err != nil {
		return "", err
	}
	id := store.ID()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 SELECT $1::text,i.id,i.application_id,$2::text,$3::jsonb,$4::text,$5::text
 FROM investigations i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=$2
 JOIN application_environments e ON e.id=i.environment_id
   AND e.application_id=i.application_id AND e.key=ve.environment_id AND NOT e.archived
 WHERE i.id=$6 AND i.application_id=$7 AND i.status<>'closed'
 RETURNING id`, id, in.EnvironmentID, raw, in.CreatedBy, in.CreatorKind, in.InvestigationID, in.ApplicationID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,'v3.proposal.created',$4,'success',jsonb_build_object('environment_id',$5::text,'investigation_id',$6::text))`,
		in.CreatedBy, in.CreatorKind, in.ApplicationID, id, in.EnvironmentID, in.InvestigationID)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

type record struct {
	ID, InvestigationID, ApplicationID, EnvironmentID, Status string
	Content                                                   Content
	CreatedAt                                                 time.Time
}

func (s *Service) load(ctx context.Context, id string) (record, error) {
	var v record
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT p.id,p.investigation_id,p.application_id,p.environment_id,p.status,p.content,p.created_at
 FROM v3_proposals p
 JOIN investigations i ON i.id=p.investigation_id AND i.application_id=p.application_id
 JOIN applications a ON a.id=p.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=p.application_id AND ve.environment_id=p.environment_id
 JOIN application_environments e ON e.application_id=p.application_id AND e.key=ve.environment_id AND NOT e.archived
 WHERE p.id=$1`, id).Scan(&v.ID, &v.InvestigationID, &v.ApplicationID, &v.EnvironmentID, &v.Status, &raw, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v.Content); err != nil || !validContent(v.Content) {
		return record{}, ErrInvalidProposal
	}
	if v.Content.InvestigationID != v.InvestigationID {
		return record{}, ErrInvalidProposal
	}
	return v, nil
}

func (s *Service) scoped(w http.ResponseWriter, r *http.Request, action string) (record, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "proposal_unavailable", "Proposal storage or policy is unavailable")
		return record{}, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return record{}, false
	}
	if action != "view" && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment before writing")
		return record{}, false
	}
	v, err := s.load(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Proposal not found")
		return record{}, false
	}
	if err != nil {
		httpapi.Error(w, 503, "proposal_unavailable", "Proposal unavailable")
		return record{}, false
	}
	allowed := false
	for _, id := range scope.EnvironmentIDs {
		allowed = allowed || id == v.EnvironmentID
	}
	if !allowed {
		httpapi.Error(w, 404, "not_found", "Proposal not found")
		return record{}, false
	}
	err = s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "proposals", Action: action, EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Proposal permission required")
		return record{}, false
	}
	if err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Proposal authorization unavailable")
		return record{}, false
	}
	return v, true
}

// A review write must recheck the persisted grant on its transaction's
// connection after locking the proposal. The earlier HTTP check is useful for
// response scoping, but a grant may be revoked before the write begins.
func (s *Service) authorizeLocked(w http.ResponseWriter, r *http.Request, tx pgx.Tx, v record, action string) bool {
	err := s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "proposals", Action: action, EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Proposal permission required")
		return false
	}
	if err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Proposal authorization unavailable")
		return false
	}
	return true
}

func (s *Service) detail(ctx context.Context, v record) (Proposal, error) {
	out := Proposal{ID: v.ID, Status: v.Status, ProposedAt: v.CreatedAt.UTC().Format(time.RFC3339), Content: v.Content, Discussion: []CommentItem{}}
	rows, err := s.DB.Query(ctx, `SELECT COALESCE(NULLIF(u.name,''),c.author_id),c.created_at,c.body
 FROM v3_proposal_comments c JOIN users u ON u.id=c.author_id
 WHERE c.proposal_id=$1 ORDER BY c.created_at,c.id`, v.ID)
	if err != nil {
		return Proposal{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var author, body string
		var at time.Time
		if err = rows.Scan(&author, &at, &body); err != nil {
			return Proposal{}, err
		}
		out.Discussion = append(out.Discussion, CommentItem{Author: author, Time: at.UTC().Format(time.RFC3339), Text: body})
	}
	return out, rows.Err()
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	v, ok := s.scoped(w, r, "view")
	if !ok {
		return
	}
	out, err := s.detail(r.Context(), v)
	if err != nil {
		httpapi.Error(w, 503, "proposal_unavailable", "Proposal unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (s *Service) Comment(w http.ResponseWriter, r *http.Request) {
	v, ok := s.scoped(w, r, "change")
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if !bounded(in.Text, 4000) {
		httpapi.Error(w, 422, "invalid_comment", "Comment must contain 1 to 4000 characters")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !idempotencyKeyPattern.MatchString(key) {
		httpapi.Error(w, 422, "invalid_idempotency_key", "Invalid idempotency key")
		return
	}
	ctx := r.Context()
	user := httpapi.CurrentUser(r)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM v3_proposals WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR UPDATE`, v.ID, v.ApplicationID, v.EnvironmentID).Scan(&status)
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizeLocked(w, r, tx, v, "change") {
		return
	}
	hash := store.Hash(in.Text)
	if key != "" {
		var priorHash, author, body string
		var at time.Time
		err = tx.QueryRow(ctx, `SELECT c.request_hash,COALESCE(NULLIF(u.name,''),c.author_id),c.body,c.created_at
 FROM v3_proposal_comments c JOIN users u ON u.id=c.author_id
 WHERE c.proposal_id=$1 AND c.author_id=$2 AND c.idempotency_key=$3`, v.ID, user.ID, key).Scan(&priorHash, &author, &body, &at)
		if err == nil {
			if priorHash != hash {
				httpapi.Error(w, 409, "idempotency_conflict", "Key was used for a different comment")
				return
			}
			httpapi.WriteJSON(w, 200, CommentItem{Author: author, Time: at.UTC().Format(time.RFC3339), Text: body})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			unavailable(w)
			return
		}
	}
	if status != "pending" {
		httpapi.Error(w, 409, "proposal_closed", "Proposal is no longer pending")
		return
	}
	id := store.ID()
	var at time.Time
	err = tx.QueryRow(ctx, `INSERT INTO v3_proposal_comments(id,proposal_id,author_id,body,idempotency_key,request_hash)
 VALUES($1,$2,$3,$4,NULLIF($5,''),$6) RETURNING created_at`, id, v.ID, user.ID, in.Text, key, hash).Scan(&at)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.proposal.comment_added',$3,'success',jsonb_build_object('environment_id',$4::text,'comment_id',$5::text))`, user.ID, v.ApplicationID, v.ID, v.EnvironmentID, id)
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		unavailable(w)
		return
	}
	name := user.Name
	if name == "" {
		name = user.ID
	}
	httpapi.WriteJSON(w, 201, CommentItem{Author: name, Time: at.UTC().Format(time.RFC3339), Text: in.Text})
}

func (s *Service) Reject(w http.ResponseWriter, r *http.Request) {
	v, ok := s.scoped(w, r, "manage")
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"`
		Note    string `json:"note"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Comment != "" && in.Note != "" && in.Comment != in.Note {
		httpapi.Error(w, 422, "invalid_reason", "Use one rejection reason")
		return
	}
	reason := strings.TrimSpace(in.Comment)
	if reason == "" {
		reason = strings.TrimSpace(in.Note)
	}
	if len([]rune(reason)) > 4000 {
		httpapi.Error(w, 422, "invalid_reason", "Reason is too long")
		return
	}
	ctx := r.Context()
	user := httpapi.CurrentUser(r)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	var status, rejectedBy, priorReason string
	err = tx.QueryRow(ctx, `SELECT status,COALESCE(rejected_by,''),rejection_reason
 FROM v3_proposals WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR UPDATE`, v.ID, v.ApplicationID, v.EnvironmentID).Scan(&status, &rejectedBy, &priorReason)
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizeLocked(w, r, tx, v, "manage") {
		return
	}
	// Rejection is a human review decision even though it cannot enqueue work.
	// Lock the identity until commit so a simultaneous machine conversion or
	// disable cannot race this decision.
	var human bool
	if err = tx.QueryRow(ctx, `SELECT v3_identity_kind='human' FROM users WHERE id=$1 AND NOT disabled FOR SHARE`, user.ID).Scan(&human); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 403, "forbidden", "Human reviewer required")
		} else {
			unavailable(w)
		}
		return
	}
	if !human {
		httpapi.Error(w, 403, "forbidden", "Human reviewer required")
		return
	}
	if status == "rejected" && rejectedBy == user.ID && priorReason == reason {
		if err = tx.Commit(ctx); err != nil {
			unavailable(w)
			return
		}
		v.Status = "rejected"
	} else if status != "pending" {
		httpapi.Error(w, 409, "proposal_closed", "Proposal is no longer pending")
		return
	} else {
		_, err = tx.Exec(ctx, `UPDATE v3_proposals SET status='rejected',rejected_by=$2,rejected_at=now(),rejection_reason=$3 WHERE id=$1`, v.ID, user.ID, reason)
		if err != nil {
			unavailable(w)
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.proposal.rejected',$3,'success',jsonb_build_object('environment_id',$4::text,'reason_present',$5::boolean))`, user.ID, v.ApplicationID, v.ID, v.EnvironmentID, reason != "")
		if err != nil {
			unavailable(w)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			unavailable(w)
			return
		}
		v.Status = "rejected"
	}
	out, err := s.detail(ctx, v)
	if err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "proposal_unavailable", "Proposal unavailable")
}
