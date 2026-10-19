package v3agents

import (
	"net/http"
	"net/url"
	"time"

	"github.com/destinywans/redstring/internal/v3pagequery"
)

func interactionFacets(q url.Values, keys ...string) (map[string][]string, bool) {
	out := map[string][]string{}
	for _, key := range keys {
		vs, err := v3pagequery.Values(q, key)
		if err != nil {
			return nil, false
		}
		for _, v := range vs {
			if !bounded(v, 128) {
				return nil, false
			}
		}
		out[key] = vs
	}
	return out, true
}
func runQuery(r *http.Request) (map[string][]string, time.Duration, bool) {
	f, ok := interactionFacets(r.URL.Query(), "agent", "version", "trigger")
	if !ok {
		return nil, 0, false
	}
	for _, v := range f["trigger"] {
		if v != "manual" && v != "alert" && v != "scheduled" && v != "api" {
			return nil, 0, false
		}
	}
	window, err := v3pagequery.ParseRange(r.URL.Query(), 0)
	return f, window, err == nil
}
