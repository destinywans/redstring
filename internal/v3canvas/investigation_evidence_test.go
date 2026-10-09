package v3canvas

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
)

func evidenceCollectorFixture() (*investigationEvidenceCollector, inference.Evidence) {
	c := &investigationEvidenceCollector{ctx: context.Background(), application: "app", environment: "test", names: map[string]bool{"read_service_profile": true}, calls: map[string]inference.ToolInvocation{}, audit: func(inference.ToolInvocation) error { return nil }}
	e := inference.Evidence{Kind: "service_profile", Title: "Stored profile", Content: `{"health":"unavailable"}`, Source: "service_catalog", TargetID: base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))}
	return c, e
}
func emitInvestigationInvocation(c *investigationEvidenceCollector, status, target string) error {
	return c.handle(inference.Event{Type: "tool_invocation", StepID: "step-1", Invocation: &inference.ToolInvocation{Name: "read_service_profile", StepID: "step-1", Status: status, TargetID: target}})
}

func TestInvestigationEvidenceRequiresAuditedReadSequence(t *testing.T) {
	c, e := evidenceCollectorFixture()
	var audit []string
	c.audit = func(v inference.ToolInvocation) error { audit = append(audit, v.Status); return nil }
	if err := emitInvestigationInvocation(c, "started", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.finish(); err == nil {
		t.Fatal("unfinished read accepted")
	}
	if err := emitInvestigationInvocation(c, "succeeded", e.TargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.finish(); err == nil {
		t.Fatal("successful read without evidence accepted")
	}
	if err := c.handle(inference.Event{Type: "evidence", StepID: "step-1", Evidence: &e}); err != nil {
		t.Fatal(err)
	}
	got, err := c.finish()
	if err != nil || len(got) != 1 || got[0].Application != "app" || got[0].Environment != "test" || got[0].Evidence != e || strings.Join(audit, ",") != "started,succeeded" {
		t.Fatalf("receipt mismatch: %#v %v", got, err)
	}
	if err = c.handle(inference.Event{Type: "evidence", StepID: "step-1", Evidence: &e}); err == nil {
		t.Fatal("duplicate evidence accepted")
	}
	if _, err = c.finish(); err == nil {
		t.Fatal("ignored callback error became success")
	}
}

func TestInvestigationEvidenceRejectsForgedScopeAndUnmatchedEvents(t *testing.T) {
	for _, name := range []string{"no start", "wrong tool", "wrong step", "wrong target", "wrong source", "oversized", "invalid utf8", "nul", "duplicate start", "failed read", "audit failure"} {
		t.Run(name, func(t *testing.T) {
			c, e := evidenceCollectorFixture()
			if name == "no start" {
				if c.handle(inference.Event{Type: "evidence", StepID: "step-1", Evidence: &e}) == nil {
					t.Fatal("unstarted evidence")
				}
				return
			}
			if name == "audit failure" {
				c.audit = func(inference.ToolInvocation) error { return errors.New("audit failed") }
				if emitInvestigationInvocation(c, "started", "") == nil {
					t.Fatal("audit failure ignored")
				}
				return
			}
			if err := emitInvestigationInvocation(c, "started", ""); err != nil {
				t.Fatal(err)
			}
			if name == "duplicate start" {
				if emitInvestigationInvocation(c, "started", "") == nil {
					t.Fatal("duplicate")
				}
				return
			}
			if name == "failed read" {
				if emitInvestigationInvocation(c, "failed", "") == nil {
					t.Fatal("failed read accepted")
				}
				if _, err := c.finish(); err == nil {
					t.Fatal("failure discarded")
				}
				return
			}
			if name == "wrong tool" {
				if c.handle(inference.Event{Type: "tool_invocation", StepID: "step-1", Invocation: &inference.ToolInvocation{Name: "execute_command", StepID: "step-1", Status: "succeeded"}}) == nil {
					t.Fatal("unknown tool")
				}
				return
			}
			if err := emitInvestigationInvocation(c, "succeeded", e.TargetID); err != nil {
				t.Fatal(err)
			}
			step := "step-1"
			switch name {
			case "wrong step":
				step = "step-2"
			case "wrong target":
				e.TargetID = "other"
			case "wrong source":
				e.Source = "https://secret"
			case "oversized":
				e.Content = strings.Repeat("x", maxInvestigationText)
			case "invalid utf8":
				e.Content = "\xff"
			case "nul":
				e.Content = "a\x00b"
			}
			if c.handle(inference.Event{Type: "evidence", StepID: step, Evidence: &e}) == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestInvestigationEvidenceBoundedAndContextCancellation(t *testing.T) {
	c, _ := evidenceCollectorFixture()
	for i := 0; i < maxInvestigationToolCalls; i++ {
		step := string(rune('a' + i))
		if err := c.handle(inference.Event{Type: "tool_invocation", StepID: step, Invocation: &inference.ToolInvocation{Name: "read_service_profile", StepID: step, Status: "started"}}); err != nil {
			t.Fatal(err)
		}
	}
	if emitInvestigationInvocation(c, "started", "") == nil {
		t.Fatal("tool count exceeded")
	}
	c, _ = evidenceCollectorFixture()
	ctx, cancel := context.WithCancel(context.Background())
	c.ctx = ctx
	cancel()
	if _, err := c.finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collection accepted: %v", err)
	}
}

// PA-40: masked log samples come from the service's single Loki or
// Elasticsearch binding; any other source is still rejected.
func TestInvestigationLogEvidenceSources(t *testing.T) {
	target := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	for source, want := range map[string]bool{"loki": true, "elasticsearch": true, "prometheus": false, "https://es.internal": false} {
		e := inference.Evidence{Kind: "logs", Title: "脱敏日志样本: checkout", Content: `{"status":"masked_sample"}`, Source: source, TargetID: target}
		if validInvestigationEvidence("read_service_logs", "app", "test", e) != want {
			t.Fatalf("source %q admitted=%v", source, !want)
		}
	}
	e := inference.Evidence{Kind: "metrics", Title: "m", Content: "{}", Source: "elasticsearch", TargetID: target}
	if validInvestigationEvidence("read_service_metrics", "app", "test", e) {
		t.Fatal("elasticsearch accepted for metrics")
	}
}

// PA-52: an investigation answer that ended at a run limit carries a note in
// the request language.
func TestReplyLimitNote(t *testing.T) {
	limited := replyLimitNote("zh-CN", inference.ErrEvidenceLimitReached)
	both := replyLimitNote("en-US", errors.Join(inference.ErrOutputTruncated, inference.ErrEvidenceLimitReached))
	if !strings.Contains(limited, "取证次数已达上限") || strings.Contains(limited, "截断") ||
		!strings.Contains(both, "evidence reads") || !strings.Contains(both, "output limit") || replyLimitNote("", nil) != "" {
		t.Fatalf("notes: %q %q", limited, both)
	}
}
