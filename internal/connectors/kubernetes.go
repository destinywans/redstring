// Package connectors contains external-system adapters. It does not grant approval;
// the application must persist and authorize the exact plan before calling Execute.
package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrDrift = errors.New("deployment changed: a new proposal and approval are required")
var ErrAmbiguous = errors.New("execution outcome unknown: reconcile before any further write")
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

const operationAnnotation = "aiops-canvas.io/operation"

type Config struct {
	APIURL     string `json:"api_url"`
	CAFile     string `json:"ca_file"`
	TokenFile  string `json:"token_file"`
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
	Service    string `json:"service"`
	Port       int    `json:"port"`
}
type Kubernetes struct {
	config Config
	client *http.Client
}
type Metadata struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Generation      int64             `json:"generation"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []struct {
		UID        string `json:"uid"`
		Controller bool   `json:"controller"`
	} `json:"ownerReferences"`
}
type Deployment struct {
	RawSpec  json.RawMessage `json:"-"`
	Metadata Metadata        `json:"metadata"`
	Spec     struct {
		Replicas int             `json:"replicas"`
		Template json.RawMessage `json:"template"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64 `json:"observedGeneration"`
		Replicas           int   `json:"replicas"`
		UpdatedReplicas    int   `json:"updatedReplicas"`
		AvailableReplicas  int   `json:"availableReplicas"`
		ReadyReplicas      int   `json:"readyReplicas"`
	} `json:"status"`
}
type Revision struct {
	Revision string          `json:"revision"`
	Template json.RawMessage `json:"template"`
	Digest   string          `json:"digest"`
}
type Snapshot struct {
	Deployment     Deployment `json:"deployment"`
	TemplateDigest string     `json:"template_digest"`
	Revisions      []Revision `json:"revisions"`
	CapturedAt     time.Time  `json:"captured_at"`
}
type RollbackPlan struct {
	Namespace              string          `json:"namespace"`
	Deployment             string          `json:"deployment"`
	UID                    string          `json:"uid"`
	ExpectedTemplateDigest string          `json:"expected_template_digest"`
	ExpectedSpecDigest     string          `json:"expected_spec_digest"`
	TargetTemplate         json.RawMessage `json:"target_template"`
	TargetTemplateDigest   string          `json:"target_template_digest"`
	TargetRevision         string          `json:"target_revision"`
	ObservationSeconds     int             `json:"observation_seconds"`
	Digest                 string          `json:"digest"`
}
type ExecutionResult struct {
	State       string `json:"state"`
	OperationID string `json:"operation_id"`
	Generation  int64  `json:"generation"`
	Reason      string `json:"reason,omitempty"`
}
type Metrics struct {
	Requests uint64 `json:"requests"`
	Errors   uint64 `json:"errors"`
	Instance string `json:"instance"`
}
type Evidence struct {
	Snapshot Snapshot          `json:"snapshot"`
	Metrics  Metrics           `json:"metrics"`
	Healthy  bool              `json:"healthy"`
	Logs     map[string]string `json:"logs"`
}
type RecoveryResult struct {
	Recovered  bool      `json:"recovered"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Samples    int       `json:"samples"`
	Requests   uint64    `json:"requests"`
	Reason     string    `json:"reason,omitempty"`
}

func New(c Config) (*Kubernetes, error) {
	u, err := url.Parse(c.APIURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("Kubernetes API must be an HTTPS URL without credentials")
	}
	for _, n := range []string{c.Namespace, c.Deployment, c.Service} {
		if !namePattern.MatchString(n) {
			return nil, errors.New("invalid registered Kubernetes resource name")
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return nil, errors.New("invalid service port")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Kubernetes{c, &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func LoadConfig(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func (k *Kubernetes) request(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, 0, e
		}
		r = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(k.config.APIURL, "/")+path, r)
	if e != nil {
		return nil, 0, e
	}
	token, e := os.ReadFile(k.config.TokenFile)
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json-patch+json")
	}
	resp, e := k.client.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if e != nil {
		return nil, resp.StatusCode, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return b, resp.StatusCode, fmt.Errorf("Kubernetes %s returned HTTP %d", method, resp.StatusCode)
	}
	return b, resp.StatusCode, nil
}
func (k *Kubernetes) deploymentPath() string {
	return "/apis/apps/v1/namespaces/" + k.config.Namespace + "/deployments/" + k.config.Deployment
}
func (k *Kubernetes) current(ctx context.Context) (Deployment, error) {
	var d Deployment
	b, _, e := k.request(ctx, "GET", k.deploymentPath(), nil)
	if e == nil {
		e = json.Unmarshal(b, &d)
		if e == nil {
			var raw struct {
				Spec json.RawMessage `json:"spec"`
			}
			e = json.Unmarshal(b, &raw)
			d.RawSpec = raw.Spec
		}
	}
	return d, e
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Canonicalize nested templates so PostgreSQL jsonb key order does not invalidate plans.
func planDigest(p RollbackPlan) string { b, _ := json.Marshal(p); return templateDigest(b) }
func templateDigest(t json.RawMessage) string {
	var v any
	if json.Unmarshal(t, &v) != nil {
		return ""
	}
	return digest(v)
}

// ReplicaSets add a controller hash label which must not enter the Deployment template.
func cleanTemplate(t json.RawMessage) (json.RawMessage, error) {
	var v map[string]any
	if e := json.Unmarshal(t, &v); e != nil {
		return nil, e
	}
	if m, ok := v["metadata"].(map[string]any); ok {
		if l, ok := m["labels"].(map[string]any); ok {
			delete(l, "pod-template-hash")
		}
	}
	return json.Marshal(v)
}
func (k *Kubernetes) Snapshot(ctx context.Context) (Snapshot, error) {
	d, e := k.current(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	s := Snapshot{Deployment: d, TemplateDigest: templateDigest(d.Spec.Template), CapturedAt: time.Now().UTC()}
	b, _, e := k.request(ctx, "GET", "/apis/apps/v1/namespaces/"+k.config.Namespace+"/replicasets", nil)
	if e != nil {
		return s, e
	}
	var list struct {
		Items []Deployment `json:"items"`
	}
	if e = json.Unmarshal(b, &list); e != nil {
		return s, e
	}
	for _, rs := range list.Items {
		owned := false
		for _, o := range rs.Metadata.OwnerReferences {
			if o.UID == d.Metadata.UID && o.Controller {
				owned = true
			}
		}
		if !owned {
			continue
		}
		t, e := cleanTemplate(rs.Spec.Template)
		if e != nil {
			return s, e
		}
		s.Revisions = append(s.Revisions, Revision{rs.Metadata.Annotations["deployment.kubernetes.io/revision"], t, templateDigest(t)})
	}
	sort.Slice(s.Revisions, func(i, j int) bool {
		a, _ := strconv.Atoi(s.Revisions[i].Revision)
		b, _ := strconv.Atoi(s.Revisions[j].Revision)
		return a < b
	})
	return s, nil
}
func (k *Kubernetes) ProposeRollback(ctx context.Context, targetRevision string) (RollbackPlan, error) {
	s, e := k.Snapshot(ctx)
	if e != nil {
		return RollbackPlan{}, e
	}
	for _, r := range s.Revisions {
		if r.Revision == targetRevision {
			if r.Digest == s.TemplateDigest {
				return RollbackPlan{}, errors.New("target is already current")
			}
			p := RollbackPlan{Namespace: k.config.Namespace, Deployment: k.config.Deployment, UID: s.Deployment.Metadata.UID, ExpectedTemplateDigest: s.TemplateDigest, ExpectedSpecDigest: templateDigest(s.Deployment.RawSpec), TargetTemplate: r.Template, TargetTemplateDigest: r.Digest, TargetRevision: r.Revision, ObservationSeconds: 180}
			p.Digest = planDigest(p)
			return p, nil
		}
	}
	return RollbackPlan{}, errors.New("target revision is not owned by registered Deployment")
}
func (k *Kubernetes) validate(p RollbackPlan) error {
	want := p.Digest
	p.Digest = ""
	if want == "" || planDigest(p) != want || templateDigest(p.TargetTemplate) != p.TargetTemplateDigest {
		return errors.New("invalid or modified rollback plan")
	}
	if p.Namespace != k.config.Namespace || p.Deployment != k.config.Deployment || p.UID == "" || p.ObservationSeconds < 180 {
		return errors.New("plan does not match registered target or minimum observation")
	}
	return nil
}

// Execute applies one CAS write. A timeout must be reconciled; this function never retries writes.
func (k *Kubernetes) Execute(ctx context.Context, p RollbackPlan, operationID string) (ExecutionResult, error) {
	if e := k.validate(p); e != nil {
		return ExecutionResult{}, e
	}
	if operationID == "" || len(operationID) > 128 {
		return ExecutionResult{}, errors.New("invalid operation ID")
	}
	d, e := k.current(ctx)
	if e != nil {
		return ExecutionResult{}, e
	}
	if d.Metadata.UID != p.UID {
		return ExecutionResult{}, ErrDrift
	}
	if d.Metadata.Annotations[operationAnnotation] == operationID {
		return k.result(d, p, operationID), nil
	}
	if templateDigest(d.Spec.Template) != p.ExpectedTemplateDigest || templateDigest(d.RawSpec) != p.ExpectedSpecDigest {
		return ExecutionResult{}, ErrDrift
	}
	annotations := d.Metadata.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[operationAnnotation] = operationID
	patch := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": p.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": d.Metadata.ResourceVersion}, {"op": "test", "path": "/spec/template", "value": json.RawMessage(d.Spec.Template)}, {"op": "add", "path": "/metadata/annotations", "value": annotations}, {"op": "replace", "path": "/spec/template", "value": json.RawMessage(p.TargetTemplate)}}
	b, status, e := k.request(ctx, "PATCH", k.deploymentPath(), patch)
	if e != nil {
		if status == 409 || status == 422 {
			return ExecutionResult{}, ErrDrift
		}
		if status >= 400 && status < 500 {
			return ExecutionResult{}, e
		}
		return ExecutionResult{State: "unknown", OperationID: operationID}, fmt.Errorf("%w: %v", ErrAmbiguous, e)
	}
	if e = json.Unmarshal(b, &d); e != nil {
		return ExecutionResult{State: "unknown", OperationID: operationID}, ErrAmbiguous
	}
	return k.result(d, p, operationID), nil
}
func (k *Kubernetes) result(d Deployment, p RollbackPlan, operationID string) ExecutionResult {
	r := ExecutionResult{OperationID: operationID, Generation: d.Metadata.Generation, State: "needs_review"}
	if d.Metadata.UID == p.UID && d.Metadata.Annotations[operationAnnotation] == operationID && templateDigest(d.Spec.Template) == p.TargetTemplateDigest {
		r.State = "applied"
	} else {
		r.Reason = "operation marker, object identity or target template no longer matches"
	}
	return r
}

// Reconcile is read-only. Even an unchanged template cannot prove a delayed write will not arrive.
func (k *Kubernetes) Reconcile(ctx context.Context, p RollbackPlan, operationID string) (ExecutionResult, error) {
	if operationID == "" || len(operationID) > 128 {
		return ExecutionResult{}, errors.New("invalid operation ID")
	}
	if e := k.validate(p); e != nil {
		return ExecutionResult{}, e
	}
	d, e := k.current(ctx)
	if e != nil {
		return ExecutionResult{}, e
	}
	return k.result(d, p, operationID), nil
}

func (k *Kubernetes) servicePath(suffix string) string {
	return "/api/v1/namespaces/" + k.config.Namespace + "/services/http:" + k.config.Service + ":" + strconv.Itoa(k.config.Port) + "/proxy/" + suffix
}
func (k *Kubernetes) metrics(ctx context.Context) (Metrics, error) {
	var m Metrics
	b, _, e := k.request(ctx, "GET", k.servicePath("stats"), nil)
	if e == nil {
		e = json.Unmarshal(b, &m)
	}
	return m, e
}
func (k *Kubernetes) ReadEvidence(ctx context.Context) (Evidence, error) {
	s, e := k.Snapshot(ctx)
	if e != nil {
		return Evidence{}, e
	}
	out := Evidence{Snapshot: s, Logs: map[string]string{}}
	out.Metrics, e = k.metrics(ctx)
	if e != nil {
		return out, e
	}
	_, _, e = k.request(ctx, "GET", k.servicePath("health"), nil)
	out.Healthy = e == nil
	labels := []string{}
	for a, b := range s.Deployment.Spec.Selector.MatchLabels {
		labels = append(labels, a+"="+b)
	}
	sort.Strings(labels)
	if len(labels) == 0 {
		return out, errors.New("registered Deployment requires matchLabels selector")
	}
	b, _, e := k.request(ctx, "GET", "/api/v1/namespaces/"+k.config.Namespace+"/pods?labelSelector="+url.QueryEscape(strings.Join(labels, ",")), nil)
	if e != nil {
		return out, e
	}
	var pods struct {
		Items []struct {
			Metadata Metadata `json:"metadata"`
		} `json:"items"`
	}
	if e = json.Unmarshal(b, &pods); e != nil {
		return out, e
	}
	for i, p := range pods.Items {
		if i >= 10 {
			break
		}
		b, _, e = k.request(ctx, "GET", "/api/v1/namespaces/"+k.config.Namespace+"/pods/"+p.Metadata.Name+"/log?tailLines=30&limitBytes=16384", nil)
		if e != nil {
			return out, e
		}
		out.Logs[p.Metadata.Name] = string(b)
	}
	return out, nil
}
func deploymentReady(d Deployment) bool {
	n := d.Spec.Replicas
	return n > 0 && d.Status.ObservedGeneration >= d.Metadata.Generation && d.Status.Replicas == n && d.Status.UpdatedReplicas == n && d.Status.ReadyReplicas == n && d.Status.AvailableReplicas == n
}
func validateWindow(prev, cur Metrics) error {
	if prev.Instance == "" || cur.Instance != prev.Instance {
		return errors.New("metric instance changed: restart observation")
	}
	if cur.Requests <= prev.Requests {
		return errors.New("no business traffic in observation interval")
	}
	if cur.Errors != prev.Errors {
		return errors.New("business errors or counter reset in observation interval")
	}
	return nil
}

// Observe requires a continuously healthy window with real business traffic, not just a successful command.
// It validates the registered service's single-instance counters; multi-replica aggregation needs an observability adapter.
func (k *Kubernetes) Observe(ctx context.Context, duration time.Duration) (RecoveryResult, error) {
	return k.observe(ctx, nil, duration)
}

// ObservePlan additionally binds every sample to the approved resource identity and template.
func (k *Kubernetes) ObservePlan(ctx context.Context, p RollbackPlan, duration time.Duration) (RecoveryResult, error) {
	if e := k.validate(p); e != nil {
		return RecoveryResult{}, e
	}
	if minimum := time.Duration(p.ObservationSeconds) * time.Second; duration < minimum {
		duration = minimum
	}
	return k.observe(ctx, &p, duration)
}
func (k *Kubernetes) observe(ctx context.Context, p *RollbackPlan, duration time.Duration) (RecoveryResult, error) {
	if duration < 180*time.Second {
		duration = 180 * time.Second
	}
	r := RecoveryResult{StartedAt: time.Now().UTC()}
	prev, e := k.metrics(ctx)
	if e != nil {
		return r, e
	}
	initial := prev.Requests
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-tick.C:
			d, e := k.current(ctx)
			if e != nil {
				return r, e
			}
			if p != nil && (d.Metadata.UID != p.UID || templateDigest(d.Spec.Template) != p.TargetTemplateDigest) {
				r.Reason = "approved target changed during recovery observation"
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
			if d.Spec.Replicas != 1 {
				r.Reason = "demo counter adapter requires exactly one replica; use aggregated metrics for production"
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
			if !deploymentReady(d) {
				r.Reason = "Deployment has not fully converged"
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
			_, _, e = k.request(ctx, "GET", k.servicePath("health"), nil)
			if e != nil {
				r.Reason = "business health probe failed"
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
			cur, e := k.metrics(ctx)
			if e != nil {
				return r, e
			}
			r.Samples++
			if e = validateWindow(prev, cur); e != nil {
				r.Reason = e.Error()
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
			prev = cur
			r.Requests = cur.Requests - initial
			if time.Since(r.StartedAt) >= duration {
				r.Recovered = true
				r.FinishedAt = time.Now().UTC()
				return r, nil
			}
		}
	}
}
