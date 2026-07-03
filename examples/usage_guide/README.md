# `usage_guide` — Coding guide for obsotel

A runnable tour of every exported function, type, and method in the
`github.com/YakDa/obsotel` package.

## Run it

```bash
go run ./examples/usage_guide/
```

## What you'll see

Section banners go to **stdout** for navigability. Actual log lines
and OTel spans go to **stderr** — the same streams `docker logs` and
`kubectl logs` capture in production. So if you mix the output into
one stream with `2>&1`, you get:

```
━━ SECTION 1 — Logger construction ━━
NewLogger("prod")    => *slog.Logger with JSON handler on stderr at InfoLevel
NewLoggerWithLevel  => *slog.Logger with text handler at DebugLevel
LevelFromString("warn") => WARN

━━ SECTION 2 — Field key constants ━━
obsotel.RequestIDKey = "request_id"
obsotel.UserIDKey   = "user_id"
obsotel.TraceIDKey  = "trace_id"
obsotel.SpanIDKey   = "span_id"

━━ SECTION 3 — Context binding ━━
{"time":"...","level":"INFO","msg":"request_logged","path":"/api/users"}
RequestIDFromContext = "req-001"

... etc
```

## Section map

| Section | What it covers | APIs exercised |
|---|---|---|
| **1** | Logger construction | `NewLogger`, `NewLoggerWithLevel`, `NewLoggerToWriter`, `LevelFromString`, `slog.SetDefault` |
| **2** | Field key constants | `RequestIDKey`, `UserIDKey`, `TraceIDKey`, `SpanIDKey` |
| **3** | Context binding | `WithRequestID`, `With`, `WithLogger`, `L`, `RequestIDFromContext` |
| **4** | Error construction | `New`, `Wrap`, `WrapWith`, `ChainOf`, `AppError.WithMeta` |
| **5** | Logging errors | `LogErr` (always use this for errors) |
| **6** | HTTP server middleware | `Handler`, `HandlerWithFilter` (with `httptest`) |
| **7** | HTTP client + retry | `NewClient`, `DoRequest`, `DoRequestWithClient`, `DoRequestWithRetry`, `HTTPError` |
| **8** | Tracer setup + spans | `InitTracer`, `Tracer`, `WithSamplingRatio`, `WithExporter` (mentioned), span lifecycle |
| **9** | Metrics | `NewCounter`, `MustNewCounter`, `NewHistogram`, `MustNewHistogram`, `Counter.Add/Inc`, `Histogram.Record`, `HTTPRequestsTotal`, `HTTPRequestDuration` |

## Sections NOT here (yet)

- `goroutine_id` (n/a in Go)
- A custom analyzer enforcing obsotel usage — that lives in the `lint.md` doc, not in a code example.

## Comparing against `logerr_comparison/`

`logerr_comparison/` (the sibling demo) is a **side-by-side contrast**
between bare `slog.Error` and `obsotel.LogErr` — useful for understanding
*why* the contract exists. `usage_guide/` is a **linear walk** through
the entire API — useful for *how* to call each thing.

If you want to learn both `what to write` and `why we wrote it that way`,
run `logerr_comparison` first, then this one.
