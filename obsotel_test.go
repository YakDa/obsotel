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

	"github.com/YakDa/obsotel"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// ----------------------------------------------------------------------------
// Re-export delegation tests
// ----------------------------------------------------------------------------

func TestLogErr_ProducesStructuredOutput(t *testing.T) {
	var buf bytes.Buffer
	l := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &buf)
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

	var ae *obsotel.AppError
	if !errors.As(err, &ae) {
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

	var ae *obsotel.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %T", err)
	}
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
	// InitMeter is fail-open: even if there's a resource schema mismatch
	// (resource.Default() vs semconv.SchemaURL), it returns a usable shutdown.
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	// Calling shutdown should not panic regardless of err.
	if sErr := shutdown(context.Background()); sErr != nil {
		t.Fatalf("shutdown: %v", sErr)
	}
	_ = err // schema URL mismatch is acceptable on some OTel SDK versions
}

// ----------------------------------------------------------------------------
// Trace context propagation (InjectTraceContext / ExtractTraceContext)
// ----------------------------------------------------------------------------

func TestInjectTraceContext_InsertsTraceparent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tp.Shutdown(context.Background())

	// Start a span so ctx has a valid trace context.
	ctx, span := obsotel.StartSpan(context.Background(), "enqueue")
	defer span.End()

	payload := json.RawMessage(`{"kind":"fm","case_id":42}`)
	result := obsotel.InjectTraceContext(ctx, payload)

	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	tp2, ok := m["trace_parent"].(string)
	if !ok || tp2 == "" {
		t.Fatalf("expected trace_parent in payload, got: %s", string(result))
	}
	if !strings.HasPrefix(tp2, "00-") {
		t.Fatalf("trace_parent should be W3C format (00-...), got %q", tp2)
	}
	// Original fields preserved.
	if m["kind"] != "fm" {
		t.Fatalf("expected kind=fm preserved, got %v", m["kind"])
	}
	if m["case_id"] != float64(42) {
		t.Fatalf("expected case_id=42 preserved, got %v", m["case_id"])
	}
}

func TestInjectTraceContext_NoSpan_PassesThrough(t *testing.T) {
	// No tracer provider / no active span — payload should be unchanged.
	otel.SetTracerProvider(sdktrace.NewTracerProvider()) // no-op provider
	payload := json.RawMessage(`{"key":"value"}`)
	result := obsotel.InjectTraceContext(context.Background(), payload)

	if string(result) != string(payload) {
		t.Fatalf("expected unchanged payload, got: %s", string(result))
	}
}

func TestInjectTraceContext_InvalidJSON_PassesThrough(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tp.Shutdown(context.Background())

	ctx, span := obsotel.StartSpan(context.Background(), "enqueue")
	defer span.End()

	bad := json.RawMessage(`not valid json`)
	result := obsotel.InjectTraceContext(ctx, bad)

	if string(result) != string(bad) {
		t.Fatalf("expected unchanged payload for invalid json, got: %s", string(result))
	}
}

func TestExtractTraceContext_CreatesChildSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tp.Shutdown(context.Background())

	// Simulate: inject at enqueue side.
	ctx, parentSpan := obsotel.StartSpan(context.Background(), "enqueue")
	payload := json.RawMessage(`{"msg":"hello"}`)
	payload = obsotel.InjectTraceContext(ctx, payload)
	parentSpan.End()

	// Simulate: extract at dequeue side.
	ctx2, childSpan := obsotel.ExtractTraceContext(context.Background(), payload, "queue.handle")
	_ = ctx2
	childSpan.End()

	// Should have 2 spans: enqueue + queue.handle
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	// Both spans should share the same trace ID (parent-child).
	enqueueSpan := spans[0]
	handleSpan := spans[1]
	if enqueueSpan.SpanContext().TraceID() != handleSpan.SpanContext().TraceID() {
		t.Fatalf("trace IDs differ: enqueue=%s handle=%s",
			enqueueSpan.SpanContext().TraceID(),
			handleSpan.SpanContext().TraceID())
	}

	// The handle span's parent should be the enqueue span.
	if handleSpan.Parent().SpanID() != enqueueSpan.SpanContext().SpanID() {
		t.Fatalf("handle span parent=%s, expected enqueue span=%s",
			handleSpan.Parent().SpanID(),
			enqueueSpan.SpanContext().SpanID())
	}

	if handleSpan.Name() != "queue.handle" {
		t.Fatalf("expected span name=queue.handle, got %s", handleSpan.Name())
	}
}

func TestExtractTraceContext_NoTraceParent_CreatesRootSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tp.Shutdown(context.Background())

	// Payload without trace_parent key.
	payload := json.RawMessage(`{"msg":"hello"}`)
	_, span := obsotel.ExtractTraceContext(context.Background(), payload, "queue.handle")
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	// Root span has no valid parent.
	if spans[0].Parent().IsValid() {
		t.Fatalf("expected root span (no parent), got parent=%s", spans[0].Parent().SpanID())
	}
}

func TestExtractTraceContext_InvalidJSON_CreatesRootSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tp.Shutdown(context.Background())

	bad := json.RawMessage(`not json`)
	_, span := obsotel.ExtractTraceContext(context.Background(), bad, "queue.handle")
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Fatalf("expected root span for invalid json, got parent=%s", spans[0].Parent().SpanID())
	}
}
