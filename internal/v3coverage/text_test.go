package v3coverage

import (
	"testing"
	"unicode"
)

func TestTextFollowsAcceptLanguage(t *testing.T) {
	hasHan := func(s string) bool {
		for _, r := range s {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	for _, header := range []string{"en-US", "en", " EN-GB,en;q=0.9"} {
		for _, c := range textFor(header).columns() {
			if c.Label == "" || hasHan(c.Label) {
				t.Fatalf("%q: column %s label %q is not English", header, c.ID, c.Label)
			}
		}
	}
	zh := textFor("zh-CN").columns()
	if zh[0].Label != "指标" || textFor("").pick("中", "en") != "中" {
		t.Fatalf("Chinese default changed: %+v", zh)
	}
	en := textFor("en-US").columns()
	if len(en) != len(zh) || len(columns) != len(zh) {
		t.Fatal("column sets differ by language")
	}
	for i := range en {
		if en[i].ID != zh[i].ID || columns[i].ID != zh[i].ID {
			t.Fatalf("column IDs must not depend on language: %v %v", en[i], zh[i])
		}
	}
}
