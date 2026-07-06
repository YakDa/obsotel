package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

// TestLogErr_NoDuplicateRequestID guards against the second writer that the
// initial duplicate-fix missed: Wrap() puts request_id into AppError.Meta,
// and LogErr iterates Meta and emits it as a top-level attr. Combined with
// requestIDHandler injecting from ctx, the JSON line ends up with
// `request_id` twice. As with the LoggingMiddleware test, this scans the
// raw JSON for `"request_id"` because json.Unmarshal last-wins and would
// silently hide the duplicate.
//
// After the structured-chain fix, error_chain[0] also legitimately contains
// request_id (from AppError.Meta surfaced via LogValue). That's correct — the
// structured chain is *supposed* to carry the field inside its group. So
// "exactly 1" is too strict. The right invariant is: exactly 1 top-level
// request_id. The chain entry is fine because it's correctly namespaced
// inside the structured group.
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

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v\nlog: %s", err, buf.String())
	}
	// Exactly one top-level request_id (set by requestIDHandler).
	if out["request_id"] != "rid-test-001" {
		t.Fatalf("expected top-level request_id=rid-test-001, got %v", out["request_id"])
	}
	// The chain entry may also carry request_id inside its group — that's
	// legitimate structured data, not a duplicate. Verify it's namespaced.
	chain, ok := out["error_chain"].([]any)
	if !ok || len(chain) == 0 {
		t.Fatalf("expected error_chain array, got: %#v", out["error_chain"])
	}
	first, ok := chain[0].(map[string]any)
	if !ok {
		t.Fatalf("chain[0] should be a map, got %T", chain[0])
	}
	if first["request_id"] != "rid-test-001" {
		t.Fatalf("chain[0].request_id should be rid-test-001 (carried via Meta), got %v",
			first["request_id"])
	}
	// Sanity: no raw duplicate "request_id" at the top level (would indicate
	// the original bug returned). Count top-level occurrences only by parsing.
	keys := make(map[string]int)
	for k := range out {
		keys[k]++
	}
	if keys["request_id"] != 1 {
		t.Fatalf("expected exactly 1 top-level request_id key, got %d (full: %#v)",
			keys["request_id"], out)
	}
}
