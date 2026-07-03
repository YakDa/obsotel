package obsotel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
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

// defaultExporter creates a stdout exporter (writes spans to stderr).
// Useful for dev and tests. Replace via WithExporter for production.
func defaultExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithPrettyPrint(),
	)
}