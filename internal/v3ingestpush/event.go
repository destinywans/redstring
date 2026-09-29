package v3ingestpush

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

var eventTypes = map[string]bool{"alert": true, "change": true, "incident": true, "resource": true, "oncall": true}
var eventIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var attributeKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
var sensitiveAttribute = regexp.MustCompile(`(?i)(secret|token|password|credential|api[_-]?key|private[_-]?key|authorization|cookie)`)

type event struct {
	Schema     string
	EventID    string
	OccurredAt time.Time
	Subject    string
	Title      string
	State      string
	Severity   string
	Attributes map[string]string
}

var errInvalidEvent = errors.New("invalid canonical event")

// strictObject checks raw key spelling and duplicates before decoding values.
// encoding/json struct matching would otherwise accept case-insensitive aliases.
func strictObject(body []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errInvalidEvent
	}
	values := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errInvalidEvent
		}
		key, ok := tok.(string)
		if !ok || !allowed[key] || seen[strings.ToLower(key)] {
			return nil, errInvalidEvent
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, errInvalidEvent
		}
		values[key] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errInvalidEvent
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errInvalidEvent
	}
	return values, nil
}

func stringField(values map[string]json.RawMessage, key string, required bool, max int) (string, error) {
	raw, exists := values[key]
	if !exists {
		if required {
			return "", errInvalidEvent
		}
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || strings.TrimSpace(value) != value || len(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return "", errInvalidEvent
	}
	return value, nil
}

func parseEvent(typ string, body []byte, now time.Time) (event, error) {
	if !eventTypes[typ] || len(body) == 0 || len(body) > 64<<10 {
		return event{}, errInvalidEvent
	}
	allowed := map[string]bool{"schema": true, "event_id": true, "occurred_at": true, "subject": true, "title": true, "state": true, "severity": true, "attributes": true}
	values, err := strictObject(body, allowed)
	if err != nil {
		return event{}, err
	}
	e := event{Attributes: map[string]string{}}
	for _, field := range []struct {
		key      string
		required bool
		max      int
		dest     *string
	}{
		{"schema", true, 32, &e.Schema}, {"event_id", true, 128, &e.EventID},
		{"subject", true, 200, &e.Subject}, {"title", true, 300, &e.Title},
		{"state", true, 32, &e.State}, {"severity", typ == "alert", 32, &e.Severity},
	} {
		value, err := stringField(values, field.key, field.required, field.max)
		if err != nil {
			return event{}, errInvalidEvent
		}
		*field.dest = value
	}
	if e.Schema != typ+".v1" || !eventIdentity.MatchString(e.EventID) {
		return event{}, errInvalidEvent
	}
	occurred, err := stringField(values, "occurred_at", true, 64)
	if err != nil {
		return event{}, err
	}
	e.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
	if err != nil || e.OccurredAt.Before(now.AddDate(-1, 0, 0)) || e.OccurredAt.After(now.Add(5*time.Minute)) {
		return event{}, errInvalidEvent
	}
	states := map[string]map[string]bool{
		"alert":    {"firing": true, "resolved": true},
		"change":   {"started": true, "succeeded": true, "failed": true, "rolled_back": true},
		"incident": {"open": true, "acknowledged": true, "resolved": true},
		"resource": {"created": true, "updated": true, "deleted": true},
		"oncall":   {"started": true, "ended": true, "handoff": true},
	}
	if !states[typ][e.State] || (typ != "alert" && e.Severity != "") {
		return event{}, errInvalidEvent
	}
	if typ == "alert" && e.Severity != "critical" && e.Severity != "warning" && e.Severity != "info" {
		return event{}, errInvalidEvent
	}
	if raw, ok := values["attributes"]; ok {
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(raw, &attrs); err != nil || attrs == nil || len(attrs) > 16 {
			return event{}, errInvalidEvent
		}
		keys := map[string]bool{}
		// Decode again as a token stream to reject duplicate keys in attributes.
		dec := json.NewDecoder(bytes.NewReader(raw))
		if token, err := dec.Token(); err != nil || token != json.Delim('{') {
			return event{}, errInvalidEvent
		}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return event{}, errInvalidEvent
			}
			key, ok := token.(string)
			if !ok || !attributeKey.MatchString(key) || sensitiveAttribute.MatchString(key) || keys[strings.ToLower(key)] {
				return event{}, errInvalidEvent
			}
			keys[strings.ToLower(key)] = true
			var value string
			if err := dec.Decode(&value); err != nil || len(value) > 512 || strings.ContainsRune(value, '\x00') {
				return event{}, errInvalidEvent
			}
			e.Attributes[key] = value
		}
		if token, err := dec.Token(); err != nil || token != json.Delim('}') {
			return event{}, errInvalidEvent
		}
	}
	return e, nil
}
