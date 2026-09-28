package v3knowledge

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type searchInput struct {
	Q       string `json:"q"`
	Service string `json:"service"`
	TopK    int    `json:"top_k"`
}

type RetrievalHit struct {
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Keyword float64 `json:"keyword"`
	Vector  float64 `json:"vector"`
	Score   float64 `json:"score"`
}

// SearchHandler is registered separately because the pre-V3 knowledge service
// already owns POST /api/v1/knowledge/search. The caller must skip that old
// route when wiring V3; duplicate ServeMux registrations panic at startup.
func (s *Service) SearchHandler(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	var in searchInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	in.Q = strings.TrimSpace(in.Q)
	if in.Q == "" || utf8.RuneCountInString(in.Q) > 500 || len(in.Service) > 200 || in.TopK < 0 || in.TopK > 20 {
		httpapi.Error(w, 422, "invalid_query", "Search query or top_k is invalid")
		return
	}
	if in.TopK == 0 {
		in.TopK = 5
	}
	apps, err := s.searchableApplications(r, in.Service)
	if err != nil {
		if errors.Is(err, errScopeTooLarge) {
			httpapi.Error(w, 413, "scope_too_large", "Narrow the search to one service")
			return
		}
		httpapi.Error(w, 503, "knowledge_unavailable", "Search scope unavailable")
		return
	}
	if in.Service != "" && len(apps) == 0 {
		httpapi.Error(w, 404, "not_found", "Service is not accessible")
		return
	}
	if len(apps) == 0 {
		httpapi.WriteJSON(w, 200, map[string]any{"hits": []RetrievalHit{}, "mode": "none", "degraded": false})
		return
	}
	var keyword, vector []knowledge.Hit
	var embed []float32
	var desc inference.EmbeddingDescriptor
	degraded := false
	if s.Knowledge.Embedding != nil && s.Knowledge.Vectors != nil {
		desc = s.Knowledge.Embedding.Descriptor()
		vs, e := s.Knowledge.Embedding.Embed(r.Context(), []string{in.Q})
		if e == nil && len(vs) == 1 {
			embed = vs[0]
		} else {
			degraded = true
		}
	} else {
		degraded = true
	}
	pgRepo, ok := s.Knowledge.Repository.(*knowledge.PGRepository)
	if !ok {
		httpapi.Error(w, 503, "knowledge_unavailable", "Keyword index unavailable")
		return
	}
	for _, scope := range apps {
		hits, e := pgRepo.KeywordSearch(r.Context(), scope.app, scope.env, in.Q, in.TopK*4)
		if e != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Keyword search failed")
			return
		}
		keyword = append(keyword, hits...)
		if len(embed) > 0 {
			if scoped, ok := s.Knowledge.Vectors.(interface {
				SearchScoped(context.Context, string, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error)
			}); ok {
				results, e := scoped.SearchScoped(r.Context(), scope.app, scope.env, desc, embed, in.TopK*4)
				if e != nil {
					degraded = true
				} else {
					vector = append(vector, results...)
				}
			} else {
				degraded = true
			}
		}
	}
	if degraded {
		vector = nil
	}
	result := rankRetrieval(keyword, vector, in.TopK, degraded)
	mode := "hybrid"
	if degraded {
		mode = "keyword"
	}
	httpapi.WriteJSON(w, 200, map[string]any{"hits": result, "mode": mode, "degraded": degraded})
}

// RegisterSearch must be called only after omitting the old knowledge route.
func (s *Service) RegisterSearch(api *httpapi.Server) {
	const pattern = "POST /api/v1/knowledge/search"
	api.Register(pattern, "viewer", s.SearchHandler)
	api.MarkV3ObjectRoute(pattern, "knowledge", "view")
}

var errScopeTooLarge = errors.New("search scope too large")

type appEnvironment struct{ app, env string }

func (s *Service) searchableApplications(r *http.Request, service string) ([]appEnvironment, error) {
	envs := scopeIDs(r)
	user := httpapi.CurrentUser(r)
	if len(envs) == 0 || user.ID == "" || user.Disabled {
		return []appEnvironment{}, nil
	}
	// Apply the same environment, module and application mapping rules as
	// V3Policy before limiting the collection. Hidden applications must neither
	// consume the 200-app budget nor reveal that a larger collection exists.
	rows, err := s.DB.Query(r.Context(), `SELECT a.id FROM applications a
WHERE NOT a.archived AND ($3='' OR a.id=$3 OR a.key=$3 OR a.name=$3)
AND EXISTS (
 SELECT 1 FROM unnest($2::text[]) requested(environment_id)
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=requested.environment_id
 WHERE EXISTS (
  SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='environment' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=requested.environment_id))
 ) AND EXISTS (
  SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='knowledge' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=requested.environment_id)
   OR (g.scope_kind='application' AND g.scope_id=a.id)
   OR (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id))
 )
)
ORDER BY a.id LIMIT 201`, user.ID, envs, service)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 200 {
		return nil, errScopeTooLarge
	}
	all := ids
	if service == "" {
		all = append([]string{""}, ids...)
	}
	out := []appEnvironment{}
	for _, app := range all {
		for _, env := range envs {
			authorizationErr := s.Policy.Authorize(r.Context(), user, rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: env, ApplicationID: app})
			if errors.Is(authorizationErr, store.ErrForbidden) && app != "" {
				// V3Decision intentionally accepts either an application or a
				// team, not both. Re-read the current owner and mapping before
				// checking the team's grant against the persisted target.
				var team string
				lookupErr := s.DB.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,'')
FROM applications a JOIN v3_application_environments ae ON ae.application_id=a.id
WHERE a.id=$1 AND NOT a.archived AND ae.environment_id=$2`, app, env).Scan(&team)
				if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
					return nil, lookupErr
				}
				if lookupErr == nil && team != "" {
					authorizationErr = s.Policy.Authorize(r.Context(), user, rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: env, TeamID: team})
				}
			}
			if authorizationErr == nil {
				out = append(out, appEnvironment{app, env})
			} else if !errors.Is(authorizationErr, store.ErrForbidden) {
				return nil, authorizationErr
			}
		}
	}
	return out, nil
}

type scoredHit struct {
	hit             knowledge.Hit
	keyword, vector float64
}

func rankRetrieval(keyword, vector []knowledge.Hit, limit int, keywordOnly bool) []RetrievalHit {
	byKey := map[string]*scoredHit{}
	maxKeyword := 0.0
	key := func(h knowledge.Hit) string {
		return h.DocumentID + ":" + strconv.Itoa(h.Version) + ":" + strconv.Itoa(h.Chunk)
	}
	for _, h := range keyword {
		if h.Similarity > maxKeyword {
			maxKeyword = h.Similarity
		}
		k := key(h)
		if byKey[k] == nil {
			byKey[k] = &scoredHit{hit: h}
		}
		if h.Similarity > byKey[k].keyword {
			byKey[k].keyword = h.Similarity
		}
	}
	for _, h := range vector {
		k := key(h)
		if byKey[k] == nil {
			byKey[k] = &scoredHit{hit: h}
		}
		if h.Similarity > byKey[k].vector {
			byKey[k].vector = h.Similarity
		}
	}
	out := make([]RetrievalHit, 0, len(byKey))
	for _, s := range byKey {
		kw := 0.0
		if maxKeyword > 0 {
			kw = s.keyword / maxKeyword
		}
		vec := math.Max(0, math.Min(1, s.vector))
		score := 0.4*kw + 0.6*vec
		if keywordOnly {
			score = kw
		}
		out = append(out, RetrievalHit{Title: s.hit.Title, Snippet: snippet(s.hit.Content, 400), Keyword: kw, Vector: vec, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Title < out[j].Title
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func snippet(content string, limit int) string {
	r := []rune(strings.TrimSpace(content))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return string(r)
}
