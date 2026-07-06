package obsotel

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	obs "github.com/YakDa/obsotel/internal/obsbase"
	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a *slog.Logger configured for the given environment,
// with trace_id / span_id automatically injected into every log line
// when an active OTel span exists in ctx.
//
//	env == "prod"          → JSON to stderr
//	env == anything else   → text to stderr
//
// If no TracerProvider is configured (or no span is active in ctx),
// the log lines are emitted unchanged — the trace handler is fail-open.
func NewLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env != "prod" {
		level = slog.LevelDebug
	}
	return NewLoggerWithLevel(env, level)
}

// NewLoggerWithLevel is NewLogger with explicit level control.
func NewLoggerWithLevel(env string, level slog.Level) *slog.Logger {
	return NewLoggerToWriter(env, level, os.Stderr)
}

// NewLoggerToWriter is NewLogger with an explicit destination writer.
// Useful for tests and for services that want to route logs to a file
// or buffer instead of stderr.
func NewLoggerToWriter(env string, level slog.Level, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var base slog.Handler
	if env == "prod" {
		base = slog.NewJSONHandler(w, opts)
	} else {
		base = slog.NewTextHandler(w, opts)
	}
	// Chain: requestIDHandler -> traceHandler -> base. Order matters:
	// requestID first so a log record carries it before trace fields are
	// added; traceHandler can then attach trace_id/span_id.
	return slog.New(&requestIDHandler{base: &traceHandler{base: base}})
}

// traceHandler wraps a slog.Handler and prepends trace_id / span_id
// from the active OTel span (if any) to every log record.
//
// If no span is active in ctx, the record passes through unchanged.
// If the TracerProvider is uninitialized, the no-op span's
// SpanContext().IsValid() returns false and we skip injection.
//
// Business code never sees this handler fail or panic.
type traceHandler struct {
	base slog.Handler
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(TraceIDKey, sc.TraceID().String()),
			slog.String(SpanIDKey, sc.SpanID().String()),
		)
	}
	return h.base.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{base: h.base.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{base: h.base.WithGroup(name)}
}

// requestIDHandler wraps a slog.Handler and prepends request_id from ctx
// (when set via WithRequestID) to every log record. Symmetric to
// traceHandler for trace_id/span_id.
//
// Fail-open: if no request_id is in ctx, the record passes through unchanged.
//
// Single-writer contract: this handler is the ONLY place that should add
// request_id to a log record. slog.Record.AddAttrs is a plain append — it
// does NOT dedupe by key, so a second AddAttrs (e.g. via Logger.With, via
// literal slog.String("request_id", ...), or via AppError.Meta surfaced
// in LogErr) emits a duplicate JSON key on the same line. If you find
// request_id appearing twice in your output, another writer slipped in:
// remove it there, not here.
type requestIDHandler struct {
	base slog.Handler
}

func (h *requestIDHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if rid := obs.RequestIDFromContext(ctx); rid != "" {
		r.AddAttrs(slog.String(RequestIDKey, rid))
	}
	return h.base.Handle(ctx, r)
}

func (h *requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &requestIDHandler{base: h.base.WithAttrs(attrs)}
}

func (h *requestIDHandler) WithGroup(name string) slog.Handler {
	return &requestIDHandler{base: h.base.WithGroup(name)}
}

// LevelFromString parses "debug" | "info" | "warn" | "error" → slog.Level.
func LevelFromString(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}