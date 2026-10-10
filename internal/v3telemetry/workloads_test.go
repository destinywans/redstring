package v3telemetry

import (
	"strings"
	"testing"
)

const workloadCatalog = `{"queries":[
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"request_rate","expression":"sum(rate(http_requests_total[1m]))"},
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"error_rate","expression":"sum(rate(http_errors_total[1m]))/sum(rate(http_requests_total[1m]))"},
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"sample_age","expression":"time()-max(timestamp(http_requests_total))"}],
 "workloads":[{"connector_id":"k8s","application_id":"app","environment_id":"dev","resource_id":"checkout-api",
  "namespace":"aiops-lab","deployment":"checkout-api",
  "rollback":{"allowed":true,"observation_seconds":180,"verification":{"connector_id":"prom","resource_id":"checkout",
   "request_rate_query_id":"request_rate","error_ratio_query_id":"error_rate","sample_age_query_id":"sample_age",
   "max_error_ratio":0.02,"min_request_rate":0.2}}}]}`

// PA-08: a workload scope names its Deployment and, when rollback is
// allowed, reviewed verification queries of the same application and
// environment.
func TestWorkloadCatalog(t *testing.T) {
	catalog, err := ParseCatalog([]byte(workloadCatalog))
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := catalog.Workloads[ScopeKey{ConnectorID: "k8s", ApplicationID: "app", EnvironmentID: "dev", ResourceID: "checkout-api"}]
	if !ok || scope.Namespace != "aiops-lab" || scope.Deployment != "checkout-api" || scope.Rollback == nil || !scope.Rollback.Allowed {
		t.Fatalf("workload scope: %+v %v", scope, ok)
	}
	if file := catalog.File(); len(file.Workloads) != 1 || file.Workloads[0].Rollback.Verification.MaxErrorRatio != 0.02 {
		t.Fatalf("file shape: %+v", file.Workloads)
	}
	for name, bad := range map[string]string{
		"missing query":       strings.Replace(workloadCatalog, `"sample_age_query_id":"sample_age"`, `"sample_age_query_id":"absent"`, 1),
		"short observation":   strings.Replace(workloadCatalog, `"observation_seconds":180`, `"observation_seconds":10`, 1),
		"bad namespace":       strings.Replace(workloadCatalog, `"namespace":"aiops-lab"`, `"namespace":"Aiops_Lab"`, 1),
		"ratio above one":     strings.Replace(workloadCatalog, `"max_error_ratio":0.02`, `"max_error_ratio":1.5`, 1),
		"other app's queries": strings.Replace(workloadCatalog, `"application_id":"app","environment_id":"dev","resource_id":"checkout-api"`, `"application_id":"other","environment_id":"dev","resource_id":"checkout-api"`, 1),
		"unknown field":       strings.Replace(workloadCatalog, `"deployment":"checkout-api",`, `"deployment":"checkout-api","replicas":3,`, 1),
	} {
		if _, err := ParseCatalog([]byte(bad)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	disabled := strings.Replace(workloadCatalog, `"allowed":true,"observation_seconds":180`, `"allowed":false,"observation_seconds":0`, 1)
	if _, err := ParseCatalog([]byte(disabled)); err != nil {
		t.Fatalf("disabled rollback rejected: %v", err)
	}
}
