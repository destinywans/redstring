package v3alertrules

import (
	"net/http/httptest"
	"testing"
)

func TestImportNotesFollowAcceptLanguageOnReplay(t *testing.T) {
	stored := map[string]any{"status": "pending_review", "candidates": []any{
		map[string]any{"name": "HighErrors", "outcome": "new"},
		map[string]any{"name": "Dup", "outcome": "duplicate", "draft_state": "stale_remote"},
	}}
	stored["items"] = importResultItems([]map[string]any{{"name": "HighErrors", "outcome": "new"}})
	if stored["items"].([]map[string]any)[0]["note"] != "已保存待审草稿 / Draft saved for review" {
		t.Fatalf("persisted note must keep both languages: %+v", stored["items"])
	}
	r := httptest.NewRequest("POST", "/api/v1/alert-rules/import", nil)
	r.Header.Set("Accept-Language", "en-US")
	en := localizedImportResult(stored, r)["items"].([]map[string]any)
	r.Header.Set("Accept-Language", "zh-CN")
	zh := localizedImportResult(stored, r)["items"].([]map[string]any)
	if en[0]["note"] != "Draft saved for review" || en[1]["note"] != "Remote draft needs review" ||
		zh[0]["note"] != "已保存待审草稿" || zh[1]["note"] != "远端草稿需人工核对，已跳过" || en[1]["status"] != "duplicate" {
		t.Fatalf("localized notes: en=%+v zh=%+v", en, zh)
	}
	if _, ok := stored["items"].([]map[string]any); !ok {
		t.Fatal("localization mutated the stored result")
	}
}
