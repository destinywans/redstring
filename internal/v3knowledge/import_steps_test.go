package v3knowledge

import (
	"strings"
	"testing"
)

// A top-level numbered list becomes one read-only review step per item; the
// text is kept verbatim and numbers inside code fences do not split it.
func TestImportedReviewStepsSplitNumberedItems(t *testing.T) {
	markdown := "# Rollback checkout-api\nUse when a release fails.\n\n1. Check the error rate\n   on the dashboard\n2. Roll back:\n```\n1. kubectl rollout undo deploy/checkout-api\n```\n3) Confirm recovery\n"
	steps, err := importedReviewSteps(markdown)
	if err != nil || len(steps) != 4 {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	var joined strings.Builder
	for _, step := range steps {
		if step.Kind != "check" || !step.Readonly {
			t.Fatalf("step is not a read-only review: %+v", step)
		}
		joined.WriteString(step.Body)
	}
	if joined.String() != markdown {
		t.Fatalf("source text changed: %q", joined.String())
	}
	if steps[0].Title != "Review introduction" || steps[1].Title != "Review step 1: Check the error rate" ||
		!strings.Contains(steps[2].Body, "kubectl rollout undo") || steps[3].Title != "Review step 3: Confirm recovery" {
		t.Fatalf("titles/bodies: %+v", steps)
	}
	for _, single := range []string{"# Imported source", "Only one step:\n1. Do it\n"} {
		if steps, err := importedReviewSteps(single); err != nil || len(steps) != 1 || steps[0].Body != single {
			t.Fatalf("%q: %+v %v", single, steps, err)
		}
	}
}
