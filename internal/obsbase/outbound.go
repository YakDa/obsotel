package obs

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// DoRequest performs an outbound HTTP request and logs it. The request_id
// from ctx is propagated as X-Request-ID so downstream services can
// correlate.
//
// On failure: logs at ERROR with err + error_chain + outbound_* fields.
// On success: logs at INFO with status, duration_ms, bytes.
//
// Returns the response (caller must close Body) and any transport error.
func DoRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if reqID := RequestIDFromContext(ctx); reqID != "" && req.Header.Get("X-Request-ID") == "" {
		req.Header.Set("X-Request-ID", reqID)
	}

	l := L(ctx).With(
		slog.String("outbound_method", req.Method),
		slog.String("outbound_url", req.URL.String()),
		slog.String("outbound_host", req.URL.Host),
	)

	start := time.Now()
	resp, err := client.Do(req)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		l.LogAttrs(ctx, slog.LevelError, "outbound_failed",
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
func DoRequestWithRetry(
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

		resp, err := DoRequest(ctx, client, req)
		if err == nil && resp.StatusCode < 500 {
			if attempt > 1 {
				L(ctx).Warn("outbound_retry_succeeded",
					slog.Int("attempt", attempt),
				)
			}
			return resp, nil
		}

		// Failure: capture, close body if any, log WARN.
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
