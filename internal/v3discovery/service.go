// Package v3discovery exposes scoped, persisted records to the V3 command
// palette. It deliberately has no synthetic recent history or health data.
package v3discovery

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/searchtext"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3knowledge"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	DB           *pgxpool.Pool
	Access       Access
	runbooks     *v3knowledge.Service
	textCommands []TextCommand // configured before serving; nil means unavailable
}

func New(db *pgxpool.Pool, access Access) *Service {
	s := &Service{DB: db, Access: access}
	// Production passes the transaction-capable policy. A fake Access cannot
	// stand in for Runbook's version-bound, conjunctive object authorization.
	if policy, ok := access.(*rbac.V3Policy); ok && policy != nil {
		s.runbooks = &v3knowledge.Service{DB: db, Policy: policy}
	}
	return s
}

// These composite routes enforce per-result policy, so they have no single
// module code to mark at the API boundary. The V3 environment middleware and
// authentication still run before each handler.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/search", s.search},
		{"GET /api/v1/mentions", s.mentions},
		{"GET /api/v1/mentions/{kind}/{id}/preview", s.preview},
		{"GET /api/v1/commands", s.commands},
		{"GET /api/v1/services/{id}/peek", s.servicePeek},
		{"GET /api/v1/people/{name}/peek", s.personPeek},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3Route(route.pattern, "", "")
	}
}

type record struct {
	Kind            string
	ID              string
	Title           string
	EnvironmentID   string
	ApplicationID   string
	TeamID          string
	Tier            string
	State           string
	Version         int
	DetailAvailable bool
}

type hit struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	Title            string `json:"title"`
	Subtitle         string `json:"subtitle,omitempty"`
	Status           string `json:"status,omitempty"`
	Version          int    `json:"version,omitempty"`
	GovernanceStatus string `json:"governance_status,omitempty"`
	Href             string `json:"href"`
}

type candidate struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	Label            string `json:"label"`
	Meta             string `json:"meta,omitempty"`
	Status           string `json:"status,omitempty"`
	Tier             string `json:"tier,omitempty"`
	Version          int    `json:"version,omitempty"`
	GovernanceStatus string `json:"governance_status,omitempty"`
}

type mentionPreview struct {
	Kind             string   `json:"kind"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	Tier             string   `json:"tier,omitempty"`
	Status           string   `json:"status,omitempty"`
	StatusLabel      string   `json:"status_label,omitempty"`
	Metrics          []any    `json:"metrics"`
	Related          []any    `json:"related"`
	Href             string   `json:"href,omitempty"`
	Version          int      `json:"version,omitempty"`
	GovernanceStatus string   `json:"governance_status,omitempty"`
	Missing          []string `json:"missing,omitempty"`
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "discovery_unavailable", "Discovery storage or policy is not configured")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}

func parseQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	if len(r.URL.Query()["q"]) > 1 {
		httpapi.Error(w, 422, "invalid_query", "Select one search query")
		return "", false
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 200 || len(q) > 800 {
		httpapi.Error(w, 422, "invalid_query", "Search query is too long")
		return "", false
	}
	return q, true
}

func (s *Service) search(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return
	}
	result := []hit{}
	if q != "" {
		records, err := s.records(r, envs, q, "")
		if err != nil {
			listError(w, err)
			return
		}
		for _, item := range balanced(records, 8) {
			result = append(result, hit{Kind: item.Kind, ID: item.ID, Title: item.Title,
				Subtitle: meta(r, item), Status: tone(item), Href: href(item),
				Version: item.Version, GovernanceStatus: governanceStatus(item)})
		}
	}
	// No persisted per-user navigation history exists yet. An empty recent list
	// is truthful and avoids leaking records from another user's activity.
	httpapi.WriteJSON(w, 200, map[string]any{"query": q, "hits": result, "recent": []hit{}})
}

func (s *Service) mentions(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return
	}
	if len(r.URL.Query()["kind"]) > 1 {
		httpapi.Error(w, 422, "invalid_kind", "Select one reference kind")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "service" && kind != "incident" && kind != "investigation" && kind != "conversation" && kind != "person" && kind != "runbook" {
		httpapi.Error(w, 422, "unsupported_kind", "Mention kind is not available")
		return
	}
	records, err := s.records(r, envs, q, kind)
	if err != nil {
		listError(w, err)
		return
	}
	items := []candidate{}
	for _, item := range balanced(records, 30) {
		items = append(items, candidate{Kind: item.Kind, ID: item.ID, Label: item.Title,
			Meta: meta(r, item), Status: tone(item), Tier: item.Tier,
			Version: item.Version, GovernanceStatus: governanceStatus(item)})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if kind != "service" && kind != "incident" && kind != "investigation" && kind != "conversation" && kind != "person" && kind != "runbook" {
		httpapi.Error(w, 503, "preview_unavailable", "This reference kind has no verified preview source")
		return
	}
	// Service references encode both application and environment IDs. Keep
	// this limit aligned with parseServiceRecordID so a listed candidate can
	// still be previewed when its composite ID exceeds 200 bytes.
	if id == "" || len(id) > 1024 {
		httpapi.Error(w, 422, "invalid_reference", "Invalid reference ID")
		return
	}
	item, err := s.recordByID(r, envs, kind, id)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Reference not found")
		return
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "preview_unavailable", "Reference preview unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, previewOf(r, item))
}

func previewOf(r *http.Request, item record) mentionPreview {
	preview := mentionPreview{
		Kind: item.Kind, ID: item.ID, Title: item.Title, Subtitle: meta(r, item),
		Tier: item.Tier, Status: tone(item), StatusLabel: statusLabel(r, item),
		// No verified telemetry or relationship source is connected to this
		// preview. Never render synthetic metrics or inferred associations.
		Metrics: []any{}, Related: []any{}, Href: href(item),
		Version: item.Version, GovernanceStatus: governanceStatus(item),
	}
	if item.Kind == "runbook" {
		preview.Missing = []string{"metrics", "related", "usage", "execution_verification"}
		if !item.DetailAvailable {
			preview.Missing = append(preview.Missing, "structured_detail")
		}
	}
	return preview
}

func statusLabel(r *http.Request, item record) string {
	if item.Kind == "runbook" {
		en := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
		switch item.State {
		case "draft":
			if en {
				return "Draft"
			}
			return "草稿"
		case "review":
			if en {
				return "In review"
			}
			return "待审核"
		case "published":
			if en {
				return "Published"
			}
			return "已发布"
		}
		return ""
	}
	if item.Kind != "incident" {
		return ""
	}
	en := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	switch item.State {
	case "open":
		if en {
			return "Open"
		}
		return "待处理"
	case "investigating":
		if en {
			return "Investigating"
		}
		return "调查中"
	case "resolved":
		if en {
			return "Resolved"
		}
		return "已解决"
	}
	return ""
}

func (s *Service) commands(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	if s.textCommands == nil {
		httpapi.Error(w, 503, "commands_unavailable", "Text command catalog is not configured")
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All {
		// An aggregate environment cannot submit a conversation or canvas turn.
		httpapi.WriteJSON(w, 200, map[string]any{"items": []commandItem{}})
		return
	}
	items, err := s.commandItems(r, envs)
	if err != nil {
		httpapi.Error(w, 503, "commands_unavailable", "Text command catalog is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

// A scoped directory reader may mention only active human accounts whose
// effective assignments overlap the selected environment. A global members
// reader has the same unassigned-account visibility as GET /members. The
// object read below repeats this predicate so a guessed ID cannot bypass it.
const personSource = `SELECT DISTINCT ON (u.id) u.id,u.name,selected.env
FROM users u CROSS JOIN unnest($1::text[]) AS selected(env)
WHERE u.deleted_at IS NULL AND NOT u.disabled AND u.source<>'settings_demo'
 AND u.v3_identity_kind='human'
 AND ($3='' OR aiops_search_match(u.id||' '||u.name,$3))
 AND ($4='' OR u.id=$4)
 AND ($6='' OR u.name=$6)
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants actor
  WHERE actor.user_id=$2 AND actor.module='members' AND actor.action='view'
  AND ((actor.scope_kind='global' AND actor.scope_id='*')
    OR (actor.scope_kind='environment' AND actor.scope_id=selected.env)))
 AND (EXISTS (SELECT 1 FROM v3_effective_role_grants actor
       WHERE actor.user_id=$2 AND actor.module='members' AND actor.action='view'
       AND actor.scope_kind='global' AND actor.scope_id='*')
   OR EXISTS (SELECT 1 FROM v3_effective_role_grants target
       WHERE target.user_id=u.id AND (
        (target.scope_kind='global' AND target.scope_id='*')
        OR (target.scope_kind='environment' AND target.scope_id=selected.env)
        OR (target.scope_kind='application' AND EXISTS (
         SELECT 1 FROM v3_application_environments vae
         JOIN applications a ON a.id=vae.application_id AND NOT a.archived
         JOIN application_environments ae ON ae.application_id=a.id AND ae.key=vae.environment_id AND NOT ae.archived
         WHERE vae.application_id=target.scope_id AND vae.environment_id=selected.env))
        OR (target.scope_kind='team' AND EXISTS (
         SELECT 1 FROM applications a
         JOIN v3_application_environments vae ON vae.application_id=a.id
         JOIN application_environments ae ON ae.application_id=a.id AND ae.key=vae.environment_id AND NOT ae.archived
         WHERE a.owner_team_id=target.scope_id AND NOT a.archived AND vae.environment_id=selected.env)))))
ORDER BY u.id,selected.env LIMIT $5`

// records uses the existing SQL pinyin matcher to narrow bounded persisted
// candidates. The policy check happens before any candidate is serialized.
func (s *Service) records(r *http.Request, envs []string, q, kind string) ([]record, error) {
	items := []record{}
	limit := 50 // A blank @ query shows bounded persisted candidates.
	if q != "" {
		limit = 101
	}
	if kind == "" || kind == "service" {
		rows, err := s.DB.Query(r.Context(), `SELECT a.id,a.name,ve.environment_id,COALESCE(a.owner_team_id,''),COALESCE(p.tier,'')
FROM applications a
JOIN v3_application_environments ve ON ve.application_id=a.id
JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
LEFT JOIN v3_service_environment_profiles p ON p.application_id=a.id AND p.environment_id=ve.environment_id
WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
AND ($2='' OR aiops_search_match(concat_ws(' ',a.key,a.name,a.id),$2))
AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$4
 AND g.module='services' AND g.action='view' AND (
  (g.scope_kind='global' AND g.scope_id='*') OR
  (g.scope_kind='environment' AND g.scope_id=ve.environment_id) OR
  (g.scope_kind='application' AND g.scope_id=a.id) OR
  (g.scope_kind='team' AND g.scope_id=a.owner_team_id)))
ORDER BY a.name,ve.environment_id,a.id LIMIT $3`, envs, q, limit, httpapi.CurrentUser(r).ID)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var v record
			var appID string
			v.Kind = "service"
			if err = rows.Scan(&appID, &v.Title, &v.EnvironmentID, &v.TeamID, &v.Tier); err != nil {
				break
			}
			v.ApplicationID = appID
			v.ID = serviceRecordID(appID, v.EnvironmentID)
			v.Tier = supportedTier(v.Tier)
			items = append(items, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && count > 100 {
			return nil, errTooBroad
		}
	}
	if kind == "" || kind == "incident" {
		rows, err := s.DB.Query(r.Context(), `SELECT i.id,i.title,e.key,i.application_id,i.status
FROM ops_incidents i
JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
JOIN applications a ON a.id=i.application_id AND NOT a.archived
WHERE e.key=ANY($1::text[]) AND ($2='' OR aiops_search_match(i.id||' '||i.title,$2))
AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$4
 AND g.module='events' AND g.action='view' AND (
  (g.scope_kind='global' AND g.scope_id='*') OR
  (g.scope_kind='environment' AND g.scope_id=e.key) OR
  (g.scope_kind='application' AND g.scope_id=i.application_id)))
ORDER BY i.updated_at DESC,i.id LIMIT $3`, envs, q, limit, httpapi.CurrentUser(r).ID)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var v record
			v.Kind = "incident"
			if err = rows.Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.State); err != nil {
				break
			}
			items = append(items, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && count > 100 {
			return nil, errTooBroad
		}
	}
	if kind == "" || kind == "investigation" {
		rows, err := s.DB.Query(r.Context(), `SELECT i.id,i.title,ae.key,i.application_id,COALESCE(a.owner_team_id,'')
FROM investigations i
JOIN applications a ON a.id=i.application_id AND NOT a.archived
JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id AND NOT ae.archived
JOIN v3_application_environments vae ON vae.application_id=i.application_id AND vae.environment_id=ae.key
WHERE ae.key=ANY($1::text[]) AND ($2='' OR aiops_search_match(i.id||' '||i.title,$2))
AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$4
 AND g.module='canvas' AND g.action='view' AND (
  (g.scope_kind='global' AND g.scope_id='*') OR
  (g.scope_kind='environment' AND g.scope_id=ae.key) OR
  (g.scope_kind='application' AND g.scope_id=i.application_id) OR
  (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id)))
ORDER BY i.updated_at DESC,i.id LIMIT $3`, envs, q, limit, httpapi.CurrentUser(r).ID)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var v record
			v.Kind = "investigation"
			if err = rows.Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.TeamID); err != nil {
				break
			}
			items = append(items, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && count > 100 {
			return nil, errTooBroad
		}
	}
	if kind == "" || kind == "conversation" {
		rows, err := s.DB.Query(r.Context(), `SELECT id,title,environment_id,status FROM v3_conversations
WHERE owner_id=$1 AND environment_id=ANY($2) AND ($3='' OR aiops_search_match(id||' '||title,$3))
AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
 AND g.module='conversation' AND g.action='view' AND (
  (g.scope_kind='global' AND g.scope_id='*') OR
  (g.scope_kind='environment' AND g.scope_id=environment_id)))
ORDER BY updated_at DESC,id LIMIT $4`, httpapi.CurrentUser(r).ID, envs, q, limit)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var v record
			v.Kind = "conversation"
			if err = rows.Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.State); err != nil {
				break
			}
			items = append(items, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && count > 100 {
			return nil, errTooBroad
		}
	}
	if kind == "" || kind == "person" {
		rows, err := s.DB.Query(r.Context(), personSource, envs, httpapi.CurrentUser(r).ID, q, "", limit, "")
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var v record
			v.Kind = "person"
			if err = rows.Scan(&v.ID, &v.Title, &v.EnvironmentID); err != nil {
				break
			}
			items = append(items, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && count > 100 {
			return nil, errTooBroad
		}
	}
	visible := make([]record, 0, len(items))
	authorized := map[string]error{}
	for _, item := range items {
		if q != "" && item.Kind != "service" && !searchtext.Match(item.ID+" "+item.Title, q) {
			continue
		}
		key := item.Kind + "\x00" + item.EnvironmentID + "\x00" + item.ApplicationID + "\x00" + item.TeamID
		err, cached := authorized[key]
		if !cached {
			err = s.authorize(r, item)
			authorized[key] = err
		}
		if err == nil {
			visible = append(visible, item)
		} else if !errors.Is(err, store.ErrForbidden) {
			return nil, err
		}
	}
	// Read Runbooks last in a session/grant/version-bound transaction, after
	// ordinary kinds have been resolved, to minimize stale mixed-kind output.
	if kind == "" || kind == "runbook" {
		if s.runbooks == nil {
			if kind == "runbook" {
				return nil, v3knowledge.ErrRunbookReferencesUnavailable
			}
		} else {
			refs, err := s.runbooks.ListRunbookReferences(r, q, limit)
			if err != nil {
				return nil, err
			}
			if q != "" && len(refs) > 100 {
				return nil, errTooBroad
			}
			for _, ref := range refs {
				visible = append(visible, recordFromRunbookReference(ref))
			}
		}
	}
	return visible, nil
}

// Reserve a fair share of bounded palette slots for each visible kind, then
// group the selected rows by kind as the Composer expects.
func balanced(items []record, max int) []record {
	if len(items) <= max {
		return items
	}
	order := []string{}
	buckets := make(map[string][]record)
	for _, item := range items {
		if _, ok := buckets[item.Kind]; !ok {
			order = append(order, item.Kind)
		}
		buckets[item.Kind] = append(buckets[item.Kind], item)
	}
	selected := make(map[string][]record, len(order))
	count := 0
	for count < max {
		progress := false
		for _, kind := range order {
			if count == max || len(buckets[kind]) == 0 {
				continue
			}
			selected[kind] = append(selected[kind], buckets[kind][0])
			buckets[kind] = buckets[kind][1:]
			count++
			progress = true
		}
		if !progress {
			break
		}
	}
	out := make([]record, 0, count)
	for _, kind := range order {
		out = append(out, selected[kind]...)
	}
	return out
}

func (s *Service) recordByID(r *http.Request, envs []string, kind, id string) (record, error) {
	v := record{Kind: kind}
	var err error
	if kind == "runbook" {
		if s.runbooks == nil {
			return v, v3knowledge.ErrRunbookReferencesUnavailable
		}
		ref, err := s.runbooks.RunbookReferenceByID(r, id)
		if err != nil {
			return v, err
		}
		return recordFromRunbookReference(ref), nil
	} else if kind == "service" {
		appID, env, valid := parseServiceRecordID(id)
		if !valid {
			return v, pgx.ErrNoRows
		}
		err = s.DB.QueryRow(r.Context(), `SELECT a.name,COALESCE(a.owner_team_id,''),COALESCE(p.tier,'')
FROM applications a
JOIN v3_application_environments ve ON ve.application_id=a.id
JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
LEFT JOIN v3_service_environment_profiles p ON p.application_id=a.id AND p.environment_id=ve.environment_id
WHERE a.id=$1 AND NOT a.archived AND ve.environment_id=$2 AND ve.environment_id=ANY($3::text[])`, appID, env, envs).Scan(&v.Title, &v.TeamID, &v.Tier)
		v.ID, v.ApplicationID, v.EnvironmentID = id, appID, env
		v.Tier = supportedTier(v.Tier)
	} else if kind == "incident" {
		err = s.DB.QueryRow(r.Context(), `SELECT i.id,i.title,e.key,i.application_id,i.status FROM ops_incidents i
JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
JOIN applications a ON a.id=i.application_id AND NOT a.archived
WHERE i.id=$1 AND e.key=ANY($2::text[])`, id, envs).Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.State)
	} else if kind == "investigation" {
		err = s.DB.QueryRow(r.Context(), `SELECT i.id,i.title,ae.key,i.application_id,COALESCE(a.owner_team_id,'')
FROM investigations i
JOIN applications a ON a.id=i.application_id AND NOT a.archived
JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id AND NOT ae.archived
JOIN v3_application_environments vae ON vae.application_id=i.application_id AND vae.environment_id=ae.key
WHERE i.id=$1 AND ae.key=ANY($2::text[])`, id, envs).Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.TeamID)
	} else if kind == "person" {
		err = s.DB.QueryRow(r.Context(), personSource, envs, httpapi.CurrentUser(r).ID, "", id, 1, "").Scan(&v.ID, &v.Title, &v.EnvironmentID)
	} else {
		err = s.DB.QueryRow(r.Context(), `SELECT id,title,environment_id,status FROM v3_conversations
WHERE id=$1 AND owner_id=$2 AND environment_id=ANY($3)`, id, httpapi.CurrentUser(r).ID, envs).Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.State)
	}
	if err != nil {
		return v, err
	}
	return v, s.authorize(r, v)
}

func (s *Service) authorize(r *http.Request, v record) error {
	module := "events"
	if v.Kind == "conversation" {
		module = "conversation"
	} else if v.Kind == "service" {
		module = "services"
	} else if v.Kind == "investigation" {
		module = "canvas"
	} else if v.Kind == "person" {
		module = "members"
	}
	err := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: module, Action: "view", EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	})
	if err == nil || !errors.Is(err, store.ErrForbidden) ||
		(v.Kind != "service" && v.Kind != "investigation") || v.TeamID == "" {
		return err
	}
	return s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: module, Action: "view", EnvironmentID: v.EnvironmentID, TeamID: v.TeamID,
	})
}

func serviceRecordID(appID, env string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(appID + "\x00" + env))
}

func parseServiceRecordID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || serviceRecordID(parts[0], parts[1]) != id {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func supportedTier(value string) string {
	if value == "T0" || value == "T1" || value == "T2" {
		return value
	}
	return ""
}

var errTooBroad = errors.New("discovery candidates exceed bounded query")

func listError(w http.ResponseWriter, err error) {
	if errors.Is(err, errTooBroad) {
		httpapi.Error(w, 413, "scope_too_large", "Narrow the search query")
		return
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	httpapi.Error(w, 503, "discovery_unavailable", "Discovery records unavailable")
}

func href(v record) string {
	if v.Kind == "runbook" {
		if !v.DetailAvailable {
			return "/runbooks"
		}
		return "/runbooks/" + url.PathEscape(v.ID)
	}
	if v.Kind == "incident" {
		return "/incidents/" + url.PathEscape(v.ID)
	}
	if v.Kind == "service" {
		return "/services/" + url.PathEscape(v.ID)
	}
	if v.Kind == "investigation" {
		// The current web router recognizes only demo INV-* IDs as investigation
		// canvases. Real persisted IDs can still be found on the canvas list.
		return "/canvases"
	}
	if v.Kind == "person" {
		return "/settings/members"
	}
	return "/conversations/" + url.PathEscape(v.ID)
}

func label(r *http.Request, kind string) string {
	en := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	if kind == "runbook" {
		if en {
			return "Runbook"
		}
		return "运行手册"
	}
	if kind == "service" {
		if en {
			return "Service"
		}
		return "服务"
	}
	if kind == "incident" {
		if en {
			return "Incident"
		}
		return "事件"
	}
	if kind == "investigation" {
		if en {
			return "Investigation"
		}
		return "调查"
	}
	if kind == "person" {
		if en {
			return "Person"
		}
		return "人员"
	}
	if en {
		return "Conversation"
	}
	return "对话"
}

func meta(r *http.Request, v record) string {
	if v.Kind == "runbook" {
		parts := []string{label(r, v.Kind), "v" + strconv.Itoa(v.Version), statusLabel(r, v)}
		if v.EnvironmentID != "" {
			parts = append(parts, v.EnvironmentID)
		}
		return strings.Join(parts, " · ")
	}
	if v.Kind == "person" {
		return label(r, v.Kind)
	}
	if v.EnvironmentID == "" {
		return label(r, v.Kind)
	}
	return label(r, v.Kind) + " · " + v.EnvironmentID
}

func tone(v record) string {
	if v.Kind == "incident" {
		if v.State == "open" || v.State == "investigating" {
			return "warn"
		}
		if v.State == "resolved" {
			return "ok"
		}
	}
	return ""
}

func governanceStatus(v record) string {
	if v.Kind == "runbook" {
		return v.State
	}
	return ""
}

func recordFromRunbookReference(ref v3knowledge.RunbookReference) record {
	return record{Kind: "runbook", ID: ref.ID, Title: ref.Title,
		EnvironmentID: ref.EnvironmentID, ApplicationID: ref.ApplicationID,
		TeamID: ref.TeamID, State: ref.GovernanceStatus, Version: ref.Version,
		DetailAvailable: ref.DetailAvailable}
}
