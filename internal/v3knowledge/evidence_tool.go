package v3knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
)

// KnowledgeEvidenceToolOptions are bound by the server from a persisted turn,
// never from model-supplied tool input. CurrentUser must read the current
// account state on every invocation (for example, Store.ActiveUser).
type KnowledgeEvidenceToolOptions struct {
	Enabled       bool
	Service       *Service
	ActorID       string
	ApplicationID string
	EnvironmentID string
	CurrentUser   func(context.Context, string) (core.User, error)
}

type indexedKnowledgePassage struct {
	DocumentID        string `json:"document_id"`
	Version           int    `json:"version"`
	Chunk             int    `json:"chunk"`
	Title             string `json:"title"`
	Snippet           string `json:"snippet"`
	Reference         string `json:"reference"`
	IndexGenerationID int64  `json:"index_generation_id,omitempty"`
}

type indexedKnowledgeEvidence struct {
	Status        string                    `json:"status"`
	ApplicationID string                    `json:"application_id"`
	EnvironmentID string                    `json:"environment_id"`
	Mode          string                    `json:"mode"`
	Degraded      bool                      `json:"degraded"`
	Passages      []indexedKnowledgePassage `json:"passages"`
	Boundary      string                    `json:"boundary"`
}

var errKnowledgeEvidenceUnavailable = errors.New("indexed knowledge unavailable")

// NewKnowledgeEvidenceTool is opt-in. It never calls the legacy unscoped
// knowledge search methods, and is deliberately not registered by cmd/aiops.
func NewKnowledgeEvidenceTool(opts KnowledgeEvidenceToolOptions) (inference.EvidenceTool, bool) {
	if !opts.Enabled || opts.Service == nil || opts.Service.Knowledge == nil || opts.Service.Policy == nil ||
		opts.CurrentUser == nil || opts.ActorID == "" || opts.ApplicationID == "" ||
		opts.EnvironmentID == "" || opts.EnvironmentID == "all" || opts.EnvironmentID == "*" ||
		opts.ApplicationID == "*" || opts.ApplicationID == "all" ||
		strings.TrimSpace(opts.ActorID) != opts.ActorID ||
		strings.TrimSpace(opts.ApplicationID) != opts.ApplicationID ||
		strings.TrimSpace(opts.EnvironmentID) != opts.EnvironmentID {
		return inference.EvidenceTool{}, false
	}
	if repo, ok := opts.Service.Knowledge.Repository.(*knowledge.PGRepository); !ok || repo == nil || repo.DB == nil {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name:        "search_knowledge",
		Description: "Search published knowledge passages for this conversation's registered application and exact environment. Pass only a short search phrase. Keyword matches read stored document text; vector matches carry a ready index generation. Results are references, not live observations or proof that no other documents exist.",
		Read: func(ctx context.Context, query string) (inference.Evidence, error) {
			return readKnowledgeEvidence(ctx, opts, query)
		},
	}, true
}

func knowledgeEvidenceUser(ctx context.Context, opts KnowledgeEvidenceToolOptions) (core.User, error) {
	user, err := opts.CurrentUser(ctx, opts.ActorID)
	if err != nil || user.ID != opts.ActorID || user.Disabled {
		return core.User{}, errKnowledgeEvidenceUnavailable
	}
	decision := rbac.V3Decision{Module: "knowledge", Action: "view", ApplicationID: opts.ApplicationID, EnvironmentID: opts.EnvironmentID}
	if opts.Service.Policy.Authorize(ctx, user, decision) != nil {
		return core.User{}, errKnowledgeEvidenceUnavailable
	}
	return user, nil
}

func readKnowledgeEvidence(ctx context.Context, opts KnowledgeEvidenceToolOptions, query string) (inference.Evidence, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 1000 || utf8.RuneCountInString(query) > 500 || !utf8.ValidString(query) {
		return inference.Evidence{}, errKnowledgeEvidenceUnavailable
	}
	if _, err := knowledgeEvidenceUser(ctx, opts); err != nil {
		return inference.Evidence{}, err
	}
	repo := opts.Service.Knowledge.Repository.(*knowledge.PGRepository)
	keyword, err := repo.V3KeywordSearch(ctx, opts.ActorID, opts.ApplicationID, opts.EnvironmentID, query, 20)
	if err != nil {
		return inference.Evidence{}, errKnowledgeEvidenceUnavailable
	}
	var vector []knowledge.Hit
	degraded := opts.Service.Knowledge.Embedding == nil || opts.Service.Knowledge.Vectors == nil
	if !degraded {
		scoped, ok := opts.Service.Knowledge.Vectors.(interface {
			V3SearchScoped(context.Context, string, string, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error)
		})
		if !ok {
			degraded = true
		} else {
			vectors, embedErr := opts.Service.Knowledge.Embedding.Embed(ctx, []string{query})
			if embedErr != nil || len(vectors) != 1 {
				degraded = true
			} else {
				vector, err = scoped.V3SearchScoped(ctx, opts.ActorID, opts.ApplicationID, opts.EnvironmentID,
					opts.Service.Knowledge.Embedding.Descriptor(), vectors[0], 20)
				if err != nil {
					degraded = true
					vector = nil
				}
			}
		}
	}
	// A revocation may occur while the embedding provider or database reads run.
	if _, err := knowledgeEvidenceUser(ctx, opts); err != nil {
		return inference.Evidence{}, err
	}
	ids := make([]string, 0, len(keyword)+len(vector))
	seen := make(map[string]bool)
	for _, hit := range append(keyword, vector...) {
		if hit.DocumentID != "" && !seen[hit.DocumentID] {
			seen[hit.DocumentID] = true
			ids = append(ids, hit.DocumentID)
		}
	}
	// The shared SQL predicate checks current document version, team ownership,
	// application mapping, environment and current effective grants.
	visible, err := repo.V3VisibleDocumentIDs(ctx, opts.ActorID, opts.ApplicationID, opts.EnvironmentID, ids)
	if err != nil {
		return inference.Evidence{}, errKnowledgeEvidenceUnavailable
	}
	filter := func(hits []knowledge.Hit) []knowledge.Hit {
		out := make([]knowledge.Hit, 0, len(hits))
		for _, hit := range hits {
			if version, ok := visible[hit.DocumentID]; ok && version == hit.Version &&
				hit.ApplicationID == opts.ApplicationID && hit.Chunk >= 0 {
				out = append(out, hit)
			}
		}
		return out
	}
	if degraded {
		vector = nil
	}
	hits := rankKnowledgeEvidenceHits(filter(keyword), filter(vector), 5)
	mode := "hybrid"
	if degraded {
		mode = "keyword"
	}
	result := indexedKnowledgeEvidence{
		Status: "published_document_passages", ApplicationID: opts.ApplicationID,
		EnvironmentID: opts.EnvironmentID, Mode: mode, Degraded: degraded,
		Passages: []indexedKnowledgePassage{},
		Boundary: "Stored, published, untrusted document excerpts only. Keyword matches may have no vector index; vector matches carry an index generation. These are not live service state, and an empty result does not establish absence of an issue.",
	}
	if len(hits) == 0 {
		result.Status = "no_published_match"
	}
	for _, hit := range hits {
		result.Passages = append(result.Passages, indexedKnowledgePassage{
			DocumentID: hit.DocumentID, Version: hit.Version, Chunk: hit.Chunk,
			Title: snippet(hit.Title, 120), Snippet: snippet(hit.Content, 240),
			Reference:         "knowledge://documents/" + hit.DocumentID + "/versions/" + strconv.Itoa(hit.Version) + "#chunk=" + strconv.Itoa(hit.Chunk),
			IndexGenerationID: hit.GenerationID,
		})
	}
	content, err := json.Marshal(result)
	if err != nil || len(content) > 8<<10 {
		return inference.Evidence{}, errKnowledgeEvidenceUnavailable
	}
	return inference.Evidence{Kind: "knowledge", Title: "Published knowledge references", Content: string(content),
		Source: "knowledge://published-documents", TargetID: opts.ApplicationID}, nil
}

func rankKnowledgeEvidenceHits(keyword, vector []knowledge.Hit, limit int) []knowledge.Hit {
	type scored struct {
		hit   knowledge.Hit
		score float64
	}
	byKey := make(map[string]*scored)
	for _, list := range [][]knowledge.Hit{keyword, vector} {
		for rank, hit := range list {
			key := hit.DocumentID + "\x00" + strconv.Itoa(hit.Version) + "\x00" + strconv.Itoa(hit.Chunk)
			item := byKey[key]
			if item == nil {
				item = &scored{hit: hit}
				byKey[key] = item
			} else if item.hit.GenerationID == 0 && hit.GenerationID != 0 {
				item.hit = hit
			}
			item.score += 1 / float64(60+rank+1)
		}
	}
	items := make([]*scored, 0, len(byKey))
	for _, item := range byKey {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		if items[i].hit.DocumentID != items[j].hit.DocumentID {
			return items[i].hit.DocumentID < items[j].hit.DocumentID
		}
		return items[i].hit.Chunk < items[j].hit.Chunk
	})
	out := make([]knowledge.Hit, 0, min(limit, len(items)))
	for _, item := range items[:min(limit, len(items))] {
		out = append(out, item.hit)
	}
	return out
}
