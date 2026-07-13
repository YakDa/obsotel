package obsotel

import (
	"log/slog"
	"net/http"

	obs "github.com/YakDa/obsotel/internal/obsbase"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// Handler returns an http.Handler wrapped with:
//   - otelhttp server middleware (creates server span, extracts W3C traceparent)
//   - obs.LoggingMiddleware (logs request/response with request_id, status, duration)
//
// Order matters: OTel outermost so the span covers the entire request including
// logging. The logging middleware then reads trace_id/span_id from ctx via the
// traceHandler in NewLogger.
//
// Usage:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/foo", fooHandler)
//	http.ListenAndServe(":8080", obsotel.Handler(log, mux, "user-service"))
func Handler(log *slog.Logger, next http.Handler, serviceName string) http.Handler {
	return otelhttp.NewHandler(
		traceIDHeader(obs.LoggingMiddleware(log)(next)),
		serviceName,
	)
}

// HandlerWithFilter is Handler but allows opting out of tracing and access
// logging for specific routes (e.g. /healthz, /metrics). Pass a function that
// returns true to trace.
//
// Requests for which shouldTrace returns false are served directly by the
// underlying handler, bypassing both otelhttp and the logging middleware
// (no server span, no access log line). This prevents health-check noise.
//
//	obsotel.HandlerWithFilter(log, mux, "user-service",
//	    func(r *http.Request) bool { return r.URL.Path != "/healthz" })
func HandlerWithFilter(
	log *slog.Logger,
	next http.Handler,
	serviceName string,
	shouldTrace func(*http.Request) bool,
) http.Handler {
	logging := traceIDHeader(obs.LoggingMiddleware(log)(next))
	otelWrapped := otelhttp.NewHandler(logging, serviceName)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldTrace != nil && !shouldTrace(r) {
			next.ServeHTTP(w, r)
			return
		}
		otelWrapped.ServeHTTP(w, r)
	})
}

// traceIDHeader injects X-Trace-ID into the response when an active OTel span
// exists. Placed between otelhttp (which creates the span) and the logging
// middleware so the header is set before the first Write.
func traceIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			w.Header().Set("X-Trace-ID", sc.TraceID().String())
		}
		next.ServeHTTP(w, r)
	})
}
