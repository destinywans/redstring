package v3settings

import (
	"strings"
	"testing"
	"time"
)

func TestProfileEvidenceRejectsMismatchedOrIncompleteSecurityFacts(t *testing.T) {
	now := time.Now()
	id := strings.Repeat("a", 64)
	request := ProfileEvidenceRequest{
		InstanceID: "instance", UserID: "person", EnvironmentID: "dev", CurrentSessionID: id,
		Contacts: []ProfileEvidenceContact{{Kind: "email", Value: "person@example.invalid"}}, SessionIDs: []string{id},
	}
	answer := ProfileEvidenceSnapshot{
		InstanceID: request.InstanceID, UserID: request.UserID, EnvironmentID: request.EnvironmentID,
		Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute),
		Contacts: []ProfileVerifiedContact{{Kind: "email", Value: "person@example.invalid", Verified: false}},
		OrgRule:  "No organization MFA rule", Factors: []ProfileFactorEvidence{},
		Sessions: []ProfileSessionEvidence{{ID: id, Device: "Recorded device", LoginMethod: "password"}},
	}
	if !validProfileEvidence(request, answer, now) {
		t.Fatal("complete, exactly bound negative MFA and contact evidence was rejected")
	}
	for name, change := range map[string]func(*ProfileEvidenceSnapshot){
		"wrong environment":    func(a *ProfileEvidenceSnapshot) { a.EnvironmentID = "prod" },
		"missing contact":      func(a *ProfileEvidenceSnapshot) { a.Contacts = nil },
		"missing session":      func(a *ProfileEvidenceSnapshot) { a.Sessions = nil },
		"unknown login method": func(a *ProfileEvidenceSnapshot) { a.Sessions[0].LoginMethod = "unknown" },
		"factor contradicts status": func(a *ProfileEvidenceSnapshot) {
			a.Factors = []ProfileFactorEvidence{{Kind: "totp", Name: "TOTP", Sub: "Active"}}
		},
		"expiry before observation": func(a *ProfileEvidenceSnapshot) {
			a.ObservedAt = now.Add(10 * time.Second)
			a.ValidUntil = now.Add(time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := answer
			changed.Contacts = append([]ProfileVerifiedContact{}, answer.Contacts...)
			changed.Sessions = append([]ProfileSessionEvidence{}, answer.Sessions...)
			changed.Factors = append([]ProfileFactorEvidence{}, answer.Factors...)
			change(&changed)
			if validProfileEvidence(request, changed, now) {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}

func TestProfileEvidenceJSONRejectsDuplicateNestedFacts(t *testing.T) {
	if profileNoDuplicateKeys([]byte(`{"security":{"mfa_enabled":false,"mfa_enabled":true}}`)) {
		t.Fatal("duplicate nested MFA fact was accepted")
	}
}
