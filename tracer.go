package obsotel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracerInitGuard protects against double InitTracer. Calling InitTracer
// twice silently overwrites the previous global TracerProvider, leaking
// its batcher goroutines and any unflushed spans.
//
// On second call, we log a WARN, shut down the previous provider to flush
// any pending spans, then proceed with the new init. This is safer than
// either silently no-op'ing (caller never knows the second call was
// dropped) or panicking (breaks test setup that legitimately calls init
// twice).
var (
	tracerInitMu       sync.Mutex
	previousTracerStop func(context.Context) error
)

// InitTracer sets up the global OTel TracerProvider and W3C TraceContext
// propagator. Returns a shutdown function that should be called at process
// exit to flush pending spans.
//
// Fail-open: if the exporter fails to set up, business code is not affected.
// InitTracer returns the error AND a no-op shutdown so callers can
// `defer shutdown(ctx)` unconditionally without crashing startup.
//
// Without InitTracer, OTel uses a no-op tracer — no spans are created, but
// no errors are raised. Business code runs unchanged.
func InitTracer(ctx context.Context, serviceName string, opts ...TracerOption) (shutdown func(context.Context) error, err error) {
	cfg := defaultTracerConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	// Double-init guard: shut down any previously installed provider so
	// we don't leak its batcher. See tracerInitGuard comment.
	tracerInitMu.Lock()
	if previousTracerStop != nil {
		slog.Warn("obsotel: InitTracer called twice; shutting down previous provider",
			slog.String("service", serviceName))
		if sErr := previousTracerStop(ctx); sErr != nil {
			slog.Warn("obsotel: previous tracer shutdown error",
				slog.String("err", sErr.Error()))
		}
		previousTracerStop = nil
	}
	tracerInitMu.Unlock()

	exp, err := cfg.exporterFactory(ctx)
	if err != nil {
		return func(context.Context) error { return nil },
			fmt.Errorf("obsotel: exporter setup failed, running with no-op tracer: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return func(context.Context) error { return nil },
			fmt.Errorf("obsotel: resource setup failed: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.samplingRatio)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tracerInitMu.Lock()
	previousTracerStop = tp.Shutdown
	tracerInitMu.Unlock()

	return tp.Shutdown, nil
}

// Tracer returns the named tracer from the global TracerProvider.
// Returns a no-op tracer if InitTracer was not called or failed.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// StartSpan is a convenience helper that calls Tracer("obsotel").Start.
// Use it for custom spans within business logic so callers don't have to
// pick a tracer name on every call:
//
//	ctx, span := obsotel.StartSpan(ctx, "load_user")
//	defer span.End()
//
// Returns a no-op span if InitTracer was not called or failed.
// Pattern: pair every StartSpan with defer span.End().
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return Tracer("obsotel").Start(ctx, name)
}

// ----------------------------------------------------------------------------
// Tracer options
// ----------------------------------------------------------------------------

// tracerConfig holds internal configuration for InitTracer.
type tracerConfig struct {
	exporterFactory func(context.Context) (sdktrace.SpanExporter, error)
	samplingRatio   float64
}

func defaultTracerConfig() tracerConfig {
	return tracerConfig{
		exporterFactory: defaultExporter,
		samplingRatio:   1.0, // 100% by default; tune down for high-traffic services
	}
}

// TracerOption customizes InitTracer.
type TracerOption func(*tracerConfig)

// WithSamplingRatio sets the trace sampling ratio (0.0 to 1.0).
// 0.0 = sample nothing, 1.0 = sample everything. Default is 1.0.
func WithSamplingRatio(ratio float64) TracerOption {
	return func(c *tracerConfig) {
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		c.samplingRatio = ratio
	}
}

// WithExporter replaces the default stdout exporter with a custom one.
// Use this to wire up an OTLP exporter to a real collector:
//
//	exp, _ := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint("otel-collector:4317"))
//	shutdown, err := obsotel.InitTracer(ctx, "user-service",
//	    obsotel.WithExporter(exp),
//	    obsotel.WithSamplingRatio(0.1),
//	)
func WithExporter(exp sdktrace.SpanExporter) TracerOption {
	return func(c *tracerConfig) {
		c.exporterFactory = func(context.Context) (sdktrace.SpanExporter, error) {
			return exp, nil
		}
	}
}

// defaultExporter creates a stdout exporter for dev. Destination is gated
// on the OBSOTEL_DUMP_SPANS env var so service stderr stays readable for
// slog by default:
//
//	OBSOTEL_DUMP_SPANS=""             → silent (spans still flow through SDK, just not dumped)
//	OBSOTEL_DUMP_SPANS="stdout"       → multi-line pretty JSON on stderr (legacy)
//	OBSOTEL_DUMP_SPANS="compact"      → single-line JSON on stderr
//	OBSOTEL_DUMP_SPANS="file:/path"   → JSONL appended to /path
//
// Production callers should use WithExporter(otlpExporter); this default
// only applies when InitTracer falls back to stdout.
func defaultExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	mode := os.Getenv("OBSOTEL_DUMP_SPANS")
	switch {
	case mode == "":
		// Default: silent. Spans still flow through the SDK; we just
		// don't serialize them anywhere.
		return silentExporter()
	case strings.HasPrefix(mode, "file:"):
		path := strings.TrimPrefix(mode, "file:")
		f, err := os.OpenFile(path,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			// OpenFile returns (nil, err) on failure — f is always nil
			// here. Don't call f.Close() on it: (*os.File).Close is
			// nil-safe in the stdlib (returns ErrInvalid), so it's
			// harmless, but it's also dead code that confuses readers.
			// Fall back to silent; logs reveal the actual cause.
			return silentExporter()
		}
		// stdouttrace.New doesn't own its writer — it only writes to it.
		// Without wrapping, the FD leaks for the process lifetime.
		inner, ierr := stdouttrace.New(stdouttrace.WithWriter(f))
		if ierr != nil {
			// f is non-nil here (we passed the err check above), so
			// closing it is safe and necessary.
			_ = f.Close()
			return silentExporter()
		}
		return &fileExporter{
			exporter: inner,
			file:     f,
		}, nil
	case mode == "stdout":
		// Legacy: pretty multi-line JSON on stderr.
		exp, _ := stdouttrace.New(stdouttrace.WithPrettyPrint())
		return exp, nil
	case mode == "compact":
		// Single-line JSON on stderr.
		exp, _ := stdouttrace.New()
		return exp, nil
	default:
		// Unknown mode → silent (not stderr, not file). Safer default
		// than spamming an unexpected destination.
		return silentExporter()
	}
}

// silentExporter returns a stdouttrace exporter that drops all spans.
// Used as the default and as the fallback for failed setup paths. The
// exporter is created fresh per call because stdouttrace.New returns a
// pointer that gets bound to the SDK's tracer provider; cheap to make
// (just a struct) but only invoked at startup so per-call cost is fine.
func silentExporter() (sdktrace.SpanExporter, error) {
	exp, _ := stdouttrace.New(stdouttrace.WithWriter(io.Discard))
	return exp, nil
}

// fileExporter wraps a stdouttrace exporter and a file handle, closing the
// handle on Shutdown. Needed because stdouttrace.New only uses its writer;
// it does not own or close it. Without this wrapper, the file: exporter
// mode leaks a FD for the lifetime of the process.
//
// All other SpanExporter methods are pass-through.
type fileExporter struct {
	exporter sdktrace.SpanExporter
	file     *os.File
}

func (fe *fileExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return fe.exporter.ExportSpans(ctx, spans)
}

func (fe *fileExporter) Shutdown(ctx context.Context) error {
	err := fe.exporter.Shutdown(ctx)
	if cerr := fe.file.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}
