// Package v3audit exposes scoped, durable management audit evidence to V3.
package v3audit

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
)

type Service struct {
	DB                   *pgxpool.Pool
	instanceEnvironments map[string]bool
}

func New(db *pgxpool.Pool, environments []string) *Service {
	allowed := make(map[string]bool, len(environments))
	for _, env := range environments {
		allowed[env] = true
	}
	return &Service{DB: db, instanceEnvironments: allowed}
}

// CleanupExpired removes expired CSV data at startup and then every 15 minutes.
// Small transactions keep an old export backlog from holding table locks for
// long; cancellation stops both the query and the worker during shutdown.
func (s *Service) CleanupExpired(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		if err := s.deleteExpired(ctx); err != nil && ctx.Err() == nil {
			log.Printf("V3 audit export cleanup: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) deleteExpired(ctx context.Context) error {
	for ctx.Err() == nil {
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		tag, err := s.DB.Exec(queryCtx, `DELETE FROM v3_audit_exports WHERE id IN (
		 SELECT id FROM v3_audit_exports WHERE expires_at <= now()
		 ORDER BY expires_at, id LIMIT 200 FOR UPDATE SKIP LOCKED
		)`)
		cancel()
		if err != nil {
			return err
		}
		if tag.RowsAffected() < 200 {
			return nil
		}
	}
	return ctx.Err()
}

// Register must replace the historical GET /audit route in the V3 composition
// root. The download route is deliberately an authenticated API route.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/audit", s.List},
		{"GET /api/v1/audit/{id}", s.Detail},
		{"POST /api/v1/audit/export", s.Export},
		{"GET /api/v1/audit/exports/{id}", s.Download},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		if route.pattern == "GET /api/v1/audit/exports/{id}" {
			// The browser opens this URL without a selectable environment header.
			// Download resolves the persisted scope and checks current grants.
			api.MarkV3ObjectRoute(route.pattern, "audit", "view")
		} else {
			api.MarkV3Route(route.pattern, "audit", "view")
		}
	}
}

type query struct {
	Filter string `json:"filter"`
	Q      string `json:"q"`
	// Range is the list's time range (GET /audit range), so an export holds
	// what the list shows (PA-170); empty exports every time.
	Range string `json:"range,omitempty"`
}

func parseQuery(q query) (query, error) {
	if q.Filter == "" {
		q.Filter = "all"
	}
	if q.Filter != "all" && q.Filter != "user" && q.Filter != "agent" && q.Filter != "system" {
		return query{}, errors.New("invalid actor filter")
	}
	q.Q = strings.TrimSpace(q.Q)
	if len([]rune(q.Q)) > 200 {
		return query{}, errors.New("query too long")
	}
	return q, nil
}

func requestScope(r *http.Request) ([]string, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		return nil, errors.New("missing V3 scope")
	}
	return scope.EnvironmentIDs, nil
}

// An audit row has no first-class environment column. We include only a row
// whose environment can be proved from explicit persisted metadata. Unscoped
// instance-wide events require an explicit global audit grant. Application
// rows without a recorded environment are intentionally omitted.
const visibleCTE = `WITH visible AS (
 SELECT a.id,a.chain_seq,a.actor_id,
        CASE WHEN a.actor_kind='integration' THEN 'system' ELSE a.actor_kind END AS actor_kind,
        a.application_id,a.action,
        a.target,a.outcome,a.changes,a.created_at,COALESCE(u.name,'') AS actor_name
 FROM management_audit a LEFT JOIN users u ON u.id=a.actor_id
 WHERE (
   (a.application_id='' AND NULLIF(COALESCE(a.changes->>'environment_id',a.changes->>'environment'),'') IS NULL AND $2::boolean)
   OR (
     NULLIF(COALESCE(a.changes->>'environment_id',a.changes->>'environment'),'')=ANY($1::text[])
     AND (a.application_id='' OR EXISTS (
       SELECT 1 FROM v3_application_environments ae
       WHERE ae.application_id=a.application_id
       AND ae.environment_id=NULLIF(COALESCE(a.changes->>'environment_id',a.changes->>'environment'),'')
     ))
   )
 )
)`

const filterWhere = ` WHERE ($3='all' OR actor_kind=$3)
 AND aiops_search_match(concat_ws(' ',actor_id,actor_name,action,target,outcome,changes->>'run_id'),$4)`

// entry.Result is null for an outcome this server cannot classify; the row
// stays listed and Unavailable says why (PA-42), so one row never hides the log.
type entry struct {
	ID          string            `json:"id"`
	Time        string            `json:"time"`
	Actor       actor             `json:"actor"`
	Action      string            `json:"action"`
	Target      string            `json:"target"`
	Result      *string           `json:"result"`
	Source      string            `json:"source"`
	Unavailable map[string]string `json:"unavailable,omitempty"`
}
type actor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func resultKind(outcome string) (string, error) {
	switch strings.ToLower(outcome) {
	// These values record a successful request or a persisted lifecycle event.
	// "accepted" and "pending_manual_cleanup" do not mean the downstream work
	// completed; the action name retains that distinction in the audit row.
	case "success", "succeeded", "ok", "completed", "accepted", "test_accepted", "recorded", "pending_manual_cleanup", "no_change":
		return "ok", nil
	// A lifecycle step whose final outcome is recorded by a later row, such as
	// a reserved or dispatching silence or a requested export.
	case "pending":
		return "pending", nil
	case "blocked", "denied", "rejected", "duplicate", "invalid_event":
		return "blocked", nil
	case "failed", "failure", "error":
		return "failed", nil
	// The write's result wasn't confirmed: it is only read back, or a person
	// checks it, and the action names which (PA-206). Not a failure.
	case "unknown":
		return "unconfirmed", nil
	// Lapsed before anything ran, such as a silence whose window passed
	// before the executor sent it.
	case "expired":
		return "expired", nil
	default:
		return "", fmt.Errorf("unmapped audit outcome %q", outcome)
	}
}
func actorKind(kind string) error {
	if kind != "user" && kind != "agent" && kind != "system" {
		return fmt.Errorf("unmapped audit actor kind %q", kind)
	}
	return nil
}

func (s *Service) globalGrant(ctx context.Context, userID string) (bool, error) {
	var yes bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants
 WHERE user_id=$1 AND module='audit' AND action='view'
 AND scope_kind='global' AND scope_id='*')`, userID).Scan(&yes)
	return yes, err
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	envs, err := requestScope(r)
	if err != nil {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	pageQuery, pageErr := parseAuditPage(r)
	if pageErr != nil {
		httpapi.Error(w, 422, "invalid_filter", "Invalid audit range, action or result")
		return
	}
	q, err := parseQuery(query{Filter: r.URL.Query().Get("filter"), Q: r.URL.Query().Get("q")})
	if err != nil {
		httpapi.Error(w, 422, "invalid_filter", err.Error())
		return
	}
	cursor := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 1 {
			httpapi.Error(w, 422, "invalid_cursor", "Use a positive audit cursor")
			return
		}
	}
	global, err := s.globalGrant(r.Context(), httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	rows, err := s.DB.Query(r.Context(), visibleCTE+` SELECT id,actor_id,actor_kind,action,target,outcome,created_at,actor_name
	 FROM visible`+filterWhere+auditPageWhere(6)+` AND ($5::bigint=0 OR id<$5) ORDER BY id DESC LIMIT 251`, envs, global, q.Filter, q.Q, cursor, pageQuery.start, pageQuery.actions, pageQuery.outcomes)
	if err != nil {
		unavailable(w)
		return
	}
	items := []entry{}
	for rows.Next() {
		var id int64
		var actorID, kind, action, target, outcome, name string
		var at time.Time
		if err = rows.Scan(&id, &actorID, &kind, &action, &target, &outcome, &at, &name); err != nil {
			break
		}
		if err = actorKind(kind); err != nil {
			break
		}
		row := entry{ID: strconv.FormatInt(id, 10), Time: at.UTC().Format(time.RFC3339), Action: action, Target: target, Source: "management_audit"}
		if result, mapErr := resultKind(outcome); mapErr == nil {
			row.Result = &result
		} else {
			row.Unavailable = map[string]string{"result": "unavailable"}
		}
		if name == "" {
			name = actorID
		}
		row.Actor = actor{Kind: kind, Name: name}
		items = append(items, row)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	nextCursor := ""
	if len(items) > 250 {
		items = items[:250]
		nextCursor = items[len(items)-1].ID
	}
	counts := map[string]int64{"all": 0, "user": 0, "agent": 0, "system": 0}
	countRows, err := s.DB.Query(r.Context(), visibleCTE+` SELECT actor_kind,count(*) FROM visible
 WHERE aiops_search_match(concat_ws(' ',actor_id,actor_name,action,target,outcome,changes->>'run_id'),$3)
 `+auditPageWhere(4)+` GROUP BY actor_kind`, envs, global, q.Q, pageQuery.start, pageQuery.actions, pageQuery.outcomes)
	if err != nil {
		unavailable(w)
		return
	}
	for countRows.Next() {
		var k string
		var n int64
		if err = countRows.Scan(&k, &n); err != nil {
			break
		}
		if err = actorKind(k); err != nil {
			break
		}
		counts[k], counts["all"] = n, counts["all"]+n
	}
	if err == nil {
		err = countRows.Err()
	}
	countRows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	volume, err := s.volume(r.Context(), envs, global, pageQuery)
	if err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts, "volume": volume, "next_cursor": nextCursor})
}

func (s *Service) volume(ctx context.Context, envs []string, global bool, facets ...auditPageQuery) ([][]int64, error) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -6)
	step := 24 * time.Hour
	extra := ""
	args := []any{envs, global, start}
	if len(facets) > 0 {
		p := facets[0]
		if p.start != nil {
			start = *p.start
			step = p.duration / 7
		}
		args[2] = start
		extra = auditPageWhere(5)
		args = append(args, step.Seconds(), p.start, p.actions, p.outcomes)
	} else {
		args = append(args, step.Seconds())
	}
	rows, err := s.DB.Query(ctx, visibleCTE+` SELECT floor(extract(epoch from (created_at-$3::timestamptz))/$4::double precision)::int,actor_kind,count(*) FROM visible WHERE created_at>=$3 `+extra+` GROUP BY 1,2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	volume := make([][]int64, 7)
	for i := range volume {
		volume[i] = []int64{0, 0, 0}
	}
	for rows.Next() {
		var index int
		var kind string
		var count int64
		if err = rows.Scan(&index, &kind, &count); err != nil {
			return nil, err
		}
		slot, ok := map[string]int{"user": 0, "agent": 1, "system": 2}[kind]
		if !ok {
			return nil, fmt.Errorf("unmapped audit actor kind %q", kind)
		}
		if index >= 0 && index < len(volume) {
			volume[index][slot] = count
		}
	}
	return volume, rows.Err()
}

type detail struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Time      string  `json:"time"`
	ActorKind string  `json:"actor_kind"`
	ActorName string  `json:"actor_name"`
	Actor     []field `json:"actor"`
	Links     []field `json:"links"`
	Payload   string  `json:"payload"`
	ChainOK   bool    `json:"chain_ok"`
}
type field struct {
	K  string `json:"k"`
	V  string `json:"v"`
	To string `json:"to,omitempty"`
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httpapi.Error(w, 404, "not_found", "Audit record not found")
		return
	}
	envs, err := requestScope(r)
	if err != nil {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	global, err := s.globalGrant(r.Context(), httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	var seq int64
	var actorID, kind, action, target, outcome, name string
	var at time.Time
	err = s.DB.QueryRow(r.Context(), visibleCTE+` SELECT chain_seq,actor_id,actor_kind,action,target,outcome,created_at,actor_name
 FROM visible WHERE id=$3`, envs, global, id).Scan(&seq, &actorID, &kind, &action, &target, &outcome, &at, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Audit record not found")
		return
	}
	if err != nil || actorKind(kind) != nil {
		unavailable(w)
		return
	}
	if name == "" {
		name = actorID
	}
	var valid bool
	err = s.DB.QueryRow(r.Context(), `WITH chain AS (
 SELECT chain_seq,chain_prev_hash,chain_hash,
 encode(digest(jsonb_build_array(chain_prev_hash,id,actor_id,actor_kind,
 application_id,action,target,outcome,changes,created_at)::text,'sha256'),'hex') AS expected,
 lag(chain_hash) OVER (ORDER BY chain_seq) AS previous
 FROM management_audit WHERE chain_seq<=$1
) SELECT COALESCE(bool_and(chain_hash=expected AND chain_prev_hash=COALESCE(previous,'')),false) FROM chain`, seq).Scan(&valid)
	if err != nil {
		unavailable(w)
		return
	}
	// Raw changes can contain legacy secrets under arbitrary keys. Only safe
	// provenance fields enter the V3 payload until those writes are sanitized.
	payload, _ := json.MarshalIndent(map[string]any{"action": action, "target": target, "outcome": outcome, "changes": "redacted"}, "", "  ")
	v := detail{ID: strconv.FormatInt(id, 10), Title: action, Time: at.UTC().Format(time.RFC3339), ActorKind: kind, ActorName: name,
		Actor: []field{{K: "ID", V: actorID}, {K: "Source", V: "management_audit"}}, Links: []field{}, Payload: string(payload), ChainOK: valid}
	httpapi.WriteJSON(w, 200, v)
}

func (s *Service) Export(w http.ResponseWriter, r *http.Request) {
	envs, err := requestScope(r)
	if err != nil {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	var input query
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			httpapi.Error(w, 422, "invalid_body", "Invalid audit export filter")
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			httpapi.Error(w, 422, "invalid_body", "Expected one audit export filter")
			return
		}
	}
	input, err = parseQuery(input)
	if err != nil {
		httpapi.Error(w, 422, "invalid_filter", err.Error())
		return
	}
	global, err := s.globalGrant(r.Context(), httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	var since time.Time
	if input.Range != "" {
		d, rangeErr := v3pagequery.ParseRange(url.Values{"range": {input.Range}}, 0)
		if rangeErr != nil {
			httpapi.Error(w, 422, "invalid_filter", "Use one supported audit range")
			return
		}
		since = time.Now().UTC().Add(-d)
	}
	rows, err := s.DB.Query(r.Context(), visibleCTE+` SELECT id,created_at,actor_id,actor_kind,action,target,outcome
 FROM visible`+filterWhere+` AND created_at>=$5 ORDER BY id DESC LIMIT 10001`, envs, global, input.Filter, input.Q, since)
	if err != nil {
		unavailable(w)
		return
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	_ = writer.Write([]string{"id", "time", "actor_id", "actor_kind", "action", "target", "outcome"})
	count := 0
	for rows.Next() {
		var id int64
		var at time.Time
		var actorID, kind, action, target, outcome string
		if err = rows.Scan(&id, &at, &actorID, &kind, &action, &target, &outcome); err != nil {
			break
		}
		if err = actorKind(kind); err != nil {
			break
		}
		count++
		if count > 10000 {
			break
		}
		if err = writer.Write([]string{strconv.FormatInt(id, 10), at.UTC().Format(time.RFC3339), csvSafe(actorID), csvSafe(kind), csvSafe(action), csvSafe(target), csvSafe(outcome)}); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	writer.Flush()
	if err == nil {
		err = writer.Error()
	}
	if err != nil {
		unavailable(w)
		return
	}
	if count > 10000 {
		httpapi.Error(w, 409, "audit_export_too_broad", "Narrow the export to at most 10000 records")
		return
	}
	var id string
	err = s.DB.QueryRow(r.Context(), `INSERT INTO v3_audit_exports(id,owner_id,environment_ids,includes_global,csv_data)
 VALUES('audit-' || encode(gen_random_bytes(18),'hex'),$1,$2,$3,$4)
 RETURNING id`, httpapi.CurrentUser(r).ID, envs, global, buf.Bytes()).Scan(&id)
	if err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"url": "/api/v1/audit/exports/" + id})
}

func (s *Service) Download(w http.ResponseWriter, r *http.Request) {
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	// A supplied header must have passed the environment middleware and retain
	// the exact scope selected when the export was created.
	scope, scoped := httpapi.V3ScopeFromContext(r.Context())
	if !scoped && len(r.Header.Values("X-AIOps-Environment")) != 0 {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	var csvData []byte
	var exportedEnvs []string
	var includesGlobal bool
	err := s.DB.QueryRow(r.Context(), `SELECT environment_ids,includes_global,csv_data FROM v3_audit_exports
	 WHERE id=$1 AND owner_id=$2 AND expires_at>now()`, r.PathValue("id"), user.ID).Scan(&exportedEnvs, &includesGlobal, &csvData)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Audit export not found or expired")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if len(exportedEnvs) == 0 || (scoped && !sameScope(scope.EnvironmentIDs, exportedEnvs)) {
		httpapi.Error(w, 403, "forbidden", "Select the original export environment")
		return
	}
	// The owner may have lost an environment or the audit grant since export.
	// Recheck every persisted environment before returning any CSV bytes.
	for _, env := range exportedEnvs {
		if !s.instanceEnvironments[env] {
			httpapi.Error(w, 403, "forbidden", "Export environment is outside this instance")
			return
		}
		allowed, err := s.downloadGrant(r.Context(), user.ID, env)
		if err != nil {
			unavailable(w)
			return
		}
		if !allowed {
			httpapi.Error(w, 403, "forbidden", "Audit export permission changed")
			return
		}
	}
	if includesGlobal {
		global, err := s.globalGrant(r.Context(), user.ID)
		if err != nil {
			unavailable(w)
			return
		}
		if !global {
			httpapi.Error(w, 403, "forbidden", "Audit export permission changed")
			return
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(csvData)
}

// downloadGrant mirrors the current V3 environment and audit:view decisions
// for a persisted environment. The route cannot rely on a request-selected
// scope when a browser opens the export URL without a custom header.
func (s *Service) downloadGrant(ctx context.Context, userID, env string) (bool, error) {
	if userID == "" || env == "" || env == "all" {
		return false, nil
	}
	var allowed bool
	err := s.DB.QueryRow(ctx, `SELECT
	 EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
	   AND g.module='environment' AND g.action='view'
	   AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=$2)))
	 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
	   AND g.module='audit' AND g.action='view'
	   AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=$2)))`, userID, env).Scan(&allowed)
	return allowed, err
}

func sameScope(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

func csvSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "audit_unavailable", "Audit evidence is temporarily unavailable")
}
