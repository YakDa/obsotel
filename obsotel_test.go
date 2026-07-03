package obsotel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mingdos/obsotel"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// ----------------------------------------------------------------------------
// Re-export delegation tests
// ----------------------------------------------------------------------------

func TestLogErr_ProducesStructuredOutput(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := obsotel.WithLogger(obsotel.WithRequestID(context.Background(), "req-xyz"), l)

	root := errors.New("root cause")
	mid := fmt.Errorf("mid: %w", root)
	top := obsotel.Wrap(ctx, mid, "load_user")

	obsotel.LogErr(ctx, "load_failed", top)

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if out["level"] != "ERROR" {
		t.Fatalf("expected level=ERROR, got %v", out["level"])
	}
	if out["msg"] != "load_failed" {
		t.Fatalf("expected msg=load_failed, got %v", out["msg"])
	}
	if out["request_id"] != "req-xyz" {
		t.Fatalf("expected request_id=req-xyz, got %v", out["request_id"])
	}
	chain, ok := out["error_chain"].([]any)
	if !ok || len(chain) < 2 {
		t.Fatalf("expected error_chain array, got: %#v", out["error_chain"])
	}
}

func TestWrap_AttachesRequestID(t *testing.T) {
	ctx := obsotel.WithRequestID(context.Background(), "rid-1")
	err := obsotel.Wrap(ctx, errors.New("boom"), "do_stuff")

	ae, ok := err.(*obsotel.AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T", err)
	}
	if ae.Op != "do_stuff" {
		t.Fatalf("expected op=do_stuff, got %s", ae.Op)
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		t.Fatal("unwrap chain broken")
	}
}

func TestChainOf_WalksWrapped(t *testing.T) {
	root := errors.New("root")
	mid := fmt.Errorf("mid: %w", root)
	top := fmt.Errorf("top: %w", mid)
	chain := obsotel.ChainOf(top)
	if len(chain) != 3 {
		t.Fatalf("expected 3, got %d", len(chain))
	}
	if chain[len(chain)-1].Error() != "root" {
		t.Fatalf("expected root at end, got %v", chain[len(chain)-1])
	}
}

func TestWrapWith_AddsMetadata(t *testing.T) {
	ctx := obsotel.WithRequestID(context.Background(), "rid-2")
	err := obsotel.WrapWith(ctx, errors.New("boom"), "load_user",
		"user_id", "u42", "tenant", "t1")

	ae := err.(*obsotel.AppError)
	if ae.Meta["user_id"] != "u42" || ae.Meta["tenant"] != "t1" {
		t.Fatalf("metadata missing: %#v", ae.Meta)
	}
}

// ----------------------------------------------------------------------------
// HTTP handler
// ----------------------------------------------------------------------------

func TestHandler_WrapsMux(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/foo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := obsotel.Handler(log, mux, "test-service")

	req := httptest.NewRequest("GET", "/foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHandlerWithFilter_RespectsPredicate(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/foo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := obsotel.HandlerWithFilter(log, mux, "test-service",
		func(r *http.Request) bool { return r.URL.Path != "/healthz" })

	for _, path := range []string{"/healthz", "/foo"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("expected 200 for %s, got %d", path, rr.Code)
		}
	}
}

func TestHandlerWithFilter_SkipsOtelWhenFiltered(t *testing.T) {
	// With a span recorder attached, requests that pass the predicate should
	// create a server span; requests that fail it should not.
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/foo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := obsotel.HandlerWithFilter(log, mux, "test-service",
		func(r *http.Request) bool { return r.URL.Path != "/healthz" })

	// /healthz — filtered out, no span expected.
	req := httptest.NewRequest("GET", "/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := len(recorder.Started()); got != 0 {
		t.Fatalf("expected 0 spans for filtered /healthz, got %d", got)
	}

	// /foo — traced, exactly one span expected.
	req2 := httptest.NewRequest("GET", "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if got := len(recorder.Started()); got != 1 {
		t.Fatalf("expected 1 span for /foo, got %d", got)
	}
}

// ----------------------------------------------------------------------------
// HTTP client
// ----------------------------------------------------------------------------

func TestNewClient_ReturnsInstrumentedClient(t *testing.T) {
	c := obsotel.NewClient()
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.Timeout == 0 {
		t.Fatal("expected non-zero default timeout")
	}
}

func TestDoRequest_PropagatesRequestID(t *testing.T) {
	var seenReqID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := obsotel.WithRequestID(context.Background(), "rid-ot-1")

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := obsotel.DoRequest(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if seenReqID != "rid-ot-1" {
		t.Fatalf("expected X-Request-ID=rid-ot-1, got %q", seenReqID)
	}
}

func TestHTTPError_ErrorString(t *testing.T) {
	e := &obsotel.HTTPError{Status: 503}
	got := e.Error()
	if !strings.Contains(got, "503") {
		t.Fatalf("expected 503 in error, got %q", got)
	}
}

// ----------------------------------------------------------------------------
// Metrics
// ----------------------------------------------------------------------------

func TestNewCounter(t *testing.T) {
	c, err := obsotel.NewCounter("test_counter_total", "test counter")
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil counter")
	}
	// Should not panic even without a meter provider
	c.Inc(context.Background())
}

func TestNewHistogram(t *testing.T) {
	h, err := obsotel.NewHistogram("test_hist", "test histogram", "s")
	if err != nil {
		t.Fatalf("NewHistogram: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil histogram")
	}
	// Should not panic even without a meter provider
	h.Record(context.Background(), 1.5)
}

func TestPrebuiltMetrics(t *testing.T) {
	obsotel.HTTPRequestsTotal.Inc(context.Background())
	obsotel.HTTPRequestDuration.Record(context.Background(), 0.123)
}

// ----------------------------------------------------------------------------
// Trace injection: trace_id/span_id appear in log records when span is active
// ----------------------------------------------------------------------------

func TestLog_IncludesTraceIDWhenSpanActive(t *testing.T) {
	// Set up an in-memory span recorder to drive the OTel SDK without a real exporter.
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)

	var buf bytes.Buffer
	log := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &buf)

	ctx := context.Background()
	ctx, span := tp.Tracer("test").Start(ctx, "test-span")
	defer span.End()

	log.InfoContext(ctx, "hello", "user_id", "u1")

	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if out[obsotel.TraceIDKey] == "" || out[obsotel.TraceIDKey] == nil {
		t.Fatalf("expected trace_id in log, got: %s", buf.String())
	}
	if out[obsotel.SpanIDKey] == "" || out[obsotel.SpanIDKey] == nil {
		t.Fatalf("expected span_id in log, got: %s", buf.String())
	}
	if out["user_id"] != "u1" {
		t.Fatalf("expected user_id=u1, got %v", out["user_id"])
	}
}

func TestLog_NoTraceIDWhenNoSpan(t *testing.T) {
	var buf bytes.Buffer
	l := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &buf)

	l.InfoContext(context.Background(), "hello")

	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if _, has := out[obsotel.TraceIDKey]; has {
		t.Fatalf("expected NO trace_id without active span, got: %s", buf.String())
	}
}

// ----------------------------------------------------------------------------
// request_id auto-injection (requestIDHandler)
// ----------------------------------------------------------------------------

func TestLog_InjectsRequestIDFromCtx(t *testing.T) {
	var buf bytes.Buffer
	l := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &buf)

	ctx := obsotel.WithRequestID(context.Background(), "rid-auto-1")
	l.InfoContext(ctx, "hello")

	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if out[obsotel.RequestIDKey] != "rid-auto-1" {
		t.Fatalf("expected request_id=rid-auto-1, got %v (full: %s)", out[obsotel.RequestIDKey], buf.String())
	}
}

func TestLog_NoRequestIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	l := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &buf)

	l.InfoContext(context.Background(), "hello")

	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if _, has := out[obsotel.RequestIDKey]; has {
		t.Fatalf("expected NO request_id without WithRequestID, got: %s", buf.String())
	}
}

// ----------------------------------------------------------------------------
// StartSpan helper
// ----------------------------------------------------------------------------

func TestStartSpan_ReturnsUsableSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	_, span := obsotel.StartSpan(context.Background(), "test-op")
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	span.End()

	if got := len(recorder.Started()); got != 1 {
		t.Fatalf("expected 1 span, got %d", got)
	}
	if name := recorder.Started()[0].Name(); name != "test-op" {
		t.Fatalf("expected span name=test-op, got %s", name)
	}
}

// ----------------------------------------------------------------------------
// InitMeter helper
// ----------------------------------------------------------------------------

func TestInitMeter_ReturnsShutdown(t *testing.T) {
	shutdown, err := obsotel.InitMeter(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("InitMeter: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	// Calling shutdown should not panic.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

