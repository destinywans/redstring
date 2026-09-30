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
	N          int       `json:"n"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	Connectors []string  `json:"connectors,omitempty"`
	Code       string    `json:"code,omitempty"`
	Readonly   bool      `json:"readonly,omitempty"`
	Approval   bool      `json:"approval,omitempty"`
	Tool       *StepTool `json:"tool,omitempty"`
}

// Tool is source/editor display data, never a connector invocation contract.
type StepTool struct {
	Brand string `json:"brand"`
	Text  string `json:"text"`
}

// Register adds routes that do not conflict with the old knowledge module.
// RegisterSearch separately after omitting the legacy search registration.
func (s *Service) Register(api *httpapi.Server) {
	routes := []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/runbooks", "view", s.listHandler()},
		{"GET /api/v1/runbooks/draft", "view", s.draftHandler()},
		{"GET /api/v1/runbooks/{id}", "view", s.getHandler()},
		{"POST /api/v1/runbooks", "manage", s.createHandler(api)},
		{"POST /api/v1/runbooks/{id}/submit", "manage", s.submitHandler(api)},
		{"POST /api/v1/runbooks/{id}/dry-run", "manage", s.dryRunHandler(api)},
		{"POST /api/v1/runbooks/{id}/reviews", "manage", s.reviewHandler()},
		{"POST /api/v1/runbooks/{id}/reindex", "manage", s.reindexHandler()},
		{"GET /api/v1/runbooks/{id}/record", "view", s.recordHandler()},
		{"GET /api/v1/runbooks/{id}/history", "view", s.historyHandler()},
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

type createInput struct {
	Title                string `json:"title"`
	Source               string `json:"source"`
	From                 string `json:"from"`
	ExpectedSourceSHA256 string `json:"expected_source_sha256,omitempty"`
	Steps                []Step `json:"steps"`
	Status               string `json:"status"`
}

func (s *Service) createHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a Runbook")
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
		in.Steps = normalizeSteps(in.Steps)
		user := httpapi.CurrentUser(r)
		id := store.ID()
		body := renderBody(in)
		metadata := knowledge.Metadata{MaintainerID: user.ID, Origin: "human", ActorID: user.ID, Visibility: "workspace", EnvironmentIDs: append([]string(nil), scopeIDs(r)...)}
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
		binding, sourceErr := s.loadDraftSource(r, tx, in)
		if sourceErr != nil {
			if errors.Is(sourceErr, errSourceTooLarge) {
				httpapi.Error(w, 413, "source_too_large", "Source exceeds the bounded Runbook draft limit")
				return
			}
			if errors.Is(sourceErr, store.ErrForbidden) || errors.Is(sourceErr, store.ErrNotFound) || errors.Is(sourceErr, pgx.ErrNoRows) || errors.Is(sourceErr, errSourceEmpty) {
				httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
				return
			}
			httpapi.Error(w, 503, "source_unavailable", "Source validation failed")
			return
		}
		if binding.ApplicationID != "" {
			metadata.Visibility = "application"
			metadata.TeamID = binding.TeamID
			decision := rbac.V3Decision{Module: "knowledge", Action: "manage", EnvironmentID: scopeIDs(r)[0], ApplicationID: binding.ApplicationID}
			authErr := s.Policy.AuthorizeWithTx(r.Context(), tx, user, decision)
			if authErr == nil && binding.TeamID != "" {
				decision.ApplicationID = ""
				decision.TeamID = binding.TeamID
				authErr = s.Policy.AuthorizeWithTx(r.Context(), tx, user, decision)
			}
			if authErr != nil {
				if errors.Is(authErr, store.ErrForbidden) {
					httpapi.Error(w, 403, "forbidden", "Runbook source scope cannot be managed")
				} else {
					httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
				}
				return
			}
		}
		cookie, cookieErr := r.Cookie("aiops_session")
		if cookieErr != nil {
			httpapi.Error(w, 401, "unauthorized", "Login required")
			return
		}
		if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
			if errors.Is(err, store.ErrSessionRevoked) {
				httpapi.Error(w, 401, "unauthorized", "Session expired")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Session could not be checked")
			}
			return
		}
		if err = lockRunbookGrantSources(r.Context(), tx, user.ID); err != nil {
			httpapi.Error(w, 503, "authorization_unavailable", "Role grants could not be checked")
			return
		}
		if err = s.Policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{Module: "knowledge", Action: "manage", EnvironmentID: scopeIDs(r)[0], ApplicationID: binding.ApplicationID}); err != nil {
			knowledgeError(w, err)
			return
		}
		if binding.TeamID != "" {
			if err = s.Policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{Module: "knowledge", Action: "manage", EnvironmentID: scopeIDs(r)[0], TeamID: binding.TeamID}); err != nil {
				knowledgeError(w, err)
				return
			}
		}
		// Re-read state and source grants after the source lock and session
		// wait. The persisted digest identifies the exact content considered.
		binding, sourceErr = s.loadDraftSource(r, tx, in)
		if sourceErr != nil {
			if errors.Is(sourceErr, errSourceTooLarge) {
				httpapi.Error(w, 413, "source_too_large", "Source exceeds the bounded Runbook draft limit")
				return
			}
			if errors.Is(sourceErr, store.ErrForbidden) || errors.Is(sourceErr, store.ErrNotFound) || errors.Is(sourceErr, pgx.ErrNoRows) || errors.Is(sourceErr, errSourceEmpty) {
				httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
			} else {
				httpapi.Error(w, 503, "source_unavailable", "Source validation failed")
			}
			return
		}
		if in.ExpectedSourceSHA256 != "" && in.ExpectedSourceSHA256 != binding.ContentHash {
			httpapi.Error(w, 409, "source_changed", "Runbook source changed; load a new draft")
			return
		}
		snapshotJSON, _ := json.Marshal(map[string]string{"source_id": binding.SourceID, "source_kind": in.Source, "content_sha256": binding.ContentHash})
		metaJSON, _ := json.Marshal(metadata)
		var created time.Time
		err = tx.QueryRow(r.Context(), `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES($1,NULLIF($2,''),$3,$4,$5,1,$6,$7,$8,$9) RETURNING created_at`, id, binding.ApplicationID, in.Title, body, "runbook://v3/"+id, store.Hash(body), user.ID, status, metaJSON).Scan(&created)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_runbooks(document_id,source_kind,source_ref,conversation_id,steps,source_snapshot) VALUES($1,$2,$3,NULLIF($4,''),$5,$6::jsonb)`, id, in.Source, binding.SourceID, binding.ConversationID, stepsJSON, snapshotJSON)
		}
		if err == nil {
			snapshot := knowledge.Document{ID: id, ApplicationID: binding.ApplicationID, Title: in.Title, Body: body, Source: "runbook://v3/" + id, Version: 1, ContentHash: store.Hash(body), Lifecycle: status, Metadata: metadata, CreatedAt: created, EvidenceIDs: []string{}}
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) VALUES($1,1,$2,$3)`, id, snapshot, user.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,1,$2,'v3.runbook.created')`, id, user.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'runbook.created',$3,'succeeded',$4)`, user.ID, binding.ApplicationID, id, map[string]any{"status": status, "source": in.Source, "environment_id": scopeIDs(r)[0]})
		}
		if err == nil {
			err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID)
		}
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, 401, "unauthorized", "Session expired")
			return
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
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a Runbook")
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
		// The document and its ownership/scope may change after the first read.
		// Lock the current row and recheck both maintainer and matrix grants on
		// this transaction before changing lifecycle or writing audit records.
		var current knowledge.Document
		current.ID = id
		var metadata knowledge.Metadata
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR UPDATE`, id).Scan(&current.ApplicationID, &current.Source, &current.Version, &current.Lifecycle, &metadata)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbook unavailable")
			return
		}
		current.Metadata = metadata
		if !isRunbook(current.Source) || !documentInScope(r, current) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if current.Version != doc.Version {
			knowledgeError(w, store.ErrConflict)
			return
		}
		if current.Lifecycle != "draft" {
			httpapi.Error(w, 409, "invalid_state", "Only drafts can be submitted")
			return
		}
		if current.Metadata.MaintainerID != httpapi.CurrentUser(r).ID {
			httpapi.Error(w, 403, "forbidden", "Only the maintainer can submit this draft")
			return
		}
		if err = s.authorizeDocumentWithTx(r, tx, current, "manage"); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				httpapi.Error(w, 403, "forbidden", "Runbook management denied")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
			}
			return
		}
		result, err := tx.Exec(r.Context(), `UPDATE knowledge_documents SET lifecycle='review' WHERE id=$1 AND version=$2 AND lifecycle='draft'`, id, current.Version)
		if err == nil && result.RowsAffected() != 1 {
			err = store.ErrConflict
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,'lifecycle.review')`, id, current.Version, httpapi.CurrentUser(r).ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'runbook.submitted',$3,'succeeded',$4)`, httpapi.CurrentUser(r).ID, current.ApplicationID, id, map[string]any{"version": current.Version, "environment_id": scopeIDs(r)[0]})
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

// Dry-run is a recorded validation preview. It never invokes connectors,
// tools, models, proposals, or the independent execution service.
func (s *Service) dryRunHandler(api *httpapi.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment to validate a Runbook")
			return
		}
		id := r.PathValue("id")
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		var doc knowledge.Document
		doc.ID = id
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR SHARE`, id).
			Scan(&doc.ApplicationID, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (!isRunbook(doc.Source) || !documentInScope(r, doc)) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
			return
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		// A preview is persisted and audited, so published Runbooks also
		// require write authority over this specific document.
		if err = s.authorizeDocumentWithTx(r, tx, doc, "manage"); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				httpapi.Error(w, 404, "not_found", "Runbook not found")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Knowledge access could not be checked")
			}
			return
		}
		var steps []Step
		err = tx.QueryRow(r.Context(), `SELECT steps FROM v3_runbooks WHERE document_id=$1 FOR SHARE`, doc.ID).Scan(&steps)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 422, "no_structured_steps", "This Runbook has no structured steps to validate")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Runbook unavailable")
			return
		}
		checks, valid := previewSteps(steps)
		checkJSON, err := json.Marshal(checks)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		previewID := store.ID()
		actor := httpapi.CurrentUser(r).ID
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_runbook_validation_previews
 (id,document_id,document_version,actor_id,step_count,readonly_valid,checks)
 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, previewID, id, doc.Version, actor, len(steps), valid, checkJSON)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'runbook.validation_preview',$3,'succeeded',
  jsonb_build_object('environment_id',$4::text,'version',$5::int,'readonly_valid',$6::boolean,'steps',$7::int))`,
				actor, doc.ApplicationID, id, scopeIDs(r)[0], doc.Version, valid, len(steps))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"id": previewID, "steps": len(steps), "ok": valid, "mode": "validation_only", "checks": checks})
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
	if in.Status == "review" && len(in.Steps) == 0 {
		return errors.New("Runbook review requires at least one human-reviewed step")
	}
	if len(in.From) > 200 || len(in.Steps) > 100 {
		return errors.New("source or step count exceeds limit")
	}
	if in.Source == "blank" && strings.TrimSpace(in.From) != "" {
		return errors.New("blank Runbook cannot claim a source")
	}
	if in.Source != "blank" && strings.TrimSpace(in.From) == "" {
		return errors.New("source reference is required")
	}
	if in.ExpectedSourceSHA256 != "" {
		if in.Source == "blank" || len(in.ExpectedSourceSHA256) != 64 {
			return errors.New("invalid expected source digest")
		}
		for _, character := range in.ExpectedSourceSHA256 {
			if character < '0' || character > '9' && character < 'a' || character > 'f' {
				return errors.New("invalid expected source digest")
			}
		}
	}
	for i, step := range in.Steps {
		if step.N != 0 && step.N != i+1 {
			return fmt.Errorf("invalid step number %d", i+1)
		}
		if strings.TrimSpace(step.Title) == "" || len([]rune(step.Title)) > 200 || len(step.Body) > 16000 || len(step.Code) > 16000 {
			return fmt.Errorf("invalid step %d", i+1)
		}
		if step.Kind != "query" && step.Kind != "check" && step.Kind != "action" && step.Kind != "verify" && step.Kind != "act" && step.Kind != "judge" {
			return fmt.Errorf("unknown step kind %d", i+1)
		}
		if len(step.Connectors) > 10 {
			return fmt.Errorf("too many connectors in step %d", i+1)
		}
		if step.Tool != nil && (len(step.Tool.Brand) > 80 || len(step.Tool.Text) > 4096) {
			return fmt.Errorf("tool display exceeds limit in step %d", i+1)
		}
	}
	return nil
}

func normalizeSteps(steps []Step) []Step {
	for i := range steps {
		steps[i].N = i + 1
		switch steps[i].Kind {
		case "act":
			steps[i].Kind = "action"
		case "judge":
			steps[i].Kind = "check"
		}
		if steps[i].Kind == "action" || steps[i].Code != "" || steps[i].Tool != nil {
			steps[i].Approval = true
			steps[i].Readonly = false
		}
	}
	return steps
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

var errSourceEmpty = errors.New("source has no completed content")

type sourceBinding struct {
	ConversationID string
	ApplicationID  string
	TeamID         string
	SourceID       string
	ContentHash    string
	Title          string
	Outline        string
	Steps          []Step
}

// The source lookup and Runbook insert share one transaction. A key-share lock
// prevents deleting the conversation before the new FK is committed. A plain
// client-supplied source ID never establishes provenance by itself.
func (s *Service) validateSource(r *http.Request, tx pgx.Tx, in createInput) (sourceBinding, error) {
	switch in.Source {
	case "postmortem":
		envs := scopeIDs(r)
		if len(envs) != 1 {
			return sourceBinding{}, store.ErrForbidden
		}
		var app, team, actualEnvironment string
		err := tx.QueryRow(r.Context(), `SELECT pm.application_id,COALESCE(a.owner_team_id,''),pm.environment_id
 FROM v3_postmortems pm
 JOIN applications a ON a.id=pm.application_id AND NOT a.archived
 JOIN v3_application_environments ae ON ae.application_id=pm.application_id AND ae.environment_id=pm.environment_id
 JOIN ops_incidents i ON i.id=pm.incident_id AND i.application_id=pm.application_id
  AND i.investigation_id=pm.investigation_id AND i.status='resolved'
 JOIN application_environments app_env ON app_env.id=i.environment_id
  AND app_env.application_id=pm.application_id AND app_env.key=pm.environment_id AND NOT app_env.archived
 JOIN investigations inv ON inv.id=pm.investigation_id AND inv.application_id=pm.application_id AND inv.status='closed'
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN events closed ON closed.seq=pm.close_event_seq AND closed.investigation_id=inv.id AND closed.type='investigation.closed'
 WHERE (pm.id=$1 OR pm.incident_id=$1) AND pm.status='published'
 ORDER BY CASE WHEN pm.id=$1 THEN 0 ELSE 1 END LIMIT 1
 FOR SHARE OF pm,a,ae,i,app_env,inv,report,closed`, in.From).Scan(&app, &team, &actualEnvironment)
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceBinding{}, store.ErrNotFound
		}
		if err != nil {
			return sourceBinding{}, err
		}
		if actualEnvironment != envs[0] {
			return sourceBinding{}, store.ErrForbidden
		}
		for _, module := range []string{"knowledge", "events"} {
			decision := rbac.V3Decision{Module: module, Action: "view", EnvironmentID: actualEnvironment, ApplicationID: app}
			err = s.Policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), decision)
			if errors.Is(err, store.ErrForbidden) && team != "" {
				decision.ApplicationID = ""
				decision.TeamID = team
				err = s.Policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), decision)
			}
			if err != nil {
				return sourceBinding{}, err
			}
		}
		return sourceBinding{ApplicationID: app, TeamID: team}, nil
	case "template":
		for _, template := range knowledge.Templates() {
			if template.ID == in.From && template.ID != "blank" {
				return sourceBinding{}, nil
			}
		}
		return sourceBinding{}, store.ErrNotFound
	case "import":
		envs := scopeIDs(r)
		if len(envs) != 1 {
			return sourceBinding{}, store.ErrForbidden
		}
		var owner, actualEnvironment, kind, mime, name string
		var size int
		err := tx.QueryRow(r.Context(), `SELECT owner_id,environment_id,kind,mime,name,size_bytes FROM v3_uploads WHERE id=$1 FOR SHARE`, in.From).Scan(&owner, &actualEnvironment, &kind, &mime, &name, &size)
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceBinding{}, store.ErrNotFound
		}
		if err != nil {
			return sourceBinding{}, err
		}
		if owner != httpapi.CurrentUser(r).ID || actualEnvironment != envs[0] {
			return sourceBinding{}, store.ErrForbidden
		}
		// Uploaded Markdown can be cited as an import source. A URL or a
		// non-Markdown file is not yet a parsed Runbook source.
		if kind != "file" || size == 0 || !(mime == "text/markdown" || mime == "text/plain") || !strings.HasSuffix(strings.ToLower(name), ".md") {
			return sourceBinding{}, errSourceEmpty
		}
		return sourceBinding{}, nil
	case "conversation":
		envs := scopeIDs(r)
		if len(envs) != 1 {
			return sourceBinding{}, store.ErrForbidden
		}
		var owner, actualEnvironment string
		err := tx.QueryRow(r.Context(), `SELECT owner_id,environment_id FROM v3_conversations WHERE id=$1 FOR SHARE`, in.From).Scan(&owner, &actualEnvironment)
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceBinding{}, store.ErrNotFound
		}
		if err != nil {
			return sourceBinding{}, err
		}
		if owner != httpapi.CurrentUser(r).ID || actualEnvironment != envs[0] {
			return sourceBinding{}, store.ErrForbidden
		}
		err = s.Policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: actualEnvironment})
		if err != nil {
			return sourceBinding{}, err
		}
		var hasContent bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_conversation_messages WHERE conversation_id=$1 AND state='completed' AND jsonb_array_length(parts)>0)`, in.From).Scan(&hasContent)
		if err != nil {
			return sourceBinding{}, err
		}
		if !hasContent {
			return sourceBinding{}, errSourceEmpty
		}
		return sourceBinding{ConversationID: in.From}, nil
	default:
		return sourceBinding{}, nil
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
	return s.authorizeDocumentUsing(r, s.DB, d, action, func(decision rbac.V3Decision) error {
		return s.Policy.Authorize(r.Context(), httpapi.CurrentUser(r), decision)
	})
}

type runbookQueryRow interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) authorizeDocumentUsing(r *http.Request, q runbookQueryRow, d knowledge.Document, action string, authorize func(rbac.V3Decision) error) error {
	_, err := s.authorizedDocumentEnvironmentsUsing(r, q, d, action, authorize)
	return err
}

// Return only environments for which this exact object and the current
// principal pass every scope and grant check. A multi-environment response
// must not reveal other environment IDs merely because one ID was readable.
func (s *Service) authorizedDocumentEnvironmentsUsing(r *http.Request, q runbookQueryRow, d knowledge.Document, action string, authorize func(rbac.V3Decision) error) ([]string, error) {
	if !documentInScope(r, d) {
		return nil, store.ErrForbidden
	}
	allowed := make([]string, 0, len(scopeIDs(r)))
	for _, env := range scopeIDs(r) {
		if !contains(d.Metadata.EnvironmentIDs, env) {
			continue
		}
		// Inspect persisted JSON before authorization. Decoding into Metadata
		// erases the difference between a missing team and team_id:null/"".
		// A malformed environment list can likewise contain a valid env plus
		// an unknown entry, and must not pass a narrower Go membership check.
		var teamScoped bool
		var teamID string
		err := q.QueryRow(r.Context(), `SELECT d.metadata ? 'team_id',COALESCE(d.metadata->>'team_id','')
 FROM knowledge_documents d WHERE d.id=$1 AND d.version=$2 AND COALESCE(d.application_id,'')=$3
 AND jsonb_typeof(d.metadata)='object' AND NOT (d.metadata ? 'source_registration_id')
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
   THEN jsonb_array_length(d.metadata->'environment_ids')>0
     AND d.metadata->'environment_ids' ? $4
     AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item
       WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='')
   ELSE false END
 AND (($3<>'' AND EXISTS (SELECT 1 FROM applications a
   JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$4
   JOIN application_environments e ON e.application_id=a.id AND e.key=$4 AND NOT e.archived
   WHERE a.id=$3 AND NOT a.archived
   AND (NOT (d.metadata ? 'team_id') OR (
     jsonb_typeof(d.metadata->'team_id')='string'
     AND btrim(d.metadata->>'team_id')<>''
     AND d.metadata->>'team_id'=a.owner_team_id))))
 OR ($3='' AND d.application_id IS NULL AND d.metadata->>'visibility'='workspace'
   AND NOT (d.metadata ? 'team_id')))`, d.ID, d.Version, d.ApplicationID, env).Scan(&teamScoped, &teamID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = authorize(rbac.V3Decision{Module: "knowledge", Action: action, EnvironmentID: env, ApplicationID: d.ApplicationID})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if teamScoped {
			err = authorize(rbac.V3Decision{Module: "knowledge", Action: action, EnvironmentID: env, TeamID: teamID})
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		allowed = append(allowed, env)
	}
	if len(allowed) == 0 {
		return nil, store.ErrForbidden
	}
	return allowed, nil
}

func (s *Service) authorizeDocumentWithTx(r *http.Request, tx pgx.Tx, d knowledge.Document, action string) error {
	return s.authorizeDocumentUsing(r, tx, d, action, func(decision rbac.V3Decision) error {
		return s.Policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), decision)
	})
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
		// Legacy documents without an explicit environment cannot be safely
		// projected into a V3 instance's environment boundary.
		return false
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
