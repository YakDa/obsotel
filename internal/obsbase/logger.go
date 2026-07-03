// Package obs is the unified observability contract for the monorepo.
// Every service imports this package; nothing else. Logs, request IDs,
// outbound calls, and error wrapping all flow through these helpers
// so the AI retrofit has a single, grep-able target.
//
// Mental model:
//   - logger.go:       slog setup, JSON in prod / text in dev
//   - context.go:      bind a logger to context.Context, propagate it
//   - request_id.go:   UUID-like request ID generator (no external dep)
//   - middleware.go:   HTTP middleware that injects request_id + logs
//   - outbound.go:     HTTP client wrapper that propagates request_id + logs
//   - errors.go:       ErrorChain (Go 1.13+ wrap), AppError, LogErr
//   - sampling.go:     deterministic + random samplers for hot paths
package obs

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a *slog.Logger configured for the given environment.
//
//	env == "prod"          → JSON to stderr, level INFO
//	env == "dev" (default) → text to stderr, level DEBUG
//
// Production-grade services should run with stderr; let the container /
// k8s / journald handle persistence, rotation, and shipping.
func NewLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env != "prod" {
		level = slog.LevelDebug
	}
	return NewLoggerWithLevel(env, level)
}

// NewLoggerWithLevel is NewLogger with explicit level control.
func NewLoggerWithLevel(env string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if env == "prod" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// LevelFromString parses "debug" | "info" | "warn" | "error" → slog.Level.
// Useful for reading log level from env config.
func LevelFromString(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
