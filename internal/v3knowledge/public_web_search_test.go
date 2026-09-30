package v3knowledge

import (
	"strings"
	"testing"
)

func TestPublicWebExcerptUsesRuneOffsetAfterCaseFold(t *testing.T) {
	body := strings.Repeat("Ⱥ", 500) + "x"
	excerpt := publicWebExcerpt(body, "x", []string{"x"})
	if !strings.HasSuffix(excerpt, "x") || len([]rune(excerpt)) != 400 {
		t.Fatalf("excerpt lost the match after Unicode case fold: %q", excerpt)
	}
}
