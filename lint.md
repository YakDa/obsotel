# `obsotel` — Linting Rules

This file documents the `golangci-lint` rules that enforce the obsotel package conventions. Drop the snippet into your service's `.golangci.yml` (or merge into a monorepo-wide config).

## Why

AI-generated code will inevitably try to use `fmt.Println`, `log.Println`, or `log.Printf` because those are the Go defaults it learned from training data. Without enforcement, observability drift starts within a week. These rules ban the offenders at CI time, not in code review.

## `.golangci.yml` snippet

```yaml
linters:
  disable-all: true
  enable:
    # Default set
    - forbidigo
    - errcheck
    - govet
    - staticcheck
    - unused
    # Obsotel-specific
    - sloglint           # AST-based: enforces slog contract (context, no global)
    - errorlint          # flags fmt.Errorf("...: %v", err) — use %w to preserve chain
    - revive             # exported docs, error naming, var-naming
```

```yaml
linters-settings:
  forbidigo:
    # Ban unstructured / uncorrelated output entirely.
    # If you see one of these, use slog or obs.LogErr instead.
    forbid:
      - pattern: "fmt\\.Println"
        msg: "use slog.Info instead — see github.com/YakDa/obsotel"
      - pattern: "fmt\\.Print\\b"
        msg: "use slog.Info instead — see github.com/YakDa/obsotel"
      - pattern: "fmt\\.Printf"
        msg: "use slog.Info with structured fields instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Println"
        msg: "use slog.Info instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Print\\b"
        msg: "use slog.Info instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Printf"
        msg: "use slog.Info with structured fields instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Fatal\\b"
        msg: "use slog.Error + os.Exit instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Fatalf"
        msg: "use slog.Error with structured fields + os.Exit instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Fatalln"
        msg: "use slog.Error + os.Exit instead — see github.com/YakDa/obsotel"
      - pattern: "log\\.Panic\\b"
        msg: "use slog.Error instead — log.Panic prints and panics, neither is what you want"
      - pattern: "log\\.Panicf"
        msg: "use slog.Error instead — log.Panic prints and panics, neither is what you want"

      # The slog-contract rules below live in sloglint (AST-based, no regex).
      # What sloglint CAN'T enforce is the obsotel-specific contract:
      # "even when you use *Context variants, the error path goes through
      # obsotel.LogErr, not slog.ErrorContext." That contract is enforced
      # here. Patterns are deliberately broad — they ban the call shapes
      # entirely, not just specific field names. Combine with sloglint's
      # no-global + context: all to cover the slog side at the AST level.
      - pattern: 'slog\.Error\('
        msg: "use obsotel.LogErr instead — see github.com/YakDa/obsotel (preserves error_chain + Meta + shape)"
      - pattern: 'slog\.ErrorContext\('
        msg: "use obsotel.LogErr instead — see github.com/YakDa/obsotel (preserves error_chain + Meta + shape)"

  sloglint:
    # no-global: "all" bans slog.Info/Warn/Error/Debug AND slog.SetDefault
    # and slog.Default. The SetDefault/Default case is a known tension —
    # see "The slog.SetDefault problem" section below.
    no-global: "all"
    # context: "all" forces *Context variants (InfoContext, WarnContext,
    # ErrorContext, DebugContext) at the AST level. Replaces the brittle
    # forbidigo regex approach with structural checks that survive
    # multi-line calls, comments, and weird whitespace.
    context: "all"
    # static-msg ensures log messages are string literals, not
    # fmt.Sprintf concatenations. fmt.Sprintf in logs is unsearchable.
    static-msg: true
    # msg-style intentionally NOT enforced: obsotel's own code uses
    # sentence-case-ish messages ("User logged in", "Request failed").
    # Enable "lowercased" here if your service disagrees with that style.

  errorlint:
    # errorf: true (default) flags fmt.Errorf("...: %v", err) when %w
    # would preserve the unwrap chain. Mirrors the README anti-pattern.
    errorf: true

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

  # Note: there is intentionally no `unused.go-mod-run` (or any
  # `go-mod-*` key) — the `unused` linter does not expose such an option
  # in current golangci-lint. Don't add one; it's a phantom flag.
```

## What each rule blocks

| Pattern | Banned because |
|---|---|
| `fmt.Println` / `fmt.Print` | Unstructured. No request_id, no JSON, no Loki-queryable fields. |
| `fmt.Printf` | "Structured" by accident — the human parses the format string. AI can't search it. |
| `log.Println` / `log.Print` / `log.Printf` | Same. The stdlib `log` package predates `slog` and lacks context propagation. |
| `log.Fatal*` | Prints to stderr *and* calls `os.Exit(1)` — no deferred cleanup, no graceful shutdown, no chance to flush logs. Use `slog.Error` + `os.Exit(1)` in `main()`. |
| `log.Panic*` | Prints *and* panics — both undesirable. `slog.Error` and return the error. |
| `slog.Error(...)` / `slog.ErrorContext(...)` | Manual error logging loses the `error_chain` array, AppError Meta fields at top level, and the cross-service shape lock. Always go through `obsotel.LogErr(ctx, ...)`. |
| Any `slog.Info/Warn/Error/Debug` *without* `-Context` (sloglint `context: all`) | Loses request_id / trace_id / span_id correlation. Bare `slog.Info("msg")` silently emits a log line that no tracing backend can correlate to the request. |
| Any global `slog.*` call (sloglint `no-global: all`) | Package-level functions mutate global state. Even legitimate `slog.SetDefault` from `main()` is flagged — see next section. |
| `fmt.Errorf("...: %v", err)` (errorlint) | Destroys the wrap chain. Use `%w` or `obsotel.Wrap(ctx, err, op)`. |
| `slog.Info(fmt.Sprintf(...))` (sloglint `static-msg`) | Dynamic message = unsearchable. Logs must be string literals. |

## The slog.SetDefault problem

`sloglint`'s `no-global: "all"` is the right default — it bans `slog.Info(...)`, `slog.Error(...)`, etc. across the codebase. But it also bans `slog.SetDefault(...)` and `slog.Default()`, which you legitimately need *once* in `main()` to install the obsotel-wired logger as the process default.

Three options, in order of how much we'd recommend them:

### 1. Single `//nolint:sloglint` in `main()` (today's pragmatic answer)

```go
func main() {
    log := obsotel.NewLogger("prod")

    // The one legitimate slog.SetDefault call in the process.
    //nolint:sloglint
    slog.SetDefault(log)

    // ... rest of startup
}
```

Trade-off: one comment, scoped to one line. Documented intent — anyone reading this knows why the suppression exists. If/when `obsotel.SetDefault` lands, this single call site is the only thing that needs to change.

### 2. `obsotel.SetDefault(log)` helper (preferred, future)

If/when the obsotel package adds a thin wrapper:

```go
// inside obsotel — single //nolint:sloglint site
//
//nolint:sloglint
func SetDefault(log *slog.Logger) { slog.SetDefault(log) }
```

then consumers do `obsotel.SetDefault(log)` in `main()` and the suppression lives in one well-commented place inside the library. No scattered comments, no linter implementation leaking into user code. The lint.md doc and the sloglint config don't need to change.

This is the right answer long-term. Until it lands, use option 1.

### 3. Disable `no-global` entirely (do not do this)

You'd also disable the safety net on `slog.Info` / `slog.Error` etc. Throws out the whole point of adding sloglint.

## Excluding tests and vendored obsotel examples

`fmt.Println` in `_test.go` is fine for debugging. The vendored obsotel `examples/` directory intentionally uses bare `slog.Error` to show the shape difference between manual and `LogErr`-based logging — those need exclusion too.

The exact paths depend on where you vendor obsotel in your repo. The block below is a generic pattern; adjust to your layout:

```yaml
issues:
  exclude-rules:
    # Your service's own test files
    - path: _test\.go$
      linters: [forbidigo]

    # Vendored obsotel — adjust the path to where obsotel lives in YOUR repo.
    # Common layouts:
    #   - vendored at root:     "obsotel/"
    #   - vendored under internal/: "internal/obsotel/"
    #   - module dependency:   skip these (your service doesn't see them)
    - path: '.*obsotel/examples/.*'
      linters: [forbidigo, sloglint]   # examples intentionally show anti-patterns
    - path: '.*obsotel/.*_test\.go$'
      linters: [forbidigo, sloglint]   # package's own tests
```

> **For the obsotel repository itself**, the paths are simpler — examples live at the repo root (`examples/`) and tests are flat (`obsotel_test.go`, `tracer_test.go`). The block above matches both layouts.

## Going further: positive obs usage rules

For a long time the next step was a custom `go/analysis` analyzer to report `slog.Error` calls without context propagation. **That analyzer is no longer needed** — `sloglint` covers the same use case (and more) at the AST level, with structural checks instead of regex.

If sloglint ever proves insufficient for a specific obsotel contract (e.g., "every error log must use `LogErr` *and* pass at least one structured attr"), the path is:

1. Add it as a `gocritic` check with a custom AST pattern (lighter than a full analyzer).
2. If that's not expressive enough, build a `go/analysis` plugin following the [official guide](https://golangci-lint.run/contributing/new-linters/), published as a separate Go module.

Don't reach for a custom analyzer before you've collected 2-3 weeks of "what mistakes humans + AI actually make" data. Speculative analyzers rot.

## CI integration

Add a `lint` job to your service's CI pipeline. The Go version must match `go.mod`:

```yaml
lint:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        # Match your go.mod. For obsotel itself this is `1.22`.
        go-version: '1.22'
    - name: golangci-lint
      uses: golangci/golangci-lint-action@v6
      with:
        # v1.62+ recommended; v2.x is current and supported.
        version: v1.62
        # Working directory = service root; .golangci.yml is picked up from here
```

If your service uses Go 1.23+ or newer, bump the version accordingly. golangci-lint v2 dropped support for Go ≤ 1.22 — pick the major version that matches your toolchain.

## TL;DR

- **Banned in production code (forbidigo):** `fmt.Print*`, `log.Print*`, `log.Fatal*`, `log.Panic*`, `slog.Error(...)`, `slog.ErrorContext(...)`.
- **Banned via sloglint (AST, no regex):** bare `slog.Info/Warn/Error/Debug` without `-Context`; any package-level `slog.*` call (including `SetDefault`); dynamic messages via `fmt.Sprintf`.
- **Banned via errorlint:** `fmt.Errorf("...: %v", err)` — use `%w` or `obsotel.Wrap`.
- **`slog.SetDefault` in `main()`:** the one legitimate escape hatch — `//nolint:sloglint` at the single call site today, or `obsotel.SetDefault(log)` once that helper lands.
- **CI enforces, code review doesn't.** AI retrofit is only durable if the linter holds the line.