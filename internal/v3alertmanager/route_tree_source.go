package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// RouteTreeObservation is a projection of one environment's current
// Alertmanager configuration. It contains no receiver definitions, targets,
// credentials, delivery history, or claim that time intervals are active.
type RouteTreeObservation struct {
	Root         RouteTreeNode `json:"root"`
	ObservedAt   time.Time     `json:"observed_at"`
	ConfigSHA256 string        `json:"config_sha256"`
}

type RoutePathLeaf struct {
	NodeIDs        []string `json:"node_ids"`
	ReceiverRefs   []string `json:"receiver_refs"`
	GroupBy        []string `json:"group_by"`
	GroupWait      string   `json:"group_wait"`
	GroupInterval  string   `json:"group_interval"`
	RepeatInterval string   `json:"repeat_interval"`
}

type RoutePathObservation struct {
	Paths        []RoutePathLeaf `json:"paths"`
	ObservedAt   time.Time       `json:"observed_at"`
	ConfigSHA256 string          `json:"config_sha256"`
}

type RouteTreeMatcher struct {
	K             string `json:"k"`
	Op            string `json:"op"`
	ValueRedacted bool   `json:"value_redacted"`
}

type RouteTreeNode struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Matchers       []RouteTreeMatcher `json:"matchers"`
	ReceiverRefs   []string           `json:"receiver_refs"`
	GroupBy        []string           `json:"group_by"`
	GroupWait      string             `json:"group_wait"`
	GroupInterval  string             `json:"group_interval"`
	RepeatInterval string             `json:"repeat_interval"`
	Continue       bool               `json:"continue"`
	Conditional    bool               `json:"conditional"`
	Children       []RouteTreeNode    `json:"children"`
	compiled       *effectivePolicyRoute
}

// ObserveRouteTree reads the current config and exposes only a complete,
// exact environment branch immediately below the global root. Earlier
// siblings that might match this environment make the selected branch
// incomplete, so no partial tree is returned. The generated node IDs and
// titles are projection labels, not stable Alertmanager route identities.
func (c *Client) ObserveRouteTree(ctx context.Context, environment, environmentLabel string) (RouteTreeObservation, error) {
	if environment == "" || environment == "all" || len(environment) > maxPolicyString ||
		!policyLabelName.MatchString(environmentLabel) || environmentLabel == "application" {
		return RouteTreeObservation{}, ErrGroupPolicyInvalid
	}
	body, err := c.get(ctx, environment, "/api/v2/status")
	if errors.Is(err, ErrNotConfigured) {
		return RouteTreeObservation{}, ErrNotConfigured
	}
	if err != nil {
		return RouteTreeObservation{}, ErrGroupPolicyUnavailable
	}
	var status struct {
		Config struct {
			Original string `json:"original"`
		} `json:"config"`
	}
	if decodeOne(body, &status) != nil || status.Config.Original == "" ||
		len(status.Config.Original) > maxResponseBytes {
		return RouteTreeObservation{}, ErrGroupPolicyInvalid
	}
	var config policyConfig
	if yaml.Unmarshal([]byte(status.Config.Original), &config) != nil || config.Route == nil {
		return RouteTreeObservation{}, ErrGroupPolicyInvalid
	}
	count := 0
	root, err := compilePolicyRoute(config.Route, GroupPolicyObservation{
		GroupBy: []string{}, GroupWait: "30s", GroupInterval: "5m", RepeatInterval: "4h",
	}, false, 1, &count)
	if err != nil {
		return RouteTreeObservation{}, err
	}
	if len(root.matchers) != 0 || root.continueSiblings {
		return RouteTreeObservation{}, ErrGroupPolicyInvalid
	}
	selected := -1
	for index, child := range root.children {
		if exactRouteEnvironment(child, environmentLabel, environment) {
			if selected >= 0 {
				return RouteTreeObservation{}, ErrGroupPolicyAmbiguous
			}
			selected = index
			continue
		}
		if selected < 0 && routeMayMatchEnvironment(child, environmentLabel, environment) {
			return RouteTreeObservation{}, ErrGroupPolicyAmbiguous
		}
	}
	if selected < 0 {
		return RouteTreeObservation{}, ErrNoEnvironmentRoute
	}
	branch := root.children[selected]
	if branch.continueSiblings {
		return RouteTreeObservation{}, ErrGroupPolicyAmbiguous
	}
	receiverRefs := make(map[string]string)
	projected := projectRouteTree(branch, environmentLabel, environment, "route-root", "Environment route", receiverRefs)
	digest := sha256.Sum256([]byte(status.Config.Original))
	return RouteTreeObservation{Root: projected, ObservedAt: time.Now().UTC(), ConfigSHA256: hex.EncodeToString(digest[:])}, nil
}

// A complete direct environment branch must match every alert bearing that
// exact environment label, regardless of its other labels.
func exactRouteEnvironment(route *effectivePolicyRoute, environmentLabel, environment string) bool {
	return len(route.matchers) == 1 && route.matchers[0].name == environmentLabel &&
		route.matchers[0].op == "=" && route.matchers[0].value == environment
}

// Only an environment-label contradiction can prove that a route cannot
// match any alert in the selected environment. Other matcher names depend on
// the sample alert and are therefore ambiguous at the root sibling level.
func routeMayMatchEnvironment(route *effectivePolicyRoute, environmentLabel, environment string) bool {
	for _, matcher := range route.matchers {
		if matcher.name == environmentLabel && !matcher.matches(map[string]string{environmentLabel: environment}) {
			return false
		}
	}
	return true
}

// Receiver refs are assigned once per distinct effective receiver name in
// depth-first preorder of the projected environment tree. A draft binds the
// ref to the exact config revision; a future activation adapter must resolve
// it against that same revision before converting the proposal to AM config.
func projectRouteTree(route *effectivePolicyRoute, environmentLabel, environment, id, title string, receiverRefs map[string]string) RouteTreeNode {
	result := RouteTreeNode{
		ID: id, Title: title,
		Matchers:     make([]RouteTreeMatcher, 0, len(route.matchers)),
		ReceiverRefs: make([]string, 0, len(route.policy.Receivers)),
		GroupBy:      append([]string{}, route.policy.GroupBy...),
		GroupWait:    route.policy.GroupWait, GroupInterval: route.policy.GroupInterval,
		RepeatInterval: route.policy.RepeatInterval,
		Continue:       route.continueSiblings, Conditional: route.conditional,
		Children: []RouteTreeNode{},
		compiled: route,
	}
	for _, receiver := range route.policy.Receivers {
		ref, exists := receiverRefs[receiver]
		if !exists {
			ref = "receiver-" + strconv.Itoa(len(receiverRefs)+1)
			receiverRefs[receiver] = ref
		}
		result.ReceiverRefs = append(result.ReceiverRefs, ref)
	}
	for _, matcher := range route.matchers {
		result.Matchers = append(result.Matchers, RouteTreeMatcher{K: matcher.name, Op: matcher.op, ValueRedacted: true})
	}
	for _, child := range route.children {
		if !routeMayMatchEnvironment(child, environmentLabel, environment) {
			continue
		}
		next := len(result.Children) + 1
		childID := id + "-" + strconv.Itoa(next)
		result.Children = append(result.Children, projectRouteTree(child, environmentLabel, environment,
			childID, "Route "+strconv.Itoa(next), receiverRefs))
	}
	return result
}

// EvaluateRoutePath computes the configured matching branch paths for one
// sample. It does not send to any receiver or assert live time-interval state.
func (c *Client) EvaluateRoutePath(ctx context.Context, environment, environmentLabel string,
	labels map[string]string) (RoutePathObservation, error) {
	if labels[environmentLabel] != environment || labels["application"] == "" {
		return RoutePathObservation{}, ErrGroupPolicyInvalid
	}
	observation, err := c.ObserveRouteTree(ctx, environment, environmentLabel)
	if err != nil {
		return RoutePathObservation{}, err
	}
	paths := make([]RoutePathLeaf, 0, 2)
	if err := collectRoutePath(observation.Root, labels, []string{}, &paths); err != nil {
		return RoutePathObservation{}, err
	}
	if len(paths) == 0 || len(paths) > maxPolicyRouteNodes {
		return RoutePathObservation{}, ErrGroupPolicyInvalid
	}
	return RoutePathObservation{Paths: paths, ObservedAt: observation.ObservedAt,
		ConfigSHA256: observation.ConfigSHA256}, nil
}

func collectRoutePath(node RouteTreeNode, labels map[string]string, prefix []string,
	paths *[]RoutePathLeaf) error {
	if node.compiled == nil || node.Conditional {
		return ErrGroupPolicyAmbiguous
	}
	path := append(append([]string{}, prefix...), node.ID)
	matched := false
	for _, child := range node.Children {
		childMatches := true
		for _, matcher := range child.compiled.matchers {
			if !matcher.matches(labels) {
				childMatches = false
				break
			}
		}
		if !childMatches {
			continue
		}
		matched = true
		if err := collectRoutePath(child, labels, path, paths); err != nil {
			return err
		}
		if !child.Continue {
			break
		}
	}
	if !matched {
		*paths = append(*paths, RoutePathLeaf{NodeIDs: path,
			ReceiverRefs: append([]string{}, node.ReceiverRefs...),
			GroupBy:      append([]string{}, node.GroupBy...),
			GroupWait:    node.GroupWait, GroupInterval: node.GroupInterval,
			RepeatInterval: node.RepeatInterval})
	}
	return nil
}
