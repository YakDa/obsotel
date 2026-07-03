package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/YakDa/obsotel"
)

func main() {
	ctx := context.Background()

	// 1. Initialize the OTel tracer (fail-open: returns a no-op shutdown on error).
	shutdown, err := obsotel.InitTracer(ctx, "user-service")
	if err != nil {
		slog.Warn("otel_init_failed", "err", err) // not fatal — service still works
	}
	defer shutdown(ctx)

	// 2. Build a logger. "prod" → JSON; anything else → text.
	log := obsotel.NewLogger(os.Getenv("ENV"))
	slog.SetDefault(log)

	// 3. Wire up HTTP. Handler() wraps mux with otelhttp server span + obsbase logging.
	mux := http.NewServeMux()
	mux.HandleFunc("/foo", fooHandler)
	http.ListenAndServe(":8080", obsotel.Handler(log, mux, "user-service"))
}

func fooHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if uid := r.Header.Get("X-User-ID"); uid != "" {
		ctx, _ = obsotel.With(ctx, slog.String(obsotel.UserIDKey, uid))
		r = r.WithContext(ctx)
	}

	if err := doStuff(ctx); err != nil {
		obsotel.LogErr(ctx, "do_stuff_failed", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func doStuff(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.google.com", nil)
	// obsotel.DoRequest uses an internal defaultClient that already has
	// otelhttp client-side tracing and request-ID propagation wired up.
	// For a custom timeout/transport, use NewClient() + DoRequestWithClient.
	resp, err := obsotel.DoRequest(ctx, req)
	if err != nil {
		return obsotel.Wrap(ctx, err, "do_stuff")
	}
	defer resp.Body.Close()
	return nil
}
