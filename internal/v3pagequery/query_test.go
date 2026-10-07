package v3pagequery

import (
	"net/url"
	"testing"
	"time"
)

func TestRangeAndMultiFilters(t *testing.T) {
	for _, raw := range []string{"range=7d", "range=30m"} {
		if d, e := ParseRange(mustQuery(raw), time.Hour); e != nil || d <= 0 {
			t.Fatal(raw, d, e)
		}
	}
	for _, raw := range []string{"range=1h&range=7d", "range=0", "range="} {
		if _, e := ParseRange(mustQuery(raw), time.Hour); e == nil {
			t.Fatal(raw)
		}
	}
	v, e := Values(mustQuery("agent=a&agent=b&agent=a"), "agent")
	if e != nil || len(v) != 2 || !Matches(v, "b") || Matches(v, "c") {
		t.Fatal(v, e)
	}
	if _, e = Single(mustQuery("sort=recent&sort=title"), "sort", "recent", "title"); e == nil {
		t.Fatal("ambiguous sorting accepted")
	}
}
func mustQuery(s string) url.Values {
	q, e := url.ParseQuery(s)
	if e != nil {
		panic(e)
	}
	return q
}
