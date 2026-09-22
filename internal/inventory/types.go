// Package inventory maintains source-owned CMDB observations. It never mutates
// Kubernetes or grants users/roles access without an explicit permission binding.
package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Config struct {
	PrometheusURL        string   `json:"prometheus_url,omitempty"`
	GrantPermissionIDs   []string `json:"grant_permission_ids,omitempty"`
	Sources              []Source `json:"sources"`
	ExternalSnapshotFile string   `json:"external_snapshot_file,omitempty"`
	IntervalSeconds      int      `json:"interval_seconds,omitempty"`
}
type Source struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	APIURL              string    `json:"api_url"`
	CAFile              string    `json:"ca_file"`
	TokenFile           string    `json:"token_file"`
	EnvironmentKey      string    `json:"environment_key"`
	GrantPermissionIDs  []string  `json:"grant_permission_ids,omitempty"`
	ApplicationBindings []Binding `json:"application_bindings,omitempty"`
}
type Binding struct {
	Namespace     string            `json:"namespace"`
	LabelSelector map[string]string `json:"label_selector,omitempty"`
	ApplicationID string            `json:"application_id"`
	EnvironmentID string            `json:"environment_id"`
}
type Link struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
}
type Record struct {
	ExternalID   string         `json:"external_id"`
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Attributes   map[string]any `json:"attributes"`
	Applications []Link         `json:"applications,omitempty"`
	ObservedAt   time.Time      `json:"observed_at"`
}
type Edge struct {
	SourceExternalID string `json:"source_external_id"`
	TargetExternalID string `json:"target_external_id"`
	Kind             string `json:"kind"`
}
type Snapshot struct {
	SourceID           string    `json:"source_id"`
	SourceKind         string    `json:"source_kind"`
	SourceName         string    `json:"source_name"`
	ObservedAt         time.Time `json:"observed_at"`
	Error              string    `json:"error,omitempty"`
	GrantPermissionIDs []string  `json:"grant_permission_ids,omitempty"`
	Resources          []Record  `json:"resources"`
	Relations          []Edge    `json:"relations,omitempty"`
}
type ExternalFile struct {
	Sources []Snapshot `json:"sources"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	if err == nil && len(cfg.Sources) == 0 && cfg.ExternalSnapshotFile == "" {
		err = fmt.Errorf("inventory requires an explicit source")
	}
	return cfg, err
}
func ResourceID(sourceKind, sourceID, uid string) string {
	hash := sha256.Sum256([]byte(sourceKind + "\x00" + sourceID + "\x00" + uid))
	return "inventory-" + hex.EncodeToString(hash[:20])
}
