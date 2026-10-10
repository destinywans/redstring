package v3agents

import (
	"net/http/httptest"
	"testing"
)

// PA-62: built-in rule descriptions follow the request language; text an
// operator wrote is shown as written.
func TestGuardDescriptionFollowsRequestLanguage(t *testing.T) {
	builtin, _, _ := governedRuleDefinition("deny_sensitive_content")
	zh := httptest.NewRequest("GET", "/", nil)
	en := httptest.NewRequest("GET", "/", nil)
	en.Header.Set("Accept-Language", "en-US")
	if got := guardDescriptionFor(zh, "deny_sensitive_content", builtin); got != "拦截文档列出的私钥、Bearer 请求头和密码赋值模式。" {
		t.Fatalf("zh: %q", got)
	}
	if got := guardDescriptionFor(en, "deny_sensitive_content", builtin); got != builtin {
		t.Fatalf("en: %q", got)
	}
	if got := guardDescriptionFor(zh, "deny_sensitive_content", "Custom operator text"); got != "Custom operator text" {
		t.Fatalf("custom: %q", got)
	}
	for _, name := range []string{"read_only", "bounded_execution", "deny_sensitive_content"} {
		if governedRuleChinese[name] == "" {
			t.Fatalf("missing Chinese text for %s", name)
		}
	}
}
