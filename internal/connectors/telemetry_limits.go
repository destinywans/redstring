package connectors

// TelemetryLimits reports the effective bounds after NewTelemetry normalization,
// without exposing registration queries, endpoint URLs or authentication data.
type TelemetryLimits struct {
	MaxWindowSeconds    int `json:"max_window_seconds"`
	MaxLookbackSeconds  int `json:"max_lookback_seconds"`
	MinStepSeconds      int `json:"min_step_seconds"`
	MaxSamplesPerSeries int `json:"max_samples_per_series"`
	MaxLogEntries       int `json:"max_log_entries"`
}

func (t *Telemetry) Limits() TelemetryLimits {
	return TelemetryLimits{MaxWindowSeconds: t.config.MaxWindowSeconds, MaxLookbackSeconds: t.config.MaxLookbackSeconds, MinStepSeconds: t.config.MinStepSeconds, MaxSamplesPerSeries: t.config.MaxSamplesPerSeries, MaxLogEntries: t.config.MaxLogEntries}
}
