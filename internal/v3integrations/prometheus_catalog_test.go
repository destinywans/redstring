package v3integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPrometheusQueryCatalogFailsClosed(t *testing.T) {
	if got, err := LoadPrometheusQueryCatalog(""); err != nil || len(got) != 0 {
		t.Fatalf("empty path: catalog=%v error=%v", got, err)
	}
	path := filepath.Join(t.TempDir(), "queries.json")
	const query = `{"connector_id":"connector","application_id":"app","environment_id":"dev","resource_id":"app-metrics","query_id":"request_rate","expression":"sum(rate(requests_total{application=\"app\",environment=\"dev\"}[5m]))"}`
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"reviewed", `{"queries":[` + query + `]}`, true},
		{"duplicate", `{"queries":[` + query + `,` + query + `]}`, false},
		{"unknown_field", `{"queries":[` + query + `],"unexpected":true}`, false},
		{"trailing_json", `{"queries":[` + query + `]} {}`, false},
		{"oversize", strings.Repeat(" ", 1<<20+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := LoadPrometheusQueryCatalog(path)
			if tc.ok && (err != nil || len(catalog) != 1) {
				t.Fatalf("expected one reviewed query: catalog=%v err=%v", catalog, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("invalid catalog accepted: %s", tc.name)
			}
		})
	}
}
