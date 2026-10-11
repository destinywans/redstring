package v3ingestpull

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type parameter struct {
	Key      string `json:"k"`
	Value    string `json:"v"`
	Variable bool   `json:"variable"`
}

type field struct {
	Field     string `json:"field"`
	Required  bool   `json:"required"`
	Source    string `json:"source"`
	Transform string `json:"transform,omitempty"`
	Color     string `json:"color"`
	State     string `json:"state"`
}

type chip struct {
	Label string `json:"label"`
	Tone  string `json:"tone,omitempty"`
}

type draft struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Step    int    `json:"step"`
	Request struct {
		Method string      `json:"method"`
		Host   string      `json:"host"`
		Path   string      `json:"path"`
		Params []parameter `json:"params"`
		Auth   string      `json:"auth"`
		Rate   string      `json:"rate"`
		Paging string      `json:"paging"`
	} `json:"request"`
	// Sample.CapturedAt is the read time in RFC 3339, null without a
	// successful read; Meta no longer repeats it (PA-83).
	Sample struct {
		Status     int        `json:"status"`
		Meta       string     `json:"meta"`
		CapturedAt *string    `json:"captured_at"`
		Body       string     `json:"body"`
		Highlights []struct{} `json:"highlights"`
	} `json:"sample"`
	Contract        string  `json:"contract"`
	ContractVersion string  `json:"contract_version"`
	Fields          []field `json:"fields"`
	Preview         struct {
		Rows   []row     `json:"rows"`
		Series []float64 `json:"series"`
		Checks []chip    `json:"checks"`
	} `json:"preview"`
}

func pullDraft(source SourceConfig, initial snapshot, mapping mapping, observed result) draft {
	view := draft{ID: source.ID, Name: source.Name, Step: 3, Contract: "metric",
		ContractVersion: "MetricSeries v1"}
	endpoint, _ := url.Parse(initial.Endpoint) // validated before the live read
	view.Request.Method = "GET"
	view.Request.Host = endpoint.Scheme + "://" + endpoint.Host
	view.Request.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/query_range"
	view.Request.Params = []parameter{
		{Key: "query", Value: "{registered:" + source.QueryID + "}", Variable: true},
		{Key: "start", Value: "{now-5m}", Variable: true},
		{Key: "end", Value: "{now}", Variable: true},
		{Key: "step", Value: "30s"},
		{Key: "timeout", Value: "5s"},
		{Key: "limit", Value: "100"},
	}
	view.Request.Auth = "None"
	if initial.AuthMethod == "bearer" {
		view.Request.Auth = "Vault exact binding (Bearer)"
	}
	view.Request.Rate = "10 reads per user per source and 60 per source each minute; 4 concurrent; 5 s upstream timeout"
	view.Request.Paging = "None; one bounded query_range request. Partial or truncated results fail."
	view.Sample.Status = 200
	capturedAt := observed.Captured.UTC().Format(time.RFC3339)
	view.Sample.Meta = "Normalized preview from a successful upstream read"
	view.Sample.CapturedAt = &capturedAt
	sample := observed.Rows
	if len(sample) > 5 {
		sample = sample[:5]
	}
	encoded, _ := json.Marshal(sample)
	view.Sample.Body = string(encoded)
	view.Sample.Highlights = []struct{}{}
	state := "ok"
	if observed.RowsCount == 0 {
		state = "none"
	} else if observed.Dropped > 0 {
		state = "warn"
	}
	view.Fields = []field{
		{Field: "service", Required: true, Source: "bound application_id", Color: "c1", State: "ok"},
		{Field: "name", Required: true, Source: "metric." + mapping.NameLabel, Color: "c2", State: state},
		{Field: "timestamp", Required: true, Source: "values[*][0]", Transform: "Unix seconds to RFC3339", Color: "c3", State: state},
		{Field: "value", Required: true, Source: "values[*][1]", Transform: "validated finite float64", Color: "c4", State: state},
		{Field: "labels", Required: false, Source: "metric labels: " + strings.Join(mapping.LabelKeys, ", "),
			Transform: "configured allowlist only", Color: "c6", State: "ok"},
	}
	view.Preview.Rows = observed.Rows
	view.Preview.Series = observed.Series
	view.Preview.Checks = []chip{
		{Label: strconv.Itoa(observed.RowsCount) + " converted rows", Tone: "ok"},
		{Label: strconv.Itoa(observed.Dropped) + " dropped", Tone: "neutral"},
		{Label: "Complete bounded response", Tone: "ok"},
	}
	return view
}
