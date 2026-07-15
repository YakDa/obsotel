package obsgin_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/YakDa/obsotel"
	obsgin "github.com/YakDa/obsotel/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// ----------------------------------------------------------------------------
// WrapHandler — nil return (success)
// ----------------------------------------------------------------------------

func TestWrapHandler_NilError_DoesNothing(t *testing.T) {
	r := gin.New()
	r.GET("/ok", obsgin.WrapHandler(func(c *gin.Context) error {
		c.JSON(http.StatusOK, gin.H{"msg": "hello"})
		return nil
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ok", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", body["msg"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — HTTPError
// ----------------------------------------------------------------------------

func TestWrapHandler_HTTPError_WritesStatusAndMessage(t *testing.T) {
	r := gin.New()
	r.POST("/fail", obsgin.WrapHandler(func(c *gin.Context) error {
		return obsgin.HTTPErr(http.StatusBadGateway, "upstream timeout", errors.New("conn reset"))
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/fail", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "Bad Gateway" {
		t.Fatalf("expected error='Bad Gateway' (5xx sanitized), got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — AppError kind mapping
// ----------------------------------------------------------------------------

func TestWrapHandler_AppError_MapsKindToStatus(t *testing.T) {
	tests := []struct {
		kind       string
		wantStatus int
	}{
		{"not_found", http.StatusNotFound},
		{"forbidden", http.StatusForbidden},
		{"unauthorized", http.StatusUnauthorized},
		{"bad_request", http.StatusBadRequest},
		{"conflict", http.StatusConflict},
		{"rate_limited", http.StatusTooManyRequests},
		{"bad_gateway", http.StatusBadGateway},
		{"unavailable", http.StatusServiceUnavailable},
		{"internal", http.StatusInternalServerError},
		{"unknown_kind", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			r := gin.New()
			r.GET("/x", obsgin.WrapHandler(func(c *gin.Context) error {
				return obsotel.NewErr("test_op", tt.kind, errors.New("cause"))
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/x", nil)
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("kind=%q: expected %d, got %d", tt.kind, tt.wantStatus, w.Code)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — plain error defaults to 500
// ----------------------------------------------------------------------------

func TestWrapHandler_PlainError_Defaults500(t *testing.T) {
	r := gin.New()
	r.GET("/plain", obsgin.WrapHandler(func(c *gin.Context) error {
		return errors.New("something broke")
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plain", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// 5xx should hide internal details
	if body["error"] != "Internal Server Error" {
		t.Fatalf("expected generic 5xx message, got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — 4xx AppError exposes error message
// ----------------------------------------------------------------------------

func TestWrapHandler_4xxAppError_ExposesMessage(t *testing.T) {
	r := gin.New()
	r.GET("/notfound", obsgin.WrapHandler(func(c *gin.Context) error {
		return obsotel.NewErr("load_user", "not_found", nil)
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/notfound", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// For 4xx, the error message should come through
	errMsg, ok := body["error"].(string)
	if !ok || errMsg == "" {
		t.Fatalf("expected non-empty error message, got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — does not double-write if handler already wrote
// ----------------------------------------------------------------------------

func TestWrapHandler_AlreadyWritten_DoesNotDoubleWrite(t *testing.T) {
	r := gin.New()
	r.GET("/wrote", obsgin.WrapHandler(func(c *gin.Context) error {
		c.JSON(http.StatusConflict, gin.H{"error": "custom conflict"})
		return errors.New("also returning error")
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/wrote", nil)
	r.ServeHTTP(w, req)

	// Should keep the status the handler set
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "custom conflict" {
		t.Fatalf("expected custom conflict message, got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// HTTPErr — constructs an HTTPError properly
// ----------------------------------------------------------------------------

func TestHTTPErr_WrapsCorrectly(t *testing.T) {
	cause := errors.New("timeout")
	err := obsgin.HTTPErr(http.StatusGatewayTimeout, "gateway timed out", cause)

	var he *obsotel.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected *obsotel.HTTPError, got %T", err)
	}
	if he.Status != http.StatusGatewayTimeout {
		t.Fatalf("expected status 504, got %d", he.Status)
	}
	if he.Message != "gateway timed out" {
		t.Fatalf("expected message 'gateway timed out', got %q", he.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause not preserved in error chain")
	}
}

func TestHTTPErr_NilCause(t *testing.T) {
	err := obsgin.HTTPErr(http.StatusForbidden, "access denied", nil)

	var he *obsotel.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected *obsotel.HTTPError, got %T", err)
	}
	if he.Status != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", he.Status)
	}
	if he.Err != nil {
		t.Fatalf("expected nil Err, got %v", he.Err)
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — HTTPError with empty message for 5xx
// ----------------------------------------------------------------------------

func TestWrapHandler_HTTPError_EmptyMessage5xx_UsesGeneric(t *testing.T) {
	r := gin.New()
	r.GET("/err", obsgin.WrapHandler(func(c *gin.Context) error {
		return &obsotel.HTTPError{Status: 503, Err: errors.New("db pool exhausted")}
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/err", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Should NOT expose "db pool exhausted" to the client
	if body["error"] != "Service Unavailable" {
		t.Fatalf("expected generic message, got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — HTTPError with explicit message for 4xx
// ----------------------------------------------------------------------------

func TestWrapHandler_HTTPError_ExplicitMessage4xx(t *testing.T) {
	r := gin.New()
	r.GET("/bad", obsgin.WrapHandler(func(c *gin.Context) error {
		return &obsotel.HTTPError{Status: 400, Message: "missing field: name", Err: errors.New("validation")}
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bad", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "missing field: name" {
		t.Fatalf("expected 'missing field: name', got %v", body["error"])
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — wrapped AppError uses outermost AppError's kind
// ----------------------------------------------------------------------------

func TestWrapHandler_WrappedAppError_UsesOutermostKind(t *testing.T) {
	r := gin.New()
	r.GET("/wrapped", obsgin.WrapHandler(func(c *gin.Context) error {
		// Wrap creates a new AppError with kind="internal" wrapping the inner one.
		// statusFromError uses errors.As which finds the outermost match.
		base := obsotel.NewErr("find_ticket", "not_found", nil)
		return obsotel.Wrap(c.Request.Context(), base, "handle_sync")
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/wrapped", nil)
	r.ServeHTTP(w, req)

	// Wrap produces kind="internal" on the outer AppError
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from outer wrap, got %d", w.Code)
	}
}

// WrapHandler — direct AppError (not double-wrapped) maps correctly
func TestWrapHandler_DirectAppError_MapsKind(t *testing.T) {
	r := gin.New()
	r.GET("/direct", obsgin.WrapHandler(func(c *gin.Context) error {
		return obsotel.NewErr("find_ticket", "not_found", errors.New("no rows"))
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/direct", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 from direct AppError, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — JSON content type
// ----------------------------------------------------------------------------

func TestWrapHandler_Error_SetsJSONContentType(t *testing.T) {
	r := gin.New()
	r.GET("/ct", obsgin.WrapHandler(func(c *gin.Context) error {
		return errors.New("fail")
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ct", nil)
	r.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Fatal("expected Content-Type header to be set")
	}
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
}

// ----------------------------------------------------------------------------
// WrapHandler — HTTPError with empty message for 4xx exposes err.Error()
// ----------------------------------------------------------------------------

func TestWrapHandler_HTTPError_EmptyMessage4xx_ExposesErrText(t *testing.T) {
	r := gin.New()
	r.GET("/empty4xx", obsgin.WrapHandler(func(c *gin.Context) error {
		return &obsotel.HTTPError{Status: 404, Err: errors.New("row not found")}
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/empty4xx", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// With empty Message and status < 500, clientMessage falls through to err.Error()
	if body["error"] != "row not found" {
		t.Fatalf("expected 'row not found', got %v", body["error"])
	}
}
