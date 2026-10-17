package v3proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// staleSilenceWording is the review text written before PA-121. It said no
// silence would be created; approval now hands the proposal to the executor.
var staleSilenceWording = []string{"No silence is created", "No upstream silence is expired",
	"separate approval and execution flow", "later enabled", "any future execution", "grants no permission"}

func storedReview(t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) Content {
	t.Helper()
	var raw []byte
	if err := q.QueryRow(t.Context(), `SELECT content FROM v3_proposals WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var content Content
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	for _, stale := range staleSilenceWording {
		if strings.Contains(string(raw), stale) {
			t.Fatalf("review keeps outdated wording %q: %s", stale, raw)
		}
	}
	return content
}

// PA-121: a silence proposal's review is stored in the requester's language
// and describes the two approvals and the executor's single write.
func TestSilenceCreateReviewFollowsRequesterLanguage(t *testing.T) {
	f := newSilenceProposalFixture(t)
	for i, tc := range []struct{ lang, title, impact, checklist, requirement, duration string }{
		{"zh-CN", "静默应用 checkout 的告警",
			"批准后，执行器在 Alertmanager 中为该应用和环境写入静默，从写入时起 1 小时内匹配的告警不再发送通知。",
			"两位非发起人的审批人分别用通行密钥批准。",
			"待审期间不会写入 Alertmanager；第二位审批人批准后自动执行。", "时长：1 小时"},
		{"en-US", `Silence application "checkout"`,
			"Once approved, the executor writes the silence for this application and environment to Alertmanager; matching alerts stop notifying for 1 hour from that moment.",
			"Two approvers other than the requester each approve with a passkey.",
			"Nothing is written to Alertmanager while the review is pending; it runs automatically after the second approval.", "Duration: 1 hour"},
	} {
		ctx := httpapi.WithRequestLanguage(t.Context(), tc.lang)
		created, err := f.svc.CreateSilenceProposal(ctx, f.actor, silenceProposalInput(fmt.Sprintf("review-lang-%03d", i)), f.ledger, f.client)
		if err != nil {
			t.Fatalf("%s create: %v", tc.lang, err)
		}
		content := storedReview(t, f.db, created.ProposalID)
		if content.Title != tc.title || content.Impact != tc.impact || len(content.Checklist) == 0 ||
			content.Checklist[0] != tc.checklist || content.Requirement != tc.requirement ||
			len(content.Diff.Lines) != 5 || content.Diff.Lines[3] != tc.duration {
			t.Fatalf("%s review: %+v", tc.lang, content)
		}
		// Approval and the executor reload the action and its stored review.
		func() {
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, _, err := LoadSilenceActionWithTx(t.Context(), tx, created.ProposalID); err != nil {
				t.Fatalf("%s review rejected by loader: %v", tc.lang, err)
			}
		}()
	}
}

// A proposal written before PA-121 keeps its English review and still loads
// for approval and execution; no stored review is rewritten.
func TestSilenceCreateReviewWrittenBeforePA121StillLoads(t *testing.T) {
	f := newSilenceProposalFixture(t)
	ctx := t.Context()
	created, err := f.svc.CreateSilenceProposal(ctx, f.actor, silenceProposalInput("review-legacy-001"), f.ledger, f.client)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT action FROM v3_proposal_actions WHERE proposal_id=$1`, created.ProposalID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var action SilenceActionSnapshot
	if err := json.Unmarshal(raw, &action); err != nil {
		t.Fatal(err)
	}
	s := action.Silence
	// The pre-PA-121 review, byte for byte as that producer wrote it.
	lines := []string{}
	for _, m := range s.Matchers {
		op := map[[2]bool]string{{true, false}: "=", {false, false}: "!=", {true, true}: "=~", {false, true}: "!~"}[[2]bool{m.IsEqual, m.IsRegex}]
		lines = append(lines, fmt.Sprintf("%s %s %q", m.Name, op, m.Value))
	}
	lines = append(lines, fmt.Sprintf("Duration: %d seconds", s.DurationSeconds), fmt.Sprintf("Comment: %q", s.Comment))
	legacy := Content{
		Title:           fmt.Sprintf("Silence application %q", s.ApplicationLabel),
		Risk:            "high",
		Impact:          "Matching alerts for this application and environment would be silenced for the requested duration if a separate approval and execution flow is later enabled.",
		ProposedBy:      s.ActorID,
		InvestigationID: action.InvestigationID,
		Target:          fmt.Sprintf("Alertmanager application=%q, %s=%q", s.ApplicationLabel, s.EnvironmentLabel, s.EnvironmentID),
		Diff:            Diff{Path: "alertmanager/silences", Lines: lines},
		Method:          "Human-requested application-scoped Alertmanager silence. No silence is created by this proposal.",
		Evidence: []Evidence{{N: 1, Icon: "activity", Title: "Request context",
			Desc: "Authenticated human request linked to an open investigation in the same active application and environment."}},
		Verification:     []string{"Recheck the immutable action digest and current application, environment, access grants, and server Alertmanager binding before any future execution."},
		VerificationNote: "No current alert count or upstream silence state was asserted at proposal time.",
		Checklist:        []string{"Independent human approval with fresh passkey proof is required before execution."},
		Requirement:      "This record is pending review and grants no permission to write to Alertmanager.",
	}
	canonical, err := canonicalProposalJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE v3_proposals DISABLE TRIGGER v3_proposal_review_identity_immutable`,
		`ALTER TABLE v3_proposal_actions DISABLE TRIGGER v3_proposal_action_immutable`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE v3_proposals SET content=$2::jsonb WHERE id=$1`, created.ProposalID, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE v3_proposal_actions SET review_content_digest=$2 WHERE proposal_id=$1`,
		created.ProposalID, proposalDigest("aiops.v3.proposal.review.v1", canonical)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSilenceActionWithTx(ctx, tx, created.ProposalID); err != nil {
		t.Fatalf("review written before PA-121 rejected: %v", err)
	}
	// Any other text is still rejected.
	if _, err := tx.Exec(ctx, `UPDATE v3_proposals SET content=jsonb_set(content,'{title}','"Silence application \"other\""') WHERE id=$1`, created.ProposalID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSilenceActionWithTx(ctx, tx, created.ProposalID); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("altered review err=%v", err)
	}
}

// PA-121: the early-end (cancel) review is written in the requester's
// language in the caller's transaction.
func TestSilenceCancelReviewFollowsRequesterLanguage(t *testing.T) {
	f := newSilenceProposalFixture(t)
	producer := &SilenceProductProducer{Proposals: f.svc, Ledger: f.ledger, Client: f.client}
	var updated time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT updated_at FROM investigations WHERE id='inv-1'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ lang, title, impact, requirement, reason string }{
		{"zh-CN", "提前结束应用 checkout 的静默", "批准后，执行器让这条静默在 Alertmanager 中立即到期，匹配的告警恢复通知。",
			"待审期间不会让任何静默到期；第二位审批人批准后自动执行。", "原因：Alert fixed"},
		{"en-US", `Cancel application silence "checkout"`, "Once approved, the executor expires this silence in Alertmanager and matching alerts notify again.",
			"No silence is expired while the review is pending; it runs automatically after the second approval.", "Reason: Alert fixed"},
	} {
		ctx := httpapi.WithRequestLanguage(t.Context(), tc.lang)
		action := v3alertmanager.ProposedSilenceCancel{SchemaVersion: v3alertmanager.ProposedSilenceCancelVersion,
			Kind: v3alertmanager.ProposedSilenceCancelKind, IntentID: "intent-1", CreateOperationID: "operation-1",
			CreatePayloadHash: strings.Repeat("a", 64), BindingFingerprint: strings.Repeat("b", 64), ActorID: f.actor.ID,
			EnvironmentID: "test", EnvironmentLabel: "environment", ApplicationID: "app-1", ApplicationLabel: "checkout",
			InvestigationID: "inv-1", InvestigationUpdatedAt: updated, UpstreamID: "upstream-1",
			IdempotencyKey: fmt.Sprintf("cancel-lang-%03d", i), RequestHash: strings.Repeat("c", 64),
			ObservedDigest: strings.Repeat("d", 64), ObservedAt: time.Now().UTC(), Reason: "Alert fixed"}
		// The cancel domain commits the review together with its intent;
		// this test only reads the review inside the uncommitted transaction.
		content := func() Content {
			tx, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			result, err := producer.WritePendingSilenceCancelWithTx(ctx, tx, f.actor, action)
			if err != nil {
				t.Fatalf("%s cancel review: %v", tc.lang, err)
			}
			return storedReview(t, tx, result.ProposalID)
		}()
		if content.Title != tc.title || content.Impact != tc.impact || content.Requirement != tc.requirement ||
			len(content.Diff.Lines) != 3 || content.Diff.Lines[2] != tc.reason {
			t.Fatalf("%s cancel review: %+v", tc.lang, content)
		}
	}
}

func TestSilenceDurationWording(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		zh, en  string
	}{{3600, "1 小时", "1 hour"}, {7200, "2 小时", "2 hours"}, {5400, "90 分钟", "90 minutes"}, {60, "1 分钟", "1 minute"}, {45, "45 秒", "45 seconds"}} {
		if zh, en := silenceDuration(tc.seconds, false), silenceDuration(tc.seconds, true); zh != tc.zh || en != tc.en {
			t.Fatalf("%d s: %q / %q", tc.seconds, zh, en)
		}
	}
}

// A stored early-end review is accepted only as one of the reviews its
// action can produce, including the English one written before PA-121.
func TestStoredSilenceCancelReviewMatchesKnownWording(t *testing.T) {
	action := v3alertmanager.ProposedSilenceCancel{ActorID: "u", InvestigationID: "inv-1", UpstreamID: "upstream-1",
		ApplicationLabel: "checkout", EnvironmentLabel: "environment", EnvironmentID: "test",
		CreateOperationID: "operation-1", ObservedDigest: strings.Repeat("d", 64), Reason: "Alert fixed"}
	stored := func(c Content) ([]byte, string) {
		raw, err := canonicalProposalJSON(c)
		if err != nil {
			t.Fatal(err)
		}
		return raw, proposalDigest("aiops.v3.proposal.review.v1", raw)
	}
	for _, c := range []Content{silenceCancelReviewContent(action, false), silenceCancelReviewContent(action, true),
		legacySilenceCancelReviewContent(action)} {
		raw, digest := stored(c)
		if !storedReviewMatches(silenceCancelReviewContents(action), raw, digest) {
			t.Fatalf("known review rejected: %s", raw)
		}
		if storedReviewMatches(silenceCancelReviewContents(action), raw, strings.Repeat("0", 64)) {
			t.Fatal("review accepted with another digest")
		}
	}
	altered := legacySilenceCancelReviewContent(action)
	altered.Requirement = "Approve now"
	if raw, digest := stored(altered); storedReviewMatches(silenceCancelReviewContents(action), raw, digest) {
		t.Fatal("altered review accepted")
	}
}
