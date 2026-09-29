package v3serviceviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
)

// ErrInvalidAlertCoverage means a completeness assertion cannot be tied to
// the actual Alertmanager ingress and an active V3 application/environment.
var ErrInvalidAlertCoverage = errors.New("invalid service alert coverage binding")

// Complete is an explicit operator assertion that the upstream Alertmanager
// route sends every alert for this target through the named source and label
// selector. Source registration alone cannot establish that fact.
type AlertCoverageBinding struct {
	SourceID      string            `json:"source_id"`
	ApplicationID string            `json:"application_id"`
	EnvironmentID string            `json:"environment_id"` // V3 environment key, not the legacy row ID
	Labels        map[string]string `json:"labels"`
	Complete      bool              `json:"complete"`
}

type AlertCoverageConfig struct {
	Bindings []AlertCoverageBinding `json:"bindings"`
}

type verifiedAlertBinding struct {
	source operations.Source
	envRow string
}

// LoadAlertCoverage loads server-owned completeness assertions. An absent
// config gives nil coverage, so a zero incident count cannot be presented as
// zero alerts. The callback rechecks token readability and the persisted
// mapping on each read; removing a binding from the file requires restart.
func LoadAlertCoverage(path string, ops operations.Config, instance httpapi.V3InstanceConfig, db *pgxpool.Pool) (AlertCovered, error) {
	if path == "" {
		return nil, nil
	}
	if db == nil || !filepath.IsAbs(path) || (instance.Tier != "production" && instance.Tier != "nonprod") {
		return nil, ErrInvalidAlertCoverage
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxBytes = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(raw) > maxBytes || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, ErrInvalidAlertCoverage
	}
	var config AlertCoverageConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&config); err != nil {
		return nil, ErrInvalidAlertCoverage
	}
	if err := dec.Decode(new(any)); err != io.EOF || len(config.Bindings) > 50 {
		return nil, ErrInvalidAlertCoverage
	}
	sources := make(map[string]operations.Source, len(ops.Sources))
	for _, source := range ops.Sources {
		if source.ID == "" || sources[source.ID].ID != "" {
			return nil, ErrInvalidAlertCoverage
		}
		sources[source.ID] = source
	}
	envs := make(map[string]bool, len(instance.EnvironmentIDs))
	for _, env := range instance.EnvironmentIDs {
		if env == "" || envs[env] || (instance.Tier == "production" && env != "prod") || (instance.Tier == "nonprod" && env == "prod") {
			return nil, ErrInvalidAlertCoverage
		}
		envs[env] = true
	}
	bindings := make(map[string]verifiedAlertBinding, len(config.Bindings))
	for _, b := range config.Bindings {
		key := b.ApplicationID + "\x00" + b.EnvironmentID
		source, found := sources[b.SourceID]
		if !found || !b.Complete || b.ApplicationID == "" || strings.Contains(b.ApplicationID, "\x00") || !envs[b.EnvironmentID] || len(b.Labels) == 0 || bindings[key].source.ID != "" || !filepath.IsAbs(source.TokenFile) {
			return nil, ErrInvalidAlertCoverage
		}
		var selected int
		for i, mapping := range source.Mappings {
			if mapping.ApplicationID != b.ApplicationID || !sameLabels(mapping.Labels, b.Labels) {
				continue
			}
			selected = i + 1
		}
		if selected == 0 {
			return nil, ErrInvalidAlertCoverage
		}
		mapping := source.Mappings[selected-1]
		for i, other := range source.Mappings {
			if i != selected-1 && (other.ApplicationID == b.ApplicationID && other.EnvironmentID == mapping.EnvironmentID || selectorsOverlap(other.Labels, b.Labels)) {
				return nil, ErrInvalidAlertCoverage
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		active := activeAlertMapping(ctx, db, b.ApplicationID, b.EnvironmentID, mapping.EnvironmentID)
		cancel()
		if !active || !readableAlertToken(source.TokenFile) {
			return nil, ErrInvalidAlertCoverage
		}
		bindings[key] = verifiedAlertBinding{source: source, envRow: mapping.EnvironmentID}
	}
	return func(ctx context.Context, app, env string) string {
		binding, ok := bindings[app+"\x00"+env]
		if !ok || !readableAlertToken(binding.source.TokenFile) || !activeAlertMapping(ctx, db, app, env, binding.envRow) {
			return ""
		}
		return binding.source.ID
	}, nil
}

func sameLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if k == "" || v == "" || b[k] != v {
			return false
		}
	}
	return true
}

func selectorsOverlap(a, b map[string]string) bool {
	for k, v := range a {
		if want, ok := b[k]; ok && want != v {
			return false
		}
	}
	return true
}

func activeAlertMapping(ctx context.Context, db *pgxpool.Pool, app, env, envRow string) bool {
	var active bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM application_environments e
	 JOIN applications a ON a.id=e.application_id AND NOT a.archived
	 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
	 WHERE e.id=$1 AND e.application_id=$2 AND e.key=$3 AND NOT e.archived)`, envRow, app, env).Scan(&active)
	return err == nil && active
}

func readableAlertToken(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return false
	}
	token := strings.TrimSpace(string(data))
	return len(token) >= 24 && len(token) <= 4096
}
