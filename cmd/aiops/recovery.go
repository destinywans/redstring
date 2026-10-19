package main

import (
	"context"
	"errors"
	"github.com/destinywans/redstring/internal/connectors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// BusinessVerification selects server-reviewed aggregate raw counters and an
// actual-source sample-age query. Query-range evaluation timestamps and positive
// rate() results alone do not establish new traffic during this observation.
type BusinessVerification struct {
	TelemetryApplication    string `json:"telemetry_application"`
	RequestCounterQueryID   string `json:"request_counter_query_id"`
	ErrorCounterQueryID     string `json:"error_counter_query_id"`
	SampleAgeQueryID        string `json:"sample_age_query_id"`
	MaximumSampleAgeSeconds int    `json:"maximum_sample_age_seconds"`
	ProbeURL                string `json:"probe_url"`
}
type businessRecovery struct {
	*connectors.Kubernetes
	telemetry *connectors.Telemetry
	policy    BusinessVerification
}

func validateBusinessVerification(p BusinessVerification) error {
	u, e := url.Parse(p.ProbeURL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || p.TelemetryApplication == "" || p.RequestCounterQueryID == "" || p.ErrorCounterQueryID == "" || p.SampleAgeQueryID == "" || p.MaximumSampleAgeSeconds < 5 || p.MaximumSampleAgeSeconds > 60 {
		return errors.New("registered raw request/error counters and actual-source sample age required")
	}
	return nil
}
func newBusinessRecovery(k *connectors.Kubernetes, t *connectors.Telemetry, p BusinessVerification) (*businessRecovery, error) {
	if p.MaximumSampleAgeSeconds == 0 {
		p.MaximumSampleAgeSeconds = 30
	}
	if e := validateBusinessVerification(p); e != nil {
		return nil, e
	}
	if t == nil {
		return nil, errors.New("registered telemetry required")
	}
	return &businessRecovery{k, t, p}, nil
}

type businessCounters struct{ requests, errors float64 }

func (r *businessRecovery) counters(ctx context.Context, now time.Time) (businessCounters, error) {
	window := connectors.TimeWindow{Start: now.Add(-10 * time.Second), End: now, StepSeconds: 5}
	values := []float64{}
	for _, id := range []string{r.policy.RequestCounterQueryID, r.policy.ErrorCounterQueryID, r.policy.SampleAgeQueryID} {
		metric, e := r.telemetry.QueryMetrics(ctx, r.policy.TelemetryApplication, id, window)
		if e != nil {
			return businessCounters{}, e
		}
		if len(metric.Series) != 1 {
			return businessCounters{}, errors.New("verification query must return exactly one registered aggregate series")
		}
		value, e := latestMetricValue(metric, now)
		if e != nil {
			return businessCounters{}, e
		}
		values = append(values, value)
	}
	if values[2] > float64(r.policy.MaximumSampleAgeSeconds) {
		return businessCounters{}, errors.New("actual source samples are stale")
	}
	for _, v := range values[:2] {
		if v != math.Trunc(v) || v > 9007199254740991 {
			return businessCounters{}, errors.New("raw integer business counters required")
		}
	}
	return businessCounters{values[0], values[1]}, nil
}
func validateBusinessCounterWindow(previous, current businessCounters) error {
	if current.requests <= previous.requests {
		return errors.New("no new business requests or request counter reset")
	}
	if current.errors != previous.errors {
		return errors.New("business errors or error counter reset")
	}
	return nil
}
func (r *businessRecovery) ObservePlan(ctx context.Context, p connectors.RollbackPlan, duration time.Duration) (connectors.RecoveryResult, error) {
	if duration < 180*time.Second {
		duration = 180 * time.Second
	}
	if minimum := time.Duration(p.ObservationSeconds) * time.Second; duration < minimum {
		duration = minimum
	}
	out := connectors.RecoveryResult{StartedAt: time.Now().UTC()}
	previous, e := r.counters(ctx, out.StartedAt)
	if e != nil {
		return out, e
	}
	initialRequests := previous.requests
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-tick.C:
			snap, e := r.Snapshot(ctx)
			if e != nil {
				return out, e
			}
			d := snap.Deployment
			n := d.Spec.Replicas
			if d.Metadata.UID != p.UID || snap.TemplateDigest != p.TargetTemplateDigest || n < 1 || d.Status.ObservedGeneration < d.Metadata.Generation || d.Status.Replicas != n || d.Status.ReadyReplicas != n || d.Status.UpdatedReplicas != n || d.Status.AvailableReplicas != n {
				out.Reason = "approved Deployment is not converged"
				out.FinishedAt = time.Now().UTC()
				return out, nil
			}
			req, e := http.NewRequestWithContext(ctx, http.MethodGet, r.policy.ProbeURL, nil)
			if e != nil {
				return out, e
			}
			res, e := client.Do(req)
			if e != nil {
				return out, e
			}
			io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				out.Reason = "registered business probe failed"
				out.FinishedAt = time.Now().UTC()
				return out, nil
			}
			now := time.Now().UTC()
			current, e := r.counters(ctx, now)
			if e != nil {
				return out, e
			}
			if e = validateBusinessCounterWindow(previous, current); e != nil {
				out.Reason = e.Error()
				out.FinishedAt = now
				return out, nil
			}
			previous = current
			out.Samples++
			out.Requests = uint64(current.requests - initialRequests)
			if time.Since(out.StartedAt) >= duration {
				out.Recovered = true
				out.FinishedAt = now
				return out, nil
			}
		}
	}
}
func latestMetricValue(v connectors.MetricEvidence, now time.Time) (float64, error) {
	if v.Empty || v.LimitReached || len(v.Series) == 0 {
		return 0, errors.New("complete business metrics required")
	}
	total := 0.0
	for _, series := range v.Series {
		if len(series.Samples) == 0 {
			return 0, errors.New("missing business sample")
		}
		sample := series.Samples[len(series.Samples)-1]
		value, e := strconv.ParseFloat(sample.Value, 64)
		if e != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || sample.Timestamp < float64(now.Add(-20*time.Second).Unix()) || sample.Timestamp > float64(now.Add(5*time.Second).Unix()) {
			return 0, errors.New("invalid or stale business sample")
		}
		total += value
	}
	return total, nil
}
