package obsotel

import (
	"log/slog"
	"net/http"

	obs "github.com/mingdos/obsotel/internal/obsbase"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
		obs.LoggingMiddleware(log)(next),
		serviceName,
	)
}

// HandlerWithFilter is Handler but allows opting out of tracing for specific
// routes (e.g. /healthz, /metrics). Pass a function that returns true to trace.
//
// Requests for which shouldTrace returns false are served by the logging
// middleware directly, bypassing otelhttp entirely (no server span created,
// no W3C traceparent injection).
//
//	obsotel.HandlerWithFilter(log, mux, "user-service",
//	    func(r *http.Request) bool { return r.URL.Path != "/healthz" })
func HandlerWithFilter(
	log *slog.Logger,
	next http.Handler,
	serviceName string,
	shouldTrace func(*http.Request) bool,
) http.Handler {
	logging := obs.LoggingMiddleware(log)(next)
	otelWrapped := otelhttp.NewHandler(logging, serviceName)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldTrace != nil && !shouldTrace(r) {
			logging.ServeHTTP(w, r)
			return
		}
		otelWrapped.ServeHTTP(w, r)
	})
}