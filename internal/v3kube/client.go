// Package v3kube is the V3 Kubernetes client for Deployment rollback
// (PA-08). It reads a Deployment and the ReplicaSets it owns and applies one
// conditional template patch. Endpoint, token and CA come from the caller (a
// connector's read credential or the executor's write binding); nothing here
// reads configuration or files. The behaviour follows the legacy
// internal/connectors Kubernetes client: same template cleaning, digests and
// JSON Patch tests.
package v3kube

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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OperationAnnotation marks the Deployment with the execution that patched
// it, so an uncertain write can be reconciled by reading. It only means "the
// last execution the executor wrote": it stays on the Deployment and, like
// every Deployment annotation, is copied to ReplicaSets of later releases
// (PA-76). It must be in the same patch as the template to prove that write,
// so it is not moved or removed (that would be a second write); executions
// match it only against their own ID together with the target template, and
// it is never shown as a revision property.
const OperationAnnotation = "aiops-canvas.io/operation"

var (
	// ErrForbidden: the credential may not read or write this object.
	ErrForbidden = errors.New("kubernetes access forbidden")
	// ErrNotFound: the Deployment does not exist.
	ErrNotFound = errors.New("kubernetes object not found")
	// ErrConflict: the object changed since it was read (CAS failed).
	ErrConflict = errors.New("kubernetes object changed")
	// ErrUnavailable: the API could not be reached or answered unexpectedly.
	ErrUnavailable = errors.New("kubernetes api unavailable")
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Client talks to one API server with one bearer token.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New builds a client. The endpoint must be HTTPS unless it is a loopback
// address; caPEM, when set, replaces the system roots.
func New(endpoint, token, caPEM string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Kubernetes endpoint")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("Kubernetes endpoint must use HTTPS")
		}
	}
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("invalid Kubernetes token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("invalid Kubernetes CA")
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return &Client{base: u, token: token, http: &http.Client{Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, contentType string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, ErrUnavailable
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return data, resp.StatusCode, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, resp.StatusCode, ErrForbidden
	case resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, ErrNotFound
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity:
		return nil, resp.StatusCode, ErrConflict
	default:
		return nil, resp.StatusCode, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
}

// Deployment is the part of a Deployment the rollback reads.
type Deployment struct {
	Metadata struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		UID             string            `json:"uid"`
		ResourceVersion string            `json:"resourceVersion"`
		Generation      int64             `json:"generation"`
		Annotations     map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32          `json:"replicas"`
		Paused   bool            `json:"paused"`
		Template json.RawMessage `json:"template"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64 `json:"observedGeneration"`
		Replicas           int32 `json:"replicas"`
		UpdatedReplicas    int32 `json:"updatedReplicas"`
		ReadyReplicas      int32 `json:"readyReplicas"`
		AvailableReplicas  int32 `json:"availableReplicas"`
		Conditions         []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
	// RawSpec is the whole spec, for the spec digest.
	RawSpec json.RawMessage `json:"-"`
}

// Desired is the requested replica count (Kubernetes defaults it to 1).
func (d Deployment) Desired() int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

// Revision is the deployment.kubernetes.io/revision annotation.
func (d Deployment) Revision() string {
	return d.Metadata.Annotations["deployment.kubernetes.io/revision"]
}

// RolledOut reports a finished rollout of the current generation.
func (d Deployment) RolledOut() bool {
	n := d.Desired()
	return d.Status.ObservedGeneration >= d.Metadata.Generation && d.Status.UpdatedReplicas == n &&
		d.Status.Replicas == n && d.Status.AvailableReplicas == n
}

// ProgressDeadlineExceeded reports the controller giving up on the rollout.
// A condition from before the controller observed the current generation
// belongs to the previous rollout (for example the stuck release being
// rolled back) and is ignored, as kubectl rollout status does.
func (d Deployment) ProgressDeadlineExceeded() bool {
	if d.Status.ObservedGeneration < d.Metadata.Generation {
		return false
	}
	for _, c := range d.Status.Conditions {
		if c.Type == "Progressing" && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

func deploymentPath(namespace, name string) string {
	return "/apis/apps/v1/namespaces/" + namespace + "/deployments/" + name
}

// GetDeployment reads one Deployment.
func (c *Client) GetDeployment(ctx context.Context, namespace, name string) (Deployment, error) {
	if !nameRE.MatchString(namespace) || !nameRE.MatchString(name) {
		return Deployment{}, ErrNotFound
	}
	data, _, err := c.do(ctx, http.MethodGet, deploymentPath(namespace, name), nil, "")
	if err != nil {
		return Deployment{}, err
	}
	return decodeDeployment(data)
}

func decodeDeployment(data []byte) (Deployment, error) {
	var d Deployment
	if err := json.Unmarshal(data, &d); err != nil {
		return Deployment{}, ErrUnavailable
	}
	var raw struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || d.Metadata.UID == "" || len(d.Spec.Template) == 0 {
		return Deployment{}, ErrUnavailable
	}
	d.RawSpec = raw.Spec
	return d, nil
}

// Revision is one ReplicaSet the Deployment owns.
type Revision struct {
	Revision    string
	CreatedAt   *time.Time
	ChangeCause string
	// Template is the ReplicaSet's Pod template without pod-template-hash,
	// the template a rollback restores.
	Template json.RawMessage
	Digest   string
}

// Revisions lists the ReplicaSets the Deployment controls, newest first.
func (c *Client) Revisions(ctx context.Context, d Deployment) ([]Revision, error) {
	selector := make([]string, 0, len(d.Spec.Selector.MatchLabels))
	for k, v := range d.Spec.Selector.MatchLabels {
		selector = append(selector, k+"="+v)
	}
	if len(selector) == 0 {
		return nil, errors.New("Deployment has no matchLabels selector")
	}
	sort.Strings(selector)
	path := "/apis/apps/v1/namespaces/" + d.Metadata.Namespace + "/replicasets?labelSelector=" + url.QueryEscape(strings.Join(selector, ","))
	data, _, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Annotations       map[string]string `json:"annotations"`
				CreationTimestamp *time.Time        `json:"creationTimestamp"`
				OwnerReferences   []struct {
					UID        string `json:"uid"`
					Controller *bool  `json:"controller"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Spec struct {
				Template json.RawMessage `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, ErrUnavailable
	}
	var out []Revision
	for _, rs := range list.Items {
		owned := false
		for _, o := range rs.Metadata.OwnerReferences {
			if o.UID == d.Metadata.UID && o.Controller != nil && *o.Controller {
				owned = true
			}
		}
		revision := rs.Metadata.Annotations["deployment.kubernetes.io/revision"]
		if !owned || revision == "" {
			continue
		}
		template, err := CleanTemplate(rs.Spec.Template)
		if err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, Revision{Revision: revision, CreatedAt: rs.Metadata.CreationTimestamp,
			ChangeCause: rs.Metadata.Annotations["kubernetes.io/change-cause"], Template: template, Digest: Digest(template)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].Revision)
		b, _ := strconv.Atoi(out[j].Revision)
		return a > b
	})
	return out, nil
}

// CleanTemplate removes the pod-template-hash label a ReplicaSet adds, which
// must not enter a Deployment template.
func CleanTemplate(t json.RawMessage) (json.RawMessage, error) {
	var v map[string]any
	if err := json.Unmarshal(t, &v); err != nil {
		return nil, err
	}
	if m, ok := v["metadata"].(map[string]any); ok {
		if labels, ok := m["labels"].(map[string]any); ok {
			delete(labels, "pod-template-hash")
		}
	}
	return json.Marshal(v)
}

// Digest is the SHA-256 of the canonical JSON of a value, so key order and
// spacing (for example from PostgreSQL jsonb) do not change it.
func Digest(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SpecDigest is the Digest of a Deployment spec without replicas, so an
// autoscaler changing the replica count is not mistaken for drift.
func SpecDigest(spec json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(spec, &v) != nil {
		return ""
	}
	delete(v, "replicas")
	data, _ := json.Marshal(v)
	return Digest(data)
}

// Rollback describes the one conditional write of a rollback.
type Rollback struct {
	Namespace              string
	Deployment             string
	UID                    string
	ExpectedTemplateDigest string
	ExpectedSpecDigest     string
	TargetTemplate         json.RawMessage
	TargetTemplateDigest   string
	OperationID            string
	// ChangeCause replaces kubernetes.io/change-cause in the same patch
	// (PA-76). The controller copies the Deployment's annotations to the
	// ReplicaSet it reuses, so without it the restored revision would carry
	// the bad release's cause. Metadata only; the template is untouched.
	ChangeCause string
}

// ChangeCauseAnnotation is the standard rollout history annotation.
const ChangeCauseAnnotation = "kubernetes.io/change-cause"

// Outcome of a write or a read-only reconciliation.
type Outcome struct {
	// Applied: the Deployment carries this operation and the target template.
	Applied bool
	// Drift: the Deployment is no longer the one approved (other UID, other
	// template without this operation's marker).
	Drift      bool
	Generation int64
	Deployment Deployment
}

// outcome classifies a read Deployment. Neither Applied nor Drift means the
// write has not landed and the object is still the approved one.
func outcome(d Deployment, r Rollback) Outcome {
	o := Outcome{Generation: d.Metadata.Generation, Deployment: d}
	switch {
	case d.Metadata.UID != r.UID:
		o.Drift = true
	case d.Metadata.Annotations[OperationAnnotation] == r.OperationID:
		// This operation wrote; anything other than the target since then is
		// someone else's change.
		o.Applied = Digest(d.Spec.Template) == r.TargetTemplateDigest
		o.Drift = !o.Applied
	case Digest(d.Spec.Template) != r.ExpectedTemplateDigest || SpecDigest(d.RawSpec) != r.ExpectedSpecDigest:
		o.Drift = true
	}
	return o
}

// Apply makes the one conditional write: the patch only succeeds while the
// Deployment has the read resourceVersion and template, and it records the
// operation. It never retries. An ErrUnavailable result is ambiguous and
// must be reconciled with Reconcile, not retried.
func (c *Client) Apply(ctx context.Context, r Rollback) (Outcome, error) {
	if r.OperationID == "" || len(r.OperationID) > 63 || len(r.ChangeCause) > 256 || Digest(r.TargetTemplate) != r.TargetTemplateDigest {
		return Outcome{}, errors.New("invalid rollback")
	}
	d, err := c.GetDeployment(ctx, r.Namespace, r.Deployment)
	if err != nil {
		return Outcome{}, err
	}
	if o := outcome(d, r); o.Applied || o.Drift {
		return o, nil
	}
	annotations := map[string]string{}
	for k, v := range d.Metadata.Annotations {
		annotations[k] = v
	}
	annotations[OperationAnnotation] = r.OperationID
	if r.ChangeCause != "" {
		annotations[ChangeCauseAnnotation] = r.ChangeCause
	}
	patch, _ := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": r.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": d.Metadata.ResourceVersion},
		{"op": "test", "path": "/spec/template", "value": d.Spec.Template},
		{"op": "add", "path": "/metadata/annotations", "value": annotations},
		{"op": "replace", "path": "/spec/template", "value": r.TargetTemplate},
	})
	data, _, err := c.do(ctx, http.MethodPatch, deploymentPath(r.Namespace, r.Deployment), patch, "application/json-patch+json")
	if err != nil {
		return Outcome{}, err
	}
	written, err := decodeDeployment(data)
	if err != nil {
		return Outcome{}, ErrUnavailable
	}
	return outcome(written, r), nil
}

// Reconcile reads the Deployment and reports whether this operation is
// applied. It never writes.
func (c *Client) Reconcile(ctx context.Context, r Rollback) (Outcome, error) {
	d, err := c.GetDeployment(ctx, r.Namespace, r.Deployment)
	if err != nil {
		return Outcome{}, err
	}
	return outcome(d, r), nil
}
