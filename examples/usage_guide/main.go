// Package main is a usage guide for the github.com/mingdos/obsotel package.
//
// It exercises every exported function, type, and method so you can see how
// each one is meant to be called and what it produces. Run with:
//
//	go run ./examples/usage_guide/
//
// Output is real observability output: logs go to stderr (where they would
// land in production via `docker logs` / `kubectl logs`), section banners
// go to stdout for navigability.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/mingdos/obsotel"
)

// --------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------

// banner prints a section header to stdout so the demo output is navigable
// even though the actual log lines come from stderr.
func banner(title string) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stdout, " ", title)
	fmt.Fprintln(os.Stdout, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// pauseBetween lets the user (and tools) see the boundary between sections.
// Tracer shutdown, in particular, needs a small moment to flush.
func pauseBetween() {
	time.Sleep(50 * time.Millisecond)
}

// --------------------------------------------------------------------
// SECTION 1 — Logger construction
// --------------------------------------------------------------------

func sectionLoggerConstruction() {
	banner("SECTION 1 — Logger construction")

	// Most common path. Picks JSON or text based on env, writes to stderr,
	// uses InfoLevel. The returned *slog.Logger is wrapped with a traceHandler
	// that auto-injects trace_id/span_id when an OTel span is active in ctx.
	logger := obsotel.NewLogger("prod")
	fmt.Fprintln(os.Stdout, "NewLogger(\"prod\")      => *slog.Logger with JSON handler on stderr at InfoLevel")

	// Explicit level (e.g. Debug in dev).
	loggerDebug := obsotel.NewLoggerWithLevel("dev", slog.LevelDebug)
	_ = loggerDebug
	fmt.Fprintln(os.Stdout, "NewLoggerWithLevel      => *slog.Logger with text handler at DebugLevel")

	// Custom writer. Useful for testing (write to bytes.Buffer) or for
	// shipping logs somewhere other than stderr.
	loggerToFile := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, os.Stderr)
	_ = loggerToFile

	// Parse a level string. Useful when level comes from env or config.
	fmt.Fprintln(os.Stdout, "LevelFromString(\"warn\") =>", obsotel.LevelFromString("warn"))

	// Bind one as the process-wide default. Bare slog.* calls in service
	// code now route through it (the AI-drift defense).
	slog.SetDefault(logger)

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 2 — Field key constants
// --------------------------------------------------------------------

func sectionConstants() {
	banner("SECTION 2 — Field key constants")

	// Don't hardcode "request_id" / "user_id" / "trace_id" / "span_id" as
	// strings. Use these constants. LogQL/ELK queries and dashboards
	// break when devs use "uid" / "userId" / "USER_ID" inconsistently.
	fmt.Fprintf(os.Stdout, "obsotel.RequestIDKey = %q\n", obsotel.RequestIDKey)
	fmt.Fprintf(os.Stdout, "obsotel.UserIDKey   = %q\n", obsotel.UserIDKey)
	fmt.Fprintf(os.Stdout, "obsotel.TraceIDKey  = %q\n", obsotel.TraceIDKey)
	fmt.Fprintf(os.Stdout, "obsotel.SpanIDKey   = %q\n", obsotel.SpanIDKey)

	// In a real call site, you'd write:
	//   log.Info("login", slog.String(obsotel.UserIDKey, "u42"))
	// instead of:
	//   log.Info("login", "uid", "u42")

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 3 — Context binding
// --------------------------------------------------------------------

func sectionContextBinding() {
	banner("SECTION 3 — Context binding")

	ctx := context.Background()

	// WithRequestID stores the bare request ID in ctx. Used by middleware
	// to bind the per-request ID. Empty string clears it.
	ctx = obsotel.WithRequestID(ctx, "req-001")

	// With returns a new ctx with the bound logger having these attrs,
	// plus the logger itself for direct chained calls. The bound logger
	// is stored in ctx, so subsequent L(ctx) calls see user_id + tenant.
	ctx, log := obsotel.With(ctx,
		slog.String(obsotel.UserIDKey, "u42"),
		slog.String("tenant", "acme"),
	)

	// L reads the bound logger; falls back to slog.Default() if none is bound.
	// Note: WithLogger REPLACES the contextual logger entirely — use it only
	// when you have a brand-new logger to inject (e.g. a test buffer) and
	// don't care about previously-bound attrs. With() above is the right way
	// to add attrs to the existing contextual logger.
	//
	// Use InfoContext, not Info, so the requestIDHandler can read the
	// request_id from ctx and inject it. Bare .Info() drops ctx on the
	// floor, which is why manual binding paths need the -Context variants.
	log.InfoContext(ctx, "request_logged", slog.String("path", "/api/users"))

	// Round-trip the request ID.
	fmt.Fprintf(os.Stdout, "RequestIDFromContext = %q\n", obsotel.RequestIDFromContext(ctx))

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 4 — Error construction (AppError + wraps)
// --------------------------------------------------------------------

func sectionErrorConstruction() {
	banner("SECTION 4 — Error construction")

	// New creates a structured AppError with op + kind. The wrapped err is
	// the underlying cause. AppError implements slog.LogValuer so it renders
	// structured when logged directly; LogErr also surfaces its fields at
	// top level.
	root := errors.New("connection refused")
	dbWrap := fmt.Errorf("db query: %w", root)
	appErr := obsotel.New("load_user", "infra_error", dbWrap).
		WithMeta(obsotel.UserIDKey, "u42").
		WithMeta("tenant", "acme")

	fmt.Fprintln(os.Stdout, "appErr.Error():")
	fmt.Fprintln(os.Stdout, " ", appErr.Error())

	// Wrap: attach an operation label without changing the underlying error.
	// Walks errors.Unwrap so the chain is preserved.
	ctx := obsotel.WithRequestID(context.Background(), "req-001")
	wrapped := obsotel.Wrap(ctx, appErr, "controller.load_user")
	fmt.Fprintln(os.Stdout, "obsotel.Wrap result.Error():")
	fmt.Fprintln(os.Stdout, " ", wrapped.Error())

	// WrapWith: same, plus arbitrary key/value metadata.
	wrappedWith := obsotel.WrapWith(ctx, appErr, "controller.load_user",
		"attempt", 3,
		"max_attempts", 5,
	)
	fmt.Fprintln(os.Stdout, "obsotel.WrapWith result.Error():")
	fmt.Fprintln(os.Stdout, " ", wrappedWith.Error())

	// ChainOf returns an ErrorChain (slice) walking errors.Unwrap on every
	// layer. LogErr renders this as a top-level array.
	chain := obsotel.ChainOf(wrappedWith)
	fmt.Fprintln(os.Stdout, "obsotel.ChainOf:")
	for i, e := range chain {
		fmt.Fprintf(os.Stdout, "  [%d] %T: %s\n", i, e, e.Error())
	}

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 5 — LogErr (the only blessed way to log errors)
// --------------------------------------------------------------------

func sectionLogErr() {
	banner("SECTION 5 — LogErr (always use this for errors)")

	// Build a multi-layer chain like real code does.
	root := errors.New("connection refused")
	dbWrap := fmt.Errorf("db query: %w", root)
	appErr := obsotel.New("load_user", "infra_error", dbWrap).
		WithMeta(obsotel.UserIDKey, "u42").
		WithMeta("tenant", "acme")

	ctx := obsotel.WithRequestID(context.Background(), "req-001")
	ctx, _ = obsotel.With(ctx, slog.String("region", "sg"))

	// LogErr logs the err + chain at ERROR. Surfaces AppError op/kind/Meta
	// at top level. Caller can pass extra attrs. Nil-safe.
	obsotel.LogErr(ctx, "load_user_failed", appErr, "attempt", 3)

	// Without an AppError — bare error still gets a structured chain.
	obsotel.LogErr(ctx, "raw_error", root)

	// Nil is a no-op (no panic). The "LogErr if err != nil" pattern.
	var maybeErr error
	obsotel.LogErr(ctx, "no_op_if_nil", maybeErr)

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 6 — HTTP server middleware (Handler)
// --------------------------------------------------------------------

func sectionHTTPHandler() {
	banner("SECTION 6 — HTTP server middleware")

	// Build a mux with one route that simulates real work.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Bind request-scoped attrs in the handler so logs and trace span
		// both carry user_id for the rest of the request lifetime.
		uid := r.PathValue("id")
		ctx, _ = obsotel.With(ctx, slog.String(obsotel.UserIDKey, uid))
		r = r.WithContext(ctx)

		// The obsotel middleware will log "path" for the response line.
		// Use a different key here so slog doesn't emit duplicate fields.
		obsotel.L(ctx).Info("user_lookup", slog.String("lookup_path", r.URL.Path))

		// Pretend to do work + succeed.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"`+uid+`"}`)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // not traced via HandlerWithFilter below
	})

	// Handler = otelhttp.NewHandler(obs.LoggingMiddleware(mux), "user-service").
	// The OTel wrapper creates a server span; the obs middleware logs
	// request/response with request_id, status, duration.
	log := obsotel.NewLogger("prod")
	handler := obsotel.Handler(log, mux, "user-service")
	fmt.Fprintln(os.Stdout, "obsotel.Handler returns an http.Handler. Stand up a server to test it.")

	// Quick sanity check via httptest: issue one request through the handler
	// and verify it routed correctly. Real OTel tracing requires the
	// tracer to be initialized first (Section 8).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/users/u42", nil)
	handler.ServeHTTP(rec, req)
	fmt.Fprintf(os.Stdout, "httptest status for /api/users/u42: %d (body: %s)\n",
		rec.Code, strings.TrimSpace(rec.Body.String()))

	// HandlerWithFilter: skips otelhttp tracing for paths shouldTrace returns
	// false on (e.g. /healthz to avoid polluting traces with liveness probes).
	filtered := obsotel.HandlerWithFilter(log, mux, "user-service",
		func(r *http.Request) bool { return r.URL.Path != "/healthz" })
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/healthz", nil)
	filtered.ServeHTTP(rec2, req2)
	fmt.Fprintf(os.Stdout, "HandlerWithFilter /healthz status: %d\n", rec2.Code)

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 7 — HTTP client (NewClient, DoRequest, retry)
// --------------------------------------------------------------------

func sectionHTTPClient() {
	banner("SECTION 7 — HTTP client")

	// Spin up a tiny target server that fails the first call and succeeds
	// on the second — to demonstrate retry behavior.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true,"attempt_received":`+fmt.Sprint(calls)+`}`)
	}))
	defer srv.Close()

	// NewClient returns an *http.Client with otelhttp client-side tracing.
	// Pass to DoRequestWithClient / DoRequestWithRetryAndClient to control
	// which client is used.
	client := obsotel.NewClient()
	fmt.Fprintln(os.Stdout, "obsotel.NewClient() returned *http.Client with otelhttp transport")

	// DoRequest: one attempt. Logs the request/response. Returns the body
	// and any transport error. Status codes (incl. 5xx) are NOT treated as
	// errors here — that's intentional; the *Retry paths are what wraps
	// terminal 5xx into an HTTPError.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/x", nil)
	ctx := obsotel.WithRequestID(context.Background(), "req-client-1")
	resp, err := obsotel.DoRequestWithClient(ctx, client, req)
	if err != nil {
		var he *obsotel.HTTPError
		if errors.As(err, &he) {
			fmt.Fprintf(os.Stdout, "DoRequest failed with HTTPError: status=%d (%s)\n", he.Status, he.Error())
		} else {
			fmt.Fprintf(os.Stdout, "DoRequest transport error: %v\n", err)
		}
	} else {
		body := make([]byte, 256)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		fmt.Fprintf(os.Stdout, "DoRequest status=%d, body=%q\n",
			resp.StatusCode, strings.TrimSpace(string(body[:n])))
	}

	// DoRequestWithRetry: wraps NewClient() internally; treats transport
	// errors AND 5xx responses as retryable. After maxAttempts, returns
	// &HTTPError{Status: resp.StatusCode} for terminal 5xx, or the last
	// transport error otherwise. This call will succeed on attempt #2
	// because the test server above returns 503 on call 1, 200 thereafter.
	req2, _ := http.NewRequest("GET", srv.URL+"/api/v1/x", nil)
	ctx2 := obsotel.WithRequestID(context.Background(), "req-client-2")
	resp2, err2 := obsotel.DoRequestWithRetry(ctx2, req2, 3, 25*time.Millisecond)
	if err2 != nil {
		var he *obsotel.HTTPError
		if errors.As(err2, &he) {
			fmt.Fprintf(os.Stdout, "DoRequestWithRetry gave up after retries: status=%d (%s)\n",
				he.Status, he.Error())
		} else {
			fmt.Fprintf(os.Stdout, "DoRequestWithRetry failed: %v\n", err2)
		}
	} else {
		body2 := make([]byte, 256)
		n2, _ := resp2.Body.Read(body2)
		resp2.Body.Close()
		fmt.Fprintf(os.Stdout, "DoRequestWithRetry succeeded after %d attempt(s), body=%q\n",
			calls, strings.TrimSpace(string(body2[:n2])))
	}

	pauseBetween()
}

// --------------------------------------------------------------------
// SECTION 8 — Tracer setup + spans
// --------------------------------------------------------------------

func sectionTracer() {
	banner("SECTION 8 — Tracer setup + spans")

	// InitTracer sets up a global TracerProvider. With no options, it uses
	// the stdouttrace exporter (pretty-printed JSON spans to stderr) and
	// 100% sampling. Returns a shutdown that flushes the exporter.
	ctx := context.Background()
	shutdown, err := obsotel.InitTracer(ctx, "user-service")
	if err != nil {
		fmt.Fprintf(os.Stdout, "InitTracer returned error (and no-op shutdown): %v\n", err)
	} else {
		fmt.Fprintln(os.Stdout, "InitTracer ok; defer shutdown to flush spans.")
	}
	defer shutdown(ctx)

	// Options that can be passed:
	//   obsotel.WithSamplingRatio(0.1)  // 10% of traces recorded
	//   obsotel.WithExporter(otlpexp)   // ship to OTLP collector instead of stdout

	// Tracer returns a trace.Tracer. Use it to start spans.
	tr := obsotel.Tracer("user-service")

	spCtx, span := tr.Start(ctx, "demo.span")
	defer span.End()

	// Now any log call within spCtx auto-injects trace_id/span_id.
	obsotel.L(spCtx).Info("inside_a_span", slog.String("event", "started"))

	// End the span explicitly.
	span.End()

	pauseBetween()
	// Small wait so stdouttrace can flush its buffer before the next section.
	time.Sleep(100 * time.Millisecond)
}

// --------------------------------------------------------------------
// SECTION 9 — Metrics (counters and histograms)
// --------------------------------------------------------------------

func sectionMetrics() {
	banner("SECTION 9 — Metrics")

	// Construct a counter and a histogram. Without a MeterProvider configured
	// (we didn't set one), calls are no-ops — they don't error, they just
	// go nowhere. That's the fail-open contract.
	orders, err := obsotel.NewCounter("orders_placed_total", "Orders successfully placed")
	if err != nil {
		fmt.Fprintf(os.Stdout, "NewCounter error: %v\n", err)
	} else {
		fmt.Fprintln(os.Stdout, "NewCounter ok")
	}

	latency, err := obsotel.NewHistogram("db_query_seconds", "DB query latency", "s")
	if err != nil {
		fmt.Fprintf(os.Stdout, "NewHistogram error: %v\n", err)
	} else {
		fmt.Fprintln(os.Stdout, "NewHistogram ok")
	}

	// MustNewCounter / MustNewHistogram panic on error — for use at
	// package-init or top of main when you know the name is well-formed.
	mustOrders := obsotel.MustNewCounter("must_orders_total", "Orders (must)")
	mustLatency := obsotel.MustNewHistogram("must_db_query_seconds", "DB query latency (must)", "s")
	_ = mustOrders
	_ = mustLatency

	// Use them — Inc, Add, Record. Attributes make values filterable.
	ctx := context.Background()
	orders.Inc(ctx, attribute.String("region", "sg"))
	latency.Record(ctx, 0.012, attribute.String("query", "users.find"))

	// Prebuilt metrics for the two most common HTTP patterns.
	obsotel.HTTPRequestsTotal.Inc(ctx,
		attribute.String("method", "GET"),
		attribute.String("status", "200"),
	)
	obsotel.HTTPRequestDuration.Record(ctx, 0.045,
		attribute.String("method", "GET"),
		attribute.String("path", "/api/users/u42"),
		attribute.Int("status", 200),
	)

	fmt.Fprintln(os.Stdout, "Counter/Histogram calls without a MeterProvider are no-ops (fail-open).")

	pauseBetween()
}

// --------------------------------------------------------------------
// main
// --------------------------------------------------------------------

func main() {
	sectionLoggerConstruction()
	sectionConstants()
	sectionContextBinding()
	sectionErrorConstruction()
	sectionLogErr()
	sectionHTTPHandler()
	sectionHTTPClient()
	sectionTracer()
	sectionMetrics()

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stdout, "  end of demo — see stderr above for the actual log/trace output")
	fmt.Fprintln(os.Stdout, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}
