// Package obsgin provides Gin-specific observability helpers built on top of
// the framework-agnostic obsotel package. Import as:
//
//	import "github.com/YakDa/obsotel/gin"
//
// The sub-package isolates the gin dependency so the root obsotel package
// remains usable from non-Gin services (gRPC, plain net/http, workers, etc.).
package obsgin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel/attribute"
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
//  2. Writes a unified JSON response with omitempty fields (error, code, details, retryable, request_id).
//  3. Logs with status, method, path, err, error_chain, and rich metadata (error_code, retryable, error_details) when available.
//  4. Records the error on the active OTel span (if any) with error.code and error.retryable attributes.
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
			c.JSON(status, buildResponseBody(c, err, status))
		}

		// Log the error with rich metadata when available.
		logArgs := []any{
			"status", status,
			"method", c.Request.Method,
			"path", c.FullPath(),
		}
		var he *obsotel.HTTPError
		if errors.As(err, &he) {
			if he.Code != "" {
				logArgs = append(logArgs, "error_code", he.Code)
			}
			if he.Retryable != nil {
				logArgs = append(logArgs, "retryable", *he.Retryable)
			}
			if len(he.Details) > 0 {
				logArgs = append(logArgs, "error_details", he.Details)
			}
		}
		obsotel.LogErr(ctx, "handler_error", err, logArgs...)

		// Mark the OTel span as errored with rich attributes.
		if span := trace.SpanFromContext(ctx); span.IsRecording() {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			if errors.As(err, &he) {
				if he.Code != "" {
					span.SetAttributes(attribute.String("error.code", he.Code))
				}
				if he.Retryable != nil {
					span.SetAttributes(attribute.Bool("error.retryable", *he.Retryable))
				}
			}
		}
	}
}

// HTTPErr constructs an error that WrapHandler will map to the given status code.
// The msg is the client-facing message written in {"error": msg}. The cause is
// the underlying error logged server-side (via error_chain). Optional RichOption
// values set code, details, retryable on the constructed HTTPError.
//
// Usage in a handler:
//
//	resp, err := s.SyncTicket(ctx, ticketID, opts)
//	if err != nil {
//	    return obsgin.HTTPErr(http.StatusBadGateway, "sync failed", err)
//	}
//
// With rich options:
//
//	return obsgin.HTTPErr(http.StatusBadRequest, "invalid request", err,
//	    obsgin.WithCode("request.invalid"),
//	    obsgin.WithDetail("field", "email"),
//	    obsgin.WithDetail("reason", "malformed"),
//	)
func HTTPErr(status int, msg string, err error, opts ...RichOption) error {
	he := &obsotel.HTTPError{Status: status, Message: msg, Err: err}
	for _, opt := range opts {
		if opt != nil {
			opt(he)
		}
	}
	return he
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

// clientMessage returns the string to put in the "error" JSON field.
// For HTTPError with an explicit Message on 4xx, uses that.
// For 5xx, ALWAYS returns a generic string to avoid leaking internal
// details (DB errors, stack traces, config keys) to clients — even when
// the HTTPError has an explicit Message set. Handler authors should use
// the cause (Err field) for observability; the Message on 5xx is ignored.
func clientMessage(err error, status int) string {
	// Never expose raw internal errors to the client on 5xx.
	if status >= 500 {
		return http.StatusText(status)
	}
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

// responseBody is the unified JSON shape for all error responses.
// Uses struct tags with omitempty — absent fields simply don't appear.
type responseBody struct {
	Error     string         `json:"error"`
	Code      string         `json:"code,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable *bool          `json:"retryable,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// buildResponseBody constructs the unified JSON response shape.
// Every HTTPError gets the same treatment — omitempty fields ensure absent
// fields don't appear in the wire format.
func buildResponseBody(c *gin.Context, err error, status int) any {
	resp := responseBody{
		Error: clientMessage(err, status),
	}

	// Extract rich fields from HTTPError if present.
	var he *obsotel.HTTPError
	if errors.As(err, &he) {
		resp.Code = he.Code
		if len(he.Details) > 0 {
			resp.Details = he.Details
		}
		resp.Retryable = he.Retryable
	}

	// Resolve request_id for ALL errors: context first, then headers.
	if reqID := obsotel.RequestIDFromContext(c.Request.Context()); reqID != "" {
		resp.RequestID = reqID
	} else if id := c.GetHeader("X-Request-Id"); id != "" {
		resp.RequestID = id
	} else if id := c.GetHeader("X-Request-ID"); id != "" {
		resp.RequestID = id
	}

	return resp
}
