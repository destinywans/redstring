package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Failure classes written to the server log when a model call fails (PA-107).
// They tell a provider-side fault (upstream_5xx, rate_limited, timeout) from a
// product-side one (connect, parse, canceled) without the stored failure code,
// which is always model_unavailable.
const (
	failTimeout    = "timeout"
	failCanceled   = "canceled"
	failConnect    = "connect"
	failRateLimit  = "rate_limited"
	failUpstream5x = "upstream_5xx"
	failHTTPStatus = "http_status"
	failParse      = "parse"
	failStream     = "stream_interrupted"
)

// callFailure carries what is known about one failed provider call. Its text is
// the wrapped error's, so callers and stored failure messages are unchanged.
type callFailure struct {
	err    error
	status int
	// code and typ are the provider's own error code and type, never the message.
	code, typ string
}

func (f *callFailure) Error() string { return f.err.Error() }
func (f *callFailure) Unwrap() error { return f.err }

// upstreamError reads at most 4 KiB of a failed response and keeps only the
// error code and type, short and restricted to identifier characters. The
// message and anything else in the body, which may echo the request, is dropped.
func upstreamError(body io.Reader) (code, typ string) {
	data, _ := io.ReadAll(io.LimitReader(body, 4<<10))
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return "", ""
	}
	typ = payload.Error.Type
	if typ == "" {
		typ = payload.Type
	}
	switch c := payload.Error.Code.(type) {
	case string:
		code = c
	case float64:
		code = strconv.FormatFloat(c, 'f', -1, 64)
	}
	return identifier(code), identifier(typ)
}

// identifier truncates to 64 bytes and drops anything but identifier characters.
func identifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 64 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// failureClass names why a call failed. A canceled or expired context decides
// first, because the transport reports those as generic request errors.
func failureClass(ctx context.Context, err error) string {
	var f *callFailure
	errors.As(err, &f)
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return failTimeout
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return failCanceled
	case f != nil && f.status == 429:
		return failRateLimit
	case f != nil && f.status >= 500:
		return failUpstream5x
	case f != nil && f.status >= 300:
		return failHTTPStatus
	case f != nil && f.status == 0:
		return failConnect
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr):
		return failStream
	default:
		return failParse
	}
}

// logFailure writes one structured record for a failed provider call. It holds
// identifiers, counts and the failure class only: no credential, header,
// prompt, message or response body.
func (p *providerModel) logFailure(ctx context.Context, err error, started time.Time) {
	if err == nil || errors.Is(err, errConsumerStopped) || errors.Is(err, errTruncatedOutput) {
		return
	}
	attrs := []any{
		"provider", firstNonEmpty(p.provider.DisplayProvider, p.provider.Kind),
		"provider_id", p.provider.ID,
		"model", p.provider.Model,
		"class", failureClass(ctx, err),
		"elapsed_ms", time.Since(started).Milliseconds(),
		"model_call", p.calls,
		"attempt_id", p.attemptID,
	}
	var f *callFailure
	if errors.As(err, &f) {
		if f.status != 0 {
			attrs = append(attrs, "status", f.status)
		}
		if f.code != "" {
			attrs = append(attrs, "upstream_error_code", f.code)
		}
		if f.typ != "" {
			attrs = append(attrs, "upstream_error_type", f.typ)
		}
	}
	if u, e := url.Parse(p.provider.BaseURL); e == nil && u.Host != "" {
		attrs = append(attrs, "upstream_host", u.Host)
	}
	slog.Warn("model call failed", attrs...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
