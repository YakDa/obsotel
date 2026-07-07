package obsotel

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// InjectTraceContext serializes the current span's W3C traceparent (and
// tracestate, if present) into the given JSON payload. Use this at enqueue
// time to propagate trace context across async boundaries (PG queue, Redis
// pub/sub, etc.) where HTTP headers are not available.
//
// Keys written: "trace_parent" (required) and "trace_state" (optional).
// The snake_case naming follows obsotel's field conventions; the W3C
// standard header name is "traceparent" (no underscore). ExtractTraceContext
// accepts both forms on read.
//
// If ctx has no valid span context the payload is returned unchanged.
// All existing payload keys are preserved and the result is always valid
// JSON (assuming raw is valid JSON on input).
//
// Fail-open: if the OTel propagator is uninitialized or ctx has no span,
// the payload passes through untouched — no error, no panic.
//
//	payload, _ := json.Marshal(msg)
//	payload = obsotel.InjectTraceContext(ctx, payload)
//	queue.Enqueue(ctx, payload)
func InjectTraceContext(ctx context.Context, raw json.RawMessage) json.RawMessage {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return raw
	}

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	tp := carrier.Get("traceparent")
	if tp == "" {
		return raw
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	m["trace_parent"] = tp
	if ts := carrier.Get("tracestate"); ts != "" {
		m["trace_state"] = ts
	}

	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// ExtractTraceContext reads a W3C traceparent from the JSON payload and
// reconstructs the parent trace context, then starts a child span with
// the given operation name. Use this at dequeue time to continue the
// trace that was started at enqueue.
//
// Accepts both "trace_parent" (obsotel convention) and "traceparent"
// (W3C standard) keys for compatibility. Similarly reads "trace_state"
// or "tracestate" for the optional W3C tracestate header.
//
// If the key is missing or malformed, a new root span is created instead
// — the handler still runs, just without a parent link.
//
// The caller is responsible for calling span.End():
//
//	ctx, span := obsotel.ExtractTraceContext(ctx, item.Payload, "queue.handle")
//	defer span.End()
//	// ... handle the item with ctx
func ExtractTraceContext(ctx context.Context, raw json.RawMessage, operation string) (context.Context, trace.Span) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		tp := stringFromMap(m, "trace_parent", "traceparent")
		if tp != "" {
			carrier := propagation.MapCarrier{}
			carrier.Set("traceparent", tp)
			if ts := stringFromMap(m, "trace_state", "tracestate"); ts != "" {
				carrier.Set("tracestate", ts)
			}
			ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		}
	}
	return Tracer("obsotel").Start(ctx, operation)
}

// stringFromMap returns the first non-empty string value found under the
// given keys. Used to support both snake_case and standard W3C key names.
func stringFromMap(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
