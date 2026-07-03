package obs

import (
	"context"
	"log/slog"
)

// Conventional structured-log keys. Use these constants everywhere so
// field names stay consistent across services. Loki/ELK queries depend
// on this discipline.
const (
	RequestIDKey = "request_id"
	UserIDKey    = "user_id"
	TraceIDKey   = "trace_id"
	SpanIDKey    = "span_id"
)

type ctxKey struct{}

// WithLogger returns a new context carrying the given logger.
// A nil logger is a no-op (returns ctx unchanged).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// L returns the logger stored in ctx, or slog.Default() if none.
// Safe with a nil ctx — returns slog.Default().
func L(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// With attaches the given fields to the contextual logger and stores
// the new logger back in ctx. Use this at the start of a request or
// job to bind request_id, user_id, etc.
//
//	ctx, log := obs.With(ctx,
//	    slog.String(obs.RequestIDKey, reqID),
//	    slog.String(obs.UserIDKey, userID),
//	)
func With(ctx context.Context, attrs ...any) (context.Context, *slog.Logger) {
	l := L(ctx).With(attrs...)
	return WithLogger(ctx, l), l
}

// requestIDKey is the unexported context key for the bare request ID
// string (separate from the logger, so non-logging code can read it —
// e.g. outbound HTTP headers).
type requestIDKey struct{}

// WithRequestID stores the bare request ID string in ctx.
// Used by the middleware so DoRequest can echo it on outbound headers.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext extracts the request ID from ctx, or "" if none.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}
