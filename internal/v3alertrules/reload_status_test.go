package v3alertrules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// PA-90: Prometheus's own reload metrics, as /api/v1/query returns them.
func TestPrometheusReloadStatus(t *testing.T) {
	series := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ok := series[r.URL.Query().Get("query")]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` + body + `]}}`))
	}))
	defer server.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	one := func(value string) string {
		return `{"metric":{"job":"prometheus"},"value":[1760000000.5,"` + value + `"]}`
	}
	series["prometheus_config_last_reload_successful"] = one("1")
	series["prometheus_config_last_reload_success_timestamp_seconds"] = one("1760000000")
	status, err := source.ReloadStatus(context.Background(), "test")
	if err != nil || status.Unavailable != "" || status.LastSuccessful == nil || !*status.LastSuccessful ||
		!status.LastSuccessAt.Equal(time.Unix(1760000000, 0)) {
		t.Fatalf("reloaded: %+v %v", status, err)
	}
	series["prometheus_config_last_reload_successful"] = one("0")
	if status, err = source.ReloadStatus(context.Background(), "test"); err != nil || *status.LastSuccessful {
		t.Fatalf("failed reload: %+v %v", status, err)
	}
	series["prometheus_config_last_reload_successful"] = ""
	series["prometheus_config_last_reload_success_timestamp_seconds"] = ""
	if status, err = source.ReloadStatus(context.Background(), "test"); err != nil || status.Unavailable != "not_found" || status.LastSuccessful != nil {
		t.Fatalf("not scraped: %+v %v", status, err)
	}
	series["prometheus_config_last_reload_successful"] = strings.Join([]string{one("1"), one("0")}, ",")
	if status, err = source.ReloadStatus(context.Background(), "test"); err != nil || status.Unavailable != "ambiguous" {
		t.Fatalf("two servers: %+v %v", status, err)
	}
	delete(series, "prometheus_config_last_reload_successful")
	if _, err = source.ReloadStatus(context.Background(), "test"); err == nil {
		t.Fatal("an upstream error was not reported")
	}
	if _, err = source.ReloadStatus(context.Background(), "prod"); err == nil {
		t.Fatal("an unconfigured environment was read")
	}
}
