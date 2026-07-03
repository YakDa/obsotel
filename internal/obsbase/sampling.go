package obs

import (
	"context"
	"log/slog"
	"math/rand"
	"sync/atomic"
)

// Sampler decides whether a log line should be emitted.
// Use for hot paths (every retry, every batch item, every poll) where
// logging every event would dominate log volume.
//
// All rates are in parts-per-million for integer-only math.
//   - rate = 1_000_000 → emit everything
//   - rate = 100       → emit ~0.01% (1 in 10,000)
//   - rate = 0         → emit nothing
type Sampler struct {
	rate    uint64
	counter atomic.Uint64
}

// NewSampler returns a deterministic sampler: it emits every Nth call
// where N = 1_000_000 / rate. Deterministic intervals make it easy to
// predict log volume.
func NewSampler(rate uint64) *Sampler {
	return &Sampler{rate: rate}
}

// Allow returns true if this call should be logged.
func (s *Sampler) Allow() bool {
	if s.rate >= 1_000_000 {
		return true
	}
	if s.rate == 0 {
		return false
	}
	interval := 1_000_000 / s.rate
	if interval == 0 {
		interval = 1
	}
	n := s.counter.Add(1)
	return n%interval == 0
}

// LogIf emits msg with attrs only when the sampler allows.
// Convenience for hot paths:
//
//	var retryLog = obs.NewSampler(100) // 0.01% sample rate
//	retryLog.LogIf(obs.L(ctx), slog.LevelWarn,
//	    "retry_attempt", "attempt", n)
func (s *Sampler) LogIf(ctx context.Context, l *slog.Logger, level slog.Level, msg string, attrs ...any) {
	if !s.Allow() {
		return
	}
	if l == nil {
		l = slog.Default()
	}
	l.LogAttrs(ctx, level, msg, toAttrs(attrs)...)
}

// RandomSampler is a non-deterministic sampler.
// Use when you don't want deterministic intervals (which can reveal
// patterns — e.g. every 10,000th call always lands at the same logical
// moment in a periodic workload).
type RandomSampler struct {
	rate uint64
}

// NewRandomSampler returns a probabilistic sampler.
func NewRandomSampler(rate uint64) *RandomSampler {
	return &RandomSampler{rate: rate}
}

// Allow returns true with probability rate / 1_000_000.
func (s *RandomSampler) Allow() bool {
	if s.rate >= 1_000_000 {
		return true
	}
	if s.rate == 0 {
		return false
	}
	return rand.Uint64()%1_000_000 < s.rate
}

// LogIf emits msg with attrs only when the sampler allows.
func (s *RandomSampler) LogIf(ctx context.Context, l *slog.Logger, level slog.Level, msg string, attrs ...any) {
	if !s.Allow() {
		return
	}
	if l == nil {
		l = slog.Default()
	}
	l.LogAttrs(ctx, level, msg, toAttrs(attrs)...)
}
