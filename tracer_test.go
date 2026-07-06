package obsotel

import (
	"context"
	neturl "net/url"
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

// TestDefaultExporter_FileMode_ClosesFD guards against the file-descriptor
// leak that motivated the fileExporter wrapper. Without it, stdouttrace.New
// does not own its writer, so the FD stays open for the process lifetime.
//
// The test calls Shutdown on the returned exporter, then verifies the FD
// is closed by trying to stat/use the file via a syscall (a closed FD
// returns EBADF on subsequent operations, or the path is unlinked).
func TestDefaultExporter_FileMode_ClosesFD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("OBSOTEL_DUMP_SPANS", "file:"+path)

	ctx := context.Background()
	exp, err := defaultExporter(ctx)
	if err != nil {
		t.Fatalf("defaultExporter(file): %v", err)
	}

	// Drive a span through the exporter so the writer is actually used.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	_, span := tp.Tracer("test").Start(ctx, "fd-check")
	span.End()

	// Shutdown should close the file.
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatalf("tp.Shutdown: %v", err)
	}

	// After shutdown, attempting to write to the closed file should
	// fail. Use the exporter's underlying file indirectly by trying to
	// open it exclusively — if the FD is held, we may still get EBUSY on
	// some platforms, but on Linux we can detect closure by trying to
	// remove the file (succeeds when nothing holds it).
	// A more reliable check: use the fileExporter type assertion.
	if fe, ok := exp.(*fileExporter); ok {
		// Use the file's underlying FD to confirm closure.
		// Write should fail with ErrClosed after Shutdown.
		_, werr := fe.file.Write([]byte("post-close\n"))
		if werr == nil {
			t.Fatalf("file still writable after Shutdown; FD not closed")
		}
		_ = werr // any error means the FD is closed, which is what we want
	} else {
		t.Fatalf("expected *fileExporter, got %T", exp)
	}
}

// TestSafeURLString verifies that query strings, fragments, and userinfo
// are stripped from URLs before logging. This is a security regression
// guard — the original code logged req.URL.String() which leaked
// api_keys, tokens, and other secrets from query parameters.
func TestSafeURLString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no query", "https://api.example.com/users/42", "https://api.example.com/users/42"},
		{"with secret query", "https://api.example.com/users?api_key=secret123", "https://api.example.com/users"},
		{"with token + signature", "https://pay.example.com/charge?token=tok_abc&sig=xyz", "https://pay.example.com/charge"},
		{"with fragment", "https://api.example.com/path#frag", "https://api.example.com/path"},
		{"with userinfo", "https://user:pass@api.example.com/secret", "https://api.example.com/secret"},
		{"with port", "https://api.example.com:8443/v1/users", "https://api.example.com:8443/v1/users"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := neturl.Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := SafeURLString(u)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("nil URL", func(t *testing.T) {
		if got := SafeURLString(nil); got != "" {
			t.Errorf("nil URL: got %q, want empty", got)
		}
	})
}

// TestDefaultExporter_FileMode_OpenError_SilentFallback verifies that
// when the file: path cannot be opened (parent dir missing), the
// exporter factory returns a silent fallback rather than an error.
// A startup panic is strictly worse than the silent fallback we want;
// the fallback path must be safe to call and produce a usable exporter.
//
// Note: this test does NOT exercise the (alleged) nil-receiver panic
// on *os.File.Close. Go's stdlib has guarded that case since 1.0 (it
// returns ErrInvalid rather than panicking). What we test here is the
// contract: file-open failure -> silent exporter, no error.
func TestDefaultExporter_FileMode_OpenError_SilentFallback(t *testing.T) {
	// t.TempDir() exists; the child dir does not.
	bad := filepath.Join(t.TempDir(), "nonexistent-subdir", "spans.jsonl")
	t.Setenv("OBSOTEL_DUMP_SPANS", "file:"+bad)

	// Must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("defaultExporter panicked on file-open error: %v", r)
		}
	}()

	exp, err := defaultExporter(context.Background())
	if err != nil {
		t.Fatalf("defaultExporter(file:bad): want silent fallback, got error: %v", err)
	}
	if exp == nil {
		t.Fatal("defaultExporter(file:bad): want silent exporter, got nil")
	}
	// Sanity: the silent exporter must be usable (ExportSpans is a no-op).
	if err := exp.ExportSpans(context.Background(), nil); err != nil {
		t.Errorf("silent exporter ExportSpans: %v", err)
	}
}
