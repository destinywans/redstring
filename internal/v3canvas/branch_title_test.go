package v3canvas

import "testing"

// PA-166: the fixed root titles ("主调查" from the canvas, "Main investigation"
// from an alert) follow the request language; titles people gave are kept.
func TestRootBranchFixedTitleFollowsRequestLanguage(t *testing.T) {
	for _, tc := range []struct{ title, parent, language, want string }{
		{"Main investigation", "", "zh-CN", "主调查"},
		{"Main investigation", "", "", "主调查"},
		{"主调查", "", "en-US,en;q=0.9", "Main investigation"},
		{"Main investigation", "", "en", "Main investigation"},
		{"主调查", "", "zh-CN", "主调查"},
		// A child branch or a renamed root is a person's title.
		{"Main investigation", "root", "zh-CN", "Main investigation"},
		{"Checkout errors", "", "zh-CN", "Checkout errors"},
		{"main investigation", "", "zh-CN", "main investigation"},
	} {
		if got := branchTitle(tc.title, tc.parent, tc.language); got != tc.want {
			t.Errorf("branchTitle(%q, %q, %q) = %q, want %q", tc.title, tc.parent, tc.language, got, tc.want)
		}
	}
}
