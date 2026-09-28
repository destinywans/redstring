// Package v3discovery exposes scoped, persisted records to the V3 command
// palette. It deliberately has no synthetic recent history or health data.
package v3discovery

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

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
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3Route(route.pattern, "", "")
	}
}

type record struct {
	Kind          string
	ID            string
	Title         string
	EnvironmentID string
	ApplicationID string
	State         string
}

type hit struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Status   string `json:"status,omitempty"`
	Href     string `json:"href"`
}

type candidate struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Meta   string `json:"meta,omitempty"`
	Status string `json:"status,omitempty"`
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
		for _, item := range records {
			result = append(result, hit{Kind: item.Kind, ID: item.ID, Title: item.Title,
				Subtitle: label(r, item.Kind), Status: tone(item), Href: href(item)})
			if len(result) == 8 {
				break
			}
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
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "incident" && kind != "conversation" {
		httpapi.Error(w, 422, "unsupported_kind", "Mention kind is not available")
		return
	}
	records, err := s.records(r, envs, q, kind)
	if err != nil {
		listError(w, err)
		return
	}
	items := []candidate{}
	for _, item := range records {
		items = append(items, candidate{Kind: item.Kind, ID: item.ID, Label: item.Title,
			Meta: label(r, item.Kind), Status: tone(item)})
		if len(items) == 30 {
			break
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if kind != "incident" && kind != "conversation" {
		httpapi.Error(w, 503, "preview_unavailable", "This reference kind has no verified preview source")
		return
	}
	if id == "" || len(id) > 200 {
		httpapi.Error(w, 422, "invalid_reference", "Invalid reference ID")
		return
	}
	item, err := s.recordByID(r, envs, kind, id)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Reference not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "preview_unavailable", "Reference preview unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"kind": item.Kind, "id": item.ID, "title": item.Title, "subtitle": label(r, item.Kind),
		"status": tone(item), "metrics": []any{}, "related": []any{}, "href": href(item),
	})
}

func (s *Service) commands(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.ready(w, r); !ok {
		return
	}
	// There is no persisted executable command/template registry yet. Returning
	// hard-coded commands would imply that actions such as silence can run.
	httpapi.Error(w, 503, "commands_unavailable", "Command catalog is not configured")
}

// records uses the existing SQL pinyin matcher to narrow bounded persisted
// candidates. The policy check happens before any candidate is serialized.
func (s *Service) records(r *http.Request, envs []string, q, kind string) ([]record, error) {
	items := []record{}
	limit := 50 // A blank @ query shows the newest candidates, not an unbounded catalog.
	if q != "" {
		limit = 101
	}
	if kind == "" || kind == "incident" {
		rows, err := s.DB.Query(r.Context(), `SELECT i.id,i.title,e.key,i.application_id,i.status
FROM ops_incidents i
JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
JOIN applications a ON a.id=i.application_id AND NOT a.archived
WHERE e.key=ANY($1::text[]) AND ($2='' OR aiops_search_match(i.id||' '||i.title,$2))
ORDER BY i.updated_at DESC,i.id LIMIT $3`, envs, q, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v record
			v.Kind = "incident"
			if err = rows.Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.State); err != nil {
				break
			}
			items = append(items, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if q != "" && len(items) > 100 {
			return nil, errTooBroad
		}
	}
	if kind == "" || kind == "conversation" {
		rows, err := s.DB.Query(r.Context(), `SELECT id,title,environment_id,status FROM v3_conversations
WHERE owner_id=$1 AND environment_id=ANY($2) AND ($3='' OR aiops_search_match(id||' '||title,$3))
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
	visible := make([]record, 0, len(items))
	authorized := map[string]error{}
	for _, item := range items {
		if q != "" && !searchtext.Match(item.ID+" "+item.Title, q) {
			continue
		}
		key := item.Kind + "\x00" + item.EnvironmentID + "\x00" + item.ApplicationID
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
	return visible, nil
}

func (s *Service) recordByID(r *http.Request, envs []string, kind, id string) (record, error) {
	v := record{Kind: kind}
	var err error
	if kind == "incident" {
		err = s.DB.QueryRow(r.Context(), `SELECT i.id,i.title,e.key,i.application_id,i.status FROM ops_incidents i
JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
JOIN applications a ON a.id=i.application_id AND NOT a.archived
WHERE i.id=$1 AND e.key=ANY($2::text[])`, id, envs).Scan(&v.ID, &v.Title, &v.EnvironmentID, &v.ApplicationID, &v.State)
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
	}
	return s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: module, Action: "view", EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	})
}

var errTooBroad = errors.New("discovery candidates exceed bounded query")

func listError(w http.ResponseWriter, err error) {
	if errors.Is(err, errTooBroad) {
		httpapi.Error(w, 413, "scope_too_large", "Narrow the search query")
		return
	}
	httpapi.Error(w, 503, "discovery_unavailable", "Discovery records unavailable")
}

func href(v record) string {
	if v.Kind == "incident" {
		return "/incidents/" + url.PathEscape(v.ID)
	}
	return "/conversations/" + url.PathEscape(v.ID)
}

func label(r *http.Request, kind string) string {
	en := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	if kind == "incident" {
		if en {
			return "Incident"
		}
		return "事件"
	}
	if en {
		return "Conversation"
	}
	return "对话"
}

func tone(v record) string {
	if v.Kind == "incident" {
		if v.State == "open" || v.State == "firing" {
			return "warn"
		}
		if v.State == "resolved" || v.State == "closed" {
			return "ok"
		}
	}
	return ""
}
