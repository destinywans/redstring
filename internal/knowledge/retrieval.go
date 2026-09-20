package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/wans/aiops-canvas/internal/inference"
)

type SearchResult struct {
	Items     []Hit                          `json:"items"`
	Mode      string                         `json:"mode"`
	Degraded  bool                           `json:"degraded"`
	Reason    string                         `json:"reason,omitempty"`
	Embedding *inference.EmbeddingDescriptor `json:"embedding,omitempty"`
}

func (s *Service) Search(ctx context.Context, app, query string, limit int) ([]Hit, error) {
	r, err := s.SearchScoped(ctx, app, "", query, limit)
	return r.Items, err
}
func (s *Service) SearchScoped(ctx context.Context, app, environment, query string, limit int) (SearchResult, error) {
	out := SearchResult{Items: []Hit{}, Mode: "hybrid"}
	if app == "" || strings.TrimSpace(query) == "" || len(query) > 8000 {
		return out, fmt.Errorf("%w: application_id and query up to 8000 bytes required", ErrInvalid)
	}
	if err := s.authorize(ctx, app, "view"); err != nil {
		return out, err
	}
	if s.ValidateEnvironment != nil && environment != "" {
		if err := s.ValidateEnvironment(ctx, app, environment); err != nil {
			return out, err
		}
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	r, governed := s.Repository.(GovernanceRepository)
	if !governed {
		hits, err := s.vectorSearch(ctx, app, query, limit)
		out.Items = hits
		return out, err
	}
	keyword, err := r.KeywordSearch(ctx, app, environment, query, limit*4)
	if err != nil {
		return out, err
	}
	var vectors []Hit
	if s.Embedding == nil || s.Vectors == nil {
		out.Degraded = true
	} else {
		desc := s.Embedding.Descriptor()
		out.Embedding = &desc
		vs, embedErr := s.Embedding.Embed(ctx, []string{query})
		if embedErr != nil || len(vs) != 1 {
			out.Degraded = true
		} else {
			if scoped, ok := s.Vectors.(interface {
				SearchScoped(context.Context, string, string, inference.EmbeddingDescriptor, []float32, int) ([]Hit, error)
			}); ok {
				vectors, err = scoped.SearchScoped(ctx, app, environment, desc, vs[0], limit*4)
			} else {
				vectors, err = s.Vectors.Search(ctx, app, desc, vs[0], limit*4)
			}
			if err != nil {
				out.Degraded = true
				vectors = nil
			}
		}
	}
	if out.Degraded {
		out.Mode = "keyword"
		out.Reason = "Vector retrieval unavailable; keyword results remain available."
	}
	out.Items = fuseHits(keyword, vectors, limit)
	return out, nil
}
func queryTerms(query string) []string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	terms := []string{normalized}
	seen := map[string]bool{normalized: true}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	for _, word := range strings.FieldsFunc(normalized, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;:()[]{}，。；：", r) }) {
		add(word)
		rs := []rune(word)
		for i := 0; i+1 < len(rs); i++ {
			if unicode.Is(unicode.Han, rs[i]) && unicode.Is(unicode.Han, rs[i+1]) {
				add(string(rs[i : i+2]))
			}
		}
	}
	if len(terms) > 128 {
		terms = terms[:128]
	}
	return terms
}
func keywordScore(query string, terms []string, title, tags, body string) float64 {
	title = strings.ToLower(title)
	tags = strings.ToLower(tags)
	body = strings.ToLower(body)
	score := 0.0
	for _, term := range terms {
		if strings.Contains(title, term) {
			score += 6
		}
		if strings.Contains(tags, term) {
			score += 4
		}
		if strings.Contains(body, term) {
			score++
		}
	}
	if strings.Contains(body, strings.ToLower(strings.TrimSpace(query))) {
		score += 8
	}
	return score
}
func sortHits(h []Hit) {
	sort.SliceStable(h, func(i, j int) bool {
		if h[i].Similarity != h[j].Similarity {
			return h[i].Similarity > h[j].Similarity
		}
		if h[i].DocumentID != h[j].DocumentID {
			return h[i].DocumentID < h[j].DocumentID
		}
		return h[i].Chunk < h[j].Chunk
	})
}
func fuseHits(keyword, vectors []Hit, limit int) []Hit {
	hits := map[string]Hit{}
	scores := map[string]float64{}
	for _, list := range [][]Hit{keyword, vectors} {
		for i, h := range list {
			key := fmt.Sprintf("%s:%d:%d", h.DocumentID, h.Version, h.Chunk)
			if current, ok := hits[key]; !ok || (current.GenerationID == 0 && h.GenerationID != 0) {
				hits[key] = h
			}
			scores[key] += 1 / float64(60+i+1)
		}
	}
	out := make([]Hit, 0, len(hits))
	for key, h := range hits {
		h.Similarity = scores[key]
		out = append(out, h)
	}
	sortHits(out)
	// Keep output bounded, with a total character budget as well as a passage limit.
	result := []Hit{}
	budget := 12000
	for _, h := range out {
		n := len([]rune(h.Content))
		if n > budget {
			continue
		}
		budget -= n
		result = append(result, h)
		if len(result) >= limit {
			break
		}
	}
	return result
}
