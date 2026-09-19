package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// Descriptor is part of index identity. Callers must never mix generations with
// a different provider/model or returned dimension in one vector index.
type EmbeddingDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
type Embedder interface {
	Descriptor() EmbeddingDescriptor
	Embed(context.Context, []string) ([][]float32, error)
}
type EmbeddingConfig struct {
	Provider, Model, BaseURL, APIKey string
	HTTPClient                       *http.Client
}
type embeddingAdapter struct{ cfg EmbeddingConfig }

func NewEmbedder(c EmbeddingConfig) (Embedder, error) {
	if c.Provider != "ollama" && c.Provider != "openai" {
		return nil, errors.New("unsupported embedding provider")
	}
	if c.Model == "" || c.BaseURL == "" {
		return nil, errors.New("embedding model and endpoint required")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &embeddingAdapter{cfg: c}, nil
}
func NewEmbedderFromEnv() (Embedder, error) {
	p := os.Getenv("EMBEDDING_PROVIDER")
	if p == "" {
		p = "ollama"
	}
	m := os.Getenv("EMBEDDING_MODEL")
	b := os.Getenv("EMBEDDING_BASE_URL")
	key := os.Getenv("EMBEDDING_API_KEY")
	if p == "ollama" {
		if m == "" {
			m = "bge-m3"
		}
		if b == "" {
			b = "http://localhost:11434"
		}
	} else if p == "openai" {
		if m == "" {
			m = "text-embedding-3-small"
		}
		if b == "" {
			b = "https://api.openai.com/v1"
		}
	}
	return NewEmbedder(EmbeddingConfig{Provider: p, Model: m, BaseURL: b, APIKey: key})
}
func (e *embeddingAdapter) Descriptor() EmbeddingDescriptor {
	return EmbeddingDescriptor{Provider: e.cfg.Provider, Model: e.cfg.Model}
}
func (e *embeddingAdapter) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > 128 {
		return nil, errors.New("embedding batch must contain 1 to 128 texts")
	}
	size := 0
	for _, s := range texts {
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("embedding input is blank")
		}
		size += len(s)
	}
	if size > 2<<20 {
		return nil, errors.New("embedding batch exceeds 2 MiB")
	}
	path := "/embeddings"
	body := map[string]any{"model": e.cfg.Model, "input": texts}
	if e.cfg.Provider == "ollama" {
		path = "/api/embed"
		body["truncate"] = false
	}
	resp, err := post(ctx, e.cfg.HTTPClient, Provider{Kind: e.cfg.Provider, BaseURL: e.cfg.BaseURL, APIKey: e.cfg.APIKey}, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Data       []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	vectors := out.Embeddings
	if e.cfg.Provider == "openai" {
		vectors = make([][]float32, len(texts))
		for _, v := range out.Data {
			if v.Index < 0 || v.Index >= len(vectors) || vectors[v.Index] != nil {
				return nil, errors.New("invalid embedding indices")
			}
			vectors[v.Index] = v.Embedding
		}
	}
	if len(vectors) != len(texts) {
		return nil, errors.New("embedding count mismatch")
	}
	dimension := len(vectors[0])
	if dimension == 0 {
		return nil, errors.New("empty embedding")
	}
	for _, v := range vectors {
		if len(v) != dimension {
			return nil, errors.New("inconsistent embedding dimensions")
		}
		for _, n := range v {
			if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
				return nil, fmt.Errorf("nonfinite embedding")
			}
		}
	}
	return vectors, nil
}
