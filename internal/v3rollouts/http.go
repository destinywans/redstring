package v3rollouts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3kube"
	"github.com/destinywans/redstring/internal/v3proposals"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
)

// Register adds the rollback routes. Both are object routes: the service's
// application and team are resolved from the persisted record.
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/services/{id}/rollback-candidates", "viewer", s.candidates)
	api.MarkV3ObjectRoute("GET /api/v1/services/{id}/rollback-candidates", "resources", "view")
	api.Register("POST /api/v1/rollback-proposals", "viewer", s.propose)
	api.MarkV3ObjectRoute("POST /api/v1/rollback-proposals", "proposals", "change")
}

// inspection is what a rollback request sees of one service.
type inspection struct {
	App, Env, Team  string
	Workload        v3telemetry.Workload
	Binding         WriteBinding
	Deployment      *v3kube.Deployment
	Revisions       []v3kube.Revision
	Blocked         string
	InvestigationAt time.Time
}

var errUnavailable = errors.New("workload unavailable")

// inspect authorizes the caller for the service, resolves its workload and
// reads the Deployment. A configuration gap is a Blocked reason; an
// unreadable cluster is errUnavailable.
func (s *Service) inspect(ctx context.Context, user core.User, app, env string, checks ...[2]string) (inspection, error) {
	in := inspection{App: app, Env: env}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return in, err
	}
	defer tx.Rollback(ctx)
	if in.Team, err = ownerTeamReadTx(ctx, tx, app, env); err != nil {
		return in, err
	}
	if err = s.authorizeTx(ctx, tx, user, app, env, in.Team, checks...); err != nil {
		return in, err
	}
	workload, resolveErr := s.Registry.ResolveWorkload(ctx, tx, app, env)
	if err = tx.Commit(ctx); err != nil {
		return in, err
	}
	err = resolveErr
	block := func(reason string) {
		if in.Blocked == "" {
			in.Blocked = reason
		}
	}
	switch {
	case errors.Is(err, v3telemetry.ErrNotConfigured):
		in.Blocked = "workload_not_configured"
		return in, nil
	case errors.Is(err, v3telemetry.ErrAmbiguous):
		return in, errUnavailable
	case err != nil:
		return in, err
	}
	in.Workload = workload
	for _, reason := range workload.NotReady {
		switch reason {
		case "credential_missing", "credential_not_approved":
			block("credential_unavailable")
		default:
			block("workload_not_configured")
		}
	}
	if in.Blocked != "" {
		return in, nil
	}
	if p := workload.Scope.Rollback; p == nil || !p.Allowed {
		block("rollback_not_allowed")
	}
	if b, ok := findWriteBinding(s.WriteBindings, s.Tier, env, workload.ConnectorID, workload.Scope.Namespace, workload.Scope.Deployment); ok {
		in.Binding = b
	} else {
		block("write_not_approved")
	}
	client, err := s.readClient(ctx, env, workload)
	if err != nil {
		block("credential_unavailable")
		return in, nil
	}
	d, err := client.GetDeployment(ctx, workload.Scope.Namespace, workload.Scope.Deployment)
	switch {
	case errors.Is(err, v3kube.ErrForbidden):
		block("credential_unavailable")
		return in, nil
	case errors.Is(err, v3kube.ErrNotFound):
		block("workload_not_configured")
		return in, nil
	case err != nil:
		return in, errUnavailable
	}
	revisions, err := client.Revisions(ctx, d)
	if errors.Is(err, v3kube.ErrForbidden) {
		block("credential_unavailable")
		return in, nil
	}
	if err != nil {
		return in, errUnavailable
	}
	in.Deployment, in.Revisions = &d, revisions
	if d.Spec.Paused {
		block("deployment_paused")
	}
	if s.Ready(ctx, env) != nil {
		block("executor_not_ready")
	}
	return in, nil
}

// candidateJSON's created_at is the ReplicaSet's creation. A reused
// ReplicaSet keeps its old creation time, so at says when the revision took
// effect, from time_basis, or is null with time_unknown_reason and the
// not_before..not_after interval, as on the changes page (PA-94, PA-172).
type candidateJSON struct {
	Revision          string             `json:"revision"`
	CreatedAt         *string            `json:"created_at"`
	At                *string            `json:"at"`
	TimeBasis         string             `json:"time_basis"`
	TimeUnknownReason *string            `json:"time_unknown_reason"`
	NotBefore         *string            `json:"not_before"`
	NotAfter          *string            `json:"not_after"`
	ChangeCause       *string            `json:"change_cause"`
	Current           bool               `json:"current"`
	Containers        []v3kube.Container `json:"containers"`
	Changes           []v3kube.Change    `json:"changes"`
	TemplateDigest    string             `json:"template_digest"`
}

// RevisionTime is when a revision took effect (PA-94), as the change
// service works it out for the same Deployment.
type RevisionTime struct {
	At                  *time.Time
	TimeBasis           string
	TimeUnknownReason   *string
	NotBefore, NotAfter *time.Time
}

func rfc3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

type workloadJSON struct {
	ConnectorID           string `json:"connector_id"`
	Namespace             string `json:"namespace"`
	Deployment            string `json:"deployment"`
	UID                   string `json:"uid"`
	Paused                bool   `json:"paused"`
	CurrentRevision       string `json:"current_revision"`
	CurrentTemplateDigest string `json:"current_template_digest"`
	Replicas              struct {
		Desired   int32 `json:"desired"`
		Updated   int32 `json:"updated"`
		Available int32 `json:"available"`
	} `json:"replicas"`
}

func (s *Service) plan(w v3telemetry.Workload) *v3proposals.RollbackVerificationPlan {
	p := w.Scope.Rollback
	if p == nil || !p.Allowed {
		return nil
	}
	v := p.Verification
	return &v3proposals.RollbackVerificationPlan{ObservationSeconds: s.minimumObservation(p.ObservationSeconds),
		RequestRateQueryID: v.RequestRateQueryID, ErrorRatioQueryID: v.ErrorRatioQueryID, SampleAgeQueryID: v.SampleAgeQueryID,
		MaxErrorRatio: v.MaxErrorRatio, MinRequestRate: v.MinRequestRate}
}

// candidateList takes each revision's time from times when the change
// service gave one. Without it, a new ReplicaSet took effect when it was
// created and a reused one's time is unknown.
func candidateList(d v3kube.Deployment, revisions []v3kube.Revision, times map[string]RevisionTime) []candidateJSON {
	current := v3kube.Digest(d.Spec.Template)
	out := make([]candidateJSON, 0, len(revisions))
	reusedReason := "replicaset_reused"
	for _, r := range revisions {
		c := candidateJSON{Revision: r.Revision, Current: r.Digest == current, Containers: v3kube.Containers(r.Template),
			Changes: v3kube.Changes(d.Spec.Template, r.Template), TemplateDigest: r.Digest}
		if c.Current {
			c.Changes = []v3kube.Change{}
		}
		c.CreatedAt = rfc3339(r.CreatedAt)
		if t, ok := times[r.Revision]; ok {
			c.At, c.TimeBasis, c.TimeUnknownReason = rfc3339(t.At), t.TimeBasis, t.TimeUnknownReason
			c.NotBefore, c.NotAfter = rfc3339(t.NotBefore), rfc3339(t.NotAfter)
		} else if len(r.History) == 0 && r.CreatedAt != nil {
			c.At, c.TimeBasis = c.CreatedAt, "replicaset_created"
		} else {
			c.TimeBasis, c.TimeUnknownReason = "unknown", &reusedReason
		}
		if r.ChangeCause != "" {
			cause := r.ChangeCause
			c.ChangeCause = &cause
		}
		out = append(out, c)
	}
	return out
}

func (s *Service) scopeEnv(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return "", false
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		if write {
			httpapi.Error(w, 409, "env_view_only", "Select one environment")
		} else {
			httpapi.Error(w, 422, "single_environment_required", "Select one environment")
		}
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

func inspectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 404, "not_found", "Service not found")
	case errors.Is(err, errUnavailable):
		httpapi.Error(w, 503, "workload_unavailable", "The workload could not be read from the cluster")
	default:
		httpapi.Error(w, 503, "rollback_unavailable", "Rollback information is unavailable")
	}
}

func (s *Service) candidates(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scopeEnv(w, r, false)
	if !ok {
		return
	}
	serviceID := r.PathValue("id")
	app, serviceEnv, valid := decodeServiceID(serviceID)
	if !valid || serviceEnv != env {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	in, err := s.inspect(r.Context(), httpapi.CurrentUser(r), app, env, [2]string{"resources", "view"})
	if err != nil {
		inspectError(w, err)
		return
	}
	out := map[string]any{"service_id": serviceID, "application_id": app, "environment_id": env,
		"workload": nil, "verification": nil, "candidates": []candidateJSON{}, "blocked_reason": nil,
		"observed_at": s.now().Format(time.RFC3339)}
	if in.Workload.TargetID != "" {
		if plan := s.plan(in.Workload); plan != nil {
			out["verification"] = plan
		}
	}
	if in.Deployment != nil {
		d := *in.Deployment
		wl := workloadJSON{ConnectorID: in.Workload.ConnectorID, Namespace: in.Workload.Scope.Namespace,
			Deployment: in.Workload.Scope.Deployment, UID: d.Metadata.UID, Paused: d.Spec.Paused,
			CurrentRevision: d.Revision(), CurrentTemplateDigest: v3kube.Digest(d.Spec.Template)}
		wl.Replicas.Desired, wl.Replicas.Updated, wl.Replicas.Available = d.Desired(), d.Status.UpdatedReplicas, d.Status.AvailableReplicas
		out["workload"] = wl
		if in.Blocked == "" {
			var times map[string]RevisionTime
			if s.RevisionTimes != nil {
				// A failed read leaves the fallback above; the list itself stays usable.
				times, _ = s.RevisionTimes(r.Context(), httpapi.CurrentUser(r), app, env)
			}
			out["candidates"] = candidateList(d, in.Revisions, times)
		}
	}
	if in.Blocked != "" {
		out["blocked_reason"] = in.Blocked
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

type proposalInput struct {
	InvestigationID        string `json:"investigation_id"`
	ServiceID              string `json:"service_id"`
	TargetRevision         string `json:"target_revision"`
	ExpectedTemplateDigest string `json:"expected_template_digest"`
	Reason                 string `json:"reason"`
}

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Service) propose(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scopeEnv(w, r, true)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !idempotencyKey.MatchString(key) {
		httpapi.Error(w, 428, "idempotency_key_required", "Send one Idempotency-Key of 8-128 characters")
		return
	}
	var in proposalInput
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		httpapi.Error(w, 422, "invalid_rollback", "Invalid rollback proposal")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	app, serviceEnv, valid := decodeServiceID(in.ServiceID)
	if !valid || in.InvestigationID == "" || len(in.InvestigationID) > 128 || !digestRE.MatchString(in.ExpectedTemplateDigest) ||
		in.Reason == "" || len([]rune(in.Reason)) > 2000 || in.TargetRevision == "" || len(in.TargetRevision) > 10 {
		httpapi.Error(w, 422, "invalid_rollback", "Invalid rollback proposal")
		return
	}
	if serviceEnv != env {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	user := httpapi.CurrentUser(r)
	requestHash := v3proposals.RollbackRequestHash(user.ID, in.InvestigationID, in.ServiceID, in.TargetRevision,
		in.ExpectedTemplateDigest, in.Reason, key)
	switch id, replay, err := v3proposals.LookupRollbackReplay(r.Context(), s.DB, user.ID, key, requestHash); {
	case errors.Is(err, v3proposals.ErrIdempotencyConflict):
		httpapi.Error(w, 409, "idempotency_conflict", "The Idempotency-Key was used for a different request")
		return
	case err != nil:
		httpapi.Error(w, 503, "rollback_unavailable", "Rollback proposal could not be saved")
		return
	case replay:
		httpapi.WriteJSON(w, 200, map[string]string{"proposal_id": id, "status": "pending", "action_kind": v3proposals.RollbackActionKind})
		return
	}
	// Visibility decides 404; the change grants are checked again in the
	// write transaction, where a missing one is 403.
	inspected, err := s.inspect(r.Context(), user, app, env, [2]string{"resources", "view"})
	if err != nil {
		inspectError(w, err)
		return
	}
	if inspected.Blocked != "" {
		httpapi.Error(w, 409, inspected.Blocked, "Rollback is not possible: "+inspected.Blocked)
		return
	}
	d := *inspected.Deployment
	current := v3kube.Digest(d.Spec.Template)
	if current != in.ExpectedTemplateDigest {
		httpapi.Error(w, 409, "rollback_target_changed", "The Deployment changed since the candidates were read")
		return
	}
	var target *v3kube.Revision
	for i := range inspected.Revisions {
		if inspected.Revisions[i].Revision == in.TargetRevision {
			target = &inspected.Revisions[i]
		}
	}
	if target == nil || target.Digest == current {
		httpapi.Error(w, 422, "invalid_rollback", "The target is not a revision this Deployment can be rolled back to")
		return
	}
	policy := inspected.Workload.Scope.Rollback
	v := policy.Verification
	action := v3proposals.RollbackAction{SchemaVersion: 1, Kind: v3proposals.RollbackActionKind, ActorID: user.ID,
		EnvironmentID: env, ApplicationID: app, OwnerTeamID: inspected.Team, InvestigationID: in.InvestigationID,
		ServiceID: in.ServiceID, ConnectorID: inspected.Workload.ConnectorID, TargetID: inspected.Workload.TargetID,
		ResourceID: inspected.Workload.ResourceID, Namespace: inspected.Workload.Scope.Namespace,
		Deployment: inspected.Workload.Scope.Deployment, UID: d.Metadata.UID, FromRevision: d.Revision(),
		ExpectedTemplateDigest: current, ExpectedSpecDigest: v3kube.SpecDigest(d.RawSpec), TargetRevision: target.Revision,
		TargetTemplate: target.Template, TargetTemplateDigest: target.Digest, Containers: v3kube.Containers(target.Template),
		Changes: v3kube.Changes(d.Spec.Template, target.Template), ObservationSeconds: s.minimumObservation(policy.ObservationSeconds),
		Verification: v3proposals.RollbackVerification{ConnectorID: v.ConnectorID, ResourceID: v.ResourceID,
			RequestRateQueryID: v.RequestRateQueryID, ErrorRatioQueryID: v.ErrorRatioQueryID, SampleAgeQueryID: v.SampleAgeQueryID,
			MaxErrorRatio: v.MaxErrorRatio, MinRequestRate: v.MinRequestRate},
		Reason: in.Reason, IdempotencyKey: key, RequestHash: requestHash,
		BindingFingerprint: bindingFingerprint(inspected.Workload, inspected.Binding)}
	english := strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "en")
	id, replay, err := s.writeProposal(r, user, action, english)
	switch {
	case errors.Is(err, v3proposals.ErrIdempotencyConflict):
		httpapi.Error(w, 409, "idempotency_conflict", "The Idempotency-Key was used for a different request")
		return
	case errors.Is(err, errBindingChanged):
		httpapi.Error(w, 409, "rollback_target_changed", "The workload binding changed; read the candidates again")
		return
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	case errors.Is(err, errHumanRequired):
		httpapi.Error(w, 403, "forbidden", "A human session is required")
		return
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Rollback proposal permission required")
		return
	case errors.Is(err, store.ErrNotFound), errors.Is(err, errNotFound):
		httpapi.Error(w, 404, "not_found", "Investigation not found")
		return
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, 409, "investigation_changed", "The investigation changed; retry")
		return
	case errors.Is(err, v3proposals.ErrInvalidProposal):
		httpapi.Error(w, 422, "invalid_rollback", "Invalid rollback proposal")
		return
	case err != nil:
		httpapi.Error(w, 503, "rollback_unavailable", "Rollback proposal could not be saved")
		return
	}
	status := 201
	if replay {
		status = 200
	}
	httpapi.WriteJSON(w, status, map[string]string{"proposal_id": id, "status": "pending", "action_kind": v3proposals.RollbackActionKind})
}

var (
	errBindingChanged = errors.New("workload binding changed")
	errHumanRequired  = errors.New("human session required")
)

// writeProposal re-checks the session, identity, grants and binding inside
// the write transaction, then writes the proposal and its action.
func (s *Service) writeProposal(r *http.Request, user core.User, a v3proposals.RollbackAction, english bool) (string, bool, error) {
	ctx := r.Context()
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return "", false, store.ErrSessionRevoked
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		return "", false, err
	}
	var human bool
	if err = tx.QueryRow(ctx, `SELECT v3_identity_kind='human' FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR SHARE`,
		user.ID).Scan(&human); err != nil || !human {
		return "", false, errHumanRequired
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles, rbac_department_roles, v3_role_grants IN SHARE MODE`); err != nil {
		return "", false, err
	}
	team, err := ownerTeamTx(ctx, tx, a.ApplicationID, a.EnvironmentID)
	if err != nil {
		return "", false, err
	}
	if err = s.authorizeTx(ctx, tx, user, a.ApplicationID, a.EnvironmentID, team,
		[2]string{"resources", "view"}, [2]string{"resources", "change"}, [2]string{"proposals", "change"}); err != nil {
		return "", false, err
	}
	workload, err := s.Registry.ResolveWorkload(ctx, tx, a.ApplicationID, a.EnvironmentID)
	if err != nil || len(workload.NotReady) > 0 || team != a.OwnerTeamID {
		return "", false, errBindingChanged
	}
	binding, ok := findWriteBinding(s.WriteBindings, s.Tier, a.EnvironmentID, workload.ConnectorID, workload.Scope.Namespace, workload.Scope.Deployment)
	if !ok || bindingFingerprint(workload, binding) != a.BindingFingerprint {
		return "", false, errBindingChanged
	}
	if err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND e.key=$3 AND NOT e.archived AND i.status<>'closed'`,
		a.InvestigationID, a.ApplicationID, a.EnvironmentID).Scan(&a.InvestigationUpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return "", false, errNotFound
	} else if err != nil {
		return "", false, err
	}
	id, replay, err := s.Proposals.WritePendingRollbackWithTx(ctx, tx, user, a, english)
	if err != nil {
		return "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return id, replay, nil
}
