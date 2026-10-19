package v3events

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/destinywans/redstring/internal/store"
)

var errGroupingLabelUnavailable = errors.New("a selected grouping label is absent from persisted evidence")

// Display groups keep application and environment as implicit boundaries.
// A viewgroup ID cannot authorize the older exact-source bulk action route.
func displayAlertGroupIdentity(v incidentRecord, groupBy string) (string, string, error) {
	if groupBy == "" {
		key := strings.Join([]string{v.EnvironmentID, v.ApplicationID, v.SourceID, v.Fingerprint}, "\x00")
		return localAlertGroupID(v), key, nil
	}
	parts := []string{groupBy, v.EnvironmentID, v.ApplicationID}
	for _, name := range strings.Split(groupBy, ",") {
		value := v.Labels[name]
		if name == "service" {
			value = v.ApplicationID
		}
		if value == "" {
			return "", "", errGroupingLabelUnavailable
		}
		parts = append(parts, value)
	}
	raw, _ := json.Marshal(parts)
	key := string(raw)
	return "viewgroup:" + store.Hash(key)[:24], key, nil
}
