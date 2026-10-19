package v3ingestpull

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/jackc/pgx/v5"
)

type pullError struct {
	status int
	code   string
}

func (e *pullError) Error() string          { return e.code }
func failure(status int, code string) error { return &pullError{status: status, code: code} }

func writeError(w http.ResponseWriter, err error) {
	var p *pullError
	if !errors.As(err, &p) {
		p = &pullError{http.StatusServiceUnavailable, "storage_unavailable"}
	}
	if p.status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	httpapi.Error(w, p.status, p.code, p.code)
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/ingest/pull", "viewer", s.list)
	api.MarkV3ObjectRoute("GET /api/v1/ingest/pull", "connectors", "view")
	api.Register("GET /api/v1/ingest/pull/{id}", "viewer", s.get)
	api.Register("POST /api/v1/ingest/pull/{id}/test", "viewer", s.test)
	api.MarkV3ObjectRoute("GET /api/v1/ingest/pull/{id}", "connectors", "view")
	api.MarkV3ObjectRoute("POST /api/v1/ingest/pull/{id}/test", "connectors", "manage")
}

type snapshot struct {
	ConnectorID   string
	TargetID      string
	ApplicationID string
	EnvironmentID string
	ResourceID    string
	Name          string
	Endpoint      string
	AuthMethod    string
	CredentialRef string
	TeamID        string
}

func (s *Service) policy() *rbac.V3Policy { return &rbac.V3Policy{Store: &store.Store{DB: s.DB}} }

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Service) sourceTx(ctx context.Context, tx pgx.Tx, user core.User, source SourceConfig, test bool) (snapshot, error) {
	var out snapshot
	err := tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$2 AND NOT ae.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ae,ve`,
		source.ApplicationID, source.EnvironmentID).Scan(&out.TeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot{}, failure(404, "source_not_found")
	}
	if err != nil {
		return snapshot{}, failure(503, "storage_unavailable")
	}
	access := s.policy()
	decision := rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: source.EnvironmentID,
		ApplicationID: source.ApplicationID}
	err = access.AuthorizeWithTx(ctx, tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && out.TeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = out.TeamID
		err = access.AuthorizeWithTx(ctx, tx, user, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return snapshot{}, failure(404, "source_not_found")
	}
	if err != nil {
		return snapshot{}, failure(503, "authorization_unavailable")
	}
	err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "connectors", Action: "view",
		EnvironmentID: source.EnvironmentID})
	if errors.Is(err, store.ErrForbidden) {
		return snapshot{}, failure(403, "forbidden")
	}
	if err != nil {
		return snapshot{}, failure(503, "authorization_unavailable")
	}
	if test {
		err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "connectors", Action: "manage",
			EnvironmentID: source.EnvironmentID})
		if errors.Is(err, store.ErrForbidden) {
			return snapshot{}, failure(403, "forbidden")
		}
		if err != nil {
			return snapshot{}, failure(503, "authorization_unavailable")
		}
	}
	var enabled bool
	var tools []byte
	err = tx.QueryRow(ctx, `SELECT c.id,t.id,t.application_id,t.environment_id,t.external_id,c.name,c.endpoint,
	 c.auth_method,c.credential_ref,c.enabled,c.tools
 FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.id=$1 AND t.connector_id=$2 AND t.application_id=$3 AND t.environment_id=$4
 AND t.external_id=$5 AND t.target_kind='metric_resource' AND c.provider='prometheus'
 FOR SHARE OF c,t`, source.TargetID, source.ConnectorID, source.ApplicationID,
		source.EnvironmentID, source.ResourceID).Scan(&out.ConnectorID, &out.TargetID, &out.ApplicationID,
		&out.EnvironmentID, &out.ResourceID, &out.Name, &out.Endpoint, &out.AuthMethod,
		&out.CredentialRef, &enabled, &tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot{}, failure(409, "source_unavailable")
	}
	if err != nil {
		return snapshot{}, failure(503, "storage_unavailable")
	}
	var preferences []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(tools, &preferences) != nil {
		return snapshot{}, failure(409, "source_unavailable")
	}
	queryEnabled := false
	for _, preference := range preferences {
		queryEnabled = queryEnabled || preference.Name == "query_metrics" && preference.Enabled
	}
	endpoint, parseErr := url.Parse(out.Endpoint)
	if !enabled || !queryEnabled || parseErr != nil || endpoint.Host == "" ||
		(endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(out.AuthMethod != "none" && out.AuthMethod != "bearer") ||
		(out.AuthMethod == "none" && out.CredentialRef != "") ||
		(out.AuthMethod == "bearer" && out.CredentialRef == "") {
		return snapshot{}, failure(409, "source_unavailable")
	}
	return out, nil
}

func (s *Service) initialSource(ctx context.Context, user core.User, source SourceConfig, test bool) (snapshot, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return snapshot{}, failure(503, "storage_unavailable")
	}
	defer rollback(tx)
	out, err := s.sourceTx(ctx, tx, user, source, test)
	if err != nil {
		return snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return snapshot{}, failure(503, "storage_unavailable")
	}
	return out, nil
}

type mapping struct {
	NameLabel string   `json:"name_label"`
	LabelKeys []string `json:"label_keys"`
}

type testBody struct {
	Mapping json.RawMessage `json:"mapping"`
}

func defaultMapping(source SourceConfig) mapping {
	return mapping{NameLabel: source.NameLabels[0], LabelKeys: slices.Clone(source.LabelKeys)}
}

func validateMapping(source SourceConfig, value mapping) bool {
	if !slices.Contains(source.NameLabels, value.NameLabel) || len(value.LabelKeys) > len(source.LabelKeys) ||
		!validLabels(value.LabelKeys) {
		return false
	}
	for _, label := range value.LabelKeys {
		if !slices.Contains(source.LabelKeys, label) {
			return false
		}
	}
	return true
}

func (s *Service) requestScope(r *http.Request, test bool) (SourceConfig, error) {
	if s == nil || s.DB == nil || s.Reader == nil {
		return SourceConfig{}, failure(503, "storage_unavailable")
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.Tier != s.Tier || len(scope.EnvironmentIDs) == 0 {
		return SourceConfig{}, failure(503, "environment_scope_unavailable")
	}
	if scope.All {
		return SourceConfig{}, failure(409, "env_view_only")
	}
	if len(scope.EnvironmentIDs) != 1 {
		return SourceConfig{}, failure(422, "invalid_environment")
	}
	source, ok := s.sources[r.PathValue("id")]
	if !ok {
		return SourceConfig{}, failure(404, "source_not_found")
	}
	if !slices.Contains(scope.EnvironmentIDs, source.EnvironmentID) {
		return SourceConfig{}, failure(404, "source_not_found")
	}
	return source, nil
}

// reserve serializes the source budget across replicas. A pending attempt also
// consumes capacity if a process dies during its bounded upstream call.
func sourceDigest(source SourceConfig) string {
	encoded, _ := json.Marshal(struct {
		Expression string   `json:"expression"`
		NameLabels []string `json:"name_labels"`
		LabelKeys  []string `json:"label_keys"`
	}{source.Expression, source.NameLabels, source.LabelKeys})
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func (s *Service) reserve(ctx context.Context, source SourceConfig, actor, kind string) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, failure(503, "storage_unavailable")
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('v3.pull.budget'),hashtext($1))`, source.ID); err != nil {
		return 0, failure(503, "storage_unavailable")
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_pull_source_identities(source_id,application_id,environment_id,
 connector_id,target_id,resource_id,query_id,config_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT (source_id) DO NOTHING`, source.ID, source.ApplicationID, source.EnvironmentID,
		source.ConnectorID, source.TargetID, source.ResourceID, source.QueryID, sourceDigest(source))
	if err != nil {
		return 0, failure(503, "storage_unavailable")
	}
	var applicationID, environmentID, connectorID, targetID, resourceID, queryID, digest string
	err = tx.QueryRow(ctx, `SELECT application_id,environment_id,connector_id,target_id,resource_id,query_id,config_digest
 FROM v3_pull_source_identities WHERE source_id=$1 FOR SHARE`, source.ID).
		Scan(&applicationID, &environmentID, &connectorID, &targetID, &resourceID, &queryID, &digest)
	if err != nil {
		return 0, failure(503, "storage_unavailable")
	}
	if applicationID != source.ApplicationID || environmentID != source.EnvironmentID ||
		connectorID != source.ConnectorID || targetID != source.TargetID ||
		resourceID != source.ResourceID || queryID != source.QueryID || digest != sourceDigest(source) {
		return 0, failure(409, "source_identity_changed")
	}
	var sourceCount, actorCount, concurrent int
	err = tx.QueryRow(ctx, `SELECT count(*)::int,
 count(*) FILTER (WHERE actor_id=$2)::int,
 count(*) FILTER (WHERE status='pending' AND started_at>clock_timestamp()-interval '15 seconds')::int
 FROM v3_pull_attempts WHERE source_id=$1 AND started_at>clock_timestamp()-interval '1 minute'`,
		source.ID, actor).Scan(&sourceCount, &actorCount, &concurrent)
	if err != nil {
		return 0, failure(503, "storage_unavailable")
	}
	if sourceCount >= 60 || actorCount >= 10 || concurrent >= 4 {
		return 0, failure(429, "rate_limited")
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO v3_pull_attempts(source_id,actor_id,kind,status)
	 VALUES($1,$2,$3,'pending') RETURNING id`, source.ID, actor, kind).Scan(&id)
	if err != nil || tx.Commit(ctx) != nil {
		return 0, failure(503, "storage_unavailable")
	}
	return id, nil
}

func upstreamError(err error) error {
	if err == nil {
		return nil
	}
	var read *v3integrations.PrometheusReadError
	if errors.As(err, &read) {
		switch read.Code {
		case "credential_unavailable":
			return failure(503, "credential_not_authorized")
		case "forbidden":
			return failure(403, "forbidden")
		case "source_not_found", "source_changed", "source_unhealthy":
			return failure(409, "source_changed")
		case "query_unregistered":
			return failure(409, "source_changed")
		case "storage_unavailable":
			return failure(503, "storage_unavailable")
		case "authorization_unavailable":
			return failure(503, "authorization_unavailable")
		}
	}
	return failure(502, "upstream_failed")
}

type row struct {
	Service string            `json:"service"`
	Name    string            `json:"name"`
	Time    string            `json:"time"`
	Value   string            `json:"value"`
	Labels  map[string]string `json:"labels"`
}

type result struct {
	Rows      []row
	Series    []float64
	RowsCount int
	Dropped   int
	Captured  time.Time
}

func project(source SourceConfig, mapping mapping, evidence v3integrations.PrometheusMetricEvidence) result {
	out := result{Rows: []row{}, Series: []float64{}, Captured: evidence.CapturedAt}
	for index, series := range evidence.Series {
		name := series.Labels[mapping.NameLabel]
		for _, point := range series.Samples {
			if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
				out.Dropped++
				continue
			}
			value, err := strconv.ParseFloat(point.Value, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				out.Dropped++
				continue
			}
			out.RowsCount++
			if index == 0 {
				out.Series = append(out.Series, value)
			}
			if len(out.Rows) < 20 {
				labels := make(map[string]string)
				for _, key := range mapping.LabelKeys {
					if value, ok := series.Labels[key]; ok {
						if len(value) > 256 {
							value = value[:256]
						}
						labels[key] = value
					}
				}
				out.Rows = append(out.Rows, row{Service: source.ApplicationID, Name: name,
					Time:  time.Unix(0, int64(point.Timestamp*1e9)).UTC().Format(time.RFC3339Nano),
					Value: point.Value, Labels: labels})
			}
		}
	}
	return out
}

func (s *Service) execute(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	source SourceConfig, initial snapshot, selected mapping) (result, error) {
	if initial.AuthMethod == "bearer" && (s.Resolver == nil || !s.Resolver.CredentialAllowed(v3integrations.CredentialUse{
		Tier: s.Tier, EnvironmentID: source.EnvironmentID, ConnectorID: source.ConnectorID,
		Provider: "prometheus", Endpoint: initial.Endpoint,
	}, initial.CredentialRef)) {
		return result{}, failure(503, "credential_not_authorized")
	}
	end := time.Now().UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-5 * time.Minute), End: end, StepSeconds: 30}
	evidence, err := s.Reader.Query(ctx, scope, user, source.ApplicationID, source.EnvironmentID,
		source.TargetID, source.QueryID, window)
	if err != nil {
		return result{}, upstreamError(err)
	}
	return project(source, selected, evidence), nil
}

// finish pairs the recorded outcome and audit in one transaction, then checks
// the exact session, grants and connector again before exposing any sample.
func (s *Service) finish(ctx context.Context, r *http.Request, source SourceConfig, initial snapshot,
	attemptID int64, test bool, observed result, runErr error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return failure(503, "audit_unavailable")
	}
	defer rollback(tx)
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return failure(401, "unauthorized")
	}
	actor := httpapi.CurrentUser(r)
	if err := store.LockActiveSession(ctx, tx, cookie.Value, actor.ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			return failure(401, "unauthorized")
		}
		return failure(503, "authorization_unavailable")
	}
	// The short final transaction pins grant sources against concurrent revoke.
	if _, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return failure(503, "authorization_unavailable")
	}
	current, err := s.sourceTx(ctx, tx, actor, source, test)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, initial) {
		return failure(409, "source_changed")
	}
	status := "success"
	code := ""
	if runErr != nil {
		status = "failure"
		var p *pullError
		if errors.As(runErr, &p) {
			code = p.code
		} else {
			code = "upstream_failed"
		}
	}
	rows, dropped := 0, 0
	if runErr == nil {
		rows, dropped = observed.RowsCount, observed.Dropped
	}
	command, err := tx.Exec(ctx, `UPDATE v3_pull_attempts
 SET status=$2,rows_count=$3,dropped_count=$4,error_code=$5,completed_at=clock_timestamp()
 WHERE id=$1 AND source_id=$6 AND actor_id=$7 AND status='pending'`,
		attemptID, status, rows, dropped, code, source.ID, actor.ID)
	if err != nil || command.RowsAffected() != 1 {
		return failure(503, "audit_unavailable")
	}
	kind := "preview"
	if test {
		kind = "test"
	}
	changes, _ := json.Marshal(map[string]any{"source_id": source.ID, "environment_id": source.EnvironmentID,
		"kind": kind, "rows": rows, "dropped": dropped, "error_code": code})
	if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,$5,$6)`, actor.ID, source.ApplicationID,
		"v3.ingest_pull."+kind, source.ID, status, changes); err != nil {
		return failure(503, "audit_unavailable")
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), actor.ID).Scan(&active); err != nil || !active {
		return failure(401, "unauthorized")
	}
	if err := tx.Commit(ctx); err != nil {
		return failure(503, "audit_unavailable")
	}
	return nil
}

// reject closes a reserved attempt when the final authority check fails. It
// records only the rejection code and counts, never the upstream observation.
// This audit is a security record of an attempted read, not authorization to
// return any data to the caller.
func (s *Service) reject(ctx context.Context, source SourceConfig, attemptID int64, cause error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return failure(503, "audit_unavailable")
	}
	defer rollback(tx)
	var actor, status string
	err = tx.QueryRow(ctx, `SELECT actor_id,status FROM v3_pull_attempts
 WHERE id=$1 AND source_id=$2 FOR UPDATE`, attemptID, source.ID).Scan(&actor, &status)
	if err != nil {
		return failure(503, "audit_unavailable")
	}
	if status != "pending" {
		return tx.Commit(ctx)
	}
	code := "read_rejected"
	var p *pullError
	if errors.As(cause, &p) {
		code = p.code
	}
	if _, err := tx.Exec(ctx, `UPDATE v3_pull_attempts SET status='failure',error_code=$2,
 completed_at=clock_timestamp() WHERE id=$1`, attemptID, code); err != nil {
		return failure(503, "audit_unavailable")
	}
	changes, _ := json.Marshal(map[string]any{"source_id": source.ID, "environment_id": source.EnvironmentID,
		"error_code": code, "rows": 0, "dropped": 0})
	if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.ingest_pull.rejected',$3,'failure',$4)`, actor, source.ApplicationID, source.ID, changes); err != nil {
		return failure(503, "audit_unavailable")
	}
	if err := tx.Commit(ctx); err != nil {
		return failure(503, "audit_unavailable")
	}
	return nil
}

func (s *Service) run(w http.ResponseWriter, r *http.Request, test bool) {
	source, err := s.requestScope(r, test)
	if err != nil {
		writeError(w, err)
		return
	}
	selected := defaultMapping(source)
	if test {
		data, readErr := io.ReadAll(io.LimitReader(r.Body, (4<<10)+1))
		if readErr != nil || len(data) > 4<<10 || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
			writeError(w, failure(422, "invalid_mapping"))
			return
		}
		var body testBody
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(w, failure(422, "invalid_mapping"))
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			writeError(w, failure(422, "invalid_mapping"))
			return
		}
		if len(body.Mapping) > 0 {
			var proposed mapping
			if bytes.Equal(bytes.TrimSpace(body.Mapping), []byte("null")) {
				writeError(w, failure(422, "invalid_mapping"))
				return
			}
			var parts map[string]json.RawMessage
			if json.Unmarshal(body.Mapping, &parts) != nil || parts == nil ||
				len(parts["name_label"]) == 0 || len(parts["label_keys"]) == 0 ||
				bytes.TrimSpace(parts["label_keys"])[0] != '[' {
				writeError(w, failure(422, "invalid_mapping"))
				return
			}
			mappingDecoder := json.NewDecoder(bytes.NewReader(body.Mapping))
			mappingDecoder.DisallowUnknownFields()
			if mappingDecoder.Decode(&proposed) != nil || mappingDecoder.Decode(new(any)) != io.EOF ||
				!validateMapping(source, proposed) {
				writeError(w, failure(422, "invalid_mapping"))
				return
			}
			selected = proposed
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	initial, err := s.initialSource(ctx, user, source, test)
	if err != nil {
		writeError(w, err)
		return
	}
	kind := "preview"
	if test {
		kind = "test"
	}
	attemptID, err := s.reserve(ctx, source, user.ID, kind)
	if err != nil {
		writeError(w, err)
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	observed, runErr := s.execute(ctx, scope, user, source, initial, selected)
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	finishErr := s.finish(finishCtx, r, source, initial, attemptID, test, observed, runErr)
	finishCancel()
	if finishErr != nil {
		rejectCtx, rejectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		rejectErr := s.reject(rejectCtx, source, attemptID, finishErr)
		rejectCancel()
		if rejectErr != nil {
			writeError(w, failure(503, "audit_unavailable"))
			return
		}
		writeError(w, finishErr)
		return
	}
	if runErr != nil {
		writeError(w, runErr)
		return
	}
	if test {
		httpapi.WriteJSON(w, 200, map[string]any{"ok": true, "rows": observed.RowsCount,
			"dropped": observed.Dropped, "captured_at": observed.Captured})
		return
	}
	httpapi.WriteJSON(w, 200, pullDraft(source, initial, selected, observed))
}

func (s *Service) get(w http.ResponseWriter, r *http.Request)  { s.run(w, r, false) }
func (s *Service) test(w http.ResponseWriter, r *http.Request) { s.run(w, r, true) }
