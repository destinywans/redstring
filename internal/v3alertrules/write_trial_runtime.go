package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

const (
	trialStep       = time.Minute
	trialMaxPoints  = 1440
	trialMaxSeries  = 100
	trialMaxWindows = 500
)

// TrialEndpoint must address a deployment-enforced single-application query
// view. A normal environment-wide Prometheus endpoint is forbidden here. The
// private config owner must verify that its read credential cannot query any
// other application's samples, including through recording rules and metadata
// functions. A normal shared environment Prometheus cannot satisfy this
// contract. Distinct URLs and credentials avoid reuse but are not proof; the
// explicit review attestation is required and remains an operational claim.
type TrialEndpoint struct {
	EnvironmentID, ApplicationID string
	URL                          string
	BearerTokenFile              string
	AllowHTTP                    bool // test-only isolated endpoint
	IsolationAttested            bool
	IsolationReviewedBy          string
	IsolationReviewedAt          time.Time
}

type PrometheusTrialRuntime struct {
	service   *WriteService
	endpoints map[string]TrialEndpoint
	client    *http.Client
	running   atomic.Bool
}

func trialEndpointKey(env, app string) string { return env + "\x00" + app }

// LoadPrometheusTrialEndpoints reads private deployment configuration. The
// endpoint for each pair must be a genuinely application-isolated query view;
// this loader validates shape, not the upstream proxy's data-plane policy.
func LoadPrometheusTrialEndpoints(path string) ([]TrialEndpoint, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return nil, errors.New("trial endpoint configuration exceeds limit")
	}
	var config struct {
		Endpoints []struct {
			EnvironmentID       string `json:"environment_id"`
			ApplicationID       string `json:"application_id"`
			URL                 string `json:"url"`
			BearerTokenFile     string `json:"bearer_token_file"`
			AllowHTTP           bool   `json:"allow_http"`
			IsolationAttested   bool   `json:"isolation_attested"`
			IsolationReviewedBy string `json:"isolation_reviewed_by"`
			IsolationReviewedAt string `json:"isolation_reviewed_at"`
		} `json:"endpoints"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(config.Endpoints) == 0 || len(config.Endpoints) > 100 {
		return nil, errors.New("invalid trial endpoint configuration")
	}
	result := make([]TrialEndpoint, 0, len(config.Endpoints))
	for _, item := range config.Endpoints {
		reviewedAt, err := time.Parse(time.RFC3339, item.IsolationReviewedAt)
		if err != nil || reviewedAt.After(time.Now().Add(5*time.Minute)) {
			return nil, errors.New("invalid trial isolation review")
		}
		result = append(result, TrialEndpoint{EnvironmentID: item.EnvironmentID,
			ApplicationID: item.ApplicationID, URL: item.URL,
			BearerTokenFile: item.BearerTokenFile, AllowHTTP: item.AllowHTTP,
			IsolationAttested: item.IsolationAttested, IsolationReviewedBy: item.IsolationReviewedBy,
			IsolationReviewedAt: reviewedAt})
	}
	return result, nil
}

func NewPrometheusTrialRuntime(service *WriteService, endpoints []TrialEndpoint, client *http.Client) (*PrometheusTrialRuntime, error) {
	if service == nil || service.DB == nil || service.Access == nil || len(endpoints) == 0 {
		return nil, errors.New("trial runtime has no database, access policy, or isolated endpoints")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	runtime := &PrometheusTrialRuntime{service: service, endpoints: make(map[string]TrialEndpoint, len(endpoints)), client: &copyClient}
	usedURLs := make(map[string]bool, len(endpoints))
	usedTokens := make(map[string]bool, len(endpoints))
	for _, endpoint := range endpoints {
		key := trialEndpointKey(endpoint.EnvironmentID, endpoint.ApplicationID)
		u, err := url.Parse(endpoint.URL)
		canonicalURL := strings.TrimRight(endpoint.URL, "/")
		if endpoint.EnvironmentID == "" || endpoint.EnvironmentID == "all" || endpoint.ApplicationID == "" ||
			!endpoint.IsolationAttested || strings.TrimSpace(endpoint.IsolationReviewedBy) == "" ||
			endpoint.IsolationReviewedAt.IsZero() || endpoint.IsolationReviewedAt.After(time.Now().Add(5*time.Minute)) ||
			strings.ContainsRune(endpoint.EnvironmentID, 0) || strings.ContainsRune(endpoint.ApplicationID, 0) ||
			err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(endpoint.AllowHTTP && u.Scheme == "http")) ||
			(endpoint.BearerTokenFile == "" && !endpoint.AllowHTTP) ||
			(endpoint.BearerTokenFile != "" && !filepath.IsAbs(endpoint.BearerTokenFile)) ||
			strings.ContainsAny(endpoint.BearerTokenFile, "\r\n") ||
			usedURLs[canonicalURL] || (endpoint.BearerTokenFile != "" && usedTokens[endpoint.BearerTokenFile]) ||
			runtime.endpoints[key].URL != "" {
			return nil, errors.New("invalid or shared application-isolated trial endpoint")
		}
		usedURLs[canonicalURL] = true
		if endpoint.BearerTokenFile != "" {
			usedTokens[endpoint.BearerTokenFile] = true
		}
		runtime.endpoints[key] = endpoint
	}
	return runtime, nil
}

func (p *PrometheusTrialRuntime) IsolationFingerprint(_ context.Context, env, app string) (string, error) {
	if p == nil {
		return "", errors.New("trial runtime unavailable")
	}
	endpoint, ok := p.endpoints[trialEndpointKey(env, app)]
	if !ok {
		return "", errors.New("application-isolated endpoint missing")
	}
	// Include the credential content digest so a rotation invalidates an active
	// trial's source binding. The credential and its digest never enter an API or
	// audit record; the returned value hashes the entire binding once more.
	var tokenDigest [32]byte
	if endpoint.BearerTokenFile != "" {
		token, err := os.ReadFile(endpoint.BearerTokenFile)
		if err != nil || len(token) == 0 || len(token) > 16<<10 {
			return "", errors.New("trial credential unavailable")
		}
		trimmed := strings.TrimSpace(string(token))
		if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
			return "", errors.New("trial credential invalid")
		}
		tokenDigest = sha256.Sum256([]byte(trimmed))
	}
	material := env + "\x00" + app + "\x00" + endpoint.URL + "\x00" + hex.EncodeToString(tokenDigest[:]) +
		"\x00" + endpoint.IsolationReviewedBy + "\x00" + endpoint.IsolationReviewedAt.UTC().Format(time.RFC3339Nano)
	hash := sha256.Sum256([]byte(material))
	return hex.EncodeToString(hash[:]), nil
}

func (p *PrometheusTrialRuntime) Enqueue(ctx context.Context, job TrialJob) error {
	if p == nil || !p.running.Load() || job.Duration != trialDuration {
		return errors.New("durable trial worker is not running")
	}
	fingerprint, err := p.IsolationFingerprint(ctx, job.EnvironmentID, job.ApplicationID)
	if err != nil || fingerprint != job.SourceFingerprint {
		return errors.New("trial source changed")
	}
	// The row is already committed and the worker polls it. No volatile queue
	// message is required, so process restart preserves the accepted job.
	return nil
}

// Run is the independent durable worker loop. It is intentionally opt-in:
// composition must start it and bind this runtime to WriteService before the
// create route can succeed. Cancellation drops running readiness immediately;
// any leased row is retried after its lease expires by another worker.
func (p *PrometheusTrialRuntime) Run(ctx context.Context) error {
	if p == nil || p.service == nil || !p.running.CompareAndSwap(false, true) {
		return errors.New("trial worker already running or unavailable")
	}
	defer p.running.Store(false)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// A request can reserve a slot and lose its connection before worker
		// acknowledgement. Reclaim that slot; it was never accepted as queued.
		_, _ = p.service.DB.Exec(ctx, `UPDATE v3_alert_rule_trials
 SET status='stopped',updated_at=clock_timestamp()
 WHERE status='dispatch_pending' AND created_at<clock_timestamp()-interval '1 minute'`)
		if err := p.runOne(ctx); err != nil && ctx.Err() == nil {
			// A database outage leaves persisted jobs queued. Retry on the next
			// poll; never convert an unobserved interval into success.
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type trialSeriesState struct {
	First       time.Time `json:"first"`
	WindowIndex int       `json:"window_index"` // -1 until for is satisfied
}

type claimedTrial struct {
	trialRecord
	Token, NextAt string
	Active        map[string]trialSeriesState
	Next          time.Time
}

func (p *PrometheusTrialRuntime) claim(ctx context.Context) (claimedTrial, bool, error) {
	tx, err := p.service.DB.Begin(ctx)
	if err != nil {
		return claimedTrial{}, false, err
	}
	defer tx.Rollback(ctx)
	var row claimedTrial
	var specRaw, windowsRaw, activeRaw []byte
	err = tx.QueryRow(ctx, `SELECT id,actor_id,environment_id,application_id,source_fingerprint,provider_file_digest,spec,status,
 started_at,expires_at,next_eval_at,evaluated_points,missing_points,would_fire,windows,active_series
 FROM v3_alert_rule_trials WHERE status IN ('queued','running') AND next_eval_at<=clock_timestamp()
 AND (lease_until IS NULL OR lease_until<clock_timestamp())
 ORDER BY next_eval_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(
		&row.ID, &row.ActorID, &row.EnvironmentID, &row.ApplicationID, &row.SourceFingerprint, &row.ProviderFileDigest,
		&specRaw, &row.Status, &row.StartedAt, &row.ExpiresAt, &row.Next,
		&row.EvaluatedPoints, &row.MissingPoints, &row.WouldFire, &windowsRaw, &activeRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimedTrial{}, false, nil
	}
	if err != nil {
		return claimedTrial{}, false, err
	}
	if json.Unmarshal(specRaw, &row.Spec) != nil || json.Unmarshal(windowsRaw, &row.Windows) != nil ||
		json.Unmarshal(activeRaw, &row.Active) != nil {
		return claimedTrial{}, false, errors.New("invalid trial state")
	}
	if row.Active == nil {
		row.Active = map[string]trialSeriesState{}
	}
	row.Token = store.ID()
	_, err = tx.Exec(ctx, `UPDATE v3_alert_rule_trials SET lease_token=$2,lease_until=clock_timestamp()+interval '60 seconds'
 WHERE id=$1`, row.ID, row.Token)
	if err != nil {
		return claimedTrial{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return claimedTrial{}, false, err
	}
	return row, true, nil
}

func (p *PrometheusTrialRuntime) runOne(ctx context.Context) error {
	row, ok, err := p.claim(ctx)
	if err != nil || !ok {
		return err
	}
	return p.evaluate(ctx, row)
}

func (p *PrometheusTrialRuntime) evaluate(ctx context.Context, row claimedTrial) error {
	forDuration, parseErr := time.ParseDuration(row.Spec.For)
	if row.Spec.Mode != "dry_run" || row.Spec.EnvironmentID != row.EnvironmentID ||
		row.Spec.ApplicationID != row.ApplicationID || parseErr != nil ||
		forDuration < 0 || forDuration > dryRunMaximumFor || len(row.Spec.Expr) == 0 || len(row.Spec.Expr) > 8192 {
		return p.saveTick(ctx, row, "inconclusive")
	}
	now := time.Now().UTC()
	if row.StartedAt == nil {
		start := now
		end := start.Add(trialDuration)
		row.StartedAt, row.ExpiresAt, row.Next = &start, &end, start
	}
	if !row.Next.Before(*row.ExpiresAt) || !now.Before(*row.ExpiresAt) {
		remaining := trialMaxPoints - row.EvaluatedPoints - row.MissingPoints
		if remaining > 0 {
			row.MissingPoints += remaining
		}
		status := "completed"
		if row.MissingPoints > 0 {
			status = "inconclusive"
		}
		return p.saveTick(ctx, row, status)
	}
	// Never evaluate an old instant as though the worker had been running.
	if delay := now.Sub(row.Next); delay > 30*time.Second {
		missed := int(delay/trialStep) + 1
		if missed > trialMaxPoints-row.EvaluatedPoints-row.MissingPoints {
			missed = trialMaxPoints - row.EvaluatedPoints - row.MissingPoints
		}
		row.MissingPoints += missed
		row.Active = map[string]trialSeriesState{}
		row.Next = row.Next.Add(time.Duration(missed) * trialStep)
		return p.saveTick(ctx, row, "running")
	}
	actor := core.User{ID: row.ActorID}
	if err := p.service.authorize(ctx, actor, row.EnvironmentID, row.ApplicationID); err != nil {
		return p.saveTick(ctx, row, "stopped")
	}
	fingerprint, err := p.IsolationFingerprint(ctx, row.EnvironmentID, row.ApplicationID)
	if err != nil || fingerprint != row.SourceFingerprint {
		return p.saveTick(ctx, row, "stopped")
	}
	if same, err := p.ruleSourceUnchanged(ctx, row); err != nil {
		return p.saveTick(ctx, row, "inconclusive")
	} else if !same {
		return p.saveTick(ctx, row, "stopped")
	}
	labels, queryErr := p.queryInstant(ctx, row, row.Next)
	// Source and grant changes during the remote read must suppress the result.
	if err := p.service.authorize(ctx, actor, row.EnvironmentID, row.ApplicationID); err != nil {
		return p.saveTick(ctx, row, "stopped")
	}
	fingerprint, err = p.IsolationFingerprint(ctx, row.EnvironmentID, row.ApplicationID)
	if err != nil || fingerprint != row.SourceFingerprint {
		return p.saveTick(ctx, row, "stopped")
	}
	if same, err := p.ruleSourceUnchanged(ctx, row); err != nil {
		return p.saveTick(ctx, row, "inconclusive")
	} else if !same {
		return p.saveTick(ctx, row, "stopped")
	}
	if queryErr != nil {
		row.MissingPoints++
		row.Active = map[string]trialSeriesState{}
	} else if err := applyTrialVector(&row, labels); err != nil {
		row.MissingPoints++
		row.Active = map[string]trialSeriesState{}
		return p.saveTick(ctx, row, "inconclusive")
	} else {
		row.EvaluatedPoints++
	}
	row.Next = row.Next.Add(trialStep)
	return p.saveTick(ctx, row, "running")
}

// The reviewed group-to-application binding and provider rule absence are
// checked on both sides of every remote metric read. A source outage is an
// inconclusive trial; a changed file binding or occupied rule identity stops
// the trial without publishing the metric result.
func (p *PrometheusTrialRuntime) ruleSourceUnchanged(ctx context.Context, row claimedTrial) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	groups, observed, err := p.service.inspectCandidates(checkCtx, row.EnvironmentID, row.ApplicationID, []DraftRuleSpec{row.Spec})
	if err != nil {
		return false, err
	}
	return len(groups) == 1 && len(observed) == 1 &&
		fileIdentityDigest(groups[0].file) == row.ProviderFileDigest && !observed[0].exists, nil
}

func applyTrialVector(row *claimedTrial, vector []map[string]string) error {
	if len(vector) > trialMaxSeries || len(row.Windows) > trialMaxWindows {
		return errors.New("trial result limit")
	}
	next := make(map[string]trialSeriesState, len(vector))
	windows := make([]string, len(row.Windows))
	copy(windows, row.Windows)
	for _, labels := range vector {
		if labels["environment"] != row.EnvironmentID || labels["application"] != row.ApplicationID || len(labels) > 64 {
			return errors.New("unscoped trial vector")
		}
		encoded, err := json.Marshal(labels)
		if err != nil || len(encoded) > 8192 {
			return errors.New("invalid trial labels")
		}
		hash := sha256.Sum256(encoded)
		key := hex.EncodeToString(hash[:])
		if _, duplicate := next[key]; duplicate {
			return errors.New("duplicate trial vector")
		}
		state, found := row.Active[key]
		if !found {
			state = trialSeriesState{First: row.Next, WindowIndex: -1}
		}
		if state.WindowIndex == -1 && !row.Next.Before(state.First.Add(mustTrialFor(row.Spec.For))) {
			if len(windows) == trialMaxWindows {
				return errors.New("trial firing window limit")
			}
			state.WindowIndex = len(windows)
			windows = append(windows, row.Next.Format(time.RFC3339Nano)+"/"+row.Next.Format(time.RFC3339Nano))
		} else if state.WindowIndex >= 0 {
			if state.WindowIndex >= len(windows) {
				return errors.New("invalid trial state")
			}
			start, _, _ := strings.Cut(windows[state.WindowIndex], "/")
			windows[state.WindowIndex] = start + "/" + row.Next.Format(time.RFC3339Nano)
		}
		next[key] = state
	}
	row.Active = next
	row.Windows = windows
	row.WouldFire = len(windows)
	return nil
}

func mustTrialFor(raw string) time.Duration {
	d, _ := time.ParseDuration(raw) // validated before persistence
	return d
}

func (p *PrometheusTrialRuntime) saveTick(ctx context.Context, row claimedTrial, status string) error {
	active, err := json.Marshal(row.Active)
	if err != nil {
		return err
	}
	windows, err := json.Marshal(row.Windows)
	if err != nil {
		return err
	}
	if len(active) > 32<<10 || len(windows) > 64<<10 || row.EvaluatedPoints+row.MissingPoints > trialMaxPoints {
		return errors.New("trial state exceeds bound")
	}
	tag, err := p.service.DB.Exec(ctx, `UPDATE v3_alert_rule_trials SET status=$3,started_at=$4,expires_at=$5,
 next_eval_at=$6,evaluated_points=$7,missing_points=$8,would_fire=$9,windows=$10::jsonb,
 active_series=$11::jsonb,lease_token='',lease_until=NULL,updated_at=clock_timestamp()
 WHERE id=$1 AND lease_token=$2 AND status IN ('queued','running')`,
		row.ID, row.Token, status, row.StartedAt, row.ExpiresAt, row.Next,
		row.EvaluatedPoints, row.MissingPoints, row.WouldFire, windows, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("trial lease changed")
	}
	return nil
}

func (p *PrometheusTrialRuntime) queryInstant(ctx context.Context, row claimedTrial, at time.Time) ([]map[string]string, error) {
	endpoint, ok := p.endpoints[trialEndpointKey(row.EnvironmentID, row.ApplicationID)]
	if !ok {
		return nil, errors.New("trial endpoint missing")
	}
	form := url.Values{"query": {row.Spec.Expr}, "time": {prometheusTimestamp(at)}, "timeout": {"15s"}, "limit": {"101"}}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		strings.TrimRight(endpoint.URL, "/")+"/api/v1/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if endpoint.BearerTokenFile != "" {
		token, err := os.ReadFile(endpoint.BearerTokenFile)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(string(token))
		if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
			return nil, errors.New("invalid trial credential")
		}
		req.Header.Set("Authorization", "Bearer "+trimmed)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trial query HTTP %d", resp.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(contents) > 2<<20 {
		return nil, errors.New("trial query response exceeds limit")
	}
	var envelope struct {
		Status   string          `json:"status"`
		Warnings json.RawMessage `json:"warnings"`
		Infos    json.RawMessage `json:"infos"`
		Data     *struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Status != "success" ||
		envelope.Data == nil || envelope.Data.ResultType != "vector" ||
		noPrometheusMessages(envelope.Warnings) != nil || noPrometheusMessages(envelope.Infos) != nil {
		return nil, errors.New("invalid or incomplete trial query response")
	}
	var rows []struct {
		Metric map[string]string `json:"metric"`
		Value  []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(envelope.Data.Result, &rows) != nil || rows == nil || len(rows) > trialMaxSeries {
		return nil, errors.New("invalid trial vector")
	}
	vector := make([]map[string]string, 0, len(rows))
	for _, item := range rows {
		if item.Metric == nil || len(item.Value) != 2 {
			return nil, errors.New("invalid trial vector element")
		}
		seconds, err := strconv.ParseFloat(string(item.Value[0]), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Abs(seconds-float64(at.UnixNano())/1e9) > .05 {
			return nil, errors.New("invalid trial evaluation timestamp")
		}
		var rawValue string
		if json.Unmarshal(item.Value[1], &rawValue) != nil {
			return nil, errors.New("invalid trial sample")
		}
		value, err := strconv.ParseFloat(rawValue, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("invalid trial sample")
		}
		vector = append(vector, item.Metric)
	}
	// Keep processing stable even when the provider shuffles vector elements.
	sort.Slice(vector, func(i, j int) bool {
		a, _ := json.Marshal(vector[i])
		b, _ := json.Marshal(vector[j])
		return string(a) < string(b)
	})
	return vector, nil
}
