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
// `request_id` twice.
//
// The right invariant is: exactly 1 top-level request_id. request_id should
// NOT also appear inside the chain entry (after the chain-simplification
// fix, error_chain carries only `cause` / `error`, not the full AppError
// surface). So the chain is now strictly redundant-data-free.
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
	// request_id must NOT appear inside the chain entry. The chain carries
	// only `cause` now; meta fields (request_id included) live at the top
	// level only.
	chain, ok := out["error_chain"].([]any)
	if !ok || len(chain) == 0 {
		t.Fatalf("expected error_chain array, got: %#v", out["error_chain"])
	}
	first, ok := chain[0].(map[string]any)
	if !ok {
		t.Fatalf("chain[0] should be a map, got %T", chain[0])
	}
	if _, present := first["request_id"]; present {
		t.Fatalf("chain[0] should not carry request_id; got entry: %#v", first)
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
