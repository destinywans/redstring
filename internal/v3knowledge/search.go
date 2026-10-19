package v3knowledge

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type searchInput struct {
	Q       string `json:"q"`
	Service string `json:"service"`
	TopK    int    `json:"top_k"`
}

// RetrievalHit.Vector is null when the hit has no vector score: the search
// read no vectors (degraded), the hit's corpus has no vector index (public
// Web and upload sources), or a document passage was found by keyword only and
// never entered the vector candidates (PA-222). Corpus names the corpora entry
// it came from.
type RetrievalHit struct {
	Title   string   `json:"title"`
	Snippet string   `json:"snippet"`
	Keyword float64  `json:"keyword"`
	Vector  *float64 `json:"vector"`
	Score   float64  `json:"score"`
	Corpus  string   `json:"corpus"`
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
	// Keep the whole search, including source reads and final authorization
	// checks, within one request budget even across many visible applications.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
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
		httpapi.WriteJSON(w, 200, map[string]any{"hits": []RetrievalHit{}, "mode": "none", "degraded": false,
			"corpora": searchCorpora(false, false, "no_authorized_scope")})
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
	userID := httpapi.CurrentUser(r).ID
	type scopedHits struct {
		scope           appEnvironment
		keyword, vector []knowledge.Hit
		publicWeb       []publicWebHit
		uploads         []publicWebHit
	}
	observed := make([]scopedHits, 0, len(apps))
	remainingWeb := publicWebCandidateLimit
	for _, scope := range apps {
		hits, e := pgRepo.V3KeywordSearch(r.Context(), userID, scope.app, scope.env, in.Q, in.TopK*4)
		if e != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Keyword search failed")
			return
		}
		found := scopedHits{scope: scope, keyword: hits}
		var matchedWeb int
		found.publicWeb, matchedWeb, e = s.publicWebKeywordSearch(r.Context(), userID, scope.app, scope.env, in.Q, in.TopK*4, remainingWeb)
		if e != nil {
			if errors.Is(e, errPublicWebScopeTooLarge) {
				httpapi.Error(w, 413, "public_web_source_scope_too_large", "More than 80 matching public Web sources")
			} else {
				httpapi.Error(w, 503, "knowledge_unavailable", "Public Web source search failed")
			}
			return
		}
		remainingWeb -= matchedWeb
		if found.uploads, e = s.uploadSourceKeywordSearch(r.Context(), userID, scope.app, scope.env, in.Q, in.TopK*4); e != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Upload source search failed")
			return
		}
		if len(embed) > 0 {
			if scoped, ok := s.Knowledge.Vectors.(interface {
				V3SearchScoped(context.Context, string, string, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error)
			}); ok {
				results, e := scoped.V3SearchScoped(r.Context(), userID, scope.app, scope.env, desc, embed, in.TopK*4)
				if e != nil {
					degraded = true
				} else {
					found.vector = results
				}
			} else {
				degraded = true
			}
		}
		observed = append(observed, found)
	}
	// Recheck current document ownership and grants after all retrieval reads.
	// A revocation or application/team remap between candidate selection and
	// retrieval must not leave its already-read passage in the response.
	for _, found := range observed {
		ids := make([]string, 0, len(found.keyword)+len(found.vector))
		seen := map[string]bool{}
		for _, h := range append(found.keyword, found.vector...) {
			if h.DocumentID != "" && !seen[h.DocumentID] {
				seen[h.DocumentID] = true
				ids = append(ids, h.DocumentID)
			}
		}
		if len(ids) > 0 {
			visible, e := pgRepo.V3VisibleDocumentIDs(r.Context(), userID, found.scope.app, found.scope.env, ids)
			if e != nil {
				httpapi.Error(w, 503, "knowledge_unavailable", "Search scope changed")
				return
			}
			for _, h := range found.keyword {
				if version, ok := visible[h.DocumentID]; ok && version == h.Version {
					keyword = append(keyword, h)
				}
			}
			for _, h := range found.vector {
				if version, ok := visible[h.DocumentID]; ok && version == h.Version {
					vector = append(vector, h)
				}
			}
		}
		webVisible, e := s.visiblePublicWebHits(r.Context(), userID, found.scope.app, found.scope.env, found.publicWeb)
		if e != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Public Web source scope changed")
			return
		}
		keyword = append(keyword, webVisible...)
		uploadVisible, e := s.visibleUploadSourceHits(r.Context(), userID, found.scope.app, found.scope.env, found.uploads)
		if e != nil {
			httpapi.Error(w, 503, "knowledge_unavailable", "Upload source scope changed")
			return
		}
		keyword = append(keyword, uploadVisible...)
	}
	if degraded {
		vector = nil
	}
	result := rankRetrieval(keyword, vector, in.TopK, degraded)
	mode := "hybrid"
	if degraded {
		mode = "keyword"
	}
	httpapi.WriteJSON(w, 200, map[string]any{"hits": result, "mode": mode, "degraded": degraded,
		"corpora": searchCorpora(true, !degraded, "")})
}

// searchCorpus says what one search read (PA-66). keyword and vector are
// searched, unavailable (tried and failed), not_indexed (no such index
// exists for this corpus) or not_searched (this search does not read it).
type searchCorpus struct {
	Corpus  string `json:"corpus"`
	Keyword string `json:"keyword"`
	Vector  string `json:"vector"`
	Reason  string `json:"reason,omitempty"`
}

func searchCorpora(searched, vectorSearched bool, reason string) []searchCorpus {
	keyword, documentVector := "searched", "searched"
	if !searched {
		keyword, documentVector = "not_searched", "not_searched"
	} else if !vectorSearched {
		documentVector = "unavailable"
	}
	documents := searchCorpus{Corpus: "documents", Keyword: keyword, Vector: documentVector, Reason: reason}
	if searched && !vectorSearched {
		documents.Reason = "embedding_unavailable"
	}
	return []searchCorpus{
		documents,
		{Corpus: "public_web_sources", Keyword: keyword, Vector: "not_indexed", Reason: reason},
		{Corpus: "upload_sources", Keyword: keyword, Vector: "not_indexed", Reason: reason},
		// Feishu and other bound external readers need the server's binding
		// check per read; their synced documents are evidence only for now.
		{Corpus: "external_sources", Keyword: "not_searched", Vector: "not_indexed", Reason: "binding_check_not_in_search"},
		{Corpus: "git_markdown_sources", Keyword: "not_searched", Vector: "not_indexed", Reason: "not_connected_to_search"},
	}
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
 JOIN application_environments e ON e.application_id=a.id AND e.key=requested.environment_id AND NOT e.archived
 WHERE EXISTS (
  SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='environment' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=requested.environment_id))
 ) AND EXISTS (
  SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='knowledge' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=requested.environment_id)
   OR (g.scope_kind='application' AND g.scope_id=a.id))
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
	// inVector says the passage was among the vector candidates; one found
	// by keyword only has no vector similarity, which is not 0 (PA-222).
	inVector bool
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
		byKey[k].inVector = true
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
		corpus := hitCorpus(s.hit)
		var vector *float64
		if keywordOnly {
			score = kw
		} else if corpus == "documents" && s.inVector {
			vector = &vec
		}
		out = append(out, RetrievalHit{Title: s.hit.Title, Snippet: snippet(s.hit.Content, 400), Keyword: kw, Vector: vector, Score: score, Corpus: corpus})
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

// hitCorpus names the searchCorpora entry a hit came from. Public Web and
// upload passages carry their own ID prefix and have no vector index.
func hitCorpus(h knowledge.Hit) string {
	switch {
	case strings.HasPrefix(h.DocumentID, publicWebHitPrefix):
		return "public_web_sources"
	case strings.HasPrefix(h.DocumentID, uploadSourceHitPrefix):
		return "upload_sources"
	default:
		return "documents"
	}
}

func snippet(content string, limit int) string {
	r := []rune(strings.TrimSpace(content))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return string(r)
}
