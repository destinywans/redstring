package v3alertrules

import (
	"net/http"
	"sort"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type ruleLabel struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

// mainRuleDetail retains the V3 page field names but does not manufacture
// editor-only values from a provider-observed Prometheus rule.
type mainRuleDetail struct {
	ID                        string            `json:"id"`
	Folder                    string            `json:"folder"`
	Name                      string            `json:"name"`
	State                     string            `json:"state"`
	EnvironmentID             string            `json:"environment_id"`
	ApplicationID             string            `json:"application_id"`
	Datasource                string            `json:"datasource"`
	QueryRef                  *string           `json:"query_ref"`
	Expr                      string            `json:"expr"`
	ForSeconds                float64           `json:"for_seconds"`
	Preview                   any               `json:"preview"`
	Condition                 any               `json:"condition"`
	Evaluation                any               `json:"evaluation"`
	EvaluationIntervalSeconds *float64          `json:"evaluation_interval_seconds"`
	EvaluationMS              *float64          `json:"evaluation_ms"`
	Labels                    []ruleLabel       `json:"labels"`
	Annotations               map[string]string `json:"annotations"`
	Annotation                *string           `json:"annotation"`
	Runbook                   *string           `json:"runbook"`
	RoutePath                 any               `json:"route_path"`
	AITriage                  any               `json:"ai_triage"`
	LastEval                  *string           `json:"last_eval"`
	// LastError is Prometheus's error for a rule whose evaluation fails
	// (state "error"); null otherwise (PA-210).
	LastError   *string  `json:"last_error"`
	Partial     bool     `json:"partial"`
	Unavailable []string `json:"unavailable"`
}

func (s *Service) MainDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	item, lookupError := s.findRule(r, r.PathValue("id"))
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	labels := make([]ruleLabel, 0, len(item.Labels))
	for key, value := range item.Labels {
		labels = append(labels, ruleLabel{Key: key, Value: value})
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
	annotations := item.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	var annotation *string
	if value, ok := annotations["summary"]; ok {
		annotation = &value
	}
	var interval *float64
	if item.evaluationInterval > 0 {
		value := item.evaluationInterval.Seconds()
		interval = &value
	}
	var lastEval *string
	if item.LastEvaluation != "" {
		lastEval = &item.LastEvaluation
	}
	var lastError *string
	if item.State == "error" && item.lastError != "" {
		lastError = &item.lastError
	}
	var evaluationMS *float64
	if lastEval != nil && item.EvaluationMS > 0 {
		evaluationMS = &item.EvaluationMS
	}
	unavailable := []string{"query_ref", "preview", "condition", "evaluation", "runbook", "route_path", "ai_triage"}
	if annotation == nil {
		unavailable = append(unavailable, "annotation")
	}
	if interval == nil {
		unavailable = append(unavailable, "evaluation_interval_seconds")
	}
	if lastEval == nil {
		unavailable = append(unavailable, "last_eval")
	}
	if evaluationMS == nil {
		unavailable = append(unavailable, "evaluation_ms")
	}
	httpapi.WriteJSON(w, http.StatusOK, mainRuleDetail{
		ID: item.ID, Folder: item.Folder, Name: item.Name, State: item.State,
		EnvironmentID: item.EnvironmentID, ApplicationID: item.ApplicationID,
		Datasource: item.Datasource, Expr: item.Expr, ForSeconds: item.ForSeconds,
		EvaluationIntervalSeconds: interval, EvaluationMS: evaluationMS,
		Labels: labels, Annotations: annotations, Annotation: annotation,
		LastEval: lastEval, LastError: lastError, Partial: true, Unavailable: unavailable,
	})
}
