// Package obsotel is the public observability API for the monorepo.
// It composes the internal pure-slog package (internal/obsbase) with
// OpenTelemetry for traces and metrics. Services import only this
// package; the obsbase core is hidden by Go's internal/ visibility rule.
//
// Mental model:
//   - api.go         — re-exports of internal/obsbase (logs, errors, context)
//   - logger.go      — slog.Logger with auto trace_id/span_id injection
//   - middleware.go  — HTTP handler wrapped with otelhttp server span
//   - client.go      — http.Client with otelhttp client span + DoRequest
//   - tracer.go      — TracerProvider setup with fail-open defaults
//   - metrics.go     — Counter / Histogram helpers
//
// Services import only this package:
//
//	import "github.com/mingdos/obsotel"
//
// Fail-open principle: if the OTel collector is down, sampling is off,
// or the SDK is uninitialized, observability calls do not impact business
// logic. Business code completes; you lose visibility, not availability.
package obsotel

import (
	"context"
	"log/slog"

	obs "github.com/mingdos/obsotel/internal/obsbase"
)

// ----------------------------------------------------------------------------
// Field-name constants. Use these everywhere — never hard-code the strings.
// ----------------------------------------------------------------------------

const (
	RequestIDKey = obs.RequestIDKey
	UserIDKey    = obs.UserIDKey
	TraceIDKey   = obs.TraceIDKey
	SpanIDKey    = obs.SpanIDKey
)

// ----------------------------------------------------------------------------
// Re-exports of the pure-slog layer. Every function delegates to internal/obsbase
// without adding observable behavior change. Wrapped here so services have
// a single import path.
// ----------------------------------------------------------------------------

// L returns the logger bound to ctx, or slog.Default() if none.
func L(ctx context.Context) *slog.Logger {
	return obs.L(ctx)
}

// With binds attrs to the contextual logger and returns the updated ctx + logger.
func With(ctx context.Context, attrs ...any) (context.Context, *slog.Logger) {
	return obs.With(ctx, attrs...)
}

// WithLogger returns a new context carrying the given logger. A nil logger
// is a no-op (returns ctx unchanged). Use this to inject a logger at a
// request boundary or in tests; obsotel.L(ctx) will then return it.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return obs.WithLogger(ctx, l)
}

// RequestIDFromContext extracts the request ID from ctx.
func RequestIDFromContext(ctx context.Context) string {
	return obs.RequestIDFromContext(ctx)
}

// WithRequestID stores the bare request ID in ctx. Used by middleware to
// bind the ID generated/observed per request so DoRequest can echo it on
// outbound headers and LogErr can include it in the structured log line.
func WithRequestID(ctx context.Context, id string) context.Context {
	return obs.WithRequestID(ctx, id)
}

// LogErr logs err at ERROR with the chain as a structured field.
// See internal/obsbase for full behavior.
func LogErr(ctx context.Context, msg string, err error, attrs ...any) {
	obs.LogErr(ctx, msg, err, attrs...)
}

// Wrap wraps err with the given operation name and request_id.
func Wrap(ctx context.Context, err error, op string) error {
	return obs.Wrap(ctx, err, op)
}

// WrapWith is Wrap plus metadata.
func WrapWith(ctx context.Context, err error, op string, kv ...any) error {
	return obs.WrapWith(ctx, err, op, kv...)
}

// New constructs an AppError.
func New(op, kind string, err error) *obs.AppError {
	return obs.New(op, kind, err)
}

// ChainOf walks the wrapped-error chain.
func ChainOf(err error) obs.ErrorChain {
	return obs.ChainOf(err)
}

// AppError is the structured error type. Type alias of internal/obsbase.
type AppError = obs.AppError

// ErrorChain is the chain of wrapped errors. Type alias of internal/obsbase.
type ErrorChain = obs.ErrorChain