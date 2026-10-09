package v3audit

import (
	"fmt"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
	"net/http"
	"time"
)

type auditPageQuery struct {
	start             *time.Time
	actions, outcomes []string
	duration          time.Duration
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
	mapping := map[string][]string{"ok": {"success", "succeeded", "ok", "completed", "accepted", "test_accepted", "recorded", "pending_manual_cleanup", "no_change"}, "pending": {"pending"}, "blocked": {"blocked", "denied", "rejected", "duplicate", "invalid_event"}, "failed": {"failed", "failure", "error", "unknown"}}
	out.outcomes = []string{}
	for _, result := range results {
		values, ok := mapping[result]
		if !ok {
			return out, v3pagequery.ErrInvalid
		}
		out.outcomes = append(out.outcomes, values...)
	}
	return out, nil
}
func auditPageWhere(start int) string {
	return fmt.Sprintf(` AND ($%d::timestamptz IS NULL OR created_at >= $%d) AND (cardinality($%d::text[])=0 OR action=ANY($%d)) AND (cardinality($%d::text[])=0 OR lower(outcome)=ANY($%d))`, start, start, start+1, start+1, start+2, start+2)
}
