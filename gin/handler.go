// Package obsgin provides Gin-specific observability helpers built on top of
// the framework-agnostic obsotel package. Import as:
//
//	import "thedesk/internal/obsotel/gin"
//
// The sub-package isolates the gin dependency so the root obsotel package
// remains usable from non-Gin services (gRPC, plain net/http, workers, etc.).
package obsgin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/YakDa/obsotel"
)

// HandlerFunc is a gin handler that returns an error. On success (nil return),
// the handler is expected to have written the response itself (e.g. c.JSON(200, ...)).
// On error, WrapHandler writes the JSON error response and logs the error
// automatically — the handler should NOT call c.JSON or log when returning non-nil.
type HandlerFunc func(c *gin.Context) error

// WrapHandler adapts a HandlerFunc into a gin.HandlerFunc.
//
// When the returned error is non-nil:
//  1. Extracts the HTTP status code (from *obsotel.HTTPError, *obsotel.AppError kind, or defaults to 500).
//  2. Writes {"error": msg} to the client — matching the existing response format.
//  3. Logs with status, method, path, err, and error_chain.
//  4. Records the error on the active OTel span (if any).
//
// When the handler returns nil, WrapHandler does nothing — the handler has
// already written the success response.
//
// Usage:
//
//	g.POST("/tickets/:id/sync", obsgin.WrapHandler(s.handleManualSync))
func WrapHandler(fn HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := fn(c)
		if err == nil {
			return
		}

		ctx := c.Request.Context()
		status := statusFromError(err)

		// Write the error response (only if nothing was written yet).
		if !c.Writer.Written() {
			c.JSON(status, gin.H{"error": clientMessage(err, status)})
		}

		// Log the error via LogErr — this surfaces AppError op/kind/meta
		// at the top level of the structured log line automatically.
		obsotel.LogErr(ctx, "handler_error", err,
			"status", status,
			"method", c.Request.Method,
			"path", c.FullPath(),
		)

		// Mark the OTel span as errored.
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}
}

// HTTPErr constructs an error that WrapHandler will map to the given status code.
// The msg is the client-facing message written in {"error": msg}. The cause is
// the underlying error logged server-side (via error_chain).
//
// Usage in a handler:
//
//	resp, err := s.SyncTicket(ctx, ticketID, opts)
//	if err != nil {
//	    return obsgin.HTTPErr(http.StatusBadGateway, "sync failed", err)
//	}
func HTTPErr(status int, msg string, cause error) error {
	return &obsotel.HTTPError{Status: status, Message: msg, Err: cause}
}

// statusFromError extracts the HTTP status code from err.
// Checks (in order): *obsotel.HTTPError, *obsotel.AppError kind mapping, default 500.
func statusFromError(err error) int {
	var he *obsotel.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	var ae *obsotel.AppError
	if errors.As(err, &ae) {
		return statusFromKind(ae.Kind)
	}
	return http.StatusInternalServerError
}

// clientMessage returns the string to put in {"error": msg}.
// For HTTPError with an explicit Message, uses that.
// For 5xx without an explicit message, returns a generic string to avoid
// leaking internal details (DB errors, stack traces) to clients.
func clientMessage(err error, status int) string {
	var he *obsotel.HTTPError
	if errors.As(err, &he) && he.Message != "" {
		return he.Message
	}
	// Never expose raw internal errors to the client.
	if status >= 500 {
		return http.StatusText(status)
	}
	return err.Error()
}

// statusFromKind maps AppError.Kind to an HTTP status code.
func statusFromKind(kind string) int {
	switch kind {
	case "not_found":
		return http.StatusNotFound
	case "forbidden":
		return http.StatusForbidden
	case "unauthorized":
		return http.StatusUnauthorized
	case "bad_request":
		return http.StatusBadRequest
	case "conflict":
		return http.StatusConflict
	case "rate_limited":
		return http.StatusTooManyRequests
	case "bad_gateway":
		return http.StatusBadGateway
	case "unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
