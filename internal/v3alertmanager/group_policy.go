package v3alertmanager

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GroupPolicyObservation is the currently configured Alertmanager routing
// policy for one exact alert label set. It is not notification delivery history
// and does not assert that a conditional route is active at this instant.
type GroupPolicyObservation struct {
	GroupBy        []string  `json:"group_by"`
	Receivers      []string  `json:"receivers"`
	GroupWait      string    `json:"group_wait"`
	GroupInterval  string    `json:"group_interval"`
	RepeatInterval string    `json:"repeat_interval"`
	ObservedAt     time.Time `json:"observed_at"`
}

var (
	ErrGroupPolicyUnavailable = errors.New("Alertmanager group policy unavailable")
	ErrGroupPolicyInvalid     = errors.New("invalid Alertmanager group policy")
	ErrGroupPolicyAmbiguous   = errors.New("Alertmanager group policy cannot be determined exactly")
	// ErrNoEnvironmentRoute is the ambiguous case where the root has no exact
	// environment branch at all. Callers that only test for ambiguity are
	// unaffected; the route-tree read reports it as not configured.
	ErrNoEnvironmentRoute = fmt.Errorf("%w: no exact environment route", ErrGroupPolicyAmbiguous)
)

const (
	maxPolicyRouteNodes = 500
	maxPolicyRouteDepth = 32
	maxPolicyMatchers   = 100
	maxPolicyString     = 1024
)

var (
	policyLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	policyDuration  = regexp.MustCompile(`^(0|([0-9]+(ms|s|m|h|d|w|y))+)$`)
)

type policyConfig struct {
	Route *policyRoute `yaml:"route"`
}

type policyRoute struct {
	Receiver        *string           `yaml:"receiver"`
	GroupBy         *[]string         `yaml:"group_by"`
	GroupWait       *string           `yaml:"group_wait"`
	GroupInterval   *string           `yaml:"group_interval"`
	RepeatInterval  *string           `yaml:"repeat_interval"`
	Matchers        []string          `yaml:"matchers"`
	Match           map[string]string `yaml:"match"`
	MatchRE         map[string]string `yaml:"match_re"`
	MuteIntervals   []string          `yaml:"mute_time_intervals"`
	ActiveIntervals []string          `yaml:"active_time_intervals"`
	Continue        bool              `yaml:"continue"`
	Routes          []*policyRoute    `yaml:"routes"`
}

type policyMatcher struct {
	name, value, op string
	re              *regexp.Regexp
}

type effectivePolicyRoute struct {
	matchers         []policyMatcher
	children         []*effectivePolicyRoute
	continueSiblings bool
	conditional      bool
	policy           GroupPolicyObservation
}

// ObserveGroupPolicy reads only the routing tree from Alertmanager's status
// config. The raw config may contain receiver credentials and must never be
// returned or included in errors. Callers must establish an exact, authorized
// environment/application/source binding for labels before calling this method.
func (c *Client) ObserveGroupPolicy(ctx context.Context, environment string, labels map[string]string) (GroupPolicyObservation, error) {
	if len(labels) == 0 || len(labels) > maxPolicyMatchers {
		return GroupPolicyObservation{}, ErrGroupPolicyInvalid
	}
	for name, value := range labels {
		if !policyLabelName.MatchString(name) || len(value) > maxPolicyString {
			return GroupPolicyObservation{}, ErrGroupPolicyInvalid
		}
	}
	body, err := c.get(ctx, environment, "/api/v2/status")
	if errors.Is(err, ErrNotConfigured) {
		return GroupPolicyObservation{}, ErrNotConfigured
	}
	if err != nil {
		return GroupPolicyObservation{}, ErrGroupPolicyUnavailable
	}
	var status struct {
		Config struct {
			Original string `json:"original"`
		} `json:"config"`
	}
	if decodeOne(body, &status) != nil || status.Config.Original == "" || len(status.Config.Original) > maxResponseBytes {
		return GroupPolicyObservation{}, ErrGroupPolicyInvalid
	}
	var config policyConfig
	if yaml.Unmarshal([]byte(status.Config.Original), &config) != nil || config.Route == nil {
		return GroupPolicyObservation{}, ErrGroupPolicyInvalid
	}
	count := 0
	root, err := compilePolicyRoute(config.Route, GroupPolicyObservation{
		GroupBy: []string{}, GroupWait: "30s", GroupInterval: "5m", RepeatInterval: "4h",
	}, false, 1, &count)
	if err != nil {
		return GroupPolicyObservation{}, err
	}
	if len(root.matchers) != 0 || root.continueSiblings {
		// Alertmanager's root route must match every alert and cannot continue.
		return GroupPolicyObservation{}, ErrGroupPolicyInvalid
	}
	leaves := make([]GroupPolicyObservation, 0, 2)
	conditional := false
	collectPolicyRoutes(root, labels, &leaves, &conditional)
	if len(leaves) == 0 {
		return GroupPolicyObservation{}, ErrGroupPolicyInvalid
	}
	if conditional {
		// Time intervals are evaluated by Alertmanager at delivery time.
		// A static route read cannot say which receiver is currently active.
		return GroupPolicyObservation{}, ErrGroupPolicyAmbiguous
	}
	result := leaves[0]
	receivers := make(map[string]bool, len(leaves))
	for _, leaf := range leaves {
		if !equalPolicyGroupBy(result.GroupBy, leaf.GroupBy) || result.GroupWait != leaf.GroupWait ||
			result.GroupInterval != leaf.GroupInterval || result.RepeatInterval != leaf.RepeatInterval {
			return GroupPolicyObservation{}, ErrGroupPolicyAmbiguous
		}
		for _, receiver := range leaf.Receivers {
			receivers[receiver] = true
		}
	}
	result.Receivers = make([]string, 0, len(receivers))
	for receiver := range receivers {
		result.Receivers = append(result.Receivers, receiver)
	}
	sort.Strings(result.Receivers)
	result.ObservedAt = time.Now().UTC()
	return result, nil
}

func compilePolicyRoute(raw *policyRoute, inherited GroupPolicyObservation, inheritedConditional bool, depth int, count *int) (*effectivePolicyRoute, error) {
	if raw == nil || depth > maxPolicyRouteDepth || *count >= maxPolicyRouteNodes ||
		len(raw.Matchers)+len(raw.Match)+len(raw.MatchRE) > maxPolicyMatchers {
		return nil, ErrGroupPolicyInvalid
	}
	*count++
	current := inherited
	if raw.Receiver != nil {
		if *raw.Receiver == "" || len(*raw.Receiver) > maxPolicyString ||
			strings.ContainsAny(*raw.Receiver, "\\?@#\r\n\x00") || strings.Contains(*raw.Receiver, "://") {
			// Receiver names are the only provider text we return. Never allow a
			// URL or an address embedded in a name into the response.
			return nil, ErrGroupPolicyInvalid
		}
		current.Receivers = []string{*raw.Receiver}
	}
	if raw.GroupBy != nil {
		if len(*raw.GroupBy) > maxPolicyMatchers {
			return nil, ErrGroupPolicyInvalid
		}
		groupBy := append([]string{}, (*raw.GroupBy)...)
		for _, name := range groupBy {
			if name != "..." && !policyLabelName.MatchString(name) {
				return nil, ErrGroupPolicyInvalid
			}
		}
		if len(groupBy) > 1 {
			for _, name := range groupBy {
				if name == "..." {
					return nil, ErrGroupPolicyInvalid
				}
			}
		}
		current.GroupBy = groupBy
	}
	for _, override := range []struct {
		value *string
		dest  *string
	}{{raw.GroupWait, &current.GroupWait}, {raw.GroupInterval, &current.GroupInterval}, {raw.RepeatInterval, &current.RepeatInterval}} {
		if override.value != nil {
			if len(*override.value) > 64 || !policyDuration.MatchString(*override.value) {
				return nil, ErrGroupPolicyInvalid
			}
			*override.dest = *override.value
		}
	}
	if len(current.Receivers) == 0 {
		return nil, ErrGroupPolicyInvalid
	}
	matchers, err := compilePolicyMatchers(raw)
	if err != nil {
		return nil, err
	}
	compiled := &effectivePolicyRoute{policy: current, matchers: matchers, continueSiblings: raw.Continue,
		conditional: inheritedConditional || len(raw.MuteIntervals) > 0 || len(raw.ActiveIntervals) > 0}
	for _, child := range raw.Routes {
		next, err := compilePolicyRoute(child, current, compiled.conditional, depth+1, count)
		if err != nil {
			return nil, err
		}
		compiled.children = append(compiled.children, next)
	}
	return compiled, nil
}

func compilePolicyMatchers(raw *policyRoute) ([]policyMatcher, error) {
	result := make([]policyMatcher, 0, len(raw.Matchers)+len(raw.Match)+len(raw.MatchRE))
	for _, text := range raw.Matchers {
		items, err := splitPolicyMatcherSet(text)
		if err != nil || len(result)+len(items)+len(raw.Match)+len(raw.MatchRE) > maxPolicyMatchers {
			return nil, ErrGroupPolicyInvalid
		}
		for _, item := range items {
			parts := inhibitionMatcher.FindStringSubmatch(item)
			if parts == nil {
				return nil, ErrGroupPolicyInvalid
			}
			value := strings.TrimSpace(parts[3])
			if strings.HasPrefix(value, `"`) {
				decoded, err := strconv.Unquote(value)
				if err != nil {
					return nil, ErrGroupPolicyInvalid
				}
				value = decoded
			} else if strings.ContainsAny(value, "{},\"'`\\\t\n\r !~=") {
				return nil, ErrGroupPolicyInvalid
			}
			matcher, err := newPolicyMatcher(parts[1], parts[2], value)
			if err != nil {
				return nil, ErrGroupPolicyInvalid
			}
			result = append(result, matcher)
		}
	}
	for name, value := range raw.Match {
		matcher, err := newPolicyMatcher(name, "=", value)
		if err != nil {
			return nil, err
		}
		result = append(result, matcher)
	}
	for name, value := range raw.MatchRE {
		matcher, err := newPolicyMatcher(name, "=~", value)
		if err != nil {
			return nil, err
		}
		result = append(result, matcher)
	}
	return result, nil
}

// One YAML list entry can contain a comma-separated matcher set. Split only
// outside quoted values, so a comma in a literal or regex remains data.
func splitPolicyMatcherSet(raw string) ([]string, error) {
	text := strings.TrimSpace(raw)
	if text == "" || len(text) > maxPolicyString {
		return nil, ErrGroupPolicyInvalid
	}
	if strings.HasPrefix(text, "{") != strings.HasSuffix(text, "}") {
		return nil, ErrGroupPolicyInvalid
	}
	if strings.HasPrefix(text, "{") {
		text = strings.TrimSpace(text[1 : len(text)-1])
		if text == "" {
			return []string{}, nil
		}
	}
	items := make([]string, 0, 2)
	start := 0
	quoted, escaped := false, false
	for index, r := range text {
		if escaped {
			escaped = false
			continue
		}
		if quoted && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && r == ',' {
			item := strings.TrimSpace(text[start:index])
			if item == "" || len(items) == maxPolicyMatchers {
				return nil, ErrGroupPolicyInvalid
			}
			items = append(items, item)
			start = index + 1
		}
	}
	if quoted || escaped {
		return nil, ErrGroupPolicyInvalid
	}
	last := strings.TrimSpace(text[start:])
	if last != "" {
		items = append(items, last)
	} else if start == 0 || len(items) == 0 {
		return nil, ErrGroupPolicyInvalid
	}
	return items, nil
}

func newPolicyMatcher(name, op, value string) (policyMatcher, error) {
	if !policyLabelName.MatchString(name) || len(value) > maxPolicyString {
		return policyMatcher{}, ErrGroupPolicyInvalid
	}
	m := policyMatcher{name: name, value: value, op: op}
	if op == "=~" || op == "!~" {
		re, err := regexp.Compile("^(?:" + value + ")$")
		if err != nil {
			return policyMatcher{}, ErrGroupPolicyInvalid
		}
		m.re = re
	} else if op != "=" && op != "!=" {
		return policyMatcher{}, ErrGroupPolicyInvalid
	}
	return m, nil
}

func (m policyMatcher) matches(labels map[string]string) bool {
	value := labels[m.name] // Alertmanager treats an absent label as empty.
	switch m.op {
	case "=":
		return value == m.value
	case "!=":
		return value != m.value
	case "=~":
		return m.re.MatchString(value)
	default:
		return !m.re.MatchString(value)
	}
}

func collectPolicyRoutes(route *effectivePolicyRoute, labels map[string]string, leaves *[]GroupPolicyObservation, conditional *bool) {
	matchedChild := false
	for _, child := range route.children {
		matches := true
		for _, matcher := range child.matchers {
			if !matcher.matches(labels) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		matchedChild = true
		collectPolicyRoutes(child, labels, leaves, conditional)
		if !child.continueSiblings {
			break
		}
	}
	if !matchedChild {
		*leaves = append(*leaves, route.policy)
		*conditional = *conditional || route.conditional
	}
}

func equalPolicyGroupBy(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
