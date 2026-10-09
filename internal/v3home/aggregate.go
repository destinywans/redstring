package v3home

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type incident struct {
	id, title, summary, severity, status, alertStatus, owner, appID, env string
	starts, updated                                                      time.Time
}

type proposal struct {
	id, title, appID, env string
	created               time.Time
}

func (s *Service) populate(ctx context.Context, tx pgx.Tx, user core.User, targets []target, envs []string, now time.Time, t text, out *Summary, ranges ...time.Duration) error {
	duration := 24 * time.Hour
	if len(ranges) > 0 {
		duration = ranges[0]
	}
	incidents := make([]incident, 0)
	proposals := make([]proposal, 0)
	serviceViews, eventViews, proposalViews := 0, 0, 0
	for _, row := range targets {
		if row.ServicesView {
			serviceViews++
			out.Services = append(out.Services, ServiceItem{
				ID:   base64.RawURLEncoding.EncodeToString([]byte(row.AppID + "\x00" + row.Env)),
				Name: row.Name, Env: row.Env, Tier: frontendTier(row.Tier),
				Status: nil, BudgetPct: nil, P99: nil, ErrorRate: nil, Series: []float64{},
			})
		}
		if row.EventsView {
			eventViews++
			items, e := loadIncidents(ctx, tx, row)
			if e != nil {
				return e
			}
			incidents = append(incidents, items...)
			if len(incidents) > maxIncidents {
				return errTooMany
			}
		}
		if row.ProposalsView {
			proposalViews++
			items, e := loadProposals(ctx, tx, row)
			if e != nil {
				return e
			}
			proposals = append(proposals, items...)
			if len(proposals) > maxProposals {
				return errTooMany
			}
		}
	}
	active := make([]incident, 0)
	for _, row := range incidents {
		if row.status == "open" {
			active = append(active, row)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].updated.After(active[j].updated) })
	for _, row := range active {
		if len(out.Attention) >= 8 {
			break
		}
		// Keep an incident visible even when its legacy severity has no V3
		// P-level mapping. Null remains unknown to the frontend adapter.
		out.Attention = append(out.Attention, Attention{ID: row.id, Severity: frontendSeverity(row.severity),
			Service: serviceName(targets, row.appID, row.env), Env: row.env,
			Symptom: cleanTitle(row.title), Meta: t.incidentMeta(row), AI: nil,
			Owner: row.owner, Href: "/alerts/" + row.id})
	}
	pending := len(proposals)
	out.Greeting.Summary = t.summary(len(active), pending, eventViews > 0, proposalViews > 0)
	if eventViews > 0 {
		out.KPIs = append(out.KPIs, KPI{ID: "incidents", Label: t.pick("可见未解决事件", "Visible open incidents"), Value: strconv.Itoa(len(active)), Series: []float64{}})
		out.Availability["incidents"] = "persisted_scoped"
	} else {
		out.Availability["incidents"] = "no_authorized_target"
	}
	if proposalViews > 0 {
		out.KPIs = append(out.KPIs, KPI{ID: "reviews", Label: t.pick("可见待复核提案", "Visible proposals awaiting review"), Value: strconv.Itoa(pending), Series: []float64{}})
		out.Availability["proposals"] = "persisted_scoped"
	} else {
		out.Availability["proposals"] = "no_authorized_target"
	}
	if serviceViews > 0 {
		out.KPIs = append(out.KPIs, KPI{ID: "registered_services", Label: t.pick("可见服务登记", "Visible registered services"), Value: strconv.Itoa(len(out.Services)), Series: []float64{}})
		out.Availability["services"] = "registered_without_health"
	} else {
		out.Availability["services"] = "no_authorized_target"
	}
	buildActivity(incidents, proposals, now, t, out, duration)
	if eventViews > 0 {
		trend, err := loadTrend(ctx, tx, incidents, now, t, duration)
		if err != nil {
			return err
		}
		out.AlertTrend = trend
		out.Availability["alert_trend"] = "persisted_occurrences_only_ingest_unverified"
	} else {
		out.Availability["alert_trend"] = "no_authorized_target"
	}
	if err := s.recentConversations(ctx, tx, user, envs, out, now.Add(-duration)); err != nil {
		return err
	}
	return nil
}

func frontendTier(tier *string) *string {
	if tier == nil {
		return nil
	}
	switch *tier {
	case "T0", "T1", "T2":
		return tier
	}
	return nil // T3 is supported by the backend catalog, not the frozen HomeSummary type.
}

func frontendSeverity(value string) *string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "p1", "critical", "fatal":
		v := "P1"
		return &v
	case "p2", "warning", "warn", "high":
		v := "P2"
		return &v
	case "p3", "info", "medium":
		v := "P3"
		return &v
	case "p4", "low":
		v := "P4"
		return &v
	}
	return nil
}

// Arrival counts describe durable ingress only, never live Alertmanager state.
// The service intentionally says so even when the local count is zero.
func loadTrend(ctx context.Context, tx pgx.Tx, incidents []incident, now time.Time, t text, ranges ...time.Duration) (AlertTrend, error) {
	duration := 24 * time.Hour
	if len(ranges) > 0 {
		duration = ranges[0]
	}
	step := duration / 24
	out := AlertTrend{Labels: make([]string, 24), Counts: make([][4]int, 24)}
	start := now.Add(-duration)
	for i := range out.Labels {
		out.Labels[i] = start.Add(time.Duration(i) * step).Format("15:04")
	}
	ids := make([]string, 0, len(incidents))
	severity := make(map[string]int, len(incidents))
	for _, v := range incidents {
		if level := frontendSeverity(v.severity); level != nil {
			ids = append(ids, v.id)
			severity[v.id] = int((*level)[1] - '1')
		}
	}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT incident_id,floor(extract(epoch from (received_at-$2::timestamptz))/$4::double precision)::bigint,count(*)
		 FROM ops_alert_occurrences
		 WHERE incident_id=ANY($1::text[]) AND received_at >= $2 AND received_at <= $3
		 GROUP BY incident_id,floor(extract(epoch from (received_at-$2::timestamptz))/$4::double precision)::bigint`, ids, start, now, step.Seconds())
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id string
			var hour int64
			var count int
			if err = rows.Scan(&id, &hour, &count); err != nil {
				break
			}
			index := int(hour)
			if index == 24 {
				index = 23
			}
			if index >= 0 && index < len(out.Counts) {
				out.Counts[index][severity[id]] += count
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	total := 0
	for _, bucket := range out.Counts {
		for _, count := range bucket {
			total += count
		}
	}
	out.Summary = t.trendSummary(duration, total)
	return out, nil
}

func serviceName(targets []target, app, env string) string {
	for _, t := range targets {
		if t.AppID == app && t.Env == env {
			return t.Name
		}
	}
	return app
}

func loadIncidents(ctx context.Context, tx pgx.Tx, row target) ([]incident, error) {
	rows, err := tx.Query(ctx, `SELECT i.id,i.title,i.summary,i.severity,i.status,i.alert_status,
	 COALESCE(u.name,''),i.starts_at,i.updated_at
	 FROM ops_incidents i
	 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
	 AND e.key=$2 AND NOT e.archived
	 LEFT JOIN users u ON u.id=i.assignee_id AND NOT u.disabled AND u.deleted_at IS NULL
	 WHERE i.application_id=$1 ORDER BY i.updated_at DESC,i.id DESC LIMIT 1001`, row.AppID, row.Env)
	if err != nil {
		return nil, err
	}
	out := []incident{}
	for rows.Next() {
		v := incident{appID: row.AppID, env: row.Env}
		if err = rows.Scan(&v.id, &v.title, &v.summary, &v.severity, &v.status, &v.alertStatus, &v.owner, &v.starts, &v.updated); err != nil {
			break
		}
		out = append(out, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) > maxIncidents {
		return nil, errTooMany
	}
	return out, nil
}

func loadProposals(ctx context.Context, tx pgx.Tx, row target) ([]proposal, error) {
	rows, err := tx.Query(ctx, `SELECT p.id,COALESCE(NULLIF(p.content->>'title',''),p.id),p.created_at
	 FROM v3_proposals p
	 JOIN applications a ON a.id=p.application_id AND NOT a.archived
	 JOIN v3_application_environments ve ON ve.application_id=p.application_id AND ve.environment_id=p.environment_id
	 WHERE p.application_id=$1 AND p.environment_id=$2 AND p.status='pending'
	 ORDER BY p.created_at DESC,p.id DESC LIMIT 501`, row.AppID, row.Env)
	if err != nil {
		return nil, err
	}
	out := []proposal{}
	for rows.Next() {
		p := proposal{appID: row.AppID, env: row.Env}
		if err = rows.Scan(&p.id, &p.title, &p.created); err != nil {
			break
		}
		out = append(out, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) > maxProposals {
		return nil, errTooMany
	}
	return out, nil
}

func buildActivity(incidents []incident, proposals []proposal, now time.Time, t text, out *Summary, ranges ...time.Duration) {
	duration := 24 * time.Hour
	if len(ranges) > 0 {
		duration = ranges[0]
	}
	type at struct {
		when  time.Time
		event Activity
	}
	items := make([]at, 0, len(incidents)+len(proposals))
	cutoff := now.Add(-duration)
	for _, v := range incidents {
		if v.updated.Before(cutoff) || v.updated.After(now) {
			continue
		}
		items = append(items, at{v.updated, Activity{ID: "incident:" + v.id, Time: v.updated.UTC().Format(time.RFC3339), Kind: "alert", Tone: "warn",
			Text: t.pick("事件记录更新：", "Incident updated: ") + cleanTitle(v.title), Href: "/alerts/" + v.id}})
	}
	for _, v := range proposals {
		if v.created.Before(cutoff) || v.created.After(now) {
			continue
		}
		items = append(items, at{v.created, Activity{ID: "proposal:" + v.id, Time: v.created.UTC().Format(time.RFC3339), Kind: "approval", Tone: "info",
			Text: t.pick("提案待复核：", "Proposal awaiting review: ") + cleanTitle(v.title), Href: "/review/" + v.id}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].when.After(items[j].when) })
	for _, v := range items {
		if len(out.Activity) >= 8 {
			break
		}
		out.Activity = append(out.Activity, v.event)
	}
	out.Availability["activity"] = "persisted_scoped"
}

func (s *Service) recentConversations(ctx context.Context, tx pgx.Tx, user core.User, envs []string, out *Summary, cutoffs ...time.Time) error {
	allowedEnvs := []string{}
	for _, env := range envs {
		err := s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: env})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return err
		}
		allowedEnvs = append(allowedEnvs, env)
	}
	if len(allowedEnvs) == 0 {
		out.Availability["recent"] = "not_authorized"
		return nil
	}
	var cutoff *time.Time
	if len(cutoffs) > 0 {
		cutoff = &cutoffs[0]
	}
	rows, err := tx.Query(ctx, `SELECT id,title,environment_id,updated_at
	 FROM v3_conversations WHERE owner_id=$1 AND environment_id=ANY($2::text[]) AND ($3::timestamptz IS NULL OR updated_at >= $3)
	 ORDER BY updated_at DESC,id DESC LIMIT 9`, user.ID, allowedEnvs, cutoff)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, title, env string
		var updated time.Time
		if err = rows.Scan(&id, &title, &env, &updated); err != nil {
			break
		}
		if len(out.Recent) == 8 {
			continue
		}
		out.Recent = append(out.Recent, Recent{ID: id, Kind: "conversation", Title: title,
			Meta: env + " · " + updated.UTC().Format(time.RFC3339), Href: "/conversations/" + id})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil {
		out.Availability["recent"] = "persisted_owner_only"
	}
	return err
}
