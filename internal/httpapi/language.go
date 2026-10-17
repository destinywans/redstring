package httpapi

import (
	"context"
	"strings"
)

type languageKey struct{}

// WithRequestLanguage records the request's Accept-Language for code that
// receives only a context, such as producers that store review text when a
// proposal is written (PA-121).
func WithRequestLanguage(ctx context.Context, acceptLanguage string) context.Context {
	return context.WithValue(ctx, languageKey{}, acceptLanguage)
}

// EnglishRequested reports whether the authenticated request asked for
// English. Anything else, including no recorded language, is Simplified
// Chinese, like the handlers that read Accept-Language directly.
func EnglishRequested(ctx context.Context) bool {
	lang, _ := ctx.Value(languageKey{}).(string)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en")
}
