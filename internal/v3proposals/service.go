// Package v3proposals owns V3 review records, discussion, and durable
// passkey-bound approval. Execution remains with an independent worker;
// historical action_plans are a different model.
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
	DB               *pgxpool.Pool
	Access           Access
	ApprovalPasskeys ProposalPasskeys
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
	ID, InvestigationID, ApplicationID, EnvironmentID, TeamID, Status string
	RejectedBy, RejectionReason                                       string
	Content                                                           Content
	CreatedAt                                                         time.Time
}

func loadReviewTx(ctx context.Context, tx pgx.Tx, id string, write bool) (record, error) {
	var v record
	var raw []byte
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT id,investigation_id,application_id,environment_id,status,
 COALESCE(rejected_by,''),rejection_reason,content,created_at
 FROM v3_proposals WHERE id=$1`+lock, id).Scan(&v.ID, &v.InvestigationID, &v.ApplicationID,
		&v.EnvironmentID, &v.Status, &v.RejectedBy, &v.RejectionReason, &raw, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	// Keep the target and its current scope stable through authorization and
	// response assembly. Each lock is row-scoped and taken in the same order.
	err = tx.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications
 WHERE id=$1 AND NOT archived FOR SHARE`, v.ApplicationID).Scan(&v.TeamID)
	if err != nil {
		return record{}, err
	}
	var investigationEnvironment string
	err = tx.QueryRow(ctx, `SELECT environment_id FROM investigations
 WHERE id=$1 AND application_id=$2 FOR SHARE`, v.InvestigationID, v.ApplicationID).Scan(&investigationEnvironment)
	if err != nil {
		return record{}, err
	}
	var mappedEnvironment string
	err = tx.QueryRow(ctx, `SELECT key FROM application_environments
 WHERE id=$1 AND application_id=$2 AND key=$3 AND NOT archived FOR SHARE`,
		investigationEnvironment, v.ApplicationID, v.EnvironmentID).Scan(&mappedEnvironment)
	if err != nil {
		return record{}, err
	}
	err = tx.QueryRow(ctx, `SELECT environment_id FROM v3_application_environments
 WHERE application_id=$1 AND environment_id=$2 FOR SHARE`, v.ApplicationID, v.EnvironmentID).Scan(&mappedEnvironment)
	if err != nil {
		return record{}, err
	}
	if err = json.Unmarshal(raw, &v.Content); err != nil || !validContent(v.Content) {
		return record{}, ErrInvalidProposal
	}
	if v.Content.InvestigationID != v.InvestigationID {
		return record{}, ErrInvalidProposal
	}
	return v, nil
}

func (s *Service) beginReview(w http.ResponseWriter, r *http.Request) (pgx.Tx, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "proposal_unavailable", "Proposal storage or policy is unavailable")
		return nil, false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return nil, false
	}
	return tx, true
}

func (s *Service) scopedTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, action string, beforeAuthorization func(record) error) (record, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return record{}, false
	}
	if action != "view" && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment before writing")
		return record{}, false
	}
	v, err := loadReviewTx(r.Context(), tx, r.PathValue("id"), action != "view")
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
	user := httpapi.CurrentUser(r)
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return record{}, false
	}
	var activeUser string
	err = tx.QueryRow(r.Context(), `SELECT id FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, user.ID).Scan(&activeUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return record{}, false
	}
	if err != nil {
		unavailable(w)
		return record{}, false
	}
	var sessionUser string
	err = tx.QueryRow(r.Context(), `SELECT user_id FROM sessions
 WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`,
		store.Hash(cookie.Value), user.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return record{}, false
	}
	if err != nil {
		unavailable(w)
		return record{}, false
	}
	// Refuse discussion reads from callers with no current proposal grant.
	// This first check is only admission to the potentially long query: the
	// final check below still decides whether its result may be returned.
	if beforeAuthorization != nil {
		err = s.authorizeTx(r.Context(), tx, user, v, action)
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Proposal permission required")
			return record{}, false
		}
		if err != nil {
			httpapi.Error(w, 503, "authorization_unavailable", "Proposal authorization unavailable")
			return record{}, false
		}
		if action == "manage" {
			// Reject is a human decision. The final check remains in Reject,
			// but a machine must not scan the discussion before reaching it.
			var human bool
			err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR SHARE`, user.ID).Scan(&human)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !human {
				httpapi.Error(w, 403, "forbidden", "Human reviewer required")
				return record{}, false
			}
			if err != nil {
				unavailable(w)
				return record{}, false
			}
		}
		if err = beforeAuthorization(v); err != nil {
			unavailable(w)
			return record{}, false
		}
	}
	if err = lockReviewAuthorityTables(r.Context(), tx); err != nil {
		unavailable(w)
		return record{}, false
	}
	// A session may expire while waiting for the authority tables. Use wall time after
	// every lock has been acquired, then evaluate policy on this transaction.
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), user.ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return record{}, false
	}
	if err != nil {
		unavailable(w)
		return record{}, false
	}
	err = s.authorizeTx(r.Context(), tx, user, v, action)
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

// Locking only rows found by a role scan leaves new assignments and grants
// unlocked. A role writer also locks the role before deleting assignments,
// so an assignment-then-role row-lock order can deadlock. These three SHARE
// locks serialize all V3 authorization source writes for the short final
// authorization/commit window, without taking any role row lock.
func lockReviewAuthorityTables(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles, rbac_department_roles, v3_role_grants IN SHARE MODE`)
	return err
}

func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, user core.User, v record, action string) error {
	decision := rbac.V3Decision{
		Module: "proposals", Action: action, EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	if !errors.Is(err, store.ErrForbidden) || v.TeamID == "" {
		return err
	}
	decision.ApplicationID = ""
	decision.TeamID = v.TeamID
	return s.Access.AuthorizeWithTx(ctx, tx, user, decision)
}

func detailTx(ctx context.Context, tx pgx.Tx, v record) (Proposal, error) {
	out := Proposal{ID: v.ID, Status: v.Status, ProposedAt: v.CreatedAt.UTC().Format(time.RFC3339), Content: v.Content, Discussion: []CommentItem{}}
	rows, err := tx.Query(ctx, `SELECT COALESCE(NULLIF(u.name,''),c.author_id),c.created_at,c.body
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

// Row locks hold account, scope, and grant changes until commit. Session
// expiry is time-based, so check wall time again immediately before commit.
func commitReview(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return false
	}
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return false
	}
	if err != nil {
		unavailable(w)
		return false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return false
	}
	return true
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	tx, ok := s.beginReview(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var out Proposal
	_, ok = s.scopedTx(w, r, tx, "view", func(v record) error {
		var err error
		out, err = detailTx(r.Context(), tx, v)
		return err
	})
	if !ok {
		return
	}
	if !commitReview(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (s *Service) Comment(w http.ResponseWriter, r *http.Request) {
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
	tx, ok := s.beginReview(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	v, ok := s.scopedTx(w, r, tx, "change", nil)
	if !ok {
		return
	}
	ctx := r.Context()
	user := httpapi.CurrentUser(r)
	hash := store.Hash(in.Text)
	if key != "" {
		var priorHash, author, body string
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT c.request_hash,COALESCE(NULLIF(u.name,''),c.author_id),c.body,c.created_at
 FROM v3_proposal_comments c JOIN users u ON u.id=c.author_id
 WHERE c.proposal_id=$1 AND c.author_id=$2 AND c.idempotency_key=$3`, v.ID, user.ID, key).Scan(&priorHash, &author, &body, &at)
		if err == nil {
			if priorHash != hash {
				httpapi.Error(w, 409, "idempotency_conflict", "Key was used for a different comment")
				return
			}
			if !commitReview(w, r, tx) {
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
	if v.Status != "pending" {
		httpapi.Error(w, 409, "proposal_closed", "Proposal is no longer pending")
		return
	}
	id := store.ID()
	var at time.Time
	err := tx.QueryRow(ctx, `INSERT INTO v3_proposal_comments(id,proposal_id,author_id,body,idempotency_key,request_hash)
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
	if !commitReview(w, r, tx) {
		return
	}
	name := user.Name
	if name == "" {
		name = user.ID
	}
	httpapi.WriteJSON(w, 201, CommentItem{Author: name, Time: at.UTC().Format(time.RFC3339), Text: in.Text})
}

func (s *Service) Reject(w http.ResponseWriter, r *http.Request) {
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
	tx, ok := s.beginReview(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var out Proposal
	v, ok := s.scopedTx(w, r, tx, "manage", func(v record) error {
		var err error
		out, err = detailTx(r.Context(), tx, v)
		return err
	})
	if !ok {
		return
	}
	ctx := r.Context()
	user := httpapi.CurrentUser(r)
	// Rejection is a human review decision even though it cannot enqueue work.
	// Lock the identity until commit so a simultaneous machine conversion or
	// disable cannot race this decision.
	var human bool
	if err := tx.QueryRow(ctx, `SELECT v3_identity_kind='human' FROM users WHERE id=$1 AND NOT disabled FOR SHARE`, user.ID).Scan(&human); err != nil {
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
	if v.Status == "rejected" && v.RejectedBy == user.ID && v.RejectionReason == reason {
		v.Status = "rejected"
	} else if v.Status != "pending" {
		httpapi.Error(w, 409, "proposal_closed", "Proposal is no longer pending")
		return
	} else {
		_, err := tx.Exec(ctx, `UPDATE v3_proposals SET status='rejected',rejected_by=$2,rejected_at=now(),rejection_reason=$3 WHERE id=$1`, v.ID, user.ID, reason)
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
		v.Status = "rejected"
	}
	out.Status = v.Status
	if !commitReview(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "proposal_unavailable", "Proposal unavailable")
}
