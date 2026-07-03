package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// captureLogger returns a *slog.Logger that writes JSON to buf, plus the buf.
func captureLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	return slog.New(h), buf
}

// decodeJSON parses the last line written to buf as a single JSON object.
func decodeJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := buf.String()
	if line == "" {
		t.Fatal("no log output")
	}
	// last non-empty line
	lines := strings.Split(strings.TrimRight(line, "\n"), "\n")
	last := lines[len(lines)-1]
	var out map[string]any
	if err := json.Unmarshal([]byte(last), &out); err != nil {
		t.Fatalf("invalid json: %v\nline: %s", err, last)
	}
	return out
}

// ----------------------------------------------------------------------------
// request_id
// ----------------------------------------------------------------------------

func TestNewRequestID_Format(t *testing.T) {
	id := NewRequestID()
	if len(id) != 32 {
		t.Fatalf("expected 32-char hex, got %d (%q)", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char in id: %q", id)
		}
	}
}

func TestNewRequestID_Unique(t *testing.T) {
	const n = 10_000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := NewRequestID()
		if seen[id] {
			t.Fatalf("collision after %d: %s", i, id)
		}
		seen[id] = true
	}
}

// ----------------------------------------------------------------------------
// context-bound logger
// ----------------------------------------------------------------------------

func TestL_ReturnsDefault_WhenCtxMissing(t *testing.T) {
	got := L(context.Background())
	if got != slog.Default() {
		t.Fatal("expected slog.Default() when ctx has no logger")
	}
}

func TestL_ReturnsDefault_WhenCtxNil(t *testing.T) {
	got := L(nil) //nolint:staticcheck // intentional nil ctx
	if got != slog.Default() {
		t.Fatal("expected slog.Default() for nil ctx")
	}
}

func TestWithLogger_RoundTrip(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := WithLogger(context.Background(), l)
	if L(ctx) != l {
		t.Fatal("L(ctx) did not return the stored logger")
	}
}

func TestWithLogger_NilLogger_NoOp(t *testing.T) {
	ctx := context.Background()
	out := WithLogger(ctx, nil)
	if L(out) != slog.Default() {
		t.Fatal("expected default logger after WithLogger(ctx, nil)")
	}
}

func TestWith_AttachesFields(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	ctx, _ = With(ctx, slog.String(RequestIDKey, "abc123"))

	L(ctx).Info("hello", "k", "v")
	got := decodeJSON(t, buf)
	if got[RequestIDKey] != "abc123" {
		t.Fatalf("expected request_id=abc123 in log, got %#v", got)
	}
}

func TestRequestIDFromContext_RoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-xyz")
	if got := RequestIDFromContext(ctx); got != "req-xyz" {
		t.Fatalf("expected req-xyz, got %q", got)
	}
}

func TestRequestIDFromContext_EmptyForMissing(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// ----------------------------------------------------------------------------
// ErrorChain
// ----------------------------------------------------------------------------

func TestChainOf_WalksWrapped(t *testing.T) {
	root := errors.New("root cause")
	mid := fmt.Errorf("mid layer: %w", root)
	top := fmt.Errorf("top layer: %w", mid)

	chain := ChainOf(top)
	if len(chain) != 3 {
		t.Fatalf("expected chain length 3, got %d: %#v", len(chain), chain)
	}
	if chain[0] != top {
		t.Fatalf("chain[0] should be top, got %v", chain[0])
	}
	if chain[len(chain)-1] != root {
		t.Fatalf("last element should be root, got %v", chain[len(chain)-1])
	}
}

func TestChainOf_NilReturnsNil(t *testing.T) {
	if got := ChainOf(nil); got != nil {
		t.Fatalf("expected nil for nil err, got %#v", got)
	}
}

func TestChainOf_BreaksOnCycle(t *testing.T) {
	a := &cycleErr{msg: "a"}
	b := &cycleErr{msg: "b"}
	a.next = b
	b.next = a
	chain := ChainOf(a)
	if len(chain) == 0 || len(chain) > 100 {
		t.Fatalf("cycle protection failed; chain len=%d", len(chain))
	}
}

type cycleErr struct {
	msg  string
	next error
}

func (c *cycleErr) Error() string { return c.msg }
func (c *cycleErr) Unwrap() error { return c.next }

// ----------------------------------------------------------------------------
// AppError
// ----------------------------------------------------------------------------

func TestAppError_Formats(t *testing.T) {
	e := New("load_user", "not_found", errors.New("sql: no rows"))
	got := e.Error()
	if !strings.Contains(got, "load_user") || !strings.Contains(got, "not_found") || !strings.Contains(got, "sql: no rows") {
		t.Fatalf("error string missing parts: %q", got)
	}
}

func TestAppError_UnwrapPropagates(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := New("op", "kind", sentinel)
	if !errors.Is(e, sentinel) {
		t.Fatal("errors.Is should find sentinel via Unwrap")
	}
}

func TestAppError_WithMetaCopies(t *testing.T) {
	e := New("op", "kind", nil).WithMeta("user_id", "u1")
	cp := e.WithMeta("tenant", "t1")
	if e.Meta["tenant"] != nil {
		t.Fatal("WithMeta should copy, not mutate original")
	}
	if cp.Meta["user_id"] != "u1" || cp.Meta["tenant"] != "t1" {
		t.Fatalf("copy missing fields: %#v", cp.Meta)
	}
}

// ----------------------------------------------------------------------------
// Wrap / WrapWith
// ----------------------------------------------------------------------------

func TestWrap_NilReturnsNil(t *testing.T) {
	if got := Wrap(context.Background(), nil, "op"); got != nil {
		t.Fatalf("expected nil for nil err, got %v", got)
	}
}

func TestWrap_AttachesRequestID(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-abc")
	err := Wrap(ctx, errors.New("boom"), "load_user")

	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T", err)
	}
	if ae.Op != "load_user" {
		t.Fatalf("expected op=load_user, got %s", ae.Op)
	}
	if ae.Meta[RequestIDKey] != "req-abc" {
		t.Fatalf("expected request_id=req-abc in meta, got %#v", ae.Meta)
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		t.Fatal("unwrap chain broken")
	}
}

func TestWrap_NoRequestIDInContext(t *testing.T) {
	err := Wrap(context.Background(), errors.New("boom"), "load_user")
	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T", err)
	}
	if _, ok := ae.Meta[RequestIDKey]; ok {
		t.Fatal("expected no request_id in meta when ctx has none")
	}
}

func TestWrapWith_AddsMetadata(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-1")
	err := WrapWith(ctx, errors.New("boom"), "load_user", "user_id", "u42", "tenant", "t1")

	ae, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T", err)
	}
	if ae.Meta["user_id"] != "u42" || ae.Meta["tenant"] != "t1" {
		t.Fatalf("metadata missing: %#v", ae.Meta)
	}
}

// ----------------------------------------------------------------------------
// LogErr
// ----------------------------------------------------------------------------

func TestLogErr_NoOpForNil(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	LogErr(ctx, "msg", nil)
	if buf.Len() != 0 {
		t.Fatalf("expected no output for nil err, got: %s", buf.String())
	}
}

func TestLogErr_LogsErrorWithChain(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	root := errors.New("root cause")
	mid := fmt.Errorf("mid: %w", root)
	top := WrapWith(ctx, mid, "op1", "user_id", "u1")

	LogErr(ctx, "load_failed", top, "extra", "x")

	got := decodeJSON(t, buf)
	if got["level"] != "ERROR" {
		t.Fatalf("expected level=ERROR, got %v", got["level"])
	}
	if got["msg"] != "load_failed" {
		t.Fatalf("expected msg=load_failed, got %v", got["msg"])
	}
	if got["extra"] != "x" {
		t.Fatalf("expected extra=x, got %v", got["extra"])
	}
	if got["user_id"] != "u1" {
		t.Fatalf("expected user_id from WrapWith meta, got %v", got["user_id"])
	}

	chain, ok := got["error_chain"].([]any)
	if !ok || len(chain) < 2 {
		t.Fatalf("expected error_chain array, got: %#v", got["error_chain"])
	}
	if !strings.Contains(chain[len(chain)-1].(string), "root cause") {
		t.Fatalf("expected chain root to mention 'root cause', got: %#v", chain)
	}
}

// TestLogErr_AppError_SurfacesOpKindAtTopLevel verifies that *AppError's
// Op and Kind fields are surfaced as top-level structured fields when
// LogErr is called with an *AppError, not nested inside the err object's
// LogValue() rendering. This is what makes them queryable as first-class
// columns in Loki/ELK (e.g. `op="load_user"`).
func TestLogErr_AppError_SurfacesOpKindAtTopLevel(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	root := errors.New("connection refused")
	mid := fmt.Errorf("db query: %w", root)
	appErr := New("load_user", "infra_error", mid).WithMeta("user_id", "u42")

	LogErr(ctx, "load_failed", appErr)

	got := decodeJSON(t, buf)
	if got["op"] != "load_user" {
		t.Fatalf("expected top-level op=load_user, got %v (full: %#v)", got["op"], got)
	}
	if got["kind"] != "infra_error" {
		t.Fatalf("expected top-level kind=infra_error, got %v (full: %#v)", got["kind"], got)
	}
	if got["user_id"] != "u42" {
		t.Fatalf("expected top-level user_id=u42, got %v (full: %#v)", got["user_id"], got)
	}
	// op/kind must NOT appear inside the err object's nested rendering.
	// With the fix, they are top-level fields with their own keys.
	if errObj, ok := got["err"].(map[string]any); ok {
		if _, has := errObj["op"]; has {
			t.Fatalf("op should be top-level, not nested inside err: %#v", errObj)
		}
		if _, has := errObj["kind"]; has {
			t.Fatalf("kind should be top-level, not nested inside err: %#v", errObj)
		}
	}
}

// TestLogErr_NonAppError_NoOpKind verifies that bare errors (not *AppError)
// do NOT emit empty op/kind fields — only *AppError gets them surfaced.
func TestLogErr_NonAppError_NoOpKind(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	plainErr := errors.New("just a plain error")

	LogErr(ctx, "boom", plainErr)

	got := decodeJSON(t, buf)
	if _, has := got["op"]; has {
		t.Fatalf("op should not appear for non-AppError err, got: %#v", got)
	}
	if _, has := got["kind"]; has {
		t.Fatalf("kind should not appear for non-AppError err, got: %#v", got)
	}
}

// ----------------------------------------------------------------------------
// LoggingMiddleware
// ----------------------------------------------------------------------------

func TestLoggingMiddleware_GeneratesRequestID(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	h := LoggingMiddleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// request_id should be readable from ctx inside the handler
		if got := RequestIDFromContext(r.Context()); got == "" {
			t.Fatal("expected request_id in handler ctx")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	logged := decodeJSON(t, buf)
	if logged[RequestIDKey] == "" {
		t.Fatal("expected request_id in log")
	}
	if logged["path"] != "/foo" {
		t.Fatalf("expected path=/foo, got %v", logged["path"])
	}
	if logged["status"] != float64(200) {
		t.Fatalf("expected status=200, got %v", logged["status"])
	}
}

func TestLoggingMiddleware_ReusesIncomingRequestID(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	h := LoggingMiddleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/foo", nil)
	req.Header.Set("X-Request-ID", "incoming-123")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got != "incoming-123" {
		t.Fatalf("expected X-Request-ID echoed, got %q", got)
	}
	logged := decodeJSON(t, buf)
	if logged[RequestIDKey] != "incoming-123" {
		t.Fatalf("expected request_id=incoming-123 in log, got %v", logged[RequestIDKey])
	}
}

func TestLoggingMiddleware_LogsNonZeroStatus(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	h := LoggingMiddleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	req := httptest.NewRequest("GET", "/foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	logged := decodeJSON(t, buf)
	if logged["status"] != float64(500) {
		t.Fatalf("expected status=500, got %v", logged["status"])
	}
}

// ----------------------------------------------------------------------------
// DoRequest
// ----------------------------------------------------------------------------

func TestDoRequest_PropagatesRequestID(t *testing.T) {
	var seenReqID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(WithRequestID(context.Background(), "rid-9"), l)

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := DoRequest(ctx, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if seenReqID != "rid-9" {
		t.Fatalf("expected X-Request-ID=rid-9 seen by server, got %q", seenReqID)
	}
	logged := decodeJSON(t, buf)
	if logged["outbound_host"] == "" {
		t.Fatalf("expected outbound_host in log, got: %s", buf.String())
	}
}

func TestDoRequest_LogsErrorOnFailure(t *testing.T) {
	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:1/never", nil)
	resp, err := DoRequest(ctx, nil, req)
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
	if resp != nil {
		t.Fatal("expected nil response on transport error")
	}
	logged := decodeJSON(t, buf)
	if logged["level"] != "ERROR" {
		t.Fatalf("expected level=ERROR, got %v", logged["level"])
	}
	if logged["msg"] != "outbound_failed" {
		t.Fatalf("expected msg=outbound_failed, got %v", logged["msg"])
	}
}

// ----------------------------------------------------------------------------
// DoRequestWithRetry
// ----------------------------------------------------------------------------

func TestDoRequestWithRetry_RetriesUntilSuccess(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "fail", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	l, buf := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := DoRequestWithRetry(ctx, nil, req, 5, 1) // 1ms backoff
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	defer resp.Body.Close()
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	if !strings.Contains(buf.String(), "outbound_retry_succeeded") {
		t.Fatalf("expected outbound_retry_succeeded log, got: %s", buf.String())
	}
}

func TestDoRequestWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	defer srv.Close()

	l, _ := captureLogger(slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := DoRequestWithRetry(ctx, nil, req, 2, 1)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if resp != nil {
		t.Fatal("expected nil response on final failure")
	}
}

// ----------------------------------------------------------------------------
// Sampler
// ----------------------------------------------------------------------------

func TestSampler_DeterministicInterval(t *testing.T) {
	// rate = 100 → 1 in 10,000
	s := NewSampler(100)
	const calls = 100_000
	hits := 0
	for i := 0; i < calls; i++ {
		if s.Allow() {
			hits++
		}
	}
	if hits < 5 || hits > 20 {
		t.Fatalf("expected ~10 hits in 100,000 calls, got %d", hits)
	}
}

func TestSampler_ZeroRateNeverAllows(t *testing.T) {
	s := NewSampler(0)
	for i := 0; i < 1000; i++ {
		if s.Allow() {
			t.Fatal("rate=0 should never allow")
		}
	}
}

func TestSampler_FullRateAlwaysAllows(t *testing.T) {
	s := NewSampler(1_000_000)
	for i := 0; i < 1000; i++ {
		if !s.Allow() {
			t.Fatal("rate=1_000_000 should always allow")
		}
	}
}

func TestRandomSampler_RoughlyRespectsRate(t *testing.T) {
	s := NewRandomSampler(100_000) // 10%
	hits := 0
	const calls = 100_000
	for i := 0; i < calls; i++ {
		if s.Allow() {
			hits++
		}
	}
	if hits < 8_000 || hits > 12_000 {
		t.Fatalf("expected ~10,000 hits (10%%), got %d", hits)
	}
}

// ----------------------------------------------------------------------------
// concurrent safety
// ----------------------------------------------------------------------------

func TestConcurrent_WrapAndLogErr(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := WithLogger(context.Background(), l)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := Wrap(ctx, errors.New("boom"), "op")
				LogErr(ctx, "msg", err)
			}
		}()
	}
	wg.Wait()
}
