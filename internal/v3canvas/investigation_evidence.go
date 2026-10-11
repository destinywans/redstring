package v3canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// maxInvestigationToolCalls bounds the evidence reads of one reply, and
// maxInvestigationTools the distinct tools offered (PA-72: 4 → 6, so a
// release question can read profile, metrics, logs and the workload).
const (
	maxInvestigationToolCalls = 6
	maxInvestigationTools     = 6
)

var errInvestigationEvidence = errors.New("invalid investigation evidence or tool sequence")
var investigationStepID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

type investigationReplyEvidence struct {
	ID, Application, Environment, Tool, Step string
	Evidence                                 inference.Evidence
}

type investigationEvidenceCollector struct {
	mu                       sync.Mutex
	ctx                      context.Context
	application, environment string
	names                    map[string]bool
	calls                    map[string]inference.ToolInvocation
	evidence                 []investigationReplyEvidence
	err                      error
	audit                    func(inference.ToolInvocation) error
}

func investigationToolKind(name string) (kind, source string) {
	switch name {
	case "read_service_profile":
		return "service_profile", "service_catalog"
	case "read_service_metrics":
		return "metrics", "prometheus"
	case "read_service_logs":
		return "logs", "loki"
	case "search_knowledge":
		return "knowledge", "knowledge://published-documents"
	case workloadToolName:
		return "workload", "kubernetes"
	case changesToolName:
		return "changes", "gitlab"
	}
	return "", ""
}

func validInvestigationEvidence(name, app, env string, e inference.Evidence) bool {
	kind, source := investigationToolKind(name)
	target := base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
	if name == "search_knowledge" {
		target = app
	}
	// The log tool reads the service's single Loki or Elasticsearch binding.
	if name == "read_service_logs" && e.Source == "elasticsearch" {
		source = e.Source
	}
	if kind == "" || e.Kind != kind || e.Source != source || e.TargetID != target ||
		strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Content) == "" ||
		len(e.Title) > 512 || len(e.Kind)+len(e.Title)+len(e.Content)+len(e.Source) > maxInvestigationText {
		return false
	}
	for _, value := range []string{e.Kind, e.Title, e.Content, e.Source} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

// Every terminal callback precedes the runtime handing evidence back to the
// model. The collector fails closed even if a broken runner ignores a callback
// error, duplicates an event, or claims evidence without its audited invocation.
func (c *investigationEvidenceCollector) handle(e inference.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	fail := func(err error) error { c.err = err; return err }
	if err := c.ctx.Err(); err != nil {
		return fail(err)
	}
	if e.Invocation != nil {
		v := *e.Invocation
		if e.Type != "tool_invocation" || e.StepID != v.StepID || !investigationStepID.MatchString(v.StepID) ||
			!c.names[v.Name] || v.DurationMS < 0 {
			return fail(errInvestigationEvidence)
		}
		previous, exists := c.calls[v.StepID]
		switch v.Status {
		case "started":
			if exists || len(c.calls) >= maxInvestigationToolCalls || v.DurationMS != 0 || v.TargetID != "" {
				return fail(errInvestigationEvidence)
			}
		case "succeeded", "failed":
			if !exists || previous.Status != "started" || previous.Name != v.Name || (v.Status == "failed" && v.TargetID != "") {
				return fail(errInvestigationEvidence)
			}
		default:
			return fail(errInvestigationEvidence)
		}
		if err := c.audit(v); err != nil {
			return fail(err)
		}
		c.calls[v.StepID] = v
		if v.Status == "failed" {
			return fail(errInvestigationEvidence)
		}
		return nil
	}
	if e.Type == "tool_invocation" {
		return fail(errInvestigationEvidence)
	}
	if e.Type == "evidence" || e.Evidence != nil {
		v, ok := c.calls[e.StepID]
		if e.Type != "evidence" || e.Evidence == nil || !ok || v.Status != "succeeded" || v.TargetID != e.Evidence.TargetID ||
			!validInvestigationEvidence(v.Name, c.application, c.environment, *e.Evidence) {
			return fail(errInvestigationEvidence)
		}
		for _, saved := range c.evidence {
			if saved.Step == e.StepID {
				return fail(errInvestigationEvidence)
			}
		}
		c.evidence = append(c.evidence, investigationReplyEvidence{ID: "EVD-" + store.ID(), Application: c.application, Environment: c.environment, Tool: v.Name, Step: e.StepID, Evidence: *e.Evidence})
	}
	return nil
}

func (c *investigationEvidenceCollector) finish() ([]investigationReplyEvidence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if len(c.evidence) != len(c.calls) {
		return nil, errInvestigationEvidence
	}
	return append([]investigationReplyEvidence(nil), c.evidence...), nil
}

// Lock order matches reply completion: principal, branch, investigation, reply.
// A fixed application captured for this run must still match after any wait.
func (s *Service) investigationToolTx(ctx context.Context, r *http.Request, user core.User, env, id, app string) (pgx.Tx, investigationCanvasTarget, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return nil, investigationCanvasTarget{}, err
	}
	rollback := func(err error) (pgx.Tx, investigationCanvasTarget, error) {
		_ = tx.Rollback(ctx)
		return nil, investigationCanvasTarget{}, err
	}
	if f := s.canvasPrincipal(ctx, tx, r); f != nil {
		return rollback(errors.New(f.code))
	}
	_, revision, f := s.lockedCanvasBranch(ctx, tx, r.PathValue("id"), r.PathValue("node"))
	if f != nil {
		return rollback(errors.New(f.code))
	}
	target, f := s.writeCanvasTarget(ctx, tx, r, env, r.PathValue("id"))
	if f != nil {
		return rollback(errors.New(f.code))
	}
	if app != "" && target.application != app {
		return rollback(errInvestigationEvidence)
	}
	var owner string
	var baseRevision int64
	err = tx.QueryRow(ctx, `SELECT user_id,base_revision FROM v3_investigation_node_replies
 WHERE id=$1 AND investigation_id=$2 AND branch_id=$3 AND state='running'
 AND lease_expires_at>clock_timestamp() FOR UPDATE`, id, r.PathValue("id"), r.PathValue("node")).Scan(&owner, &baseRevision)
	if err != nil {
		return rollback(err)
	}
	if owner != user.ID || baseRevision != revision {
		return rollback(errInvestigationEvidence)
	}
	return tx, target, nil
}

func (s *Service) authorizeInvestigationTool(ctx context.Context, r *http.Request, user core.User, env, id, app, name string) error {
	tx, target, err := s.investigationToolTx(ctx, r, user, env, id, app)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorizeInvestigationToolTx(ctx, tx, s.Store, user, target, name); err != nil {
		return err
	}
	if f := s.canvasSession(ctx, tx, r); f != nil {
		return errors.New(f.code)
	}
	return tx.Commit(ctx)
}

func authorizeInvestigationToolTx(ctx context.Context, tx pgx.Tx, st *store.Store, user core.User, target investigationCanvasTarget, name string) error {
	modules := []string{"services"}
	if name == "search_knowledge" {
		modules = []string{"knowledge"}
	}
	if name == "read_service_logs" {
		modules = append(modules, "logs")
	}
	if name == workloadToolName {
		modules = append(modules, "resources")
	}
	policy := &rbac.V3Policy{Store: st}
	for _, module := range modules {
		decision := rbac.V3Decision{Module: module, Action: "view", ApplicationID: target.application, EnvironmentID: target.environment}
		err := policy.AuthorizeWithTx(ctx, tx, user, decision)
		if errors.Is(err, store.ErrForbidden) && target.team != "" && module != "knowledge" {
			decision.ApplicationID = ""
			decision.TeamID = target.team
			err = policy.AuthorizeWithTx(ctx, tx, user, decision)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Published passages may have narrower team scope than the investigation.
// Recheck each referenced document at the final evidence transaction, after
// generation has finished, so unpublishing, version changes, or team-grant
// revocation cannot publish an older private snippet into a new branch reply.
func authorizeInvestigationKnowledgeTx(ctx context.Context, tx pgx.Tx, st *store.Store, user core.User, target investigationCanvasTarget, value investigationReplyEvidence) error {
	if value.Tool != "search_knowledge" {
		return nil
	}
	var body struct {
		Application string `json:"application_id"`
		Environment string `json:"environment_id"`
		Passages    []struct {
			ID      string `json:"document_id"`
			Version int    `json:"version"`
		} `json:"passages"`
	}
	if json.Unmarshal([]byte(value.Evidence.Content), &body) != nil || body.Application != target.application ||
		body.Environment != target.environment || body.Passages == nil || len(body.Passages) > 5 {
		return errInvestigationEvidence
	}
	for _, passage := range body.Passages {
		var team string
		var visible bool
		err := tx.QueryRow(ctx, `SELECT COALESCE(metadata->>'team_id',''),
 lifecycle='published' AND version=$2 AND application_id=$3 AND jsonb_typeof(metadata)='object'
 AND CASE WHEN jsonb_typeof(metadata->'environment_ids')='array' THEN
 (metadata->'environment_ids') ? $4 AND NOT EXISTS
 (SELECT 1 FROM jsonb_array_elements(metadata->'environment_ids') item WHERE jsonb_typeof(item)<>'string' OR btrim(item #>> '{}')='')
 ELSE false END
 AND NOT (metadata ? 'source_registration_id')
 AND (NOT (metadata ? 'team_id') OR (jsonb_typeof(metadata->'team_id')='string' AND metadata->>'team_id'=$5 AND $5<>''))
 FROM knowledge_documents WHERE id=$1 FOR SHARE`, passage.ID, passage.Version, target.application, target.environment, target.team).Scan(&team, &visible)
		if err != nil {
			return err
		}
		if !visible {
			return errInvestigationEvidence
		}
		if team != "" {
			policy := &rbac.V3Policy{Store: st}
			if err = policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: target.environment, TeamID: team}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) investigationReplyTools(ctx context.Context, r *http.Request, user core.User, env, id string, model inference.Model) ([]inference.EvidenceTool, *investigationEvidenceCollector, error) {
	collected := &investigationEvidenceCollector{ctx: ctx, names: map[string]bool{}, calls: map[string]inference.ToolInvocation{}}
	if !model.Tools || s.InvestigationToolsForTurn == nil {
		return nil, collected, nil
	}
	tx, target, err := s.investigationToolTx(ctx, r, user, env, id, "")
	if err != nil {
		return nil, collected, err
	}
	if f := s.canvasSession(ctx, tx, r); f != nil {
		_ = tx.Rollback(ctx)
		return nil, collected, errors.New(f.code)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, collected, err
	}
	collected.application, collected.environment = target.application, env
	tools, err := s.InvestigationToolsForTurn(ctx, user.ID, target.application, env, model.ID)
	if err != nil {
		return nil, collected, err
	}
	if len(tools) > maxInvestigationTools {
		return nil, collected, errInvestigationEvidence
	}
	for i, t := range tools {
		kind, _ := investigationToolKind(t.Name)
		if kind == "" || t.Read == nil || collected.names[t.Name] {
			return nil, collected, errInvestigationEvidence
		}
		collected.names[t.Name] = true
		read, name := t.Read, t.Name
		tools[i].Read = func(toolCtx context.Context, query string) (inference.Evidence, error) {
			if err := s.authorizeInvestigationTool(toolCtx, r, user, env, id, target.application, name); err != nil {
				return inference.Evidence{}, err
			}
			ev, err := read(toolCtx, query)
			if err != nil {
				return inference.Evidence{}, err
			}
			if !validInvestigationEvidence(name, target.application, env, ev) {
				return inference.Evidence{}, errInvestigationEvidence
			}
			if err = s.authorizeInvestigationTool(toolCtx, r, user, env, id, target.application, name); err != nil {
				return inference.Evidence{}, err
			}
			return ev, nil
		}
	}
	collected.audit = func(v inference.ToolInvocation) error {
		tx, current, err := s.investigationToolTx(ctx, r, user, env, id, target.application)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		// A failed read may have lost its source grant. Preserve its failed audit
		// while the attempt itself is still authorized; never label it success.
		if v.Status != "failed" {
			if err = authorizeInvestigationToolTx(ctx, tx, s.Store, user, current, v.Name); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'agent',$2,'v3.investigation_canvas.tool',$3,$4,$5::jsonb)`, user.ID, target.application, v.Name, v.Status,
			mustJSON(map[string]any{"investigation_id": r.PathValue("id"), "branch_id": r.PathValue("node"), "reply_id": id, "environment_id": env, "step_id": v.StepID, "duration_ms": v.DurationMS, "resource_id": v.TargetID}))
		if err != nil {
			return err
		}
		if f := s.canvasSession(ctx, tx, r); f != nil {
			return errors.New(f.code)
		}
		return tx.Commit(ctx)
	}
	return tools, collected, nil
}

// Evidence is committed atomically with the successful branch revision and
// reply audit. Legacy runs own evidence.run_id, so V3 leaves that FK null and
// binds reply/step identity through the receipt and Message.EvidenceIDs.
func persistInvestigationReplyEvidence(ctx context.Context, tx pgx.Tx, actor, app, env, investigation, node, reply string, values []investigationReplyEvidence) ([]string, *graphFailure) {
	if len(values) > maxInvestigationToolCalls {
		return nil, graphUnavailable()
	}
	if len(values) == 0 {
		return nil, nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE b.investigation_id=$1`, investigation).Scan(&count); err != nil {
		return nil, graphUnavailable()
	}
	if count+len(values) > maxInvestigationEvidence {
		return nil, &graphFailure{413, "canvas_graph_too_large", "Investigation evidence exceeds display limit"}
	}
	ids := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		if v.Application != app || v.Environment != env || !investigationStepID.MatchString(v.Step) || seen[v.Step] || !validInvestigationEvidence(v.Tool, app, env, v.Evidence) {
			return nil, graphUnavailable()
		}
		seen[v.Step] = true
		var proof bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management_audit WHERE actor_id=$1 AND application_id=$2
 AND action='v3.investigation_canvas.tool' AND target=$3 AND outcome='succeeded'
 AND changes->>'reply_id'=$4 AND changes->>'step_id'=$5 AND changes->>'branch_id'=$6
 AND changes->>'environment_id'=$7 AND changes->>'resource_id'=$8)`, actor, app, v.Tool, reply, v.Step, node, env, v.Evidence.TargetID).Scan(&proof); err != nil || !proof {
			return nil, graphUnavailable()
		}
		_, err := tx.Exec(ctx, `INSERT INTO evidence(id,branch_id,kind,title,content,source) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, node, v.Evidence.Kind, v.Evidence.Title, v.Evidence.Content, v.Evidence.Source)
		if err != nil {
			return nil, graphUnavailable()
		}
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'agent',$2,'v3.investigation_canvas.evidence',$3,'succeeded',$4::jsonb)`, actor, app, v.Tool,
			mustJSON(map[string]any{"investigation_id": investigation, "branch_id": node, "reply_id": reply, "step_id": v.Step, "evidence_id": v.ID, "environment_id": env, "source": v.Evidence.Source}))
		if err != nil {
			return nil, graphUnavailable()
		}
		ids = append(ids, v.ID)
	}
	return ids, nil
}
