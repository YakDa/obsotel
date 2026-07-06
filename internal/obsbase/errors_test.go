package obs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestLogErr_NoDuplicateRequestID guards against the second writer that the
// initial duplicate-fix missed: Wrap() puts request_id into AppError.Meta,
// and LogErr iterates Meta and emits it as a top-level attr. Combined with
// requestIDHandler injecting from ctx, the JSON line ends up with
// `request_id` twice. As with the LoggingMiddleware test, this scans the
// raw JSON for `"request_id"` because json.Unmarshal last-wins and would
// silently hide the duplicate.
func TestLogErr_NoDuplicateRequestID(t *testing.T) {
	buf := &bytes.Buffer{}
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(&testRequestIDHandler{base: base})

	ctx := WithLogger(context.Background(), log)
	ctx = WithRequestID(ctx, "rid-test-001")

	// Build the same error shape that user code would: Wrap produces an
	// *AppError with request_id baked into Meta.
	err := Wrap(ctx, errors.New("boom"), "do_stuff")

	LogErr(ctx, "do_stuff_failed", err)

	if n := strings.Count(buf.String(), `"request_id"`); n != 1 {
		t.Fatalf("expected exactly 1 occurrence of \"request_id\" in log, got %d\nlog: %s",
			n, buf.String())
	}
}
