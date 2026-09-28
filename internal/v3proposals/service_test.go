package v3proposals

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/store"
)

func completeContent() Content {
	return Content{
		Title: "Restore checked configuration", Risk: "medium", Impact: "One service",
		ProposedBy: "investigation agent", InvestigationID: "inv-1", Target: "app-1 in test",
		Diff:   Diff{Path: "config.yaml", Lines: []string{"- old", "+ new", "   context: 30s"}},
		Method: "Apply after approval", Evidence: []Evidence{{N: 1, Icon: "file", Title: "Source diff", Desc: "Commit 123"}},
		Verification: []string{"Check health"}, VerificationNote: "Escalate on failure",
		Checklist: []string{"Confirm target"}, Requirement: "Human review",
	}
}

func TestProposalRequiresSourcedReviewFields(t *testing.T) {
	c := completeContent()
	if !validContent(c) {
		t.Fatal("complete content rejected")
	}
	c.Evidence = nil
	if validContent(c) {
		t.Fatal("unsourced proposal accepted")
	}
	_, err := (&Service{}).Create(context.Background(), NewProposal{
		ApplicationID: "app-1", EnvironmentID: "test", CreatedBy: "agent-1", CreatorKind: "agent", Content: c,
	})
	if !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("Create error = %v; want invalid proposal", err)
	}
}

func TestProposalWireShapeDoesNotNestContent(t *testing.T) {
	p := Proposal{ID: store.ID(), Status: "pending", ProposedAt: "2026-09-28T00:00:00Z", Content: completeContent(), Discussion: []CommentItem{}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"title", "risk", "diff", "evidence", "verification", "checklist", "investigation_id", "discussion"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing V3 field %s", key)
		}
	}
	if _, nested := got["Content"]; nested {
		t.Fatal("embedded content serialized as a nested object")
	}
}
