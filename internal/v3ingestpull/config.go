// Package v3ingestpull executes one operator-reviewed Prometheus Pull source.
// It does not accept arbitrary HTTP endpoints, expressions or transformations
// from API callers.
package v3ingestpull

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

const maxSources = 100

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var queryIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var labelPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type Config struct {
	Sources []SourceConfig `json:"sources"`
}

// SourceConfig ties one Pull ID to an existing connector and an explicitly
// enrolled metric resource. Expression is private, operator-reviewed PromQL.
type SourceConfig struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	ConnectorID   string   `json:"connector_id"`
	TargetID      string   `json:"target_id"`
	ResourceID    string   `json:"resource_id"`
	QueryID       string   `json:"query_id"`
	Expression    string   `json:"expression"`
	NameLabels    []string `json:"name_labels"`
	LabelKeys     []string `json:"label_keys"`
}

// LoadConfig returns a disabled catalog for an empty path. The file is private
// process configuration; API requests never supply a query expression.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{Sources: []SourceConfig{}}, nil
	}
	if !filepath.IsAbs(path) {
		return Config{}, errors.New("Pull catalog path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open Pull catalog: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Config{}, errors.New("Pull catalog must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return Config{}, errors.New("invalid Pull catalog size")
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("invalid Pull catalog JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("invalid trailing Pull catalog data")
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateConfig(config Config) error {
	if len(config.Sources) > maxSources {
		return errors.New("Pull source count exceeds limit")
	}
	seen := map[string]bool{}
	for _, source := range config.Sources {
		if !sourceIDPattern.MatchString(source.ID) || seen[source.ID] ||
			strings.TrimSpace(source.Name) != source.Name || source.Name == "" || len(source.Name) > 100 ||
			source.ApplicationID == "" || source.EnvironmentID == "" || source.ConnectorID == "" || source.TargetID == "" ||
			len(source.ApplicationID) > 128 || len(source.EnvironmentID) > 128 || len(source.ConnectorID) > 128 || len(source.TargetID) > 128 ||
			!resourceIDPattern.MatchString(source.ResourceID) || !queryIDPattern.MatchString(source.QueryID) ||
			strings.TrimSpace(source.Expression) != source.Expression || source.Expression == "" || len(source.Expression) > 2048 ||
			len(source.NameLabels) == 0 || len(source.NameLabels) > 8 || len(source.LabelKeys) > 16 ||
			!validLabels(source.NameLabels) || !validLabels(source.LabelKeys) {
			return fmt.Errorf("invalid Pull source %q", source.ID)
		}
		seen[source.ID] = true
	}
	return nil
}

func validLabels(labels []string) bool {
	seen := map[string]bool{}
	for _, label := range labels {
		lower := strings.ToLower(label)
		if !labelPattern.MatchString(label) || seen[label] ||
			strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "credential") ||
			strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") ||
			strings.Contains(lower, "apikey") {
			return false
		}
		seen[label] = true
	}
	return true
}

type Service struct {
	DB       *pgxpool.Pool
	Tier     string
	Resolver *v3integrations.BoundResolver
	Client   *http.Client
	Reader   *v3integrations.PrometheusReader
	sources  map[string]SourceConfig
}

func New(db *pgxpool.Pool, tier string, resolver *v3integrations.BoundResolver, config Config, client *http.Client) (*Service, error) {
	if db == nil || (tier != "production" && tier != "nonprod") {
		return nil, errors.New("invalid Pull service dependencies")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	queries := make(map[v3integrations.PrometheusQueryKey]string, len(config.Sources))
	sources := make(map[string]SourceConfig, len(config.Sources))
	for _, source := range config.Sources {
		key := v3integrations.PrometheusQueryKey{
			ConnectorID: source.ConnectorID, ApplicationID: source.ApplicationID,
			EnvironmentID: source.EnvironmentID, ResourceID: source.ResourceID, QueryID: source.QueryID,
		}
		if queries[key] != "" {
			return nil, errors.New("duplicate Pull query catalog key")
		}
		queries[key] = source.Expression
		sources[source.ID] = source
	}
	reader, err := v3integrations.NewPrometheusReader(db, tier, resolver, queries)
	if err != nil {
		return nil, err
	}
	reader.Client = client
	return &Service{DB: db, Tier: tier, Resolver: resolver, Client: client, Reader: reader, sources: sources}, nil
}
