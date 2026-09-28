package v3alertmanager

import (
	"testing"
	"time"
)

func snapshotFixture() (SilenceCreateOperation, Silence) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	request := CreateSilenceRequest{
		Matchers: []Matcher{{Name: "environment", Value: "test", IsEqual: true}, {Name: "application", Value: "api", IsEqual: true}},
		StartsAt: start, EndsAt: start.Add(time.Hour), CreatedBy: "operator", Comment: "maintenance",
		Annotations: map[string]string{silenceOperationAnnotation: "operation-1", "ticket": "WAN-118"},
	}
	op := SilenceCreateOperation{ID: "operation-1", Request: request}
	observed := Silence{
		ID: "123e4567-e89b-12d3-a456-426614174000", Matchers: append([]Matcher(nil), request.Matchers...),
		StartsAt: start.Add(10 * time.Second), EndsAt: request.EndsAt,
		CreatedBy: request.CreatedBy, Comment: request.Comment,
		Annotations: map[string]string{silenceOperationAnnotation: op.ID, "ticket": "WAN-118"},
	}
	return op, observed
}

func TestEvaluateCreateSnapshotMissingAndUniqueAcrossStatuses(t *testing.T) {
	op, observed := snapshotFixture()
	other := observed
	other.Annotations = map[string]string{silenceOperationAnnotation: "other-operation"}
	if got, disposition := evaluateCreateSnapshot(op, []Silence{other}); got != nil || disposition != "missing" {
		t.Fatalf("unrelated marker = (%v, %q), want missing", got, disposition)
	}
	for _, state := range []string{"pending", "active", "expired"} {
		t.Run(state, func(t *testing.T) {
			candidate := observed
			candidate.Status.State = state
			candidate.Matchers = []Matcher{observed.Matchers[1], observed.Matchers[0]}
			got, disposition := evaluateCreateSnapshot(op, []Silence{other, candidate})
			if disposition != "unique" || got == nil || got.ID != candidate.ID {
				t.Fatalf("status %q = (%v, %q), want unique", state, got, disposition)
			}
		})
	}
}

func TestEvaluateCreateSnapshotDuplicatesRequireManualReview(t *testing.T) {
	op, observed := snapshotFixture()
	second := observed
	second.ID = "123e4567-e89b-12d3-a456-426614174001"
	second.Comment = "tampered"
	for _, candidates := range [][]Silence{{observed, second}, {second, observed}, {observed, observed}} {
		got, disposition := evaluateCreateSnapshot(op, candidates)
		if got != nil || disposition != "manual_review" {
			t.Fatalf("duplicate marker = (%v, %q), want manual_review", got, disposition)
		}
	}
}

func TestEvaluateCreateSnapshotPayloadMismatchRequiresManualReview(t *testing.T) {
	op, observed := snapshotFixture()
	tests := map[string]func(*Silence){
		"invalid UUID": func(s *Silence) { s.ID = "not-a-uuid" },
		"matcher":      func(s *Silence) { s.Matchers = []Matcher{{Name: "environment", Value: "prod", IsEqual: true}} },
		"annotation value": func(s *Silence) {
			s.Annotations = map[string]string{silenceOperationAnnotation: op.ID, "ticket": "other"}
		},
		"extra annotation": func(s *Silence) { s.Annotations["extra"] = "value" },
		"creator":          func(s *Silence) { s.CreatedBy = "someone-else" },
		"comment":          func(s *Silence) { s.Comment = "other" },
		"early start":      func(s *Silence) { s.StartsAt = op.Request.StartsAt.Add(-time.Nanosecond) },
		"late start":       func(s *Silence) { s.StartsAt = op.Request.StartsAt.Add(maxAlertmanagerStartShift + time.Nanosecond) },
		"end":              func(s *Silence) { s.EndsAt = s.EndsAt.Add(time.Nanosecond) },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := observed
			candidate.Annotations = map[string]string{silenceOperationAnnotation: op.ID, "ticket": "WAN-118"}
			change(&candidate)
			got, disposition := evaluateCreateSnapshot(op, []Silence{candidate})
			if got != nil || disposition != "manual_review" {
				t.Fatalf("mismatch = (%v, %q), want manual_review", got, disposition)
			}
		})
	}
	op.UpstreamID = "123e4567-e89b-12d3-a456-426614174001"
	if got, disposition := evaluateCreateSnapshot(op, []Silence{observed}); got != nil || disposition != "manual_review" {
		t.Fatalf("upstream ID mismatch = (%v, %q), want manual_review", got, disposition)
	}
}
