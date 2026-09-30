package v3alertmanager

// evaluateCreateSnapshot examines a complete, unfiltered Alertmanager silence
// snapshot. A marker collision is ambiguous even if only one record has the
// expected payload, so the caller must not confirm or retry that operation.
// A missing marker is distinct from a mismatched record bearing the marker.
func evaluateCreateSnapshot(op SilenceCreateOperation, silences []Silence) (observed *Silence, disposition string) {
	if op.ID == "" {
		return nil, "manual_review"
	}
	var candidate *Silence
	for i := range silences {
		if silences[i].Annotations[silenceOperationAnnotation] != op.ID {
			continue
		}
		if candidate != nil {
			return nil, "manual_review"
		}
		candidate = &silences[i]
	}
	if candidate == nil {
		return nil, "missing"
	}
	if !matchesCreateSnapshot(op, *candidate) {
		return nil, "manual_review"
	}
	return candidate, "unique"
}

func matchesCreateSnapshot(op SilenceCreateOperation, observed Silence) bool {
	request := op.Request
	if !validSilenceID(observed.ID) || op.UpstreamID != "" && op.UpstreamID != observed.ID ||
		observed.StartsAt.Before(request.StartsAt) ||
		observed.StartsAt.After(request.StartsAt.Add(maxAlertmanagerStartShift)) ||
		!observed.StartsAt.Before(request.EndsAt) ||
		!observed.EndsAt.Equal(request.EndsAt) ||
		observed.CreatedBy != request.CreatedBy || observed.Comment != request.Comment ||
		!sameSilenceMatchers(observed.Matchers, request.Matchers) ||
		len(observed.Annotations) != len(request.Annotations) {
		return false
	}
	for key, value := range request.Annotations {
		actual, exists := observed.Annotations[key]
		if !exists || actual != value {
			return false
		}
	}
	return true
}
