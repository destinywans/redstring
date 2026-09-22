package resources

import (
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func values(q url.Values, key string, aliases ...string) []string {
	v, ok := q[key]
	if !ok {
		for _, a := range aliases {
			if x := q.Get(a); x != "" {
				v = []string{x}
				break
			}
		}
	}
	out := []string{}
	for _, x := range v {
		for _, y := range strings.Split(x, ",") {
			if y = strings.TrimSpace(y); y != "" {
				out = append(out, y)
			}
		}
	}
	return out
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func accepts(xs []string, x string) bool { return len(xs) == 0 || contains(xs, x) }

// Runtime records remain available in explicit instance/event views and in
// their owning resource details, but are not entries in the CMDB catalog.
func inventoryRows(rows []Resource, requestedKinds []string) []Resource {
	out := make([]Resource, 0, len(rows))
	for _, row := range rows {
		if (row.Kind == "pod" || row.Kind == "event") && !contains(requestedKinds, row.Kind) {
			continue
		}
		out = append(out, row)
	}
	return out
}
func appMatches(r Resource, ids []string) bool {
	if len(ids) == 0 {
		return true
	}
	for _, a := range r.Applications {
		if contains(ids, a.ID) {
			return true
		}
	}
	return false
}
func matches(r Resource, q url.Values, kindKey string) bool {
	// Existing mock rows remain explicitly selectable, but never mix into live defaults.
	if r.IsMock && !contains(values(q, "source_kind"), "mock") {
		return false
	}
	if !accepts(values(q, kindKey), r.Kind) || !accepts(values(q, "source_kind"), r.SourceKind) || !accepts(values(q, "owner_id"), r.OwnerUserID) || !accepts(values(q, "team_id"), r.OwnerTeamID) || !appMatches(r, values(q, "applications", "application_id", "application")) {
		return false
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if search == "" {
		return true
	}
	text := r.Name + " " + r.ExternalID + " " + r.Description + " " + strings.Join(r.Tags, " ")
	if ip, ok := r.Attributes["ip"].(string); ok {
		text += " " + ip
	}
	return searchtext.Match(text, search)
}
func facets(rows []Resource) Facets {
	groups := make([]map[string]Option, 5)
	for i := range groups {
		groups[i] = map[string]Option{}
	}
	add := func(n int, value, label string) {
		if value == "" {
			return
		}
		v := groups[n][value]
		v.Value = value
		v.Label = label
		v.Count++
		groups[n][value] = v
	}
	for _, r := range rows {
		label := kinds[r.Kind]
		if label == "" {
			label = r.Kind
		}
		add(0, r.Kind, label)
		seen := map[string]bool{}
		for _, a := range r.Applications {
			if !seen[a.ID] {
				add(1, a.ID, a.Key)
				seen[a.ID] = true
			}
		}
		source := r.SourceKind
		if source == "mock" {
			source = "示例导入"
		} else if source == "manual" {
			source = "人工登记"
		}
		add(2, r.SourceKind, source)
		add(3, r.OwnerUserID, r.OwnerName)
		add(4, r.OwnerTeamID, r.TeamName)
	}
	convert := func(i int) []Option {
		out := []Option{}
		for _, v := range groups[i] {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
		return out
	}
	return Facets{Kinds: convert(0), Applications: convert(1), Sources: convert(2), Owners: convert(3), Teams: convert(4)}
}
func integer(q url.Values, key string, fallback, min, max int) (int, bool) {
	if q.Get(key) == "" {
		return fallback, true
	}
	n, e := strconv.Atoi(q.Get(key))
	return n, e == nil && n >= min && n <= max
}
func (s *Service) Register(api *httpapi.Server) {
	s.registerMutations(api)
	load := func(w http.ResponseWriter, r *http.Request) ([]Resource, bool) {
		u := httpapi.CurrentUser(r)
		ids, e := api.VisibleApplicationIDs(r.Context(), u)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return nil, false
		}
		rows, e := s.visible(r.Context(), ids, u.Role == "admin", r.URL.Query().Get("environment_scope"))
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return nil, false
		}
		return rows, true
	}
	api.Register("GET /api/v1/resources", "viewer", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, ok := integer(q, "page", 1, 1, 1000000)
		size, ok2 := integer(q, "page_size", 10, 1, 100)
		if !ok || !ok2 {
			httpapi.AuthorizationError(w, store.ErrInvalidReference)
			return
		}
		rows, ok := load(w, r)
		if !ok {
			return
		}
		rows = inventoryRows(rows, values(q, "kind"))
		f := facets(rows)
		filtered := []Resource{}
		for _, v := range rows {
			if matches(v, q, "kind") {
				filtered = append(filtered, v)
			}
		}
		sort.SliceStable(filtered, func(i, j int) bool {
			a, b := filtered[i], filtered[j]
			switch q.Get("sort") {
			case "updated_desc":
				if !a.UpdatedAt.Equal(b.UpdatedAt) {
					return a.UpdatedAt.After(b.UpdatedAt)
				}
			case "kind":
				if a.Kind != b.Kind {
					return a.Kind < b.Kind
				}
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.ID < b.ID
		})
		total := len(filtered)
		last := (total + size - 1) / size
		if last < 1 {
			last = 1
		}
		if page > last {
			page = last
		}
		start := (page - 1) * size
		end := start + size
		if end > total {
			end = total
		}
		httpapi.WriteJSON(w, 200, Page{Items: filtered[start:end], Total: total, Page: page, PageSize: size, Facets: f})
	})
	api.Register("GET /api/v1/resources/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		rows, ok := load(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		var selected *Resource
		for i := range rows {
			if rows[i].ID == id {
				selected = &rows[i]
				break
			}
		}
		if selected == nil {
			httpapi.AuthorizationError(w, store.ErrNotFound)
			return
		}
		edges, e := s.edges(r.Context(), rows)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		out := Detail{Resource: *selected, RelatedResources: []Resource{}, Relations: []Relation{}, Changes: []Change{}}
		related := map[string]bool{}
		for _, edge := range edges {
			if edge.SourceResourceID == id || edge.TargetResourceID == id {
				out.Relations = append(out.Relations, edge)
				related[edge.SourceResourceID] = true
				related[edge.TargetResourceID] = true
			}
		}
		for _, v := range rows {
			if v.ID != id && related[v.ID] {
				out.RelatedResources = append(out.RelatedResources, v)
			}
		}
		out.Changes, e = s.changes(r.Context(), id)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		httpapi.WriteJSON(w, 200, out)
	})
	api.Register("GET /api/v1/resource-relations", "viewer", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("depth") == "" && q.Get("hops") != "" {
			q.Set("depth", q.Get("hops"))
		}
		depth, ok := integer(q, "depth", 2, 1, 3)
		if !ok {
			httpapi.AuthorizationError(w, store.ErrInvalidReference)
			return
		}
		rows, ok := load(w, r)
		if !ok {
			return
		}
		rows = inventoryRows(rows, nil)
		selectedRows := make([]Resource, 0, len(rows))
		for _, row := range rows {
			if !row.IsMock || contains(values(q, "source_kind"), "mock") {
				selectedRows = append(selectedRows, row)
			}
		}
		rows = selectedRows
		edges, e := s.edges(r.Context(), rows)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		if id := q.Get("root_id"); id != "" {
			found := false
			for _, v := range rows {
				if v.ID == id {
					found = true
				}
			}
			if !found {
				httpapi.AuthorizationError(w, store.ErrNotFound)
				return
			}
		}
		httpapi.WriteJSON(w, 200, buildGraph(rows, edges, q, depth))
	})
	api.Register("POST /api/v1/resources", "admin", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if !httpapi.Decode(w, r, &in) {
			return
		}
		id, e := s.create(r.Context(), in)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		u := httpapi.CurrentUser(r)
		ids, e := api.VisibleApplicationIDs(r.Context(), u)
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		rows, e := s.visible(r.Context(), ids, true, "")
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		for _, v := range rows {
			if v.ID == id {
				httpapi.WriteJSON(w, 201, v)
				return
			}
		}
		httpapi.AuthorizationError(w, store.ErrNotFound)
	})
}

func buildGraph(rows []Resource, edges []Relation, q url.Values, depth int) Graph {
	out := Graph{Nodes: []Resource{}, Edges: []Relation{}}
	selected := map[string]bool{}
	root := q.Get("root_id")
	for _, v := range rows {
		if root != "" {
			if v.ID == root {
				selected[v.ID] = true
			}
		} else if matches(v, q, "resource_kind") {
			selected[v.ID] = true
		}
	}
	allowed := []Relation{}
	for _, e := range edges {
		if accepts(values(q, "kind"), e.Kind) {
			allowed = append(allowed, e)
		}
	}
	for i := 0; i < depth; i++ {
		next := map[string]bool{}
		for _, e := range allowed {
			if selected[e.SourceResourceID] || selected[e.TargetResourceID] {
				next[e.SourceResourceID] = true
				next[e.TargetResourceID] = true
			}
		}
		for id := range next {
			selected[id] = true
		}
	}
	retained := map[string]bool{}
	for _, v := range rows {
		if selected[v.ID] {
			if len(out.Nodes) >= 300 {
				out.Truncated = true
				continue
			}
			out.Nodes = append(out.Nodes, v)
			retained[v.ID] = true
		}
	}
	for _, e := range allowed {
		if retained[e.SourceResourceID] && retained[e.TargetResourceID] {
			if len(out.Edges) >= 600 {
				out.Truncated = true
				continue
			}
			out.Edges = append(out.Edges, e)
		}
	}
	return out
}
