// Demo: shows the literal JSON output difference between bare slog.Error
// and obsotel.LogErr for the same AppError chain with Meta fields.
//
// Run with: go run .
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mingdos/obsotel"
)

func banner(s string) {
	fmt.Println()
	fmt.Println("========================================================================")
	fmt.Println(" " + s)
	fmt.Println("========================================================================")
}

func dump(label string, buf *bytes.Buffer) {
	fmt.Println()
	fmt.Println("--- " + label + " ---")
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		// Re-indent each JSON line for readability
		fmt.Println("  " + string(line))
	}
}

func main() {
	// Build the SAME error chain for every demo.
	root := errors.New("connection refused")
	mid := fmt.Errorf("db query: %w", root)
	top := obsotel.New("load_user", "infra_error", mid).WithMeta(
		"user_id", "u42",
		"tenant", "t1",
	)

	// Bind a request_id to ctx (this doesn't bind a logger; only a string).
	ctx := obsotel.WithRequestID(context.Background(), "rid-xyz")
	// NOTE: we deliberately do NOT bind a logger to ctx here. Doing
	// obsotel.With(ctx, ...) before slog.SetDefault(...) would store the
	// stock slog.Logger in ctx, and later LogErr calls would route through
	// that one instead of the SetDefault'd logger. user_id flows in via
	// AppError.Meta in the demo, not via ctx.

	// Build the single shared obsotel logger up front. In a real service,
	// you'd slog.SetDefault(log) right after constructing it. For this demo
	// we use it as the default so BOTH styles (bare slog.Error and
	// obsotel.LogErr) route through the same JSON + traceHandler wrapping.

	// ========================================================================
	// A. Bare slog.Error — same logger underneath, no error_chain
	// ========================================================================
	banner("A. bare slog.Error  (same obsotel logger underneath via SetDefault)")
	var bufA bytes.Buffer
	logA := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &bufA)
	slog.SetDefault(logA)
	slog.ErrorContext(ctx, "load_user_failed", "err", top)
	dump("OUTPUT", &bufA)

	// ========================================================================
	// B. obsotel.LogErr — the contract, same logger underneath
	// ========================================================================
	banner("B. obsotel.LogErr  (the contract; same obsotel logger)")
	var bufB bytes.Buffer
	logB := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &bufB)
	slog.SetDefault(logB)
	obsotel.LogErr(ctx, "load_user_failed", top, "attempt", 3)
	dump("OUTPUT", &bufB)

	// ========================================================================
	// C. The shape-lock problem: same intent, different field names
	//    (routed through the same obsotel JSON handler for fairness)
	// ========================================================================
	banner("C. shape drift — three devs, three error-log shapes (same logger)")
	var bufC bytes.Buffer
	logC := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &bufC)
	slog.SetDefault(logC)
	slog.ErrorContext(ctx, "load_user_failed", "err", top)
	slog.ErrorContext(ctx, "load_user_failed", "error", top)
	slog.ErrorContext(ctx, "load_user_failed", "e", top.Error())
	dump("OUTPUT (three lines, same intent)", &bufC)

	// ========================================================================
	// D. error_chain array — what you can grep / query
	// ========================================================================
	banner("D. error_chain array — only LogErr emits the full unwrap chain")
	var bufD bytes.Buffer
	logD := obsotel.NewLoggerToWriter("prod", slog.LevelInfo, &bufD)
	slog.SetDefault(logD)
	obsotel.LogErr(ctx, "load_user_failed", top, "attempt", 3)
	dump("OUTPUT", &bufD)
	fmt.Println()
	fmt.Println("  ↳ Pipeline queries this enables:")
	fmt.Println("    • LogQL: {job=\"...\"} | json | error_chain__contains=\"connection refused\"")
	fmt.Println("    • ELK:   error_chain:\"connection refused\"")
	fmt.Println("    • SQL:   SELECT * FROM logs WHERE payload->'error_chain' ? 'connection refused'")
}
