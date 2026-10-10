package v3kube

import (
	"encoding/json"
	"reflect"
	"sort"
)

// Change is one difference a rollback makes to the Pod template, from the
// current template (From) to the target (To). Values that come from a Secret
// or ConfigMap are never shown (ValueHidden).
type Change struct {
	Container   string  `json:"container"`
	Field       string  `json:"field"`
	Name        *string `json:"name"`
	From        *string `json:"from"`
	To          *string `json:"to"`
	ValueHidden bool    `json:"value_hidden"`
}

// Container is the name and image of one container.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

const maxChanges = 50

type podTemplate struct {
	Metadata map[string]any `json:"metadata"`
	Spec     map[string]any `json:"spec"`
}

func parseTemplate(raw json.RawMessage) (podTemplate, map[string]map[string]any, []string) {
	var t podTemplate
	_ = json.Unmarshal(raw, &t)
	containers := map[string]map[string]any{}
	var order []string
	if list, ok := t.Spec["containers"].([]any); ok {
		for _, item := range list {
			if c, ok := item.(map[string]any); ok {
				if name, ok := c["name"].(string); ok {
					containers[name] = c
					order = append(order, name)
				}
			}
		}
	}
	return t, containers, order
}

// Containers lists the containers of a template in order.
func Containers(raw json.RawMessage) []Container {
	_, containers, order := parseTemplate(raw)
	out := make([]Container, 0, len(order))
	for _, name := range order {
		image, _ := containers[name]["image"].(string)
		out = append(out, Container{Name: name, Image: image})
	}
	return out
}

func strPtr(s string) *string { return &s }

func compact(v any) *string {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return strPtr(string(data))
}

type envValue struct {
	value  string
	hidden bool
}

func envOf(c map[string]any) map[string]envValue {
	out := map[string]envValue{}
	list, _ := c["env"].([]any)
	for _, item := range list {
		e, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := e["name"].(string)
		if name == "" {
			continue
		}
		if _, ref := e["valueFrom"]; ref {
			out[name] = envValue{hidden: true}
			continue
		}
		value, _ := e["value"].(string)
		out[name] = envValue{value: value}
	}
	return out
}

func without(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// Changes lists what replacing the current template with target changes.
func Changes(current, target json.RawMessage) []Change {
	from, fromContainers, fromOrder := parseTemplate(current)
	to, toContainers, toOrder := parseTemplate(target)
	names := map[string]bool{}
	var order []string
	for _, n := range append(fromOrder, toOrder...) {
		if !names[n] {
			names[n] = true
			order = append(order, n)
		}
	}
	var out []Change
	add := func(c Change) {
		if len(out) < maxChanges {
			out = append(out, c)
		}
	}
	for _, name := range order {
		a, b := fromContainers[name], toContainers[name]
		if a == nil || b == nil {
			var fromImage, toImage *string
			if a != nil {
				fromImage = compact(a["image"])
			}
			if b != nil {
				toImage = compact(b["image"])
			}
			add(Change{Container: name, Field: "image", From: unquote(fromImage), To: unquote(toImage)})
			continue
		}
		if !reflect.DeepEqual(a["image"], b["image"]) {
			add(Change{Container: name, Field: "image", From: unquote(compact(a["image"])), To: unquote(compact(b["image"]))})
		}
		envA, envB := envOf(a), envOf(b)
		var envNames []string
		for n := range envA {
			envNames = append(envNames, n)
		}
		for n := range envB {
			if _, ok := envA[n]; !ok {
				envNames = append(envNames, n)
			}
		}
		sort.Strings(envNames)
		for _, n := range envNames {
			va, inA := envA[n]
			vb, inB := envB[n]
			if inA && inB && va == vb && !va.hidden {
				continue
			}
			if inA && inB && va.hidden && vb.hidden && reflect.DeepEqual(envEntry(a, n), envEntry(b, n)) {
				continue
			}
			c := Change{Container: name, Field: "env", Name: strPtr(n)}
			if va.hidden || vb.hidden {
				c.ValueHidden = true
			} else {
				if inA {
					c.From = strPtr(va.value)
				}
				if inB {
					c.To = strPtr(vb.value)
				}
			}
			add(c)
		}
		if !reflect.DeepEqual(a["resources"], b["resources"]) {
			add(Change{Container: name, Field: "resources", From: compact(a["resources"]), To: compact(b["resources"])})
		}
		if !reflect.DeepEqual(a["command"], b["command"]) || !reflect.DeepEqual(a["args"], b["args"]) {
			add(Change{Container: name, Field: "command",
				From: compact(map[string]any{"command": a["command"], "args": a["args"]}),
				To:   compact(map[string]any{"command": b["command"], "args": b["args"]})})
		}
		if !reflect.DeepEqual(without(a, "image", "env", "resources", "command", "args"),
			without(b, "image", "env", "resources", "command", "args")) {
			add(Change{Container: name, Field: "other"})
		}
	}
	if !reflect.DeepEqual(without(from.Spec, "containers"), without(to.Spec, "containers")) ||
		!reflect.DeepEqual(from.Metadata, to.Metadata) {
		add(Change{Container: "", Field: "other"})
	}
	if out == nil {
		out = []Change{}
	}
	return out
}

func envEntry(c map[string]any, name string) any {
	list, _ := c["env"].([]any)
	for _, item := range list {
		if e, ok := item.(map[string]any); ok && e["name"] == name {
			return e
		}
	}
	return nil
}

func unquote(s *string) *string {
	if s == nil {
		return nil
	}
	var v string
	if json.Unmarshal([]byte(*s), &v) == nil {
		return &v
	}
	return s
}
