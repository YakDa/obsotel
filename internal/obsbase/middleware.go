package obs

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// LoggingMiddleware returns an HTTP middleware that:
//   - extracts X-Request-ID from the incoming request, or generates a new one
//   - binds a contextual logger with request_id, method, path, remote
//   - records the response status and bytes written
//   - logs one structured line per request at completion
//   - echoes X-Request-ID back on the response
//
// Use:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/foo", fooHandler)
//	http.ListenAndServe(":8080", obs.LoggingMiddleware(slog.Default())(mux))
func LoggingMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = NewRequestID()
			}

			l := base.With(
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote", clientIP(r)),
			)

			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			ctx := WithLogger(r.Context(), l)
			ctx = WithRequestID(ctx, reqID)
			r = r.WithContext(ctx)

			w.Header().Set("X-Request-ID", reqID)

			start := time.Now()
			defer func() {
				l.LogAttrs(ctx, slog.LevelInfo, "http_request",
					slog.Int("status", rw.status),
					slog.Int64("duration_ms", time.Since(start).Milliseconds()),
					slog.Int("bytes", rw.bytes),
				)
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

// statusRecorder wraps http.ResponseWriter to capture status code and
// bytes written. Pass-through Hijack/Flush keep WebSockets and SSE working.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying http.ResponseWriter so that callers using
// http.NewResponseController (Go 1.20+) can discover optional interfaces
// like SetReadDeadline/SetWriteDeadline/Hijack/Flusher on the original.
// Without this, ResponseController.SetReadDeadline silently no-ops against
// the recorder.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// clientIP returns the best-guess client IP, honoring common proxy headers.
//
// SECURITY: X-Forwarded-For and X-Real-IP are trusted unconditionally. This is
// safe when the service is behind a trusted reverse proxy / load balancer that
// strips or overwrites these headers from untrusted clients. It is UNSAFE when
// the service is reachable directly from the public internet — a malicious
// caller can spoof the remote field in their log line.
//
// For internet-facing deployments, place an edge proxy that overwrites these
// headers, or sanitize them upstream before requests reach this middleware.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := indexComma(xff); i >= 0 {
			return trimSpace(xff[:i])
		}
		return trimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexComma(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
