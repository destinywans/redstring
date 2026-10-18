package v3audit

import (
	"fmt"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
	"net/http"
	"time"
)

type auditPageQuery struct {
	start *time.Time
	// results are page results (resultKind), not raw outcomes.
	actions, results []string
	duration         time.Duration
}

func parseAuditPage(r *http.Request) (auditPageQuery, error) {
	q := r.URL.Query()
	out := auditPageQuery{}
	d, e := v3pagequery.ParseRange(q, 7*24*time.Hour)
	if e != nil {
		return out, e
	}
	out.duration = d
	if q.Has("range") {
		start := time.Now().UTC().Add(-d)
		out.start = &start
	}
	out.actions, e = v3pagequery.Values(q, "action")
	if e != nil {
		return out, e
	}
	results, e := v3pagequery.Values(q, "result")
	if e != nil {
		return out, e
	}
	known := map[string]bool{}
	for _, kind := range resultKinds {
		known[kind] = true
	}
	out.results = []string{}
	for _, result := range results {
		if !known[result] {
			return out, v3pagequery.ErrInvalid
		}
		out.results = append(out.results, result)
	}
	return out, nil
}
func auditPageWhere(start int) string {
	return fmt.Sprintf(` AND ($%d::timestamptz IS NULL OR created_at >= $%d) AND (cardinality($%d::text[])=0 OR action=ANY($%d)) AND (cardinality($%d::text[])=0 OR %s=ANY($%d))`, start, start, start+1, start+1, start+2, resultKindSQL, start+2)
}
