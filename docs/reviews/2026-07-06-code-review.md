# Code Review: obsotel Observability Package

**Date:** 2026-07-06  
**Scope:** Full code + documentation review of the `obsotel` package (public layer + internal/obsbase)

---

## Part 1: Documentation vs Code Discrepancies

### 1. Wrong Internal Package Path

**README says:**
> `internal/obsotel/internal/obsbase/` is hidden by Go's `internal/` rule

**Actual path:** `internal/obsbase/`

The README references a non-existent nested path.

---

### 2. InitTracer Default Exporter Description is Wrong

**README says:**
> `InitTracer` defaults to a stdout exporter (spans printed to stderr) and 100% sampling.

**Actual code (`tracer.go`):** The default exporter writes to `io.Discard` (silent) unless the `OBSOTEL_DUMP_SPANS` env var is set. Supported values:
- `""` (empty/unset) → silent (discard)
- `"stdout"` → pretty JSON to stderr
- `"compact"` → single-line JSON to stderr
- `"file:/path"` → JSONL appended to file

The README makes it sound like spans are always printed to stderr by default.

---

### 3. `NewLoggerToWriter` is Not an obsbase Re-export

The README's architecture implies all logger functions are obsbase re-exports. `NewLoggerToWriter` is implemented purely at the obsotel layer (uses the trace/requestID handler chain) and does not exist in obsbase.

---

### 4. Sampling APIs Documented But Not Re-exported

**README's obsbase table** lists `NewSampler(rate uint64)` and `NewRandomSampler(rate uint64)`.

These exist in `internal/obsbase/sampling.go` but are **NOT re-exported** in the public `obsotel` package. Services cannot use them. These APIs are effectively inaccessible.

---

### 5. `DoRequest` Signature Difference Not Documented

- **obsbase:** `DoRequest(ctx, client, req)` — 3 args
- **obsotel:** `DoRequest(ctx, req)` — 2 args (uses internal `defaultClient`)

The README doesn't call out that the public API has a simplified signature.

---

### 6. Quickstart Doesn't Show Context-Logger Connection

The quickstart sets `slog.SetDefault(log)` but never shows `obsotel.WithLogger(ctx, log)`. The middleware handles this internally, but it's not obvious from the example.

---

## Part 2: Critical Bugs

### 1. File Descriptor Leak — `tracer.go` defaultExporter "file:" Mode

**Severity: Critical**

```go
case strings.HasPrefix(mode, "file:"):
    path := strings.TrimPrefix(mode, "file:")
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        return stdouttrace.New(stdouttrace.WithWriter(io.Discard))
    }
    return stdouttrace.New(stdouttrace.WithWriter(f))  // f is NEVER closed
```

The `stdouttrace` exporter's `Shutdown()` doesn't close the underlying writer. This leaks a file descriptor for the lifetime of the process.

**Fix:** Return a shutdown function that closes `f`, or wrap the writer in a struct that implements `io.Closer` and hook into the shutdown path.

---

### 2. Full URL Logged Including Query Parameters — `client.go`

**Severity: Critical (Security)**

```go
slog.String("outbound_url", req.URL.String()),
```

Every outbound request logs the full URL at INFO level. If services pass tokens or API keys in query strings (e.g., `?api_key=secret`), these end up in plaintext in logs. Since this is a contract package that all services must use, this is a security concern at scale.

**Fix:** Log `req.URL.Host + req.URL.Path` only, or redact the query string.

---

### 3. `DoRequestWithClient` Creates New Client Per Call on nil — `client.go`

**Severity: High (Resource Leak)**

```go
if client == nil {
    client = NewClient()
}
```

If a caller accidentally passes `nil`, a new `*http.Client` with a new `otelhttp.Transport` is created on every call. Each transport maintains its own connection pool. In a loop, this silently creates hundreds of transports with idle connections.

**Fix:**
```go
if client == nil {
    client = defaultClient
}
```

---

## Part 3: Medium Issues

### 4. No `sync.Once` Guard on `InitTracer` / `InitMeter`

Both functions mutate global OTel state. If called twice (in tests, or accidentally), the second call overwrites the first provider without shutting it down — leaking goroutines and resources.

**Fix:** Add a `sync.Once` guard or track the previous provider and warn/shutdown.

---

### 5. Context Cancellation Logged as ERROR — `client.go`

When `ctx` is already cancelled (client disconnected, timeout), the request fails and is logged at ERROR. Context-cancelled requests are expected in production. Logging them at ERROR creates noise and false alerts.

**Fix:**
```go
if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    l.LogAttrs(ctx, slog.LevelWarn, "outbound_cancelled", ...)
} else {
    l.LogAttrs(ctx, slog.LevelError, "outbound_failed", ...)
}
```

---

### 6. `statusRecorder` Missing `Unwrap()` — `internal/obsbase/middleware.go`

In Go 1.20+, `http.ResponseController` discovers optional interfaces via `Unwrap()`. Without it, features like `SetReadDeadline`/`SetWriteDeadline` silently stop working for handlers that use `ResponseController`.

**Fix:**
```go
func (r *statusRecorder) Unwrap() http.ResponseWriter {
    return r.ResponseWriter
}
```

---

### 7. Metrics Created at Package Init Before `InitMeter`

```go
var HTTPRequestDuration = MustNewHistogram(...)  // runs at package init
var HTTPRequestsTotal = MustNewCounter(...)       // runs at package init
```

These are created against the global no-op MeterProvider. OTel's delegation pattern *should* make them work after `InitMeter` is called later, but this is implementation-specific and no test verifies it.

**Recommendation:** Add a test that creates a metric before `InitMeter`, calls `InitMeter` with a manual reader, records a value, and asserts the value appears.

---

### 8. Global TracerProvider Mutation Without Synchronization — `tracer.go`

```go
otel.SetTracerProvider(tp)
otel.SetTextMapPropagator(...)
```

The gap between setting the provider and setting the propagator is not atomic. Another goroutine could observe an inconsistent state (new provider + old propagator).

**Recommendation:** Document that `InitTracer` must be called exactly once, early in `main()`.

---

## Part 4: Three Pillars — Gaps & Suggestions

### Traces

#### No Span Status on Errors

When `DoRequestWithClient` encounters an error or 5xx, the active span is never marked with `span.SetStatus(codes.Error, ...)`. OTel trace UIs (Jaeger, Tempo) rely on span status to highlight failures.

```go
if span := trace.SpanFromContext(ctx); span.IsRecording() {
    span.SetStatus(codes.Error, err.Error())
    span.RecordError(err)
}
```

#### No Span Attributes on Outbound Calls

Consider adding semantic conventions beyond otelhttp defaults:
- `http.response.status_code`
- `server.address`
- `retry.attempt` during retries

---

### Metrics

#### Middleware Doesn't Auto-Record RED Metrics

`Handler()` / `HandlerWithFilter()` logs every request but doesn't record to `HTTPRequestDuration` or `HTTPRequestsTotal`. Since this is a contract package, auto-recording in middleware ensures every service gets RED metrics (Rate, Errors, Duration) by default.

```go
// In LoggingMiddleware's defer:
HTTPRequestDuration.Record(ctx, time.Since(start).Seconds(),
    attribute.String("method", r.Method),
    attribute.String("path", r.URL.Path),
    attribute.Int("status", rw.status),
)
HTTPRequestsTotal.Inc(ctx,
    attribute.String("method", r.Method),
    attribute.Int("status", rw.status),
)
```

#### No Outbound Request Metrics

`DoRequestWithClient` logs outbound duration but doesn't record it as a metric. For dashboards showing downstream latency, services need to manually instrument.

---

### Logs

#### Linear Backoff Instead of Exponential+Jitter

The retry uses constant backoff. For production retry scenarios, exponential backoff with jitter is preferred to avoid thundering herd. This is documented as intentional, but worth reconsidering.

---

## Part 5: Test Coverage Gaps

| Gap | Priority | Impact |
|-----|----------|--------|
| No test for context cancellation during retry backoff | High | Could mask broken select/cancel logic |
| No test verifying metrics actually record values (only "no panic") | High | Metrics could silently be no-ops and tests would pass |
| No test for fail-open `StartSpan` without `InitTracer` | High | Core design principle untested |
| No test for 4xx as terminal (not retried) | Medium | Retry logic correctness |
| `tracer_test.go` uses `/tmp/` paths that don't work on Windows | Medium | Tests pass for wrong reasons on Windows |
| No concurrent test for `DoRequest` hitting `defaultClient` | Medium | Race conditions in shared client |
| No test for `InitTracer` failure returning usable no-op shutdown | Medium | Fail-open contract untested |
| No test for `GetBody` reset failure during retry | Low | Error path unverified |
| No test for `LevelFromString` edge cases | Low | Missing basic unit test |
| `TestNewClient` doesn't verify transport is otelhttp-wrapped | Low | Weak assertion |

### Pillar Coverage Summary

| Pillar | Grade | Notes |
|--------|-------|-------|
| **Logs** | A | Well tested: JSON structure, request_id injection, trace_id injection, error_chain format |
| **Traces** | B- | Span creation + trace_id in logs tested. Missing: propagation roundtrip, span status, parent-child |
| **Metrics** | D | Only "doesn't panic" level. No test reads back actual recorded values |

---

## Summary Table

| # | Severity | Category | Issue |
|---|----------|----------|-------|
| 1 | **Critical** | Resource | File descriptor leak in "file:" exporter mode |
| 2 | **Critical** | Security | Full URL with query params logged at INFO |
| 3 | **High** | Resource | NewClient() per nil-client call (connection pool leak) |
| 4 | **Medium** | Safety | No guard against double InitTracer/InitMeter |
| 5 | **Medium** | Ops | Context cancellation logged as ERROR (alert noise) |
| 6 | **Medium** | Compat | statusRecorder missing Unwrap() for Go 1.20+ |
| 7 | **Medium** | Correctness | Pre-init metrics delegation untested |
| 8 | **Medium** | Safety | Global provider mutation not atomic |
| 9 | **Medium** | Completeness | Sampling APIs documented but inaccessible |
| 10 | **Medium** | Traces | No span status/error recording on failures |
| 11 | **Low** | Metrics | Middleware doesn't auto-record RED metrics |
| 12 | **Low** | Metrics | No outbound request duration metric |
| 13 | **Low** | Design | Linear backoff instead of exponential+jitter |
| 14 | **Low** | Docs | README path, exporter description, signature differences |
