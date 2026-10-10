package v3ingestpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type pushTypeView struct {
	ID       string `json:"id"`
	Icon     string `json:"icon"`
	Name     string `json:"name"`
	Schema   string `json:"schema"`
	Today    string `json:"today"`
	Rejected string `json:"rejected"`
	Tone     string `json:"tone"`
}
type pushHeaderView struct{ K, V, Note string }
type pushResponseView struct{ Code, Tone, Text string }
type pushEndpointView struct {
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	Host      string              `json:"host"`
	Headers   []map[string]string `json:"headers"`
	Example   string              `json:"example"`
	Responses []map[string]string `json:"responses"`
}

// Time is "2006-01-02 15:04 UTC" text kept for older clients; At is the same
// instant in RFC 3339 (PA-83).
type pushDeliveryView struct {
	Time   string `json:"time"`
	At     string `json:"at"`
	Source string `json:"source"`
	Key    string `json:"key"`
	Code   string `json:"code"`
	Tone   string `json:"tone"`
	Result string `json:"result"`
	MS     string `json:"ms"`
}
type pushStackView struct {
	Enabled    bool          `json:"enabled"`
	Components []interface{} `json:"components"`
	Helm       string        `json:"helm"`
	Compose    string        `json:"compose"`
	Estimate   string        `json:"estimate"`
}
type pushView struct {
	Types        []pushTypeView     `json:"types"`
	Endpoint     pushEndpointView   `json:"endpoint"`
	Deliveries   []pushDeliveryView `json:"deliveries"`
	Stack        pushStackView      `json:"stack"`
	Availability string             `json:"availability"`
}

func (s *Service) scope(w http.ResponseWriter, r *http.Request, write bool) ([]string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope unavailable")
		return nil, false
	}
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Choose one environment for a Push test")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}

func (s *Service) authorize(w http.ResponseWriter, r *http.Request, module, action, env string) bool {
	err := s.policy.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Push permission required")
	} else {
		httpapi.Error(w, 503, "authorization_unavailable", "Push authorization unavailable")
	}
	return false
}

// visibleSources filters static config through current mapping and app/team
// grants. Stale and unauthorized sources never enter counts or delivery rows.
func (s *Service) visibleSources(ctx context.Context, tx pgx.Tx, user core.User, envs []string, action string) ([]Source, error) {
	var q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	} = s.db
	if tx != nil {
		q = tx
	}
	visible := []Source{}
	for _, source := range s.sources {
		selected := false
		for _, env := range envs {
			selected = selected || env == source.EnvironmentID
		}
		if !selected {
			continue
		}
		var ownerTeam string
		err := q.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=e.key
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,e,ve`, source.ApplicationID, source.EnvironmentID).Scan(&ownerTeam)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var boundApp, boundEnv, boundProvider string
		bindErr := q.QueryRow(ctx, `SELECT application_id,environment_id,provider
 FROM v3_push_source_bindings WHERE source_id=$1 FOR SHARE`, source.ID).Scan(&boundApp, &boundEnv, &boundProvider)
		if bindErr != nil && !errors.Is(bindErr, pgx.ErrNoRows) {
			return nil, bindErr
		}
		if bindErr == nil && (boundApp != source.ApplicationID || boundEnv != source.EnvironmentID || boundProvider != source.Provider) {
			continue
		}
		decision := rbac.V3Decision{Module: "services", Action: action, EnvironmentID: source.EnvironmentID, ApplicationID: source.ApplicationID}
		authorize := func() error {
			if tx != nil {
				return s.policy.AuthorizeWithTx(ctx, tx, user, decision)
			}
			return s.policy.Authorize(ctx, user, decision)
		}
		err = authorize()
		if errors.Is(err, store.ErrForbidden) && ownerTeam != "" {
			decision.ApplicationID, decision.TeamID = "", ownerTeam
			err = authorize()
		}
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		visible = append(visible, source)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].ID < visible[j].ID })
	return visible, nil
}

func (s *Service) view(w http.ResponseWriter, r *http.Request) {
	english := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	say := func(zh, en string) string {
		if english {
			return en
		}
		return zh
	}
	envs, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "push_unavailable", "Push view unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	// Keep all scope, source and receipt reads in one transaction. Shared
	// governance locks make a queued role/member change finish before our
	// authority check or wait until the response has been written.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push view unavailable")
			return
		}
	}
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, 401, "unauthorized", "Session expired")
		} else {
			httpapi.Error(w, 503, "push_unavailable", "Push view unavailable")
		}
		return
	}
	allowedEnvs := []string{}
	for _, env := range envs {
		err := s.policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: env})
		if errors.Is(err, store.ErrForbidden) && len(envs) > 1 {
			continue
		}
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Push permission required")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "authorization_unavailable", "Push authorization unavailable")
			return
		}
		allowedEnvs = append(allowedEnvs, env)
	}
	if len(allowedEnvs) == 0 {
		httpapi.Error(w, 403, "forbidden", "Push permission required")
		return
	}
	envs = allowedEnvs
	sources, err := s.visibleSources(r.Context(), tx, httpapi.CurrentUser(r), envs, "view")
	if err != nil {
		httpapi.Error(w, 503, "push_unavailable", "Push view unavailable")
		return
	}
	sourceIDs, sourceEnvs, appIDs := []string{}, []string{}, []string{}
	for _, source := range sources {
		sourceIDs = append(sourceIDs, source.ID)
		sourceEnvs = append(sourceEnvs, source.EnvironmentID)
		appIDs = append(appIDs, source.ApplicationID)
	}
	type counts struct{ accepted, rejected int64 }
	byType := map[string]counts{}
	if len(appIDs) > 0 {
		rows, err := tx.Query(r.Context(), `WITH visible AS (SELECT * FROM unnest($1::text[],$2::text[],$3::text[]) AS v(source_id,environment_id,application_id))
 SELECT e.event_type,count(*) FROM v3_push_events e JOIN visible v
 ON (e.source_id,e.environment_id,e.application_id)=(v.source_id,v.environment_id,v.application_id)
 WHERE NOT e.synthetic AND e.created_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
 GROUP BY e.event_type`, sourceIDs, sourceEnvs, appIDs)
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push counts unavailable")
			return
		}
		for rows.Next() {
			var typ string
			var n int64
			if err = rows.Scan(&typ, &n); err != nil {
				break
			}
			c := byType[typ]
			c.accepted = n
			byType[typ] = c
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push counts unavailable")
			return
		}
		rows, err = tx.Query(r.Context(), `WITH visible AS (SELECT * FROM unnest($1::text[],$2::text[],$3::text[]) AS v(source_id,environment_id,application_id))
 SELECT p.event_type,count(*) FROM v3_push_receipts p JOIN visible v
 ON (p.source_id,p.environment_id,p.application_id)=(v.source_id,v.environment_id,v.application_id)
 WHERE p.code=422 AND NOT p.synthetic AND p.created_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
 GROUP BY p.event_type`, sourceIDs, sourceEnvs, appIDs)
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push counts unavailable")
			return
		}
		for rows.Next() {
			var typ string
			var n int64
			if err = rows.Scan(&typ, &n); err != nil {
				break
			}
			c := byType[typ]
			c.rejected = n
			byType[typ] = c
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push counts unavailable")
			return
		}
	}
	view := pushView{Types: []pushTypeView{}, Deliveries: []pushDeliveryView{}, Stack: pushStackView{Enabled: false, Components: []interface{}{}}, Availability: "configured_unverified"}
	labels := map[string]struct{ icon, name string }{
		"alert": {"bell", say("告警", "Alerts")}, "change": {"branch", say("变更", "Changes")},
		"incident": {"alert", say("故障", "Incidents")}, "resource": {"box", say("资源变更", "Resource changes")},
		"oncall": {"user", say("值班", "On-call")},
	}
	for _, typ := range []string{"alert", "change", "incident", "resource", "oncall"} {
		c := byType[typ]
		pct := "0%"
		if c.accepted+c.rejected > 0 {
			pct = fmt.Sprintf("%.1f%%", 100*float64(c.rejected)/float64(c.accepted+c.rejected))
		}
		tone := "ok"
		if c.rejected > 0 {
			tone = "warn"
		}
		view.Types = append(view.Types, pushTypeView{ID: typ, Icon: labels[typ].icon, Name: labels[typ].name, Schema: typ + ".v1", Today: fmt.Sprint(c.accepted), Rejected: pct, Tone: tone})
	}
	if len(sources) == 0 {
		view.Availability = "not_configured"
	} else if s.publicHost == "" {
		view.Availability = "endpoint_host_unconfigured"
	}
	example, _ := json.MarshalIndent(map[string]any{"schema": "change.v1", "event_id": "deploy-812", "occurred_at": s.now().UTC().Format(time.RFC3339), "subject": "checkout-api", "title": say("部署 v2.8.1", "Deploy v2.8.1"), "state": "succeeded", "attributes": map[string]string{"version": "v2.8.1"}}, "", "  ")
	view.Endpoint = pushEndpointView{
		Method: "POST", Path: "/api/v1/events/change", Host: s.publicHost, Example: string(example),
		Headers: []map[string]string{
			{"k": "X-AIOps-Source", "v": say("<已配置来源 ID>", "<configured source id>"), "note": say("来源与应用、环境固定绑定", "Fixed source-to-application/environment mapping")},
			{"k": "Authorization", "v": say("Bearer <令牌>", "Bearer <token>"), "note": say("使用该来源配置的令牌文件", "Token from this source's configured token file")},
			{"k": "Idempotency-Key", "v": say("<唯一键>", "<unique key>"), "note": say("幂等键保留 24 小时；event_id 始终唯一", "24-hour key fence; stable event_id remains unique")},
			{"k": "X-Timestamp", "v": say("<Unix 秒>", "<Unix seconds>"), "note": say("时钟偏差最多五分钟", "Clock skew at most five minutes")},
			{"k": "X-Signature", "v": "sha256=<hex HMAC>", "note": "HMAC-SHA256(timestamp\\nsource_id\\ntype\\nidempotency_key\\nraw_body)"},
		},
		Responses: []map[string]string{
			{"code": "202", "tone": "ok", "text": say("已接收并持久化", "Accepted and persisted")},
			{"code": "409", "tone": "neutral", "text": say("幂等键或事件 ID 重复", "Duplicate key or event identity")},
			{"code": "422", "tone": "danger", "text": say("事件不符合规范", "Invalid canonical event")},
			{"code": "401", "tone": "danger", "text": say("令牌、时间戳或签名无效", "Invalid token, timestamp or signature")},
			{"code": "503", "tone": "danger", "text": say("来源、映射或存储不可用", "Source, mapping or storage unavailable")},
		},
	}
	if len(appIDs) > 0 {
		rows, err := tx.Query(r.Context(), `WITH visible AS (SELECT * FROM unnest($1::text[],$2::text[],$3::text[]) AS v(source_id,environment_id,application_id))
 SELECT p.created_at,p.source_id,p.key_hash,p.code,p.result,p.latency_ms
 FROM v3_push_receipts p JOIN visible v
 ON (p.source_id,p.environment_id,p.application_id)=(v.source_id,v.environment_id,v.application_id)
 ORDER BY p.created_at DESC,p.id DESC LIMIT 30`, sourceIDs, sourceEnvs, appIDs)
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push deliveries unavailable")
			return
		}
		for rows.Next() {
			var at time.Time
			var sourceID, keyHash, result string
			var code, ms int
			if err = rows.Scan(&at, &sourceID, &keyHash, &code, &result, &ms); err != nil {
				break
			}
			tone := "ok"
			if code == 409 {
				tone = "neutral"
			} else if code >= 400 {
				tone = "danger"
			}
			key := ""
			if len(keyHash) >= 12 {
				key = "sha256:" + keyHash[:12]
			}
			resultLabels := map[string]string{
				"accepted": say("已接收", "Accepted"), "test_accepted": say("测试已接收", "Test accepted"),
				"duplicate": say("重复事件", "Duplicate event"), "invalid_event": say("无效事件", "Invalid event"),
			}
			if label, ok := resultLabels[result]; ok {
				result = label
			}
			view.Deliveries = append(view.Deliveries, pushDeliveryView{Time: at.UTC().Format("2006-01-02 15:04 UTC"), At: at.UTC().Format(time.RFC3339), Source: sourceID, Key: key, Code: fmt.Sprint(code), Tone: tone, Result: result, MS: fmt.Sprintf("%d ms", ms)})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "push_unavailable", "Push deliveries unavailable")
			return
		}
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, 401, "unauthorized", "Session expired")
		} else {
			httpapi.Error(w, 503, "push_unavailable", "Push view unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, 200, view)
}

func (s *Service) test(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	env := envs[0]
	if !s.authorize(w, r, "connectors", "manage", env) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpapi.Error(w, 422, "invalid_body", "Expected one Push event type")
		return
	}
	values, err := strictObject(body, map[string]bool{"type": true})
	if err != nil {
		httpapi.Error(w, 422, "invalid_body", "Expected one Push event type")
		return
	}
	typ, err := stringField(values, "type", true, 16)
	if err != nil || !eventTypes[typ] {
		httpapi.Error(w, 422, "invalid_body", "Select a supported Push event type")
		return
	}
	sources, err := s.visibleSources(r.Context(), nil, httpapi.CurrentUser(r), []string{env}, "change")
	if err != nil {
		httpapi.Error(w, 503, "push_unavailable", "Push source selection unavailable")
		return
	}
	selected := []Source{}
	for _, source := range sources {
		if sourceAllows(source, typ) {
			selected = append(selected, source)
		}
	}
	if len(selected) == 0 {
		httpapi.Error(w, 409, "not_configured", "No accessible current Push source for this event type")
		return
	}
	if len(selected) > 1 {
		httpapi.Error(w, 409, "ambiguous_source", "More than one accessible Push source matches")
		return
	}
	now := s.now()
	state := map[string]string{"alert": "firing", "change": "succeeded", "incident": "open", "resource": "updated", "oncall": "started"}[typ]
	fields := map[string]string{"schema": typ + ".v1", "event_id": "test-" + store.ID(), "occurred_at": now.UTC().Format(time.RFC3339Nano), "subject": "push-test", "title": "Push pipeline test", "state": state}
	if typ == "alert" {
		fields["severity"] = "info"
	}
	raw, _ := json.Marshal(fields)
	e, err := parseEvent(typ, raw, now)
	if err != nil {
		httpapi.Error(w, 503, "push_unavailable", "Push test event unavailable")
		return
	}
	receiptID, duplicate, err := s.persistAccepted(r.Context(), selected[0], typ, hashKey("test-"+store.ID()), e, now, httpapi.CurrentUser(r).ID, "user", r)
	if err != nil {
		writePersistError(w, err)
		return
	}
	if duplicate {
		httpapi.Error(w, 409, "duplicate", "Push test identity conflicted")
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"code": 202, "receipt_id": receiptID})
}
