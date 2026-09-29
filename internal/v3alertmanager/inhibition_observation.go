package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gopkg.in/yaml.v3"
)

// InhibitionObservation is one bounded read of the current Alertmanager
// configuration. The digest covers the original bytes, but those bytes and
// receiver secrets are never returned to callers.
type InhibitionObservation struct {
	Rules        []InhibitionRule
	ConfigSHA256 string
	ObservedAt   time.Time
}

type inhibitionObservationSource interface {
	ObserveInhibitions(context.Context, string) (InhibitionObservation, error)
}

func (c *Client) ObserveInhibitions(ctx context.Context, environment string) (InhibitionObservation, error) {
	body, err := c.get(ctx, environment, "/api/v2/status")
	if err != nil {
		return InhibitionObservation{}, err
	}
	var status struct {
		Config struct {
			Original string `json:"original"`
		} `json:"config"`
	}
	if decodeOne(body, &status) != nil || status.Config.Original == "" || len(status.Config.Original) > maxResponseBytes {
		return InhibitionObservation{}, errors.New("invalid Alertmanager status configuration")
	}
	var config struct {
		InhibitRules []InhibitionRule `yaml:"inhibit_rules"`
	}
	if yaml.Unmarshal([]byte(status.Config.Original), &config) != nil || len(config.InhibitRules) > maxRecords {
		return InhibitionObservation{}, errors.New("invalid Alertmanager inhibition configuration")
	}
	for _, rule := range config.InhibitRules {
		if len(rule.SourceMatchers)+len(rule.TargetMatchers)+len(rule.Equal)+
			len(rule.SourceMatch)+len(rule.TargetMatch)+len(rule.SourceMatchRE)+len(rule.TargetMatchRE) > 100 {
			return InhibitionObservation{}, errors.New("Alertmanager inhibition matcher count exceeds limit")
		}
	}
	if config.InhibitRules == nil {
		config.InhibitRules = []InhibitionRule{}
	}
	digest := sha256.Sum256([]byte(status.Config.Original))
	return InhibitionObservation{Rules: config.InhibitRules, ConfigSHA256: hex.EncodeToString(digest[:]), ObservedAt: time.Now().UTC()}, nil
}
