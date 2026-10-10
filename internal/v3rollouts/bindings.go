// Package v3rollouts is the Kubernetes Deployment rollback of V3 (PA-08):
// rollback candidates, the rollback proposal, the independent executor and its
// ledger, and the execution projection. The design is
// docs/plans/2026-10-10-rollback-executor-and-registry-p3.md.
package v3rollouts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RollbackWriteAction is the only write a binding can approve.
const RollbackWriteAction = "deployment.rollback"

// WriteBinding approves the executor to write one connector's Deployments in
// one namespace, with a write credential that only the executor reads
// (AIOPS_V3_KUBERNETES_WRITE_BINDINGS). The connector's own credential stays
// read-only.
type WriteBinding struct {
	Tier          string   `json:"tier"`
	Environment   string   `json:"environment"`
	ConnectorID   string   `json:"connector_id"`
	Namespace     string   `json:"namespace"`
	Deployments   []string `json:"deployments"`
	Actions       []string `json:"actions"`
	CredentialRef string   `json:"credential_ref"`
}

var bindingName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
var bindingRef = regexp.MustCompile(`^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$`)

// ParseWriteBindings accepts an optional JSON array. Any invalid or
// duplicate entry rejects the whole configuration.
func ParseWriteBindings(raw string) ([]WriteBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return []WriteBinding{}, nil
	}
	var bindings []WriteBinding
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil || bindings == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid Kubernetes write bindings JSON array")
	}
	if len(bindings) > 100 {
		return nil, errors.New("too many Kubernetes write bindings")
	}
	seen := map[string]bool{}
	for i, b := range bindings {
		if (b.Tier != "production" && b.Tier != "nonprod") || b.Environment == "" || b.Environment == "all" ||
			b.ConnectorID == "" || !bindingName.MatchString(b.Namespace) || len(b.Deployments) == 0 ||
			len(b.Deployments) > 100 || len(b.Actions) != 1 || b.Actions[0] != RollbackWriteAction ||
			!bindingRef.MatchString(b.CredentialRef) {
			return nil, errors.New("invalid Kubernetes write binding")
		}
		names := map[string]bool{}
		for _, d := range b.Deployments {
			if !bindingName.MatchString(d) || names[d] {
				return nil, errors.New("invalid Kubernetes write binding Deployment")
			}
			names[d] = true
		}
		key := b.Tier + "\x00" + b.Environment + "\x00" + b.ConnectorID + "\x00" + b.Namespace
		if seen[key] {
			return nil, errors.New("duplicate Kubernetes write binding")
		}
		seen[key] = true
		sorted := append([]string(nil), b.Deployments...)
		sort.Strings(sorted)
		bindings[i].Deployments = sorted
	}
	return bindings, nil
}

// findWriteBinding returns the binding that approves rolling back this
// Deployment, if any.
func findWriteBinding(bindings []WriteBinding, tier, env, connectorID, namespace, deployment string) (WriteBinding, bool) {
	for _, b := range bindings {
		if b.Tier != tier || b.Environment != env || b.ConnectorID != connectorID || b.Namespace != namespace {
			continue
		}
		for _, d := range b.Deployments {
			if d == deployment {
				return b, true
			}
		}
	}
	return WriteBinding{}, false
}

// fingerprint identifies a binding's content; a change makes approved
// proposals built on the old binding stale.
func (b WriteBinding) fingerprint() string {
	data, _ := json.Marshal(b)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digestOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func boolString(b bool) string { return strconv.FormatBool(b) }
func itoa(n int) string        { return strconv.Itoa(n) }
func ftoa(f float64) string    { return strconv.FormatFloat(f, 'g', -1, 64) }
