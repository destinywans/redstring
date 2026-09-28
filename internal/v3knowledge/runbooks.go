// Package v3knowledge adapts the existing governed knowledge documents to the
// V3 Runbook API. Drafts and review submissions never execute Runbook steps.
package v3knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct {
	DB        *pgxpool.Pool
	Knowledge *knowledge.Service
	Policy    *rbac.V3Policy
}

type Step struct {
	N          int      `json:"n"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body,omitempty"`
	Connectors []string `json:"connectors,omitempty"`
	Code       string   `json:"code,omitempty"`
	Readonly   bool     `json:"readonly,omitempty"`
	Approval   bool     `json:"approval,omitempty"`
}

// Register adds routes that do not conflict with the old knowledge module.
// RegisterSearch separately after omitting the legacy search registration.
func (s *Service) Register(api *httpapi.Server) {
	routes := []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/runbooks", "view", s.listHandler(api)},
		{"GET /api/v1/runbooks/{id}", "view", s.getHandler(api)},
		{"POST /api/v1/runbooks", "manage", s.createHandler(api)},
		{"POST /api/v1/runbooks/{id}/submit", "manage", s.submitHandler(api)},
		{"POST /api/v1/runbooks/{id}/dry-run", "view", s.dryRunHandler(api)},
	}
	for _, route := range routes {
		api.Register(route.pattern, "viewer", route.handler)
		if route.pattern == "POST /api/v1/runbooks" {
			api.MarkV3Route(route.pattern, "knowledge", route.action)
		} else {
			api.MarkV3ObjectRoute(route.pattern, "knowledge", route.action)
		}
	}
}

func (s *Service) listHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		service := strings.TrimSpace(r.URL.Query().Get("service"))
		tab := r.URL.Query().Get("tab")
		if len(q) > 200 || len(service) > 200 || (tab != "" && tab != "all" && tab != "mine" && tab != "review" && tab != "stale") {
			httpapi.Error(w, 422, "invalid_query", "Invalid Runbook filter")
			return
		}
		rows, err := s.DB.Query(r.Context(), `SELECT d.id
FROM knowledge_documents d LEFT JOIN v3_runbooks v ON v.document_id=d.id
WHERE (d.source LIKE 'runbook://%' OR v.document_id IS NOT NULL)
ORDER BY d.created_at DESC LIMIT 1001`)
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbooks unavailable")
			return
		}
		candidates := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			candidates = append(candidates, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbooks unavailable")
			return
		}
		if len(candidates) > 1000 {
			httpapi.Error(w, 413, "scope_too_large", "Runbook collection exceeds the unpaged V3 response limit")
			return
		}
		for _, id := range candidates {
			doc, getErr := s.rawDocument(r.Context(), id)
			if errors.Is(getErr, knowledge.ErrForbidden) || errors.Is(getErr, store.ErrForbidden) || errors.Is(getErr, store.ErrNotFound) {
				continue
			}
			if getErr != nil {
				httpapi.Error(w, 503, "knowledge_unavailable", "Runbooks unavailable")
				return
			}
			if !documentInScope(r, doc) || (service != "" && doc.ApplicationID != service && !contains(doc.Metadata.SharedApplicationIDs, service)) {
				continue
			}
			if authErr := s.authorizeDocument(r, doc, "view"); authErr != nil {
				if errors.Is(authErr, store.ErrForbidden) {
					continue
				}
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
				return
			}
			if publicStatus(doc.Lifecycle) == "" {
				continue
			}
			// The response requires references, verification and stale state for
			// every item, plus counts across tabs. No complete usage/verification
			// producer exists, so even a filtered collection cannot claim zeros.
			writeRunbookReadUnavailable(w)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": []any{}, "counts": map[string]int{"all": 0, "mine": 0, "review": 0, "stale": 0}, "kpis": []any{}, "suggestions": []any{}})
	}
}

func (s *Service) getHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		doc, err := s.rawDocument(r.Context(), r.PathValue("id"))
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if !documentInScope(r, doc) || !isRunbook(doc.Source) || s.authorizeDocument(r, doc, "view") != nil {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		// Runbook.usage is mandatory in the V3 DTO. The current repository
		// records neither complete references nor resolved outcomes; returning
		// zero would falsely assert that those events were measured.
		writeRunbookReadUnavailable(w)
	}
}

type createInput struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	From   string `json:"from"`
	Steps  []Step `json:"steps"`
	Status string `json:"status"`
}

func (s *Service) createHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		var in createInput
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if err := validateCreate(in); err != nil {
			httpapi.Error(w, 422, "invalid_runbook", err.Error())
			return
		}
		for i := range in.Steps {
			if in.Steps[i].Kind == "action" || in.Steps[i].Kind == "act" {
				in.Steps[i].Approval = true
				in.Steps[i].Readonly = false
			}
		}
		user := httpapi.CurrentUser(r)
		id := store.ID()
		body := renderBody(in)
		metadata := knowledge.Metadata{MaintainerID: user.ID, Origin: "human", ActorID: user.ID, Visibility: "workspace", EnvironmentIDs: append([]string(nil), scopeIDs(r)...)}
		metaJSON, _ := json.Marshal(metadata)
		stepsJSON, _ := json.Marshal(in.Steps)
		status := in.Status
		if status == "" {
			status = "draft"
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{})
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbook unavailable")
			return
		}
		defer tx.Rollback(r.Context())
		conversationID, sourceErr := s.validateSource(r, tx, in)
		if sourceErr != nil {
			if errors.Is(sourceErr, errPostmortemUnavailable) {
				httpapi.Error(w, 503, "source_unavailable", "Postmortem storage is not available")
				return
			}
			if errors.Is(sourceErr, store.ErrForbidden) || errors.Is(sourceErr, store.ErrNotFound) || errors.Is(sourceErr, errSourceEmpty) {
				httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
				return
			}
			httpapi.Error(w, 503, "source_unavailable", "Source validation failed")
			return
		}
		var created time.Time
		err = tx.QueryRow(r.Context(), `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES($1,NULL,$2,$3,$4,1,$5,$6,$7,$8) RETURNING created_at`, id, in.Title, body, "runbook://v3/"+id, store.Hash(body), user.ID, status, metaJSON).Scan(&created)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_runbooks(document_id,source_kind,source_ref,conversation_id,steps) VALUES($1,$2,$3,NULLIF($4,''),$5)`, id, in.Source, in.From, conversationID, stepsJSON)
		}
		if err == nil {
			snapshot := knowledge.Document{ID: id, Title: in.Title, Body: body, Source: "runbook://v3/" + id, Version: 1, ContentHash: store.Hash(body), Lifecycle: status, Metadata: metadata, CreatedAt: created, EvidenceIDs: []string{}}
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) VALUES($1,1,$2,$3)`, id, snapshot, user.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,1,$2,'v3.runbook.created')`, id, user.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'runbook.created',$2,'succeeded',$3)`, user.ID, id, map[string]any{"status": status, "source": in.Source})
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Could not save Runbook")
			return
		}
		httpapi.WriteJSON(w, 201, map[string]any{"id": id})
	}
}

func (s *Service) submitHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		id := r.PathValue("id")
		doc, err := s.rawDocument(r.Context(), id)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if !isRunbook(doc.Source) || !documentInScope(r, doc) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if s.authorizeDocument(r, doc, "manage") != nil {
			httpapi.Error(w, 403, "forbidden", "Runbook management denied")
			return
		}
		if doc.Lifecycle != "draft" {
			httpapi.Error(w, 409, "invalid_state", "Only drafts can be submitted")
			return
		}
		if doc.Metadata.MaintainerID != httpapi.CurrentUser(r).ID {
			httpapi.Error(w, 403, "forbidden", "Only the maintainer can submit this draft")
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{})
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbook unavailable")
			return
		}
		defer tx.Rollback(r.Context())
		result, err := tx.Exec(r.Context(), `UPDATE knowledge_documents SET lifecycle='review' WHERE id=$1 AND version=$2 AND lifecycle='draft'`, id, doc.Version)
		if err == nil && result.RowsAffected() != 1 {
			err = store.ErrConflict
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,'lifecycle.review')`, id, doc.Version, httpapi.CurrentUser(r).ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'runbook.submitted',$3,'succeeded',$4)`, httpapi.CurrentUser(r).ID, doc.ApplicationID, id, map[string]any{"version": doc.Version})
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"ok": true, "status": "review"})
	}
}

// Dry-run is a validation preview. It never invokes connectors, tools, models,
// proposals, or the independent execution service.
func (s *Service) dryRunHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		doc, err := s.rawDocument(r.Context(), r.PathValue("id"))
		if err != nil {
			knowledgeError(w, err)
			return
		}
		if !isRunbook(doc.Source) || !documentInScope(r, doc) || s.authorizeDocument(r, doc, "view") != nil {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		var steps []Step
		err = s.DB.QueryRow(r.Context(), `SELECT steps FROM v3_runbooks WHERE document_id=$1`, doc.ID).Scan(&steps)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 422, "no_structured_steps", "This Runbook has no structured steps to validate")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbook unavailable")
			return
		}
		checks, valid := previewSteps(steps)
		httpapi.WriteJSON(w, 200, map[string]any{"steps": len(steps), "ok": valid, "mode": "validation_only", "checks": checks})
	}
}

func validateCreate(in createInput) error {
	if strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 200 {
		return errors.New("title is required and must be at most 200 characters")
	}
	if in.Source != "blank" && in.Source != "template" && in.Source != "postmortem" && in.Source != "conversation" && in.Source != "import" {
		return errors.New("unknown Runbook source")
	}
	if in.Status != "" && in.Status != "draft" && in.Status != "review" {
		return errors.New("Runbook status must be draft or review")
	}
	if len(in.From) > 200 || len(in.Steps) > 100 {
		return errors.New("source or step count exceeds limit")
	}
	if (in.Source == "postmortem" || in.Source == "conversation") && strings.TrimSpace(in.From) == "" {
		return errors.New("source reference is required")
	}
	for i, step := range in.Steps {
		if strings.TrimSpace(step.Title) == "" || len([]rune(step.Title)) > 200 || len(step.Body) > 16000 || len(step.Code) > 16000 {
			return fmt.Errorf("invalid step %d", i+1)
		}
		if step.Kind != "query" && step.Kind != "check" && step.Kind != "action" && step.Kind != "verify" && step.Kind != "act" && step.Kind != "judge" {
			return fmt.Errorf("unknown step kind %d", i+1)
		}
		if len(step.Connectors) > 10 {
			return fmt.Errorf("too many connectors in step %d", i+1)
		}
	}
	return nil
}

func previewSteps(steps []Step) ([]map[string]any, bool) {
	checks := make([]map[string]any, 0, len(steps))
	valid := true
	for _, step := range steps {
		ok := step.Kind != "action" && step.Kind != "act" && step.Readonly && len(step.Connectors) > 0
		if !ok {
			valid = false
		}
		checks = append(checks, map[string]any{"step": step.N, "validated": ok, "executed": false, "requires_approval": step.Approval || step.Kind == "action" || step.Kind == "act"})
	}
	return checks, valid
}

var (
	errPostmortemUnavailable = errors.New("postmortem storage unavailable")
	errSourceEmpty           = errors.New("source has no completed content")
)

// The source lookup and Runbook insert share one transaction. A key-share lock
// prevents deleting the conversation before the new FK is committed. A plain
// client-supplied source ID never establishes provenance by itself.
func (s *Service) validateSource(r *http.Request, tx pgx.Tx, in createInput) (string, error) {
	switch in.Source {
	case "postmortem":
		return "", errPostmortemUnavailable
	case "conversation":
		envs := scopeIDs(r)
		if len(envs) != 1 {
			return "", store.ErrForbidden
		}
		var owner, actualEnvironment string
		err := tx.QueryRow(r.Context(), `SELECT owner_id,environment_id FROM v3_conversations WHERE id=$1 FOR KEY SHARE`, in.From).Scan(&owner, &actualEnvironment)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", store.ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if owner != httpapi.CurrentUser(r).ID || actualEnvironment != envs[0] {
			return "", store.ErrForbidden
		}
		err = s.Policy.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: actualEnvironment})
		if err != nil {
			return "", err
		}
		var hasContent bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_conversation_messages WHERE conversation_id=$1 AND state='completed' AND jsonb_array_length(parts)>0)`, in.From).Scan(&hasContent)
		if err != nil {
			return "", err
		}
		if !hasContent {
			return "", errSourceEmpty
		}
		return in.From, nil
	default:
		return "", nil
	}
}

func renderBody(in createInput) string {
	var b strings.Builder
	b.WriteString("# " + strings.TrimSpace(in.Title) + "\n")
	for i, step := range in.Steps {
		b.WriteString("\n## " + strconv.Itoa(i+1) + ". " + step.Title + "\n")
		b.WriteString(step.Body)
		if step.Code != "" {
			b.WriteString("\n```\n" + step.Code + "\n```\n")
		}
	}
	return b.String()
}

func (s *Service) ready(w http.ResponseWriter) bool {
	if s == nil || s.DB == nil || s.Knowledge == nil || s.Knowledge.Repository == nil || s.Policy == nil {
		httpapi.Error(w, 503, "knowledge_unavailable", "Knowledge storage unavailable")
		return false
	}
	return true
}

func (s *Service) rawDocument(ctx context.Context, id string) (knowledge.Document, error) {
	return s.Knowledge.Repository.Get(ctx, id)
}

// The route-level check permits a module in at least one requested environment.
// This second check binds the decision to the persisted document's application
// and its actual environment scope; client supplied service names cannot grant it.
func (s *Service) authorizeDocument(r *http.Request, d knowledge.Document, action string) error {
	if !documentInScope(r, d) {
		return store.ErrForbidden
	}
	for _, env := range scopeIDs(r) {
		if len(d.Metadata.EnvironmentIDs) > 0 && !contains(d.Metadata.EnvironmentIDs, env) {
			continue
		}
		err := s.Policy.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: "knowledge", Action: action, EnvironmentID: env, ApplicationID: d.ApplicationID})
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrForbidden) {
			return err
		}
	}
	return store.ErrForbidden
}

func scopeIDs(r *http.Request) []string {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok {
		return nil
	}
	return scope.EnvironmentIDs
}
func documentInScope(r *http.Request, d knowledge.Document) bool {
	ids := scopeIDs(r)
	if len(ids) == 0 {
		return false
	}
	if len(d.Metadata.EnvironmentIDs) == 0 {
		return true
	}
	for _, id := range ids {
		if contains(d.Metadata.EnvironmentIDs, id) {
			return true
		}
	}
	return false
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func isRunbook(source string) bool { return strings.HasPrefix(source, "runbook://") }
func publicStatus(s string) string {
	if s == "draft" || s == "review" || s == "published" {
		return s
	}
	return ""
}
func writeRunbookReadUnavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "runbook_read_unavailable", "Runbook usage and verification history are not available")
}
func knowledgeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, knowledge.ErrForbidden), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Knowledge access denied")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, 404, "not_found", "Runbook not found")
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, 409, "conflict", "Runbook changed; reload and retry")
	case errors.Is(err, knowledge.ErrInvalid):
		httpapi.Error(w, 422, "invalid_runbook", "Invalid Runbook state")
	default:
		httpapi.Error(w, 503, "knowledge_unavailable", "Knowledge operation failed")
	}
}
