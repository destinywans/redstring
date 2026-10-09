package v3catalog

import "testing"

func TestMappingSystemLabelsFollowAcceptLanguage(t *testing.T) {
	en, zh := mappingSystemsFor("en-US,en;q=0.9"), mappingSystemsFor("zh-CN")
	if len(en) != len(mappingSystems) || zh[0].Label != "指标" || en[0].Label != "Metrics" {
		t.Fatalf("labels: en=%+v zh=%+v", en, zh)
	}
	for i := range en {
		if en[i].ID != mappingSystems[i].ID || en[i].Brand != mappingSystems[i].Brand || en[i].Label == "" {
			t.Fatalf("language changed identity or left a label empty: %+v", en[i])
		}
	}
	if mappingSystems[0].Label != "指标" {
		t.Fatal("English projection mutated the shared catalog")
	}
}
