package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// jsonMarshal is the package-level json marshaler used by chainList.MarshalJSON.
// Lives as a var so tests can intercept if needed.
var jsonMarshal = json.Marshal

// ----------------------------------------------------------------------------
// ErrorChain — walk the wrapped-error chain and log it as a structured array.
// ----------------------------------------------------------------------------

// ErrorChain is the chain of wrapped errors from leaf to root, suitable
// for slog.LogValuer so it serializes as a JSON array in production.
type ErrorChain []error

// LogValue implements slog.LogValuer. Renders each chain entry as a
// structured map[string]any, not a colon-jammed string.
//
// Each entry carries ONLY a `cause` (for *AppError) or `error` (for plain
// errors) field. Notably absent: `op`, `kind`, and Meta. Those already
// appear at the TOP LEVEL of the log line (via LogErr's surface pass),
// so duplicating them in every chain entry would just bloat every line
// for queries that almost always want the outermost op/kind anyway.
//
// Dispatch:
//   - If the entry is an *AppError, emit `{"cause": err.Error()}`. The
//     rendered Error() already includes op + kind + cause (AppError.Error
//     formats as "op: kind: cause"), so the chronology of ops is still
//     readable in the chain even without separate op/kind fields.
//   - Otherwise, wrap the entry's Error() in `{"error": "..."}` so stdlib
//     errors (*url.Error, *net.OpError, *net.DNSError, etc.) still arrive
//     as structured values, not opaque strings.
//
// If you need per-layer op/kind disambiguation (rare; mostly when you
// nest Wrap() calls and want to know which inner op failed), read the
// Go-level chain via obsotel.ChainOf(err) and inspect each *AppError
// directly. The log line is not the right place for that.
//
// Implementation note: the resulting []map[string]any is wrapped in a
// chainList (which implements both json.Marshaler and TextMarshaler).
// slog's JSONHandler calls MarshalJSON → real JSON array of objects.
// slog's TextHandler calls MarshalText → readable bracketed list. Without
// these Marshalers, slog.TextHandler falls back to fmt.Sprintf("%+v", ...)
// which dumps the Go default `map[k:v]` syntax — unreadable.
//
// Why a uniform shape: mixing strings and groups in the same array would
// force every consumer to know which index is which type. Every entry is
// a map; index 0 is always an object.
func (c ErrorChain) LogValue() slog.Value {
	parts := make([]map[string]any, len(c))
	for i, e := range c {
		if ae, ok := e.(*AppError); ok {
			parts[i] = map[string]any{"cause": ae.Error()}
		} else {
			parts[i] = map[string]any{"error": e.Error()}
		}
	}
	return slog.AnyValue(chainList(parts))
}

// chainList wraps a []map[string]any and implements both json.Marshaler
// (for slog.JSONHandler) and encoding.TextMarshaler (for slog.TextHandler).
//
// slog.JSONHandler (KindAny branch) checks json.Marshaler first and calls
// MarshalJSON. We delegate to encoding/json which produces a real JSON
// array of objects.
//
// slog.TextHandler (KindAny branch) checks encoding.TextMarshaler first
// and calls MarshalText. Without it, slog falls back to fmt.Sprintf("%+v", ...)
// which dumps the Go default `map[k:v]` syntax for each entry — unreadable
// in dev logs. MarshalText produces a bracketed list:
//
//	error_chain="[0] op=do_stuff kind=internal | [1] error=boom"
//
// Key order is sorted for diff-friendly output (production order in a Go
// map is randomized; sorting makes the log line stable across runs).
type chainList []map[string]any

// MarshalJSON returns a JSON array of objects. slog.JSONHandler routes
// KindAny through json.Marshaler if present; this gives identical output
// to []map[string]any marshaled directly, but lets us own the format.
//
// IMPORTANT: we cast to []map[string]any (not chainList) before calling
// json.Marshal, otherwise json.Marshal would call this MarshalJSON again
// and recurse forever.
func (c chainList) MarshalJSON() ([]byte, error) {
	return jsonMarshal([]map[string]any(c))
}

// MarshalText returns a human-readable rendering for slog.TextHandler.
// Format: `[i] k=v k=v | [j] k=v` — one bracketed entry per layer, pipe
// separated. Quoting via slog.TextHandler itself (it wraps the value in
// quotes when it contains spaces).
func (c chainList) MarshalText() ([]byte, error) {
	if len(c) == 0 {
		return []byte("[]"), nil
	}
	parts := make([]string, len(c))
	for i, m := range c {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kvs := make([]string, len(keys))
		for j, k := range keys {
			kvs[j] = k + "=" + fmt.Sprintf("%v", m[k])
		}
		parts[i] = fmt.Sprintf("[%d] %s", i, strings.Join(kvs, " "))
	}
	return []byte(strings.Join(parts, " | ")), nil
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
// Use NewErr() or Wrap() to construct; use WithMeta() to enrich.
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

// NewErr constructs an AppError. If err is non-nil it is wrapped (via Unwrap).
func NewErr(op, kind string, err error) *AppError {
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
	ae := NewErr(op, "internal", err)
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
			// requestIDHandler / traceHandler already inject these from
			// ctx. Skip them here so the JSON line has each key exactly
			// once. (Meta still carries them for non-log consumers —
			// errors.As(err, &ae).Meta is fine to read.)
			if k == RequestIDKey || k == TraceIDKey || k == SpanIDKey {
				continue
			}
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
