package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
)

// DraftRepository must be bound to a dedicated alert-rule-drafts branch prefix.
// The existing configuration-promotion prefix is reserved for signed imports.
type DraftRepository interface {
	ProjectID() int64
	BranchPrefix() string
	ExportDraft(context.Context, string, string, []byte, string) (DraftMR, error)
}

// A small value type keeps the alert-rule domain independent of config sync.
// The composition root adapts a bound GitLab client to this interface.
type DraftMR struct {
	IID, SourceProjectID, TargetProjectID int64
	URL, State, SourceBranch              string
}

var ErrDraftRemoteConflict = errors.New("alert rule GitOps draft conflicts with remote content")

type WriteAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type PromQLValidator interface {
	ValidatePromQL(context.Context, string, string) error
}

type WriteService struct {
	DB             *pgxpool.Pool
	Access         WriteAccess
	Reader         *Service
	Repository     DraftRepository
	TrialRuntime   TrialRuntime
	EnableApproval AlertRuleApprovalSource
	EnableApplier  AlertRuleApplier
}

func NewWriteService(db *pgxpool.Pool, access WriteAccess, reader *Service, repository DraftRepository) *WriteService {
	return &WriteService{DB: db, Access: access, Reader: reader, Repository: repository}
}

func (s *WriteService) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"POST /api/v1/alert-rules", s.Create},
		{"PUT /api/v1/alert-rules/{id}", s.Update},
		{"POST /api/v1/alert-rules/import", s.Import},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, "alert_rules", "change")
	}
}

type writeProblem struct {
	status int
	code   string
	text   string
}

func (p *writeProblem) Error() string { return p.text }

func writeError(w http.ResponseWriter, err error) {
	var problem *writeProblem
	if errors.As(err, &problem) {
		httpapi.Error(w, problem.status, problem.code, problem.text)
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "alert_rule_draft_unavailable", "Alert rule draft storage or source is unavailable")
}

func problem(status int, code, message string) error {
	return &writeProblem{status: status, code: code, text: message}
}

func (s *WriteService) prerequisites(r *http.Request) (string, core.User, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		return "", core.User{}, problem(http.StatusConflict, "single_environment_required", "Select exactly one environment")
	}
	actor := httpapi.CurrentUser(r)
	if actor.ID == "" || actor.Disabled {
		return "", actor, problem(http.StatusForbidden, "forbidden", "A current user is required")
	}
	if s == nil || s.DB == nil || s.Access == nil || s.Reader == nil || s.Reader.Source == nil {
		return "", actor, problem(http.StatusServiceUnavailable, "not_connected", "Alert rule source is not connected")
	}
	if _, ok := s.Reader.Source.(PromQLValidator); !ok {
		return "", actor, problem(http.StatusServiceUnavailable, "not_connected", "PromQL syntax validation is not connected")
	}
	return scope.EnvironmentIDs[0], actor, nil
}

func (s *WriteService) requireDraftRepository() error {
	if s.Repository == nil || s.Repository.ProjectID() <= 0 || s.Repository.BranchPrefix() != "aiops-alert-rule-drafts/" {
		return problem(http.StatusServiceUnavailable, "not_connected", "Alert rule draft repository is not connected")
	}
	return nil
}

var idempotencyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func requestKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !idempotencyRE.MatchString(values[0]) {
		return "", problem(http.StatusUnprocessableEntity, "idempotency_key_required", "Supply one Idempotency-Key (1-128 safe characters)")
	}
	return values[0], nil
}

func requestHash(operation, env, id string, body any) (string, error) {
	b, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Env       string `json:"environment"`
		ID        string `json:"id"`
		Body      any    `json:"body"`
	}{operation, env, id, body})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type createRequest struct {
	DraftRuleSpec
	InvestigationID string `json:"investigation_id,omitempty"`
}

type updateRequest struct {
	ExpectedVersion int           `json:"expected_version"`
	ObservedRule    *observedHint `json:"observed_rule,omitempty"`
	Rule            DraftRuleSpec `json:"rule"`
}

type observedHint struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Name  string `json:"name"`
}

type importRequest struct {
	ApplicationID string `json:"application_id"`
	YAML          string `json:"yaml"`
	Mode          string `json:"mode"`
}

func (s *WriteService) Create(w http.ResponseWriter, r *http.Request) {
	env, actor, err := s.prerequisites(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := requestKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input createRequest
	if !httpapi.Decode(w, r, &input) {
		return
	}
	switch input.Mode {
	case "dry_run":
		s.handleTrial(w, r, env, actor, key, input.DraftRuleSpec)
		return
	case "enabled":
		s.handleEnable(w, r, env, actor, key, input)
		return
	}
	if err := s.requireDraftRepository(); err != nil {
		writeError(w, err)
		return
	}
	if err := draftMode(input.Mode); err != nil {
		writeError(w, err)
		return
	}
	digest, err := requestHash("create", env, "", input)
	if err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replay(r.Context(), actor, "create", key, digest, env, input.ApplicationID); err != nil {
		writeError(w, err)
		return
	} else if found {
		httpapi.WriteJSON(w, http.StatusCreated, result)
		return
	}
	if err := validateDraftSpec(input.DraftRuleSpec, env, input.ApplicationID); err != nil {
		writeError(w, validationProblem(err))
		return
	}
	if err := s.authorize(r.Context(), actor, env, input.ApplicationID); err != nil {
		writeError(w, err)
		return
	}
	group, observed, err := s.inspectCandidates(r.Context(), env, input.ApplicationID, []DraftRuleSpec{input.DraftRuleSpec})
	if err != nil {
		writeError(w, err)
		return
	}
	if observed[0].exists && input.ObservedRuleID == "" {
		writeError(w, problem(http.StatusConflict, "duplicate_rule", "A provider rule already uses this identity"))
		return
	}
	if err := validateObservedHint(input.ObservedRuleID, observed[0]); err != nil {
		writeError(w, err)
		return
	}
	if err := s.validatePromQL(r.Context(), env, input.Expr); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.reserveCreate(r.Context(), actor, env, key, digest, input.DraftRuleSpec, group[0], observed[0])
	if err != nil {
		writeError(w, err)
		return
	}
	result, err = s.export(r.Context(), actor, "create", key, digest, result)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, result)
}

func (s *WriteService) Update(w http.ResponseWriter, r *http.Request) {
	env, actor, err := s.prerequisites(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := requestKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if len(id) != 32 || !hexID(id) {
		writeError(w, problem(http.StatusNotFound, "not_found", "Draft not found"))
		return
	}
	var input updateRequest
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if err := s.requireDraftRepository(); err != nil {
		writeError(w, err)
		return
	}
	if err := draftMode(input.Rule.Mode); err != nil {
		writeError(w, err)
		return
	}
	digest, err := requestHash("update", env, id, input)
	if err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replay(r.Context(), actor, "update", key, digest, env, input.Rule.ApplicationID); err != nil {
		writeError(w, err)
		return
	} else if found {
		httpapi.WriteJSON(w, http.StatusOK, result)
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, problem(422, "invalid_version", "Expected draft version is required"))
		return
	}
	if err := validateDraftSpec(input.Rule, env, input.Rule.ApplicationID); err != nil {
		writeError(w, validationProblem(err))
		return
	}
	if err := s.authorize(r.Context(), actor, env, input.Rule.ApplicationID); err != nil {
		writeError(w, err)
		return
	}
	group, observed, err := s.inspectCandidates(r.Context(), env, input.Rule.ApplicationID, []DraftRuleSpec{input.Rule})
	if err != nil {
		writeError(w, err)
		return
	}
	if input.ObservedRule != nil {
		if input.ObservedRule.ID != observed[0].id || input.ObservedRule.Group != input.Rule.Group || input.ObservedRule.Name != input.Rule.Name || !observed[0].exists {
			writeError(w, problem(409, "source_changed", "Observed rule identity changed"))
			return
		}
	}
	if err := s.validatePromQL(r.Context(), env, input.Rule.Expr); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.reserveUpdate(r.Context(), actor, env, key, digest, id, input, group[0], observed[0])
	if err != nil {
		writeError(w, err)
		return
	}
	result, err = s.export(r.Context(), actor, "update", key, digest, result)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (s *WriteService) Import(w http.ResponseWriter, r *http.Request) {
	env, actor, err := s.prerequisites(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := requestKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input importRequest
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if err := s.requireDraftRepository(); err != nil {
		writeError(w, err)
		return
	}
	if err := draftMode(input.Mode); err != nil {
		writeError(w, err)
		return
	}
	digest, err := requestHash("import", env, "", input)
	if err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replay(r.Context(), actor, "import", key, digest, env, input.ApplicationID); err != nil {
		writeError(w, err)
		return
	} else if found {
		status := http.StatusCreated
		if result["status"] == "duplicates_only" {
			status = http.StatusOK
		}
		httpapi.WriteJSON(w, status, result)
		return
	}
	if err := s.authorize(r.Context(), actor, env, input.ApplicationID); err != nil {
		writeError(w, err)
		return
	}
	rules, err := parseRuleImportYAML(input.YAML, env, input.ApplicationID)
	if err != nil {
		writeError(w, validationProblem(err))
		return
	}
	groups, observed, err := s.inspectCandidates(r.Context(), env, input.ApplicationID, rules)
	if err != nil {
		writeError(w, err)
		return
	}
	for i, rule := range rules {
		if observed[i].exists {
			continue
		}
		if err := s.validatePromQL(r.Context(), env, rule.Expr); err != nil {
			writeError(w, err)
			return
		}
	}
	result, err := s.reserveImport(r.Context(), actor, env, key, digest, input.ApplicationID, rules, groups, observed)
	if err != nil {
		writeError(w, err)
		return
	}
	if result["status"] == "duplicates_only" {
		httpapi.WriteJSON(w, http.StatusOK, result)
		return
	}
	result, err = s.export(r.Context(), actor, "import", key, digest, result)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, result)
}

func draftMode(mode string) error {
	if mode == "draft" {
		return nil
	}
	if mode == "dry_run" || mode == "enabled" {
		return problem(http.StatusNotImplemented, "mode_unavailable", "This mode needs a separate preview or approval and execution path")
	}
	return problem(http.StatusUnprocessableEntity, "invalid_mode", "Choose draft mode")
}

func validationProblem(err error) error {
	if err == nil {
		return nil
	}
	return problem(http.StatusUnprocessableEntity, "invalid_alert_rule", "Invalid alert rule: "+err.Error())
}

func hexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *WriteService) validatePromQL(ctx context.Context, env, expression string) error {
	validator := s.Reader.Source.(PromQLValidator)
	if err := validator.ValidatePromQL(ctx, env, expression); err != nil {
		if errors.Is(err, ErrInvalidPromQL) {
			return problem(http.StatusUnprocessableEntity, "invalid_promql", "PromQL expression is invalid")
		}
		return problem(http.StatusServiceUnavailable, "promql_validation_unavailable", "Prometheus syntax validation is unavailable")
	}
	return nil
}

type boundGroup struct{ file, group string }
type observedIdentity struct {
	exists     bool
	id, digest string
}

func (s *WriteService) resolveGroup(env, app, group string) (boundGroup, error) {
	var match boundGroup
	for key, application := range s.Reader.bindings {
		if application != app || !strings.HasPrefix(key, env+"\x00") {
			continue
		}
		identity := strings.TrimPrefix(key, env+"\x00")
		parts := strings.Split(identity, "\x00")
		if len(parts) != 2 || parts[1] != group {
			continue
		}
		if match.file != "" {
			return boundGroup{}, problem(409, "ambiguous_group", "Rule group has more than one bound source file")
		}
		match = boundGroup{file: parts[0], group: group}
	}
	if match.file == "" {
		return boundGroup{}, problem(404, "not_found", "Bound rule group not found")
	}
	return match, nil
}

func (s *WriteService) inspectCandidates(ctx context.Context, env, app string, rules []DraftRuleSpec) ([]boundGroup, []observedIdentity, error) {
	groups := make([]boundGroup, len(rules))
	for i, rule := range rules {
		group, err := s.resolveGroup(env, app, rule.Group)
		if err != nil {
			return nil, nil, err
		}
		groups[i] = group
	}
	provider, err := s.Reader.Source.List(ctx, env)
	if err != nil {
		return nil, nil, problem(503, "rule_source_unavailable", "Bound Prometheus rule source is unavailable")
	}
	if len(provider) > 500 {
		return nil, nil, problem(503, "rule_source_unavailable", "Prometheus rule source exceeded the limit")
	}
	observed := make([]observedIdentity, len(rules))
	for i, rule := range rules {
		for _, item := range provider {
			if item.GroupIdentity != groups[i].file+"\x00"+rule.Group || item.Name != rule.Name {
				continue
			}
			if item.Labels["environment"] != env || item.Labels["application"] != app {
				return nil, nil, problem(503, "rule_scope_conflict", "A bound rule has conflicting scope labels")
			}
			if observed[i].exists {
				return nil, nil, problem(503, "rule_identity_conflict", "Provider rule identity is ambiguous")
			}
			b, err := json.Marshal(struct {
				Name, Expr          string
				ForSeconds          float64
				Labels, Annotations map[string]string
			}{item.Name, item.Expression, item.Duration, item.Labels, item.Annotations})
			if err != nil {
				return nil, nil, err
			}
			h := sha256.Sum256(b)
			observed[i] = observedIdentity{true, toDetail(env, app, item).ID, hex.EncodeToString(h[:])}
		}
	}
	return groups, observed, nil
}

func validateObservedHint(hint string, observed observedIdentity) error {
	if hint == "" {
		return nil
	}
	if !observed.exists || observed.id != hint {
		return problem(409, "source_changed", "Observed rule identity changed")
	}
	return nil
}

func projectMR(projectID int64, mr DraftMR) map[string]any {
	return map[string]any{"project_id": strconv.FormatInt(projectID, 10), "iid": mr.IID, "url": mr.URL}
}
