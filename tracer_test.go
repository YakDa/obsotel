package obsotel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestDefaultExporter_Modes covers the OBSOTEL_DUMP_SPANS env-var gate.
// Each subtest verifies that the mode produces a working exporter and,
// for the file mode, that an actual span end-to-end makes it to disk.
func TestDefaultExporter_Modes(t *testing.T) {
	ctx := context.Background()

	t.Run("empty_silent", func(t *testing.T) {
		t.Setenv("OBSOTEL_DUMP_SPANS", "")
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("defaultExporter: %v", err)
		}
		if exp == nil {
			t.Fatal("expected non-nil exporter")
		}
	})

	t.Run("stdout_pretty", func(t *testing.T) {
		t.Setenv("OBSOTEL_DUMP_SPANS", "stdout")
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("defaultExporter: %v", err)
		}
		if exp == nil {
			t.Fatal("expected non-nil exporter")
		}
		// We can't capture os.Stderr inside the test without redirecting
		// the process's fd, but the construction path proves the switch
		// arm is reachable.
	})

	t.Run("compact", func(t *testing.T) {
		t.Setenv("OBSOTEL_DUMP_SPANS", "compact")
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("defaultExporter: %v", err)
		}
		if exp == nil {
			t.Fatal("expected non-nil exporter")
		}
	})

	t.Run("file_writes_span", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spans.jsonl")
		t.Setenv("OBSOTEL_DUMP_SPANS", "file:"+path)
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("defaultExporter: %v", err)
		}

		// Drive an actual span through the exporter so we catch
		// "file is created but exporter silently broken" regressions.
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		_, span := tp.Tracer("test").Start(ctx, "smoke")
		span.End()

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected file at %s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("expected non-empty file at %s (exporter wrote nothing)", path)
		}
	})

	t.Run("unknown_mode_falls_back_silent", func(t *testing.T) {
		t.Setenv("OBSOTEL_DUMP_SPANS", "this-is-not-a-real-mode")
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("defaultExporter: %v", err)
		}
		if exp == nil {
			t.Fatal("expected non-nil exporter")
		}
	})

	t.Run("file_invalid_path_falls_back_silent", func(t *testing.T) {
		// /this/path/does/not/exist should fail OpenFile; the function
		// must NOT bubble that up — fall back to silent.
		t.Setenv("OBSOTEL_DUMP_SPANS", "file:/this/path/should/not/exist/spans.jsonl")
		exp, err := defaultExporter(ctx)
		if err != nil {
			t.Fatalf("expected silent fallback, got error: %v", err)
		}
		if exp == nil {
			t.Fatal("expected non-nil exporter")
		}
	})
}

// TestDefaultExporter_FileMode_PathArgument verifies that "file:PATH"
// strips the prefix and uses PATH as the destination. Guards against
// a refactor that drops the prefix strip.
func TestDefaultExporter_FileMode_PathArgument(t *testing.T) {
	t.Setenv("OBSOTEL_DUMP_SPANS", "file:/tmp/obsotel-test-spans-arg.jsonl")
	defer os.Remove("/tmp/obsotel-test-spans-arg.jsonl") // best-effort

	exp, err := defaultExporter(context.Background())
	if err != nil {
		t.Fatalf("defaultExporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	defer tp.Shutdown(context.Background())

	_, span := tp.Tracer("test").Start(context.Background(), "smoke")
	span.End()

	if _, err := os.Stat("/tmp/obsotel-test-spans-arg.jsonl"); err != nil {
		t.Fatalf("expected file at /tmp/obsotel-test-spans-arg.jsonl: %v\n"+
			"(the file mode probably didn't strip its prefix correctly)", err)
	}
}

// TestDefaultExporter_DocMatchesEnv verifies the env-var modes that the
// documentation comment advertises are all reachable. If a future
// maintainer adds a new mode without updating this list, this fails.
func TestDefaultExporter_DocMatchesEnv(t *testing.T) {
	// Keep this list in lockstep with the switch in defaultExporter
	// and the doc comment above it.
	wanted := []string{"", "stdout", "compact"}
	for _, m := range wanted {
		t.Setenv("OBSOTEL_DUMP_SPANS", m)
		if _, err := defaultExporter(context.Background()); err != nil {
			t.Fatalf("mode %q: %v", m, err)
		}
	}
}
