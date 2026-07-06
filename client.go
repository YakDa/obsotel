package obsotel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	obs "github.com/YakDa/obsotel/internal/obsbase"
)

// NewClient returns an *http.Client with OTel client-side tracing enabled.
// Every outbound request creates a client span and injects the W3C
// traceparent header so the destination service can continue the trace.
//
// The returned client is safe for concurrent use. Construct once at startup
// and reuse. The convenience DoRequest / DoRequestWithRetry helpers reuse a
// package-level defaultClient to avoid allocating a new transport per call.
//
// Default timeouts match common production defaults; override by setting
// fields after construction:
//
//	c := obsotel.NewClient()
//	c.Timeout = 30 * time.Second
func NewClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	}
}

// defaultClient is the package-level *http.Client used by the convenience
// DoRequest / DoRequestWithRetry helpers. Allocating one here (at package
// init) avoids building a fresh otelhttp.NewTransport on every outbound
// call, which was a real cost when these helpers were the default path.
var defaultClient = NewClient()

// DoRequest performs an outbound HTTP request via the OTel-instrumented
// default client. Mirrors obs.DoRequest but uses defaultClient internally,
// so callers don't need to construct or pass an *http.Client.
//
// On failure: logs at ERROR with err + error_chain + outbound_* fields.
// On success: logs at INFO with status, duration_ms, bytes.
//
// Returns the response (caller must close Body) and any transport error.
func DoRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	return DoRequestWithClient(ctx, defaultClient, req)
}

// DoRequestWithClient is DoRequest with an explicit client. Use this when
// you need a custom timeout or transport on top of OTel.
//
// Passing nil is treated as 'use obsotel's default client' rather than
// creating a new client per call. Creating a fresh *http.Client (and
// underlying otelhttp.Transport) per call leaks idle connections — each
// transport keeps its own pool.
func DoRequestWithClient(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = defaultClient
	}
	if reqID := RequestIDFromContext(ctx); reqID != "" && req.Header.Get("X-Request-ID") == "" {
		req.Header.Set("X-Request-ID", reqID)
	}

	l := L(ctx).With(
		slog.String("outbound_method", req.Method),
		slog.String("outbound_url", obs.SafeURLString(req.URL)),
		slog.String("outbound_host", req.URL.Host),
	)

	start := time.Now()
	resp, err := client.Do(req)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		// Context cancellation is expected in production (client
		// disconnected, request deadline exceeded). Demoting to WARN avoids
		// alerting noise from these expected paths. Genuine transport
		// failures (DNS, TCP, TLS, etc.) stay at ERROR.
		level, msg := slog.LevelError, "outbound_failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			level, msg = slog.LevelWarn, "outbound_cancelled"
		}
		// Mark the active span (if any) as errored. Trace UIs (Jaeger,
		// Tempo, Honeycomb) surface failed spans via SetStatus(Error).
		// Without this, the client span looks successful even though the
		// call returned a transport error.
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		l.LogAttrs(ctx, level, msg,
			slog.Int64("duration_ms", dur),
			slog.String("err", err.Error()),
			slog.Any("error_chain", ChainOf(err)),
		)
		return nil, err
	}

	l.LogAttrs(ctx, slog.LevelInfo, "outbound_completed",
		slog.Int64("duration_ms", dur),
		slog.Int("status", resp.StatusCode),
		slog.Int64("bytes", resp.ContentLength),
	)
	return resp, nil
}

// DoRequestWithRetry retries DoRequest up to maxAttempts with linear backoff.
// Treats 5xx and transport errors as retryable; 4xx as terminal.
//
// Each attempt is logged. Final success after retries emits a WARN with
// the attempt count. Final failure returns the last error.
//
// Retry safety: req.GetBody, if set, is invoked before each attempt to reset
// the request body. This makes retries safe for non-idempotent POST/PUT/PATCH
// callers that opt in via http.NewRequestWithContext + their own GetBody
// implementation. Without GetBody, callers must only retry idempotent
// requests (GET/HEAD/PUT/DELETE) — the Body will be drained after attempt 1.
func DoRequestWithRetry(ctx context.Context, req *http.Request, maxAttempts int, backoff time.Duration) (*http.Response, error) {
	return DoRequestWithRetryAndClient(ctx, defaultClient, req, maxAttempts, backoff)
}

// DoRequestWithRetryAndClient is DoRequestWithRetry with an explicit client.
func DoRequestWithRetryAndClient(
	ctx context.Context,
	client *http.Client,
	req *http.Request,
	maxAttempts int,
	backoff time.Duration,
) (*http.Response, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastResp *http.Response
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Reset the body before each attempt if the caller opted in via GetBody.
		// Without GetBody, retries are only safe for idempotent methods.
		if attempt > 1 && req.GetBody != nil {
			if body, err := req.GetBody(); err == nil {
				req.Body = body
			} else {
				L(ctx).Warn("outbound_retry_body_reset_failed",
					slog.Int("attempt", attempt),
					slog.String("err", err.Error()),
				)
				return nil, err
			}
		}

		resp, err := DoRequestWithClient(ctx, client, req)
		if err == nil && resp.StatusCode < 500 {
			if attempt > 1 {
				L(ctx).Warn("outbound_retry_succeeded",
					slog.Int("attempt", attempt),
				)
			}
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = &HTTPError{Status: resp.StatusCode}
			resp.Body.Close()
		}
		lastResp = nil

		if attempt < maxAttempts {
			L(ctx).Warn("outbound_retry",
				slog.Int("attempt", attempt),
				slog.Int("max_attempts", maxAttempts),
				slog.String("err", lastErr.Error()),
			)
			// time.NewTimer + Stop() avoids the leaked timer that time.After
			// would leave behind under ctx cancellation with a long backoff.
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastResp, lastErr
}

// HTTPError is returned by DoRequestWithRetry for non-retryable-or-final
// 5xx responses. Implements error.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d %s", e.Status, http.StatusText(e.Status))
}