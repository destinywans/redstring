package v3configsync

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// PA-96: a template V3 saved with private_template_v1 is exported as its
// structure, without the record; what the export contract cannot carry is
// refused with a code that says what, before anything is written.
func TestTemplateExportReadsPrivateTemplateRecord(t *testing.T) {
	s := isolatedConfigStore(t)
	node := func(title, private string) string {
		extra := ""
		if private != "" {
			extra = `,"private_template_v1":` + private
		}
		return `[{"w":260,"x":16,"y":140,"id":"node-1","sub":"","icon":"layers","tone":"info","title":"` + title + `","blocks":[]` + extra + `}]`
	}
	for _, row := range []struct{ id, description, nodes string }{
		{"CT-private", "", node("节点 1", `{"steps":true,"source_services":[]}`)},
		{"CT-services", "", node("节点 1", `{"content":true,"source_services":["svc-test-checkout"]}`)},
		{"CT-runbooks", "", node("节点 1", `{"steps":true,"runbooks":[{"id":"RB-1","version":2,"x":0,"y":0}],"source_services":[]}`)},
		{"CT-content", "", node("检查发布", `{"content":true,"source_services":[]}`)},
		{"CT-unknown", "", node("节点 1", `{"steps":true,"source_services":[],"owner":"x"}`)},
		{"CT-described", "发布前检查", node("节点 1", "")},
	} {
		if _, err := s.DB.Exec(t.Context(), `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name,description,nodes,edges)
 VALUES($1,'owner','test','plan',$1,$2,$3::jsonb,'[]')`, row.id, row.description, row.nodes); err != nil {
			t.Fatal(err)
		}
	}
	var repo *fakeRepository
	export := func(id string) (int, string) {
		w := configRequest(configServer(t, s, repo, "nonprod"), "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"`+id+`"}`)
		return w.Code, w.Body.String()
	}
	// The fake repository holds one merge request, so a template each.
	for _, id := range []string{"CT-private", "CT-services"} {
		repo = &fakeRepository{}
		if code, body := export(id); code != 200 || repo.calls != 1 {
			t.Fatalf("%s: %d %s", id, code, body)
		}
		var manifest Manifest
		if err := json.Unmarshal(repo.payload, &manifest); err != nil ||
			manifest.verifyTrusted(manifest.PromotionID, map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}) != nil ||
			len(manifest.Template.Nodes) != 1 || manifest.Template.Nodes[0].Title != "节点 1" {
			t.Fatalf("%s manifest: %v %s", id, err, repo.payload)
		}
		if strings.Contains(string(repo.payload), "private_template_v1") || strings.Contains(string(repo.payload), "svc-test-checkout") {
			t.Fatalf("%s carried the private record: %s", id, repo.payload)
		}
	}
	calls := repo.calls
	for id, code := range map[string]string{
		"CT-runbooks":  "template_private_runbooks",
		"CT-content":   "template_retained_content",
		"CT-unknown":   "template_structure_invalid",
		"CT-described": "template_has_description",
	} {
		status, body := export(id)
		if status != 422 || !strings.Contains(body, `"code":"`+code+`"`) {
			t.Fatalf("%s: %d %s", id, status, body)
		}
	}
	var reserved int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_config_exports
 WHERE object_id IN ('CT-runbooks','CT-content','CT-unknown','CT-described')`).Scan(&reserved); err != nil || reserved != 0 || repo.calls != calls {
		t.Fatalf("a refused template reached the ledger or GitLab: %d %v calls=%d", reserved, err, repo.calls)
	}
}
