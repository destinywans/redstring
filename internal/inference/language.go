package inference

import "context"

type englishKey struct{}

// WithEnglish records the language of the request that started a reply, so
// evidence tools title what they read in that language (PA-71). Without it
// the language is Chinese, the product default.
func WithEnglish(ctx context.Context, english bool) context.Context {
	return context.WithValue(ctx, englishKey{}, english)
}

// English reports whether the reply was requested in English.
func English(ctx context.Context) bool {
	english, _ := ctx.Value(englishKey{}).(bool)
	return english
}

// Localized returns en for an English reply and zh otherwise.
func Localized(ctx context.Context, zh, en string) string {
	if English(ctx) {
		return en
	}
	return zh
}
