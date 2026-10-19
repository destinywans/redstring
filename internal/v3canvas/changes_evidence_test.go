package v3canvas

import (
	"encoding/base64"
	"testing"

	"github.com/destinywans/redstring/internal/inference"
)

// D11: read_service_changes evidence is accepted on the investigation canvas
// only as the investigation service's GitLab changes.
func TestInvestigationAcceptsChangesEvidence(t *testing.T) {
	target := base64.RawURLEncoding.EncodeToString([]byte("app\x00prod"))
	valid := inference.Evidence{Kind: "changes", Source: "gitlab", TargetID: target, Title: "Changes: checkout", Content: `{"coverage":{}}`}
	if !validInvestigationEvidence(changesToolName, "app", "prod", valid) {
		t.Fatal("changes evidence refused")
	}
	for name, e := range map[string]inference.Evidence{
		"other kind":    {Kind: "workload", Source: "gitlab", TargetID: target, Title: "x", Content: "{}"},
		"other source":  {Kind: "changes", Source: "kubernetes", TargetID: target, Title: "x", Content: "{}"},
		"other service": {Kind: "changes", Source: "gitlab", TargetID: base64.RawURLEncoding.EncodeToString([]byte("other\x00prod")), Title: "x", Content: "{}"},
	} {
		if validInvestigationEvidence(changesToolName, "app", "prod", e) {
			t.Fatalf("%s accepted", name)
		}
	}
}
