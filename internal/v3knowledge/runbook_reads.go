package v3knowledge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
)

// The frozen V3 page asks for all investigation citations and resolution
// outcomes. Neither has a complete producer. Null keeps those values distinct
// from a measured zero, and complete_v3_dto remains false until the producer
// and the frontend's unknown-state rendering are connected.
var runbookListUnknown = []string{
	"refs_30d", "resolve_rate", "kpis", "suggestions", "verification_provenance",
}

var runbookDetailUnknown = []string{
	"usage.refs", "usage.resolve_rate", "usage.weekly", "usage.highlight",
	"incidents", "pending_reviewers", "execution_verification",
}

type runbookSummary struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Tags           []string   `json:"tags"`
	Scope          string     `json:"scope"`
	Owner          string     `json:"owner"`
	Version        string     `json:"version"`
	Status         string     `json:"status"`
	Refs30d        *int       `json:"refs_30d"`
	ResolveRate    *float64   `json:"resolve_rate"`
	LastVerified   *string    `json:"last_verified"`
	Stale          *bool      `json:"stale"`
	EnvironmentIDs []string   `json:"environment_ids"`
	ApplicationID  string     `json:"application_id"`
	TeamID         string     `json:"team_id"`
	VerifiedAt     *time.Time `json:"-"`
}

type runbookReadRow struct {
	Doc        knowledge.Document
	Owner      string
	VerifiedAt *time.Time
}

const runbookReadBatchSQL = `SELECT d.id,COALESCE(d.application_id,''),d.title,d.source,
 d.version,d.lifecycle,d.metadata,d.created_at,
 COALESCE(u.name,d.metadata->>'maintainer_id',''),vr.last_verified_at
 FROM knowledge_documents d
 LEFT JOIN v3_runbooks vr ON vr.document_id=d.id
 LEFT JOIN users u ON u.id=d.metadata->>'maintainer_id'
 WHERE d.source LIKE 'runbook://%'
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
   THEN (d.metadata->'environment_ids') ?| $1::text[] ELSE false END
 AND ($2::timestamptz IS NULL OR (d.created_at,d.id)<($2::timestamptz,$3::text))
 ORDER BY d.created_at DESC,d.id DESC LIMIT 256`

func parseRunbookPage(r *http.Request) (int, int, bool) {
	page, perPage := 1, 50
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 10000 {
			return 0, 0, false
		}
		page = parsed
	}
	if raw := r.URL.Query().Get("per_page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, false
		}
		perPage = parsed
	}
	return page, perPage, true
}

func runbookScope(doc knowledge.Document, selected []string) (string, []string) {
	environments := make([]string, 0, len(selected))
	for _, env := range selected {
		if contains(doc.Metadata.EnvironmentIDs, env) {
			environments = append(environments, env)
		}
	}
	parts := []string{"workspace"}
	if doc.ApplicationID != "" {
		parts[0] = "app:" + doc.ApplicationID
	}
	if doc.Metadata.TeamID != "" {
		parts = append(parts, "team:"+doc.Metadata.TeamID)
	}
	parts = append(parts, "env:"+strings.Join(environments, ","))
	return strings.Join(parts, " · "), environments
}

func projectRunbookSummary(row runbookReadRow, selected []string, now time.Time) runbookSummary {
	doc := row.Doc
	scope, environments := runbookScope(doc, selected)
	owner := row.Owner
	if owner == "" {
		owner = doc.Metadata.MaintainerID
	}
	out := runbookSummary{
		ID: doc.ID, Title: doc.Title, Tags: nonnilStrings(doc.Metadata.Tags),
		Scope: scope, Owner: owner, Version: "v" + strconv.Itoa(doc.Version),
		Status: doc.Lifecycle, EnvironmentIDs: environments,
		ApplicationID: doc.ApplicationID, TeamID: doc.Metadata.TeamID,
		VerifiedAt: row.VerifiedAt,
	}
	if row.VerifiedAt != nil {
		value := row.VerifiedAt.UTC().Format(time.RFC3339)
		out.LastVerified = &value
		stale := now.Sub(*row.VerifiedAt) > 90*24*time.Hour
		out.Stale = &stale
	}
	return out
}

func (s *Service) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		owners, ownerErr := v3pagequery.Values(r.URL.Query(), "owner")
		scopes, scopeErr := v3pagequery.Values(r.URL.Query(), "scope")
		if ownerErr != nil || scopeErr != nil {
			httpapi.Error(w, 422, "invalid_filter", "Invalid Runbook owner or scope")
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		service := strings.TrimSpace(r.URL.Query().Get("service"))
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "all"
		}
		page, perPage, validPage := parseRunbookPage(r)
		if !validPage || len(q) > 200 || len(service) > 200 ||
			(tab != "all" && tab != "mine" && tab != "review" && tab != "stale") {
			httpapi.Error(w, 422, "invalid_query", "Invalid Runbook filter or page")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		tx, ok := s.beginRunbookRead(w, r, ctx)
		if !ok {
			return
		}
		defer tx.Rollback(ctx)
		counts := map[string]int{"all": 0, "mine": 0, "review": 0, "stale": 0, "verification_unknown": 0}
		items := make([]runbookSummary, 0, perPage)
		now := time.Now()
		selectedCount := 0
		var cursor *time.Time
		cursorID := ""
		for {
			rows, queryErr := tx.Query(ctx, runbookReadBatchSQL, scopeIDs(r), cursor, cursorID)
			if queryErr != nil {
				knowledgeError(w, queryErr)
				return
			}
			batch := make([]runbookReadRow, 0, 256)
			for rows.Next() {
				var row runbookReadRow
				queryErr = rows.Scan(&row.Doc.ID, &row.Doc.ApplicationID, &row.Doc.Title,
					&row.Doc.Source, &row.Doc.Version, &row.Doc.Lifecycle,
					&row.Doc.Metadata, &row.Doc.CreatedAt, &row.Owner, &row.VerifiedAt)
				if queryErr != nil {
					break
				}
				batch = append(batch, row)
			}
			if queryErr == nil {
				queryErr = rows.Err()
			}
			rows.Close()
			if queryErr != nil {
				knowledgeError(w, queryErr)
				return
			}
			for _, row := range batch {
				doc := row.Doc
				if !isRunbook(doc.Source) || publicStatus(doc.Lifecycle) == "" ||
					!documentInScope(r, doc) ||
					(service != "" && doc.ApplicationID != service && !contains(doc.Metadata.SharedApplicationIDs, service)) {
					continue
				}
				allowedEnvironments, authErr := s.authorizedRunbookEnvironmentsWithTx(r, tx, doc)
				if authErr != nil {
					if errors.Is(authErr, store.ErrForbidden) {
						continue
					}
					httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
					return
				}
				if q != "" && !strings.Contains(strings.ToLower(doc.Title), q) {
					found := false
					for _, tag := range doc.Metadata.Tags {
						found = found || strings.Contains(strings.ToLower(tag), q)
					}
					if !found {
						continue
					}
				}
				summary := projectRunbookSummary(row, allowedEnvironments, now)
				if (!v3pagequery.Matches(owners, doc.Metadata.MaintainerID) && !v3pagequery.Matches(owners, summary.Owner)) || !v3pagequery.Matches(scopes, summary.Scope) {
					continue
				}
				counts["all"]++
				if doc.Metadata.MaintainerID == httpapi.CurrentUser(r).ID {
					counts["mine"]++
				}
				if doc.Lifecycle == "review" {
					counts["review"]++
				}
				if summary.Stale == nil {
					counts["verification_unknown"]++
				} else if *summary.Stale {
					counts["stale"]++
				}
				if counts["all"] > 10000 {
					httpapi.Error(w, 413, "scope_too_large", "Visible Runbook collection exceeds 10000 items")
					return
				}
				selected := tab == "all" || tab == "mine" && doc.Metadata.MaintainerID == httpapi.CurrentUser(r).ID ||
					tab == "review" && doc.Lifecycle == "review" || tab == "stale" && summary.Stale != nil && *summary.Stale
				if selected {
					if selectedCount >= (page-1)*perPage && len(items) < perPage {
						items = append(items, summary)
					}
					selectedCount++
				}
			}
			if len(batch) < 256 {
				break
			}
			last := batch[len(batch)-1].Doc
			cursor = &last.CreatedAt
			cursorID = last.ID
		}
		if err := lockRunbookReadSession(ctx, tx, r); err != nil {
			runbookReadAuthorityError(w, err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			knowledgeError(w, err)
			return
		}
		unknown := append([]string(nil), runbookListUnknown...)
		if counts["verification_unknown"] > 0 {
			unknown = append(unknown, "last_verified", "stale")
		}
		httpapi.WriteJSON(w, 200, map[string]any{
			"items": items, "counts": counts, "kpis": nil, "suggestions": nil,
			"page": page, "per_page": perPage, "total": selectedCount,
			"has_next":        page*perPage < selectedCount,
			"complete_v3_dto": false, "partial": true, "unknown_fields": unknown,
		})
	}
}

type runbookVersion struct {
	V    string `json:"v"`
	Who  string `json:"who"`
	When string `json:"when"`
}

type runbookReviewer struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func readRunbookVersions(ctx context.Context, tx pgx.Tx, id string) ([]runbookVersion, error) {
	rows, err := tx.Query(ctx, `SELECT r.version,COALESCE(u.name,r.created_by),r.created_at
 FROM knowledge_revisions r LEFT JOIN users u ON u.id=r.created_by
 WHERE r.document_id=$1 ORDER BY r.version DESC LIMIT 101`, id)
	if err != nil {
		return nil, err
	}
	versions := make([]runbookVersion, 0)
	for rows.Next() {
		var number int
		var actor string
		var at time.Time
		if err = rows.Scan(&number, &actor, &at); err != nil {
			break
		}
		versions = append(versions, runbookVersion{V: "v" + strconv.Itoa(number), Who: actor, When: at.UTC().Format(time.RFC3339)})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return versions, err
}

func readRunbookReviewers(ctx context.Context, tx pgx.Tx, id string, version int) ([]runbookReviewer, error) {
	rows, err := tx.Query(ctx, `SELECT COALESCE(u.name,r.reviewer_id)
 FROM v3_runbook_reviews r LEFT JOIN users u ON u.id=r.reviewer_id
 WHERE r.document_id=$1 AND r.document_version=$2 AND r.decision='approve'
 ORDER BY r.created_at DESC,r.id DESC LIMIT 101`, id, version)
	if err != nil {
		return nil, err
	}
	reviewers := make([]runbookReviewer, 0)
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		if !seen[name] {
			reviewers = append(reviewers, runbookReviewer{Name: name, State: "approved"})
			seen[name] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return reviewers, err
}

func (s *Service) getHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, doc, ok := s.readableRunbook(w, r)
		if !ok {
			return
		}
		defer tx.Rollback(r.Context())
		record, err := readStructuredRunbook(r.Context(), tx, doc.ID)
		if err != nil {
			structuredRunbookError(w, err)
			return
		}
		allowedEnvironments, authErr := s.authorizedRunbookEnvironmentsWithTx(r, tx, doc)
		if authErr != nil {
			if errors.Is(authErr, store.ErrForbidden) {
				httpapi.Error(w, 404, "not_found", "Runbook not found")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
			}
			return
		}
		for _, step := range record.Steps {
			if step.Kind != "query" && step.Kind != "check" && step.Kind != "action" &&
				step.Kind != "verify" && step.Kind != "act" && step.Kind != "judge" {
				httpapi.Error(w, 503, "structured_steps_unavailable", "Runbook step kind is not representable")
				return
			}
		}
		versions, err := readRunbookVersions(r.Context(), tx, doc.ID)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		reviewers, err := readRunbookReviewers(r.Context(), tx, doc.ID, doc.Version)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if len(versions) > 100 || len(reviewers) > 100 {
			httpapi.Error(w, 413, "history_too_large", "Runbook history exceeds the bounded detail response")
			return
		}
		var verifiedAt *time.Time
		err = tx.QueryRow(r.Context(), `SELECT last_verified_at FROM v3_runbooks WHERE document_id=$1`, doc.ID).Scan(&verifiedAt)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		owner := doc.Metadata.MaintainerID
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT name FROM users WHERE id=$1),$1)`, owner).Scan(&owner); err != nil {
			knowledgeError(w, err)
			return
		}
		if err = lockRunbookReadSession(r.Context(), tx, r); err != nil {
			runbookReadAuthorityError(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			knowledgeError(w, err)
			return
		}
		scope, environments := runbookScope(doc, allowedEnvironments)
		base := record.SourceKind
		if record.SourceRef != "" {
			base += ":" + record.SourceRef
		}
		usage := map[string]any{"refs": nil, "resolve_rate": nil, "weekly": nil, "highlight": nil}
		unknown := append([]string(nil), runbookDetailUnknown...)
		if len(versions) == 0 {
			unknown = append(unknown, "versions")
		}
		if len(reviewers) == 0 {
			unknown = append(unknown, "pending_reviewers")
		}
		var verified *string
		if verifiedAt != nil {
			value := verifiedAt.UTC().Format(time.RFC3339)
			verified = &value
		}
		httpapi.WriteJSON(w, 200, map[string]any{
			"id": doc.ID, "title": doc.Title, "version": "v" + strconv.Itoa(doc.Version),
			"status": doc.Lifecycle, "base": base, "source_kind": record.SourceKind,
			"source_ref": record.SourceRef, "owner": owner, "scope": scope,
			"environment_ids": environments, "application_id": doc.ApplicationID,
			"team_id": doc.Metadata.TeamID, "tags": nonnilStrings(doc.Metadata.Tags),
			"when": record.WhenText, "steps": normalizeSteps(record.Steps),
			"reviewers": reviewers, "versions": versions, "usage": usage,
			"incidents": nil, "last_verified": verified,
			"complete_v3_dto": false, "partial": true, "unknown_fields": unknown,
		})
	}
}
