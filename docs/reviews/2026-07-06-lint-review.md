# Review: lint.md

**Date:** 2026-07-06  
**Scope:** Review of `lint.md` — linting rules that enforce the obsotel package conventions

---

## Overall Assessment

The document is well-written, clearly motivated, and actionable. The rules are sound for enforcing the obsotel contract. Issues below range from medium to low severity.

---

## Issues Found

### 1. Regex for `slog.Error` Rule is Too Narrow and Fragile

**Severity: Medium**

```yaml
- pattern: "slog\\.Error\\([^,]+,\\s*\"err\"\\s*,"
```

This only catches `slog.Error(msg, "err", err)` with that exact field name. It misses:
- `slog.Error(msg, "error", err)` — different key name
- `slog.Error(msg, "cause", err)` — another common name
- Multi-line calls where `"err"` isn't on the same line
- Calls using `slog.String("err", err.Error())` (pre-formatted)

Since any error-level logging should go through `LogErr`, consider banning all `slog.Error` / `slog.ErrorContext` calls:

```yaml
- pattern: "slog\\.Error\\("
  msg: "use obsotel.LogErr instead"
- pattern: "slog\\.ErrorContext\\("
  msg: "use obsotel.LogErr instead"
```

---

### 2. Wrong Exclusion Paths

**Severity: Medium**

```yaml
- path: internal/obsotel/examples/
- path: internal/obsotel/_test\.go$
```

The actual project paths are:
- `examples/` (not `internal/obsotel/examples/`)
- `obsotel_test.go` at root (not `internal/obsotel/_test.go`)

These exclusions won't match anything in the current project structure.

---

### 3. `go-mod-rum` Typo / Invalid Config

**Severity: Low**

```yaml
unused:
  go-mod-rum: true
```

Should be `go-mod-run: true` (though this option may not exist in current golangci-lint versions — the `unused` linter doesn't have this flag). This line should be removed or replaced with valid config.

---

### 4. CI Go Version Doesn't Match `go.mod`

**Severity: Low**

The CI example uses Go 1.21:
```yaml
go-version: '1.21'
```

But `go.mod` specifies `go 1.22` with `toolchain go1.24.4`. The CI should match:
```yaml
go-version: '1.22'
```

---

### 5. `slog.SetDefault` Ban Will Flag Legitimate Usage

**Severity: Low**

The rule bans `slog.SetDefault` everywhere, then says "belongs in main() only." But `forbidigo` can't distinguish *where* a call is made — it'll flag the legitimate call in `main()` too.

Options:
- Accept that the `main()` usage needs a `//nolint:forbidigo` comment (document this explicitly)
- Remove the rule and rely on code review for this one

---

### 6. Missing: Ban on `fmt.Errorf` with `%v` Instead of `%w`

**Severity: Medium**

The README's design principles state "only `%w` preserves the unwrap chain" and the anti-patterns table bans `fmt.Errorf("...: %v", err)`. But there's no lint rule enforcing this.

**Fix:** Add `errorlint`:

```yaml
linters:
  enable:
    - errorlint

linters-settings:
  errorlint:
    errorf: true  # flags %v when %w would preserve the chain
```

---

### 7. Missing: Ban on Bare `slog.Info()` / `slog.Warn()` Without Context

**Severity: Medium**

The README emphasizes "call the `-Context` slog variants" (`InfoContext`, `WarnContext`) to get request_id and trace_id injection. But there's no lint rule banning `slog.Info(...)` (bare, without ctx). This is the most common mistake and the one with the subtlest failure mode — logs silently lose correlation.

Consider adding:

```yaml
- pattern: "slog\\.Info\\("
  msg: "use slog.InfoContext(ctx, ...) to preserve request_id and trace_id — see obsotel README"
- pattern: "slog\\.Warn\\("
  msg: "use slog.WarnContext(ctx, ...) to preserve request_id and trace_id — see obsotel README"
- pattern: "slog\\.Debug\\("
  msg: "use slog.DebugContext(ctx, ...) to preserve request_id and trace_id — see obsotel README"
```

---

### 8. Custom Analyzer Example is Confusing

**Severity: Low**

The code snippet:
- Package is named `usenilerrcheck` but the doc says "report slog.Error calls without context propagation" — name doesn't match purpose
- Imports `golang.org/x/tools/go/analysis/passes/inspect` — the correct import for user code is `golang.org/x/tools/go/analysis`; `/passes` is for built-in passes
- Declares a `run` function but doesn't show the implementation

Not a blocker (it's a pointer for future work), but confusing as-is.

---

## Minor Nits

- The `revive` config is listed under "Recommended additional linters" but isn't in the `enable:` list in the main snippet at the top. Someone copy-pasting the top snippet won't get revive.
- The golangci-lint action `v6` with `version: v1.59` works, but since the project uses Go 1.22+, consider v1.61+ which has better Go 1.22 support.
- The `disable-all: true` + selective `enable:` approach is good practice — explicit is better than implicit.

---

## Summary Table

| # | Severity | Issue |
|---|----------|-------|
| 1 | Medium | slog.Error regex too narrow — misses most error-logging violations |
| 2 | Medium | Exclusion paths don't match actual project structure |
| 3 | Low | `go-mod-rum` typo / invalid config |
| 4 | Low | CI Go version doesn't match go.mod |
| 5 | Low | `slog.SetDefault` ban will flag the legitimate main() usage |
| 6 | Medium | Missing `errorlint` rule for `%v` vs `%w` |
| 7 | Medium | Missing ban on bare `slog.Info()` without context |
| 8 | Low | Custom analyzer example is confusing / incomplete |
