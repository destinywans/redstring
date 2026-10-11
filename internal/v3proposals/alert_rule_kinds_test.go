package v3proposals

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// D11 decision 7: a disable uses the alert rule domain verifier, quorum and
// executor readiness, like an enable.
func TestAlertRuleDisableUsesTheEnableApprovalPath(t *testing.T) {
	ready := 0
	s := &Service{
		AlertRuleEnableIntent: func(context.Context, pgx.Tx, string) (AlertRuleEnableReview, error) {
			return AlertRuleEnableReview{}, nil
		},
		AlertRuleEnableReady: func(context.Context, string) error { ready++; return nil },
	}
	for _, kind := range []string{"prometheus.alert_rule.enable", "prometheus.alert_rule.disable"} {
		if !alertRuleActionKind(kind) {
			t.Fatalf("%s is not an alert rule action", kind)
		}
		if err := s.approvalWorkflowReady(t.Context(), kind, "prod"); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if ready != 2 || alertRuleActionKind("prometheus.alert_rule.delete") {
		t.Fatalf("ready=%d", ready)
	}
	if err := s.approvalWorkflowReady(t.Context(), "prometheus.alert_rule.delete", "prod"); !errors.Is(err, v3alertmanager.ErrNotConfigured) {
		t.Fatalf("unknown kind: %v", err)
	}
}
