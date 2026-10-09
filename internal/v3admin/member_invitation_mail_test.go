package v3admin

import (
	"mime"
	"strings"
	"testing"
	"time"
)

func TestInvitationMailIsRedstringAndBilingual(t *testing.T) {
	mail := invitationMail("noreply@example.invalid", InvitationMessage{Email: "new@example.invalid", Code: "ABC-123",
		EntryURL: "https://prod.redstring.test:18443/", ExpiresAt: time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)})
	header, body, ok := strings.Cut(mail, "\r\n\r\n")
	if !ok || strings.Contains(mail, "AIOps Canvas") {
		t.Fatalf("mail: %q", mail)
	}
	var subject string
	for _, line := range strings.Split(header, "\r\n") {
		if value, found := strings.CutPrefix(line, "Subject: "); found {
			decoded, err := new(mime.WordDecoder).DecodeHeader(value)
			if err != nil {
				t.Fatal(err)
			}
			subject = decoded
		}
		for _, r := range line {
			if r > 127 {
				t.Fatalf("raw non-ASCII header line: %q", line)
			}
		}
	}
	if subject != "Redstring 邀请 / Redstring invitation" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{"你已受邀以只读成员身份加入 Redstring", "You have been invited to Redstring", "ABC-123",
		"https://prod.redstring.test:18443/", "2026-10-10T08:00:00Z"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q: %q", want, body)
		}
	}
}
