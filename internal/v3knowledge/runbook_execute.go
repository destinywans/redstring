package v3knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/jackc/pgx/v5"
)

const runbookRequestRate = "request_rate"
const maxRunbookExecuteSteps = 10

type runbookExecutionInput struct {
	ExpectedVersion int `json:"expected_version"`
}

type runbookMetricStep struct {
	Step        int       `json:"step"`
	QueryID     string    `json:"query_id"`
	CapturedAt  time.Time `json:"captured_at"`
	SampleCount int       `json:"sample_count"`
	Latest      *float64  `json:"latest"`
}

type runbookExecutionPlan struct {
	document knowledge.Document
	steps    []Step
	binding  string
}

// A small, typed operation list. Step text, scripts and tool display names
// have no role in choosing what the server sends to Prometheus.
func runbookExecutable(steps []Step) bool {
	if len(steps) < 1 || len(steps) > maxRunbookExecuteSteps {
		return false
	}
	for _, step := range steps {
		if (step.Kind != "query" && step.Kind != "check" && step.Kind != "verify") || !step.Readonly || step.Approval || step.Tool != nil || step.Code != "" ||
			len(step.Connectors) != 1 || step.Connectors[0] == "" || step.Probe != runbookRequestRate {
			return false
		}
	}
	return true
}

func (s *Service) runbookExecutionPlan(w http.ResponseWriter, r *http.Request, tx pgx.Tx, version int, env string) (runbookExecutionPlan, bool) {
	var plan runbookExecutionPlan
	doc := &plan.document
	doc.ID = r.PathValue("id")
	ctx := r.Context()
	if err := lockRunbookReadSession(ctx, tx, r); err != nil {
		runbookReadAuthorityError(w, err)
		return plan, false
	}
	if err := lockRunbookGrantSources(ctx, tx, httpapi.CurrentUser(r).ID); err != nil {
		runbookReadAuthorityError(w, err)
		return plan, false
	}
	err := tx.QueryRow(ctx, `SELECT COALESCE(application_id,''),source,version,lifecycle,metadata
 FROM knowledge_documents WHERE id=$1 FOR SHARE`, doc.ID).Scan(&doc.ApplicationID, &doc.Source, &doc.Version, &doc.Lifecycle, &doc.Metadata)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!isRunbook(doc.Source) || !documentInScope(r, *doc)) {
		httpapi.Error(w, 404, "not_found", "Runbook not found")
		return plan, false
	}
	if err != nil {
		knowledgeError(w, err)
		return plan, false
	}
	if err = s.authorizeRunbookReadWithTx(r, tx, *doc); err == nil {
		err = s.authorizeDocumentWithTx(r, tx, *doc, "manage")
	}
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Runbook not found")
		} else {
			runbookReadAuthorityError(w, err)
		}
		return plan, false
	}
	if doc.Lifecycle != "published" || doc.Version != version || doc.ApplicationID == "" {
		httpapi.Error(w, 409, "runbook_execution_unavailable", "A current, published, application-bound Runbook is required")
		return plan, false
	}
	if err = tx.QueryRow(ctx, `SELECT steps FROM v3_runbooks WHERE document_id=$1`, doc.ID).Scan(&plan.steps); err != nil {
		structuredRunbookError(w, err)
		return plan, false
	}
	if !runbookExecutable(plan.steps) {
		httpapi.Error(w, 422, "runbook_steps_unsupported", "Every step must use the registered read-only request-rate query and its Prometheus connector")
		return plan, false
	}
	rows, err := tx.Query(ctx, `SELECT t.id,t.connector_id FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind='metric_resource' AND c.provider='prometheus' LIMIT 2`, doc.ApplicationID, env)
	if err != nil {
		knowledgeError(w, err)
		return plan, false
	}
	type target struct{ Binding, Connector string }
	targets, err := pgx.CollectRows(rows, pgx.RowToStructByPos[target])
	if err != nil {
		knowledgeError(w, err)
		return plan, false
	}
	if len(targets) != 1 || slices.ContainsFunc(plan.steps, func(step Step) bool { return step.Connectors[0] != targets[0].Connector }) {
		httpapi.Error(w, 409, "runbook_connector_changed", "The steps must name the application's current sole Prometheus connector")
		return plan, false
	}
	plan.binding = targets[0].Binding
	return plan, true
}

func runbookMetricSamples(e v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow) (int, *float64, bool) {
	if len(e.Series) > 1 || e.CapturedAt.IsZero() {
		return 0, nil, false
	}
	samples := 0
	var latest *float64
	for _, series := range e.Series {
		if len(series.Samples) > 31 {
			return 0, nil, false
		}
		previous := math.Inf(-1)
		for _, sample := range series.Samples {
			v, err := strconv.ParseFloat(sample.Value, 64)
			stamp := sample.Timestamp
			if err != nil || math.IsInf(v, 0) || math.IsNaN(stamp) || stamp <= previous || stamp < float64(window.Start.Unix()) || stamp > float64(window.End.Unix()) {
				return 0, nil, false
			}
			previous = stamp
			if !math.IsNaN(v) {
				samples++
				latest = &v
			}
		}
	}
	return samples, latest, true
}

func (s *Service) executeReadOnlyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		scope, ok := httpapi.V3ScopeFromContext(r.Context())
		if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a Runbook")
			return
		}
		var in runbookExecutionInput
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if in.ExpectedVersion < 1 {
			httpapi.Error(w, 422, "invalid_runbook", "Expected version is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		env := scope.EnvironmentIDs[0]
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(ctx)
		plan, allowed := s.runbookExecutionPlan(w, r, tx, in.ExpectedVersion, env)
		if !allowed {
			return
		}
		if s.MetricReader == nil {
			httpapi.Error(w, 503, "runbook_source_unavailable", "Prometheus reader unavailable")
			return
		}
		if err = tx.Commit(ctx); err != nil {
			knowledgeError(w, err)
			return
		}
		user := httpapi.CurrentUser(r)
		end := time.Now().UTC().Truncate(time.Second)
		window := connectors.TimeWindow{Start: end.Add(-15 * time.Minute), End: end, StepSeconds: 30}
		evidence, err := s.MetricReader.QueryService(ctx, scope, user, plan.document.ApplicationID, env, runbookRequestRate, window)
		if err != nil || evidence.ApplicationID != plan.document.ApplicationID || evidence.EnvironmentID != env || evidence.BindingID != plan.binding ||
			evidence.ConnectorID != plan.steps[0].Connectors[0] || evidence.QueryID != runbookRequestRate {
			httpapi.Error(w, 503, "runbook_source_unavailable", "Authorized request-rate evidence could not be read")
			return
		}
		n, latest, valid := runbookMetricSamples(evidence, window)
		if !valid {
			httpapi.Error(w, 503, "runbook_source_unavailable", "The metric result is incomplete")
			return
		}
		results := make([]runbookMetricStep, 0, len(plan.steps))
		for _, step := range plan.steps {
			results = append(results, runbookMetricStep{step.N, runbookRequestRate, evidence.CapturedAt, n, latest})
		}
		// Recheck the exact document and connector before publishing or storing results.
		tx, err = s.DB.Begin(ctx)
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(ctx)
		current, allowed := s.runbookExecutionPlan(w, r, tx, in.ExpectedVersion, env)
		if !allowed {
			return
		}
		if current.document.ApplicationID != plan.document.ApplicationID || current.binding != plan.binding ||
			s.MetricReader.AuthorizeMetricSourceVersionTx(ctx, tx, user.ID, env, evidence.ApplicationID, evidence.BindingID, evidence.ConnectorID,
				evidence.ResourceID, evidence.QueryID, evidence.SourceFingerprint) != nil {
			httpapi.Error(w, 409, "runbook_connector_changed", "Source or authorization changed during execution")
			return
		}
		status := "no_samples"
		if n > 0 {
			status = "observed"
			_, err = tx.Exec(ctx, `UPDATE v3_runbooks SET last_verified_at=GREATEST(last_verified_at,$2) WHERE document_id=$1`, plan.document.ID, evidence.CapturedAt)
		}
		var id = store.ID()
		if err == nil {
			audit, _ := json.Marshal(map[string]any{"execution_id": id, "environment_id": env, "version": in.ExpectedVersion, "source": "prometheus", "binding_id": plan.binding, "steps": results, "status": status})
			_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'runbook.readonly_execution',$3,'succeeded',$4)`, user.ID, evidence.ApplicationID, plan.document.ID, audit)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			knowledgeError(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"id": id, "version": in.ExpectedVersion, "mode": "read_only", "status": status, "steps": results})
	}
}
