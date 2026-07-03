# `obsotel` — Linting Rules

This file documents the `golangci-lint` rules that enforce the obsotel package conventions. Drop the snippet into your service's `.golangci.yml` (or merge into a monorepo-wide config).

## Why

AI-generated code will inevitably try to use `fmt.Println`, `log.Println`, or `log.Printf` because those are the Go defaults it learned from training data. Without enforcement, observability drift starts within a week. These rules ban the offenders at CI time, not in code review.

## `.golangci.yml` snippet

```yaml
linters:
  disable-all: true
  enable:
    - forbidigo
    - errcheck
    - govet
    - staticcheck
    - unused

linters-settings:
  forbidigo:
    # Ban unstructured / uncorrelated output entirely.
    # If you see one of these, use slog or obs.LogErr instead.
    forbid:
      - pattern: "fmt\\.Println"
        msg: "use slog.Info instead — see github.com/mingdos/obsotel"
      - pattern: "fmt\\.Print\\b"
        msg: "use slog.Info instead — see github.com/mingdos/obsotel"
      - pattern: "fmt\\.Printf"
        msg: "use slog.Info with structured fields instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Println"
        msg: "use slog.Info instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Print\\b"
        msg: "use slog.Info instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Printf"
        msg: "use slog.Info with structured fields instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Fatal\\b"
        msg: "use slog.Error + os.Exit instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Fatalf"
        msg: "use slog.Error with structured fields + os.Exit instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Fatalln"
        msg: "use slog.Error + os.Exit instead — see github.com/mingdos/obsotel"
      - pattern: "log\\.Panic\\b"
        msg: "use slog.Error instead — log.Panic prints and panics, neither is what you want"
      - pattern: "log\\.Panicf"
        msg: "use slog.Error instead — log.Panic prints and panics, neither is what you want"
      # Manual error logging breaks the shape: loses error_chain array,
      # AppError Meta fields, and per-service shape lock.
      # Always use obsotel.LogErr(ctx, ...) for errors.
      - pattern: "slog\\.Error\\([^,]+,\\s*\"err\"\\s*,"
        msg: "use obsotel.LogErr instead — see github.com/mingdos/obsotel (preserves error_chain + Meta + shape)"
      - pattern: "slog\\.ErrorContext\\([^,]+,[^,]+,\\s*\"err\"\\s*,"
        msg: "use obsotel.LogErr instead — see github.com/mingdos/obsotel (preserves error_chain + Meta + shape)"
      # slog.SetDefault replaces the process-wide default logger. It must
      # only run in main() after the obsotel logger is constructed.
      # Mid-process replacement is spooky action at a distance.
      - pattern: "slog\\.SetDefault\\("
        msg: "slog.SetDefault belongs in main() only — see github.com/mingdos/obsotel"
```

## What each rule blocks

| Pattern | Banned because |
|---|---|
| `fmt.Println` / `fmt.Print` | Unstructured. No request_id, no JSON, no Loki-queryable fields. |
| `fmt.Printf` | "Structured" by accident — the human parses the format string. AI can't search it. |
| `log.Println` / `log.Print` / `log.Printf` | Same. The stdlib `log` package predates `slog` and lacks context propagation. |
| `log.Fatal*` | Prints to stderr *and* calls `os.Exit(1)` — no deferred cleanup, no graceful shutdown, no chance to flush logs. Use `slog.Error` + `os.Exit(1)` in `main()`. |
| `log.Panic*` | Prints *and* panics — both undesirable. `slog.Error` and return the error. |
| `slog.Error(..., "err", err)` / `slog.ErrorContext(..., "err", err)` | Manual error logging loses the `error_chain` array, AppError Meta fields at top level, and the cross-service shape lock. Always go through `obsotel.LogErr(ctx, ...)`. |
| `slog.SetDefault(...)` | Must only be called once in `main()` after constructing the obsotel logger. Mid-process replacement is spooky action at a distance (the logging behavior of every goroutine changes under you). |

## Excluding tests

`fmt.Println` in `_test.go` is fine for debugging. Add an exclusion:

```yaml
issues:
  exclude-rules:
    - path: _test\.go$
      linters: [forbidigo]
    - path: internal/obsotel/examples/
      linters: [forbidigo]    # the logerr_comparison demo intentionally uses bare slog.Error / SetDefault to show the shape difference
```

## Going further: forbid specific obs misuse

If you want to enforce *positive* rules too (e.g., every error log must use `obs.LogErr`, not `slog.Error`), use a custom analyzer. Build a small one with `golang.org/x/tools/go/analysis/passes`:

```go
// analyzer/usenilerrcheck/usenilerrcheck.go
package usenilerrcheck

import (
    "golang.org/x/tools/go/analysis"
    "golang.org/x/tools/go/analysis/passes/inspect"
)

var Analyzer = &analysis.Analyzer{
    Name:     "obslogusage",
    Doc:      "report slog.Error calls without context propagation",
    Requires: []*analysis.Analyzer{inspect.Analyzer},
    Run:      run,
}
```

This is heavier than `forbidigo` and not strictly necessary at the start. Recommend adding it later, after a few weeks of seeing what mistakes humans + AI actually make in your codebase.

## Recommended additional linters

Beyond `forbidigo`, these help with the rest of the obs hygiene:

```yaml
linters-settings:
  govet:
    enable-all: true
  revive:
    rules:
      - name: exported
        disabled: false       # every exported func needs a doc comment
      - name: error-strings
        disabled: false       # error strings should not be capitalized
      - name: error-naming
        disabled: false       # error variables should be prefixed with Err
      - name: var-naming
        disabled: false       # catch accidental stutter / shadowing
  staticcheck:
    checks:
      - "all"                  # everything; some teams disable SA1019 (deprecation) noise
  errcheck:
    check-blank: true         # `_ = foo()` is fine, but unhandled returns get flagged
  unused:
    go-mod-rum: true
```

## CI integration

Add a `lint` job to your service's CI pipeline:

```yaml
lint:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.21'
    - name: golangci-lint
      uses: golangci/golangci-lint-action@v6
      with:
        version: v1.59
        # Working directory = service root; .golangci.yml is picked up from here
```

If `forbidigo` flags an obs call in the obs package itself (unlikely but possible), exempt that file:

```yaml
issues:
  exclude-rules:
    - path: internal/obsotel/_test\.go$
      linters: [forbidigo]
    - path: internal/obsotel/examples/
      linters: [forbidigo]    # the logerr_comparison demo intentionally uses bare slog.Error + SetDefault to show shape drift
    - path: cmd/migrate/main\.go$
      linters: [forbidigo]    # one-off migration scripts can print
```

---

## TL;DR

`fmt.Println`, `log.Print*`, `log.Fatal*`, `log.Panic*` — all banned in production code. `slog.Error(..., "err", err)` and `slog.ErrorContext(..., "err", err)` — banned, use `obsotel.LogErr`. `slog.SetDefault(...)` outside `main()` — banned. Use `slog` (via `obsotel.L(ctx)`) or `obsotel.LogErr`. Enforce in CI, not in code review. AI retrofit is only durable if the linter holds the line.