package v3resilience

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type Step struct {
	Weight string `json:"weight"`
	Note   string `json:"note"`
	State  string `json:"state"`
}
type Analysis struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Threshold string `json:"threshold"`
	OK        bool   `json:"ok"`
}
type Split struct {
	Stable float64 `json:"stable"`
	Canary float64 `json:"canary"`
}
type Rollout struct {
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	Stable       string     `json:"stable"`
	Steps        []Step     `json:"steps"`
	Split        Split      `json:"split"`
	Analysis     []Analysis `json:"analysis"`
	StableSeries []float64  `json:"stable_series"`
	CanarySeries []float64  `json:"canary_series"`
	Next         string     `json:"next"`
	Source       string     `json:"source"`
}
type Breaker struct {
	Brand     string `json:"brand"`
	Upstream  string `json:"upstream"`
	Mechanism string `json:"mechanism"`
	State     string `json:"state"`
	Threshold string `json:"threshold"`
	Ejected   string `json:"ejected"`
}
type BreakerInterval struct {
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	State string  `json:"state"`
}
type Limit struct {
	Brand     string  `json:"brand"`
	Route     string  `json:"route"`
	Mechanism string  `json:"mechanism"`
	Limit     string  `json:"limit"`
	Used      string  `json:"used"`
	Pct       float64 `json:"pct"`
	Rejected  int     `json:"rejected"`
}
type Flag struct {
	Brand string `json:"brand"`
	Key   string `json:"key"`
	Desc  string `json:"desc"`
	On    bool   `json:"on"`
	Tag   struct {
		Text string `json:"text"`
		Tone string `json:"tone"`
	} `json:"tag"`
	Action string `json:"action"`
}
type HPA struct {
	Min           int       `json:"min"`
	Max           int       `json:"max"`
	Current       int       `json:"current"`
	Desired       int       `json:"desired"`
	Replicas      []float64 `json:"replicas"`
	DesiredSeries []float64 `json:"desired_series"`
	CPU           []float64 `json:"cpu"`
	Target        float64   `json:"target"`
	Note          string    `json:"note"`
}

// View is the frozen frontend DTO. It is emitted only after a complete
// authorized observation; zero values never stand for absent source fields.
type View struct {
	Synced          string            `json:"synced"`
	Rollout         Rollout           `json:"rollout"`
	Breakers        []Breaker         `json:"breakers"`
	BreakerTimeline []BreakerInterval `json:"breaker_timeline"`
	BreakerNote     string            `json:"breaker_note"`
	Limits          []Limit           `json:"limits"`
	LimitNote       string            `json:"limit_note"`
	Flags           []Flag            `json:"flags"`
	HPA             HPA               `json:"hpa"`
	AI              []string          `json:"ai"`
}

// Observation is supplied by an operator-reviewed aggregate reader. Each
// source identity must correspond to the selected application/environment.
// Complete means the upstream positively attested its inventory, including
// an empty list. An empty source ID means unknown coverage.
type Observation struct {
	ApplicationID string                    `json:"application_id"`
	EnvironmentID string                    `json:"environment_id"`
	CapturedAt    time.Time                 `json:"captured_at"`
	Sources       map[string]SourceEvidence `json:"sources"` // rollout, breakers, limits, flags, hpa
	View          View                      `json:"view"`
}

type SourceEvidence struct {
	ID            string    `json:"id"`
	ApplicationID string    `json:"application_id"`
	EnvironmentID string    `json:"environment_id"`
	Revision      string    `json:"revision"`
	CapturedAt    time.Time `json:"captured_at"`
	Complete      bool      `json:"complete"`
	ItemCount     int       `json:"item_count"`
}

func finitePercent(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100 }

func validTone(v string) bool {
	switch v {
	case "neutral", "accent", "ok", "warn", "danger", "info":
		return true
	default:
		return false
	}
}

func validSeries(v []float64, max int) bool {
	if len(v) == 0 || len(v) > max {
		return false
	}
	for _, n := range v {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return false
		}
	}
	return true
}

func complete(o Observation, t target, now time.Time) bool {
	if o.ApplicationID != t.ApplicationID || o.EnvironmentID != t.EnvironmentID || o.CapturedAt.IsZero() ||
		o.CapturedAt.Before(now.Add(-5*time.Minute)) || o.CapturedAt.After(now.Add(30*time.Second)) {
		return false
	}
	if len(o.Sources) != 5 {
		return false
	}
	for _, kind := range []string{"rollout", "breakers", "limits", "flags", "hpa"} {
		src := o.Sources[kind]
		if !src.Complete || src.ItemCount < 0 || src.ID == "" || src.Revision == "" || src.ApplicationID != t.ApplicationID ||
			src.EnvironmentID != t.EnvironmentID || src.CapturedAt.IsZero() ||
			src.CapturedAt.Before(now.Add(-5*time.Minute)) || src.CapturedAt.After(now.Add(30*time.Second)) ||
			src.CapturedAt.After(o.CapturedAt.Add(30*time.Second)) {
			return false
		}
	}
	v := o.View
	if o.Sources["rollout"].ItemCount != 1 || o.Sources["breakers"].ItemCount != len(v.Breakers) ||
		o.Sources["limits"].ItemCount != len(v.Limits) || o.Sources["flags"].ItemCount != len(v.Flags) ||
		o.Sources["hpa"].ItemCount != 1 {
		return false
	}
	if v.Rollout.Title == "" || v.Rollout.Status == "" || v.Rollout.Stable == "" || v.Rollout.Next == "" ||
		v.Rollout.Source == "" || len(v.Rollout.Steps) == 0 || len(v.Rollout.Steps) > 20 ||
		v.Rollout.Analysis == nil || !finitePercent(v.Rollout.Split.Stable) || !finitePercent(v.Rollout.Split.Canary) ||
		math.Abs(v.Rollout.Split.Stable+v.Rollout.Split.Canary-100) > 0.01 ||
		!validSeries(v.Rollout.StableSeries, 120) || !validSeries(v.Rollout.CanarySeries, 120) ||
		len(v.Rollout.StableSeries) != len(v.Rollout.CanarySeries) ||
		v.Breakers == nil || len(v.Breakers) > 100 || v.BreakerTimeline == nil || len(v.BreakerTimeline) > 120 || v.BreakerNote == "" ||
		v.Limits == nil || len(v.Limits) > 100 || v.LimitNote == "" || v.Flags == nil || len(v.Flags) > 100 || v.AI == nil || len(v.AI) > 20 ||
		v.HPA.Min < 0 || v.HPA.Max < 1 || v.HPA.Max < v.HPA.Min || v.HPA.Current < 0 || v.HPA.Desired < 0 ||
		!finitePercent(v.HPA.Target) || !validSeries(v.HPA.Replicas, 120) ||
		!validSeries(v.HPA.DesiredSeries, 120) || !validSeries(v.HPA.CPU, 120) ||
		len(v.HPA.Replicas) != len(v.HPA.DesiredSeries) || len(v.HPA.CPU) != len(v.HPA.Replicas) || v.HPA.Note == "" {
		return false
	}
	for _, st := range v.Rollout.Steps {
		if st.Weight == "" || st.Note == "" || (st.State != "done" && st.State != "current" && st.State != "todo") {
			return false
		}
	}
	for _, a := range v.Rollout.Analysis {
		if a.Name == "" || a.Value == "" || a.Threshold == "" {
			return false
		}
	}
	for _, b := range v.Breakers {
		if b.Brand == "" || b.Upstream == "" || b.Mechanism == "" || b.Threshold == "" || b.Ejected == "" ||
			(b.State != "closed" && b.State != "open" && b.State != "half_open" && b.State != "none") {
			return false
		}
	}
	for _, b := range v.BreakerTimeline {
		if !finitePercent(b.From) || !finitePercent(b.To) || b.From >= b.To ||
			(b.State != "closed" && b.State != "open" && b.State != "half_open") {
			return false
		}
	}
	for _, l := range v.Limits {
		if l.Brand == "" || l.Route == "" || l.Mechanism == "" || l.Limit == "" || l.Used == "" || !finitePercent(l.Pct) || l.Rejected < 0 {
			return false
		}
	}
	for _, f := range v.Flags {
		if f.Brand == "" || f.Key == "" || f.Desc == "" || f.Tag.Text == "" || !validTone(f.Tag.Tone) || f.Action == "" {
			return false
		}
	}
	for _, note := range v.AI {
		if note == "" {
			return false
		}
	}
	return true
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil || s.Now == nil {
		writeError(w, nil)
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_filter", "Resilience detail accepts no filters")
		return
	}
	t, ok := s.selected(r)
	if !ok {
		httpapi.Error(w, 404, "not_found", "Service not found in selected environment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	pre, err := s.DB.Begin(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	defer pre.Rollback(ctx)
	t, err = s.loadTarget(ctx, pre, t)
	if err == nil {
		err = s.authorize(ctx, pre, user, t, "services", "view")
	}
	if err == nil {
		err = pre.Commit(ctx)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if s.Reader == nil {
		// Not connected is its own code, so the page doesn't call it a failed
		// or timed-out source (PA-171).
		httpapi.Error(w, 503, "resilience_not_connected", "Five scoped resilience sources are not connected")
		return
	}
	o, err := s.Reader.ReadResilience(ctx, t.ApplicationID, t.EnvironmentID)
	if err != nil || !complete(o, t, s.Now()) {
		httpapi.Error(w, 503, "resilience_source_unavailable", "Complete current resilience evidence is unavailable")
		return
	}
	// Reopen after external reads so a source call holds no database locks.
	post, err := s.DB.Begin(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	defer post.Rollback(ctx)
	current, err := s.loadTarget(ctx, post, t)
	if err == nil && !reflect.DeepEqual(t, current) {
		err = store.ErrConflict
	}
	cookie, cookieErr := r.Cookie("aiops_session")
	if err == nil && cookieErr != nil {
		err = store.ErrSessionRevoked
	}
	if err == nil {
		err = store.LockActiveSession(ctx, post, cookie.Value, user.ID)
	}
	if err == nil {
		err = lockAuthorityTables(ctx, post)
	}
	if err == nil {
		err = s.authorize(ctx, post, user, current, "services", "view")
	}
	if err == nil {
		err = sessionCurrent(ctx, post, cookie.Value, user.ID)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	// Source seals and scope were validated above. A reader that cannot supply
	// a stable complete snapshot must return an error rather than merge samples.
	v := o.View
	v.Synced = o.CapturedAt.UTC().Format(time.RFC3339)
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, v)
	// The read-only transaction's deferred rollback releases session, target
	// and authority locks only after the response has been written.
}
