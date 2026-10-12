package inference

import (
	"context"
	"time"
)

type alertStartKey struct{}

// WithAlertStart records, for the tools built for one turn, when the
// incident an investigation was opened from started (D11 §4.4); tools whose
// default window reaches back from it read it with AlertStart.
func WithAlertStart(ctx context.Context, at time.Time) context.Context {
	if at.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, alertStartKey{}, at.UTC())
}

// AlertStart returns the alert start WithAlertStart recorded, if any.
func AlertStart(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(alertStartKey{}).(time.Time)
	return at, ok && !at.IsZero()
}
