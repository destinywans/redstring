// Package kubetest is an in-memory Kubernetes API for rollback tests: one
// namespace, Deployments with owned ReplicaSets, JSON Patch with "test"
// operations, and the Deployment controller's rollback behaviour (reusing a
// ReplicaSet with the same template under a new revision number).
package kubetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is a fake API server. Its fields can be changed between requests to
// inject failures.
type Server struct {
	*httptest.Server
	Token string

	mu          sync.Mutex
	namespace   string
	deployments map[string]*deployment
	// PatchStatus, when set, answers PATCH with this status instead of
	// applying it (403, 409, 500). PatchAppliesThenFails applies the patch and
	// then answers 500, an ambiguous write.
	PatchStatus           int
	PatchAppliesThenFails bool
	// StallRollout keeps a patched Deployment's new pods from becoming ready;
	// LagObservation keeps the controller from observing the new generation;
	// DeadlineExceeded marks it ProgressDeadlineExceeded.
	StallRollout     bool
	LagObservation   bool
	DeadlineExceeded bool
	// Patches counts PATCH requests that reached the object.
	Patches int
}

type replicaSet struct {
	revision  int
	history   []string // earlier revision numbers, as revision-history
	template  any
	created   time.Time
	cause     string
	uid       string
	templateH string
}

type deployment struct {
	name            string
	uid             string
	resourceVersion int
	generation      int64
	annotations     map[string]any
	replicas        int
	paused          bool
	template        any
	selector        map[string]any
	replicaSets     []*replicaSet
	observed        int64
	stalled         bool
}

// New starts a TLS fake for namespace with the given bearer token.
func New(namespace, token string) *Server {
	s := &Server{Token: token, namespace: namespace, deployments: map[string]*deployment{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	return s
}

func hash(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:10]
}

// AddDeployment creates a Deployment whose history is the given templates in
// order (revision 1..n); the last one is current. Each template is a Pod
// template object.
func (s *Server) AddDeployment(name string, replicas int, causes []string, templates ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := &deployment{name: name, uid: "uid-" + name, resourceVersion: 1, generation: int64(len(templates)),
		annotations: map[string]any{}, replicas: replicas, selector: map[string]any{"app": name}}
	for i, t := range templates {
		cause := ""
		if i < len(causes) {
			cause = causes[i]
		}
		d.replicaSets = append(d.replicaSets, &replicaSet{revision: i + 1, template: clone(t), created: time.Date(2026, 10, 1+i, 9, 0, 0, 0, time.UTC),
			cause: cause, uid: fmt.Sprintf("rs-%s-%d", name, i+1), templateH: hash(t)})
	}
	d.template = clone(templates[len(templates)-1])
	d.annotations["deployment.kubernetes.io/revision"] = strconv.Itoa(len(templates))
	// The current release's cause stays on the Deployment, as kubectl
	// annotate or a pipeline leaves it.
	if len(causes) >= len(templates) && causes[len(templates)-1] != "" {
		d.annotations["kubernetes.io/change-cause"] = causes[len(templates)-1]
	}
	d.observed = d.generation
	s.deployments[name] = d
}

// SetPaused pauses or resumes a Deployment.
func (s *Server) SetPaused(name string, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deployments[name].paused = paused
}

// Touch changes a Deployment outside the rollback, as another actor would.
func (s *Server) Touch(name string, template map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollout(s.deployments[name], clone(template), "")
}

// Revision returns the current revision and template hash.
func (s *Server) Revision(name string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deployments[name]
	return d.annotations["deployment.kubernetes.io/revision"].(string), hash(d.template)
}

// Annotation returns one Deployment annotation.
func (s *Server) Annotation(name, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.deployments[name].annotations[key].(string)
	return v
}

func clone(v any) any {
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

// rollout applies a new template the way the Deployment controller does.
func (s *Server) rollout(d *deployment, template any, cause string) {
	d.template = template
	d.generation++
	d.resourceVersion++
	next := 0
	for _, rs := range d.replicaSets {
		if rs.revision > next {
			next = rs.revision
		}
	}
	next++
	h := hash(template)
	var reused *replicaSet
	for _, rs := range d.replicaSets {
		if rs.templateH == h {
			reused = rs
		}
	}
	// Like the controller: the ReplicaSet takes the Deployment's
	// change-cause annotation, a reused one included.
	if annotated, ok := d.annotations["kubernetes.io/change-cause"].(string); ok && cause == "" {
		cause = annotated
	}
	if reused != nil {
		// Like the controller: the earlier number moves to revision-history.
		reused.history = append(reused.history, strconv.Itoa(reused.revision))
		reused.revision = next
		if cause != "" {
			reused.cause = cause
		}
	} else {
		d.replicaSets = append(d.replicaSets, &replicaSet{revision: next, template: clone(template), created: time.Now().UTC(),
			cause: cause, uid: fmt.Sprintf("rs-%s-%d", d.name, next), templateH: h})
	}
	d.annotations["deployment.kubernetes.io/revision"] = strconv.Itoa(next)
	d.stalled = s.StallRollout
	if !s.LagObservation {
		d.observed = d.generation
	}
}

func (s *Server) deploymentJSON(d *deployment) map[string]any {
	ready := d.replicas
	if d.observed < d.generation || d.stalled {
		ready = 0
	}
	conditions := []any{map[string]any{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"}}
	if s.DeadlineExceeded {
		conditions = []any{map[string]any{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}}
	}
	return map[string]any{
		"metadata": map[string]any{"name": d.name, "namespace": s.namespace, "uid": d.uid,
			"resourceVersion": strconv.Itoa(d.resourceVersion), "generation": d.generation, "annotations": clone(d.annotations)},
		"spec": map[string]any{"replicas": d.replicas, "paused": d.paused, "template": clone(d.template),
			"selector": map[string]any{"matchLabels": d.selector}},
		"status": map[string]any{"observedGeneration": d.observed, "replicas": d.replicas, "updatedReplicas": ready,
			"readyReplicas": ready, "availableReplicas": ready, "conditions": conditions},
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := "/apis/apps/v1/namespaces/" + s.namespace + "/"
	switch {
	case r.URL.Path == "/version":
		writeJSON(w, map[string]any{"gitVersion": "v1.30.0"})
	case r.Method == http.MethodGet && r.URL.Path == prefix+"replicasets":
		var items []any
		for _, d := range s.deployments {
			if r.URL.Query().Get("labelSelector") != "app="+d.name {
				continue
			}
			for _, rs := range d.replicaSets {
				controller := true
				template := clone(rs.template).(map[string]any)
				if meta, ok := template["metadata"].(map[string]any); ok {
					if labels, ok := meta["labels"].(map[string]any); ok {
						labels["pod-template-hash"] = rs.templateH
					}
				}
				annotations := map[string]any{"deployment.kubernetes.io/revision": strconv.Itoa(rs.revision)}
				if rs.cause != "" {
					annotations["kubernetes.io/change-cause"] = rs.cause
				}
				if len(rs.history) > 0 {
					annotations["deployment.kubernetes.io/revision-history"] = strings.Join(rs.history, ",")
				}
				items = append(items, map[string]any{
					"metadata": map[string]any{"name": rs.uid, "annotations": annotations, "creationTimestamp": rs.created.Format(time.RFC3339),
						"ownerReferences": []any{map[string]any{"uid": d.uid, "controller": controller}}},
					"spec": map[string]any{"template": template}})
			}
		}
		writeJSON(w, map[string]any{"items": items})
	case strings.HasPrefix(r.URL.Path, prefix+"deployments/"):
		d := s.deployments[strings.TrimPrefix(r.URL.Path, prefix+"deployments/")]
		if d == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, s.deploymentJSON(d))
		case http.MethodPatch:
			if s.PatchStatus != 0 {
				w.WriteHeader(s.PatchStatus)
				return
			}
			s.Patches++
			var ops []map[string]any
			if json.NewDecoder(r.Body).Decode(&ops) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			current := s.deploymentJSON(d)
			for _, op := range ops {
				if op["op"] != "test" {
					continue
				}
				if !reflect.DeepEqual(lookup(current, op["path"].(string)), clone(op["value"])) {
					w.WriteHeader(http.StatusUnprocessableEntity)
					return
				}
			}
			for _, op := range ops {
				switch op["path"] {
				case "/metadata/annotations":
					d.annotations = clone(op["value"]).(map[string]any)
				case "/spec/template":
					if op["op"] == "replace" {
						s.rollout(d, clone(op["value"]), "")
					}
				}
			}
			if s.PatchAppliesThenFails {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSON(w, s.deploymentJSON(d))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Inject changes the failure fields under the server lock, for tests whose
// executor is already running.
func (s *Server) Inject(change func(*Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

// PatchCount reads Patches under the server lock.
func (s *Server) PatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Patches
}

// FinishRollout lets a stalled rollout complete.
func (s *Server) FinishRollout(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StallRollout, s.LagObservation = false, false
	d := s.deployments[name]
	d.observed, d.stalled = d.generation, false
}

func lookup(v any, path string) any {
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return clone(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Template is a small Pod template with one container "api".
func Template(app, image string, env map[string]string) map[string]any {
	var vars []any
	for k, v := range env {
		vars = append(vars, map[string]any{"name": k, "value": v})
	}
	container := map[string]any{"name": "api", "image": image}
	if vars != nil {
		container["env"] = vars
	}
	return map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": app}},
		"spec": map[string]any{"containers": []any{container}}}
}
