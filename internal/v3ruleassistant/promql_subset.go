package v3ruleassistant

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// This intentionally small PromQL parser accepts instant selectors, bounded
// rate/increase, arithmetic and aggregations that retain all scope labels.
// Unsupported PromQL must be reviewed and added explicitly, never passed
// through because a string merely looks plausible.
var errDraftPromQL = errors.New("unsupported or unscoped PromQL draft")
var metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type promToken struct{ kind, value string }

func lexDraftPromQL(input string) ([]promToken, error) {
	if input == "" || len(input) > 2048 || strings.TrimSpace(input) != input || strings.ContainsAny(input, "\x00\r\n") {
		return nil, errDraftPromQL
	}
	result := make([]promToken, 0, 96)
	for i := 0; i < len(input); {
		c := input[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		if c == '"' {
			start := i
			i++
			for i < len(input) {
				if input[i] == '\\' {
					i += 2
					continue
				}
				if input[i] == '"' {
					i++
					break
				}
				i++
			}
			if i > len(input) || input[i-1] != '"' {
				return nil, errDraftPromQL
			}
			value, err := strconv.Unquote(input[start:i])
			if err != nil || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, errDraftPromQL
			}
			result = append(result, promToken{"string", value})
			continue
		}
		if isPromIdentStart(c) {
			start := i
			for i < len(input) && isPromIdent(input[i]) {
				i++
			}
			result = append(result, promToken{"ident", input[start:i]})
			continue
		}
		if c >= '0' && c <= '9' || c == '.' && i+1 < len(input) && input[i+1] >= '0' && input[i+1] <= '9' {
			start := i
			for i < len(input) && (input[i] >= '0' && input[i] <= '9' || input[i] == '.') {
				i++
			}
			result = append(result, promToken{"number", input[start:i]})
			continue
		}
		if i+1 < len(input) && (input[i:i+2] == "=~" || input[i:i+2] == "!=") {
			result = append(result, promToken{"symbol", input[i : i+2]})
			i += 2
			continue
		}
		if strings.ContainsRune("{}[](),=+-*/", rune(c)) {
			result = append(result, promToken{"symbol", string(c)})
			i++
			continue
		}
		return nil, errDraftPromQL
	}
	if len(result) == 0 || len(result) > 256 {
		return nil, errDraftPromQL
	}
	result = append(result, promToken{"eof", ""})
	return result, nil
}

func isPromIdentStart(c byte) bool {
	return c == '_' || c == ':' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
func isPromIdent(c byte) bool {
	return isPromIdentStart(c) || c >= '0' && c <= '9'
}

type draftParser struct {
	tokens  []promToken
	i       int
	binding PreviewBinding
	metrics map[string]bool
	seen    map[string]bool
	depth   int
}

func (p *draftParser) peek() promToken { return p.tokens[p.i] }
func (p *draftParser) take(value string) bool {
	if p.peek().value != value {
		return false
	}
	p.i++
	return true
}
func (p *draftParser) require(value string) error {
	if !p.take(value) {
		return errDraftPromQL
	}
	return nil
}

// expression returns one of scalar, instant, or range. Prometheus itself
// still validates execution; this parser is a strict admission boundary.
func (p *draftParser) expression(minPrecedence int) (string, error) {
	if p.depth > 24 {
		return "", errDraftPromQL
	}
	p.depth++
	defer func() { p.depth-- }()
	kind, err := p.primary()
	if err != nil {
		return "", err
	}
	for {
		op := p.peek().value
		precedence := 0
		if op == "+" || op == "-" {
			precedence = 1
		} else if op == "*" || op == "/" {
			precedence = 2
		}
		if precedence < minPrecedence || precedence == 0 {
			break
		}
		p.i++
		right, e := p.expression(precedence + 1)
		if e != nil || kind == "range" || right == "range" {
			return "", errDraftPromQL
		}
		if kind == "scalar" && right == "scalar" {
			kind = "scalar"
		} else if kind == "raw" || right == "raw" {
			kind = "raw"
		} else {
			kind = "aggregate"
		}
	}
	return kind, nil
}

func (p *draftParser) primary() (string, error) {
	token := p.peek()
	if token.value == "(" {
		p.i++
		kind, err := p.expression(1)
		if err != nil || p.require(")") != nil {
			return "", errDraftPromQL
		}
		return kind, nil
	}
	if token.kind == "number" {
		p.i++
		value, err := strconv.ParseFloat(token.value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1e9 {
			return "", errDraftPromQL
		}
		return "scalar", nil
	}
	if token.kind != "ident" {
		return "", errDraftPromQL
	}
	p.i++
	switch token.value {
	case "rate", "increase":
		if p.require("(") != nil {
			return "", errDraftPromQL
		}
		kind, err := p.selector()
		if err != nil || kind != "range" || p.require(")") != nil {
			return "", errDraftPromQL
		}
		return "raw", nil
	case "sum", "avg", "max", "min":
		if p.require("by") != nil || p.require("(") != nil {
			return "", errDraftPromQL
		}
		labels := map[string]bool{}
		for {
			label := p.peek()
			if label.kind != "ident" || !labelNamePattern.MatchString(label.value) || labels[label.value] || len(labels) >= 12 {
				return "", errDraftPromQL
			}
			labels[label.value] = true
			p.i++
			if !p.take(",") {
				break
			}
		}
		if p.require(")") != nil || p.require("(") != nil || len(labels) != 3 || !labels[p.binding.ApplicationLabel] ||
			!labels[p.binding.EnvironmentLabel] || !labels[p.binding.ServiceLabel] {
			return "", errDraftPromQL
		}
		kind, err := p.expression(1)
		if err != nil || kind != "raw" && kind != "aggregate" || p.require(")") != nil {
			return "", errDraftPromQL
		}
		return "aggregate", nil
	default:
		p.i--
		return p.selector()
	}
}

func (p *draftParser) selector() (string, error) {
	metric := p.peek()
	if metric.kind != "ident" || !metricNamePattern.MatchString(metric.value) || metric.value == "__name__" ||
		p.metrics != nil && !p.metrics[metric.value] {
		return "", errDraftPromQL
	}
	p.i++
	p.seen[metric.value] = true
	if p.require("{") != nil {
		return "", errDraftPromQL
	}
	labels := map[string]string{}
	for {
		label := p.peek()
		if label.kind != "ident" || !labelNamePattern.MatchString(label.value) || label.value == "__name__" || len(labels) >= 12 {
			return "", errDraftPromQL
		}
		p.i++
		op := p.peek().value
		if op != "=" && op != "=~" {
			return "", errDraftPromQL
		}
		p.i++
		value := p.peek()
		if value.kind != "string" || len(value.value) == 0 {
			return "", errDraftPromQL
		}
		p.i++
		if _, exists := labels[label.value]; exists {
			return "", errDraftPromQL
		}
		if (label.value == p.binding.ApplicationLabel || label.value == p.binding.EnvironmentLabel || label.value == p.binding.ServiceLabel) && op != "=" {
			return "", errDraftPromQL
		}
		if op == "=~" && (len(value.value) > 32 || strings.ContainsAny(value.value, "\\{}[]()")) {
			return "", errDraftPromQL
		}
		if op == "=~" {
			if _, err := regexp.Compile(value.value); err != nil {
				return "", errDraftPromQL
			}
		}
		labels[label.value] = value.value
		if !p.take(",") {
			break
		}
	}
	if p.require("}") != nil || labels[p.binding.ApplicationLabel] != p.binding.ApplicationID ||
		labels[p.binding.EnvironmentLabel] != p.binding.EnvironmentID || labels[p.binding.ServiceLabel] != p.binding.Service {
		return "", errDraftPromQL
	}
	if p.take("[") {
		length := p.peek()
		if length.kind != "number" || length.value != "1" && length.value != "5" && length.value != "10" &&
			length.value != "15" && length.value != "30" {
			return "", errDraftPromQL
		}
		p.i++
		if p.peek().value != "m" {
			return "", errDraftPromQL
		}
		p.i++
		if p.require("]") != nil {
			return "", errDraftPromQL
		}
		return "range", nil
	}
	return "raw", nil
}

func parseDraftPromQL(binding PreviewBinding, query string, allowed map[string]bool) (map[string]bool, error) {
	tokens, err := lexDraftPromQL(query)
	if err != nil || binding.ApplicationLabel == "" || binding.EnvironmentLabel == "" || binding.ServiceLabel == "" {
		return nil, errDraftPromQL
	}
	p := draftParser{tokens: tokens, binding: binding, metrics: allowed, seen: map[string]bool{}}
	kind, err := p.expression(1)
	if err != nil || kind != "aggregate" || p.peek().kind != "eof" || len(p.seen) == 0 {
		return nil, errDraftPromQL
	}
	return p.seen, nil
}

func validateDraftPromQL(binding PreviewBinding, query string) error {
	approved, err := parseDraftPromQL(binding, binding.Query, nil)
	if err != nil || len(approved) > 8 {
		return errDraftPromQL
	}
	_, err = parseDraftPromQL(binding, query, approved)
	return err
}
