package obsotel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// meterInitGuard protects against double InitMeter. See tracerInitGuard
// in tracer.go for the rationale.
var (
	meterInitMu       sync.Mutex
	previousMeterStop func(context.Context) error
)

// Counter is a monotonically-increasing counter (e.g. requests_total,
// orders_placed_total). Use Add() to increment.
//
// Construct via NewCounter. The metric is registered with the global
// MeterProvider; if no provider is configured, calls are no-ops.
type Counter struct {
	meter metric.Float64Counter
}

// NewCounter constructs a Counter with the given name and description.
// Name should be in snake_case and follow OTel naming conventions
// (e.g. "http_requests_total", "orders_placed_total").
func NewCounter(name, description string) (*Counter, error) {
	m := otel.Meter("obsotel")
	c, err := m.Float64Counter(name, metric.WithDescription(description))
	if err != nil {
		return nil, err
	}
	return &Counter{meter: c}, nil
}

// MustNewCounter is NewCounter that panics on error. Use at package init
// or top-of-main when you know the metric name is valid.
func MustNewCounter(name, description string) *Counter {
	c, err := NewCounter(name, description)
	if err != nil {
		panic(err)
	}
	return c
}

// Add increments the counter by v. v must be non-negative.
func (c *Counter) Add(ctx context.Context, v float64, attrs ...attribute.KeyValue) {
	if c == nil || c.meter == nil {
		return
	}
	c.meter.Add(ctx, v, metric.WithAttributes(attrs...))
}

// Inc increments the counter by 1.
func (c *Counter) Inc(ctx context.Context, attrs ...attribute.KeyValue) {
	c.Add(ctx, 1, attrs...)
}

// Histogram records a distribution of values (e.g. request_duration_seconds,
// payload_size_bytes). Use Record() to record values.
//
// Construct via NewHistogram.
type Histogram struct {
	meter metric.Float64Histogram
}

// NewHistogram constructs a Histogram. Buckets are chosen automatically
// by OTel; for custom buckets use WithExplicitBucketBoundaries on the
// underlying meter (lower-level API).
func NewHistogram(name, description, unit string) (*Histogram, error) {
	m := otel.Meter("obsotel")
	h, err := m.Float64Histogram(name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		return nil, err
	}
	return &Histogram{meter: h}, nil
}

// MustNewHistogram is NewHistogram that panics on error.
func MustNewHistogram(name, description, unit string) *Histogram {
	h, err := NewHistogram(name, description, unit)
	if err != nil {
		panic(err)
	}
	return h
}

// Record records a value in the histogram.
func (h *Histogram) Record(ctx context.Context, v float64, attrs ...attribute.KeyValue) {
	if h == nil || h.meter == nil {
		return
	}
	h.meter.Record(ctx, v, metric.WithAttributes(attrs...))
}

// ----------------------------------------------------------------------------
// Common metric helpers — pre-built for typical HTTP request metrics.
// ----------------------------------------------------------------------------

// HTTPRequestDuration records request duration in seconds, with method,
// path, and status attributes. Use as middleware or in your logging wrapper:
//
//	start := time.Now()
//	obsotel.HTTPRequestDuration.Record(ctx, time.Since(start).Seconds(),
//	    attribute.String("method", r.Method),
//	    attribute.String("path", r.URL.Path),
//	    attribute.Int("status", rr.StatusCode),
//	)
//
// Lazily initialized; safe to use even before InitTracer is called.
var HTTPRequestDuration = MustNewHistogram(
	"http_request_duration_seconds",
	"Duration of HTTP requests in seconds",
	"s",
)

// HTTPRequestsTotal counts HTTP requests. Use Add(ctx, 1, ...) per request.
var HTTPRequestsTotal = MustNewCounter(
	"http_requests_total",
	"Total number of HTTP requests",
)

// ----------------------------------------------------------------------------
// InitMeter — global MeterProvider setup (symmetric to InitTracer).
// ----------------------------------------------------------------------------

// InitMeter sets up the global OTel MeterProvider and registers a periodic
// reader that flushes metrics to the configured exporter. Returns a shutdown
// function that flushes pending metrics and should be called at process exit.
//
// Fail-open: if exporter setup fails, returns the error AND a no-op shutdown
// so callers can `defer shutdown(ctx)` unconditionally without crashing
// startup.
//
// Without InitMeter, OTel uses a no-op meter — NewCounter / NewHistogram
// still work but metric calls are silently dropped.
func InitMeter(ctx context.Context, serviceName string, opts ...MeterOption) (shutdown func(context.Context) error, err error) {
	cfg := defaultMeterConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	// Double-init guard: shut down any previously installed provider so
	// we don't leak its periodic reader. See meterInitGuard comment.
	meterInitMu.Lock()
	if previousMeterStop != nil {
		slog.Warn("obsotel: InitMeter called twice; shutting down previous provider",
			slog.String("service", serviceName))
		if sErr := previousMeterStop(ctx); sErr != nil {
			slog.Warn("obsotel: previous meter shutdown error",
				slog.String("err", sErr.Error()))
		}
		previousMeterStop = nil
	}
	meterInitMu.Unlock()

	exp, err := cfg.exporterFactory(ctx)
	if err != nil {
		return func(context.Context) error { return nil },
			fmt.Errorf("obsotel: meter exporter setup failed, running with no-op meter: %w", err)
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
			fmt.Errorf("obsotel: meter resource setup failed: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)

	meterInitMu.Lock()
	previousMeterStop = mp.Shutdown
	meterInitMu.Unlock()

	return mp.Shutdown, nil
}

// ----------------------------------------------------------------------------
// Meter options
// ----------------------------------------------------------------------------

// meterConfig holds internal configuration for InitMeter.
type meterConfig struct {
	exporterFactory func(context.Context) (sdkmetric.Exporter, error)
}

func defaultMeterConfig() meterConfig {
	return meterConfig{
		exporterFactory: defaultMeterExporter,
	}
}

// MeterOption customizes InitMeter.
type MeterOption func(*meterConfig)

// WithMeterExporter replaces the default stdout exporter with a custom one.
// Use this to wire up an OTLP exporter to a real collector:
//
//	exp, _ := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint("otel-collector:4317"))
//	obsotel.InitMeter(ctx, "user-service", obsotel.WithMeterExporter(exp))
func WithMeterExporter(exp sdkmetric.Exporter) MeterOption {
	return func(c *meterConfig) {
		c.exporterFactory = func(context.Context) (sdkmetric.Exporter, error) {
			return exp, nil
		}
	}
}

// defaultMeterExporter creates a stdout metric exporter (writes metrics
// to stdout). Useful for dev and tests. Replace via WithMeterExporter
// for production.
func defaultMeterExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	return stdoutmetric.New()
}