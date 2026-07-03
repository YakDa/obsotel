package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ----------------------------------------------------------------------------
// ErrorChain — walk the wrapped-error chain and log it as a structured array.
// ----------------------------------------------------------------------------

// ErrorChain is the chain of wrapped errors from leaf to root, suitable
// for slog.LogValuer so it serializes as a JSON array in production.
type ErrorChain []error

// LogValue implements slog.LogValuer. Renders as a JSON array of strings.
func (c ErrorChain) LogValue() slog.Value {
	parts := make([]any, len(c))
	for i, e := range c {
		parts[i] = e.Error()
	}
	return slog.AnyValue(parts)
}

// ChainOf walks err via errors.Unwrap and returns the chain.
// Returns nil if err is nil.
func ChainOf(err error) ErrorChain {
	if err == nil {
		return nil
	}
	var c ErrorChain
	seen := make(map[error]bool)
	for err != nil {
		if seen[err] {
			// cycle protection — a custom Unwrap that loops
			break
		}
		seen[err] = true
		c = append(c, err)
		uw := errors.Unwrap(err)
		if uw == nil {
			break
		}
		err = uw
	}
	return c
}

// ----------------------------------------------------------------------------
// AppError — structured error type with operation, kind, metadata.
// ----------------------------------------------------------------------------

// AppError carries operation, classification, cause, and arbitrary metadata.
// Use New() or Wrap() to construct; use WithMeta() to enrich.
//
// AppError implements error, slog.LogValuer, and Unwrap, so it composes
// cleanly with errors.Is/As and structured logging.
type AppError struct {
	Op   string         // operation: "load_user"
	Kind string         // classification: "not_found" | "rate_limited" | "internal" | ...
	Err  error          // wrapped cause (may be nil)
	Meta map[string]any // structured metadata
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Kind, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Kind)
}

func (e *AppError) Unwrap() error { return e.Err }

// LogValue implements slog.LogValuer so AppError logs as a structured group
// containing op, kind, meta fields, and the cause message (not the chain —
// the chain is logged separately by LogErr).
func (e *AppError) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("op", e.Op),
		slog.String("kind", e.Kind),
	}
	for k, v := range e.Meta {
		attrs = append(attrs, slog.Any(k, v))
	}
	if e.Err != nil {
		attrs = append(attrs, slog.String("cause", e.Err.Error()))
	}
	return slog.GroupValue(attrs...)
}

// New constructs an AppError. If err is non-nil it is wrapped (via Unwrap).
func New(op, kind string, err error) *AppError {
	return &AppError{Op: op, Kind: kind, Err: err, Meta: map[string]any{}}
}

// WithMeta returns a copy of e with the given metadata attached.
// Accepts alternating key, value pairs; non-string keys are ignored.
func (e *AppError) WithMeta(kv ...any) *AppError {
	cp := *e
	if cp.Meta == nil {
		cp.Meta = make(map[string]any, len(kv)/2)
	} else {
		m := make(map[string]any, len(cp.Meta)+len(kv)/2)
		for k, v := range cp.Meta {
			m[k] = v
		}
		cp.Meta = m
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		cp.Meta[k] = kv[i+1]
	}
	return &cp
}

// ----------------------------------------------------------------------------
// Wrap — opinionated wrapper that produces an AppError with request_id baked in.
// ----------------------------------------------------------------------------

// Wrap wraps err with the given operation name. Returns nil if err is nil.
// The returned error is an *AppError with kind="internal" and request_id
// attached as metadata (pulled from ctx).
//
// Use this at the top of every error-returning site instead of bare
// fmt.Errorf so every error in the system carries op + kind + request_id.
//
//	if err != nil {
//	    return obs.Wrap(ctx, err, "load_user")
//	}
func Wrap(ctx context.Context, err error, op string) error {
	if err == nil {
		return nil
	}
	ae := New(op, "internal", err)
	if reqID := RequestIDFromContext(ctx); reqID != "" {
		ae = ae.WithMeta(RequestIDKey, reqID)
	}
	return ae
}

// WrapWith is Wrap plus arbitrary metadata.
// Use for the "with extra context" cases:
//
//	if err != nil {
//	    return obs.WrapWith(ctx, err, "load_user",
//	        "user_id", id, "tenant", tenant)
//	}
func WrapWith(ctx context.Context, err error, op string, kv ...any) error {
	if err == nil {
		return nil
	}
	ae := Wrap(ctx, err, op).(*AppError)
	return ae.WithMeta(kv...)
}

// ----------------------------------------------------------------------------
// LogErr — the canonical error-logging call. Use this everywhere.
// ----------------------------------------------------------------------------

// LogErr logs err at ERROR with:
//   - msg: the user-supplied message
//   - err: the rendered error message
//   - error_chain: structured array of the wrapped chain (root → top)
//   - attrs: any additional fields supplied by the caller
//
// LogErr uses the contextual logger so request_id and other bound fields
// are automatically attached.
//
// If err is nil, LogErr is a no-op. This makes it safe to call at error
// sites without an explicit nil check:
//
//	if err != nil {
//	    obs.LogErr(ctx, "load_user_failed", err, "user_id", id)
//	    return err
//	}
func LogErr(ctx context.Context, msg string, err error, attrs ...any) {
	if err == nil {
		return
	}
	chain := ChainOf(err)
	args := make([]any, 0, 4+len(attrs))
	args = append(args,
		slog.String("err", err.Error()),
		slog.Any("error_chain", chain),
	)

	// If err is an *AppError, surface op, kind, and meta fields at the
	// top level so they can be queried as first-class fields in Loki/ELK
	// (e.g. `user_id=*"u42"*` rather than nested under a group).
	//
	// Note: the type assertion only matches the top-level err. If you wrap
	// an AppError with fmt.Errorf("%w", ae), the underlying *AppError is
	// reached via ChainOf / errors.As, but this block won't pull op/kind
	// to top level. That's intentional — wrapping then adds an outer layer
	// that owns the new top-level semantics. If you want op/kind of the
	// inner AppError to still surface, errors.As it before logging.
	if ae, ok := err.(*AppError); ok {
		args = append(args,
			slog.String("op", ae.Op),
			slog.String("kind", ae.Kind),
		)
		for k, v := range ae.Meta {
			args = append(args, k, v)
		}
	}

	args = append(args, attrs...)
	L(ctx).LogAttrs(ctx, slog.LevelError, msg, toAttrs(args)...)
}

// toAttrs converts args to []slog.Attr. Supports two input shapes:
//   - alternating string, value pairs (the slog convention)
//   - pre-formed slog.Attr values (mixed in freely)
//
// Strings → slog.String; slog.Attr → as-is; everything else → slog.Any.
// This lets callers write either `obs.LogErr(ctx, "x", err, "k", "v")`
// or `obs.LogErr(ctx, "x", err, slog.String("k", "v"))` interchangeably.
func toAttrs(args []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch v := args[i].(type) {
		case slog.Attr:
			out = append(out, v)
		case string:
			if i+1 >= len(args) {
				continue
			}
			val := args[i+1]
			i++
			switch tv := val.(type) {
			case slog.Attr:
				out = append(out, slog.Attr{Key: v, Value: tv.Value})
			case string:
				out = append(out, slog.String(v, tv))
			case int:
				out = append(out, slog.Int(v, tv))
			case int64:
				out = append(out, slog.Int64(v, tv))
			case bool:
				out = append(out, slog.Bool(v, tv))
			default:
				out = append(out, slog.Any(v, val))
			}
		}
	}
	return out
}
