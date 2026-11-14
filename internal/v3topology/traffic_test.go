package v3topology

import (
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/v3integrations"
)

func TestProjectServiceTrafficFiltersBothEndpointsAndRequiresCurrentScopedSamples(t *testing.T) {
	at := time.Unix(1800000000, 0)
	checkout := trafficApplication{ID: "app-1", Key: "checkout", Name: "Checkout"}
	order := trafficApplication{ID: "app-2", Key: "order", Name: "Orders"}
	apps := map[string]trafficApplication{"checkout": checkout, "order": order}
	series := func(client, server, value string, lag time.Duration) connectors.MetricSeries {
		return connectors.MetricSeries{
			Labels:  map[string]string{"client": client, "server": server},
			Samples: []connectors.MetricSample{{Timestamp: float64(at.Add(-lag).Unix()), Value: value}},
		}
	}
	data := v3integrations.PrometheusMetricEvidence{ApplicationID: checkout.ID, EnvironmentID: "dev", QueryID: TrafficQueryID,
		Series: []connectors.MetricSeries{
			series("order", "checkout", "12.5", 0),
			series("checkout", "private-peer", "100.5", 0),
			series("checkout", "order", "20", time.Minute),
		}}
	view, err := projectServiceTraffic(data, at, "dev", checkout, apps)
	if err != nil || len(view.Links) != 1 || view.Links[0].Value != 12.5 ||
		len(view.Cols[0]) != 1 || view.Cols[0][0].Label != "Orders" {
		t.Fatalf("only current authorized traffic should be visible: %+v, %v", view, err)
	}
	data.Series = append(data.Series, series("order", "private-peer", "10", 0))
	if _, err = projectServiceTraffic(data, at, "dev", checkout, apps); err == nil {
		t.Fatal("admitted a result outside the selected application scope")
	}
}
