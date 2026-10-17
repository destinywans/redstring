package v3rollouts

import (
	"fmt"
	"testing"
)

// PA-166: a rollback review is written once, in the requester's language,
// and fixed with the proposal's review digest. A reviewer using another
// language sees the requester's wording.
func TestRollbackProposalReviewFollowsRequesterLanguage(t *testing.T) {
	for _, tc := range []struct{ lang, title, checklist string }{
		{"zh-CN", "回滚 checkout-api 到修订 1", "两位非发起人的审批人用通行密钥批准。"},
		{"en-US", "Roll back checkout-api to revision 1", "Two approvers other than the requester approve with a passkey."},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			f := newRolloutFixture(t)
			service := ServiceID("app", "dev")
			var candidates candidatesResponse
			f.decode(f.call("GET", "/api/v1/services/"+service+"/rollback-candidates", "requester", ""), 200, &candidates)
			body := fmt.Sprintf(`{"investigation_id":"INV-1","service_id":"%s","target_revision":"1","expected_template_digest":"%s","reason":"bad release"}`,
				service, candidates.Workload.CurrentTemplateDigest)
			var created struct {
				ProposalID string `json:"proposal_id"`
			}
			f.decode(f.call("POST", "/api/v1/rollback-proposals", "requester", body,
				"Idempotency-Key", "rollback-language", "Accept-Language", tc.lang), 201, &created)
			var title, checklist string
			if err := f.db.QueryRow(t.Context(), `SELECT content->>'title',content->'checklist'->>0 FROM v3_proposals WHERE id=$1`,
				created.ProposalID).Scan(&title, &checklist); err != nil {
				t.Fatal(err)
			}
			if title != tc.title || checklist != tc.checklist {
				t.Fatalf("%s review: title=%q checklist=%q", tc.lang, title, checklist)
			}
		})
	}
}
