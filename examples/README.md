# Examples

Runnable examples showing concrete log output from the `obsotel` package.

## `usage_guide/`

Linear walk through the entire `obsotel` public API — every exported
function, type, and method, called in a single `go run`. Use this as
a coding reference: copy-paste the section that matches what you're
building. Run with:

```bash
go run ./examples/usage_guide/
```

Nine sections, in order: logger construction → constants → context
binding → error construction → error logging → HTTP server middleware
→ HTTP client + retry → tracer setup + spans → metrics. Section
banners go to stdout for navigability; actual log lines and OTel
spans go to stderr (the way they would in production).

## `logerr_comparison/`

Side-by-side demo of `bare slog.Error` vs `obsotel.LogErr` against the same
`AppError` chain. Run with:

```bash
go run ./examples/logerr_comparison/
```

Output shows the four key contrasts on the same AppError chain
(`AppError.Op="load_user"`, `Kind="infra_error"`, `Meta={user_id, tenant}`
wrapping a `connection refused` root):

| Case | What it shows |
|---|---|
| A — bare `slog.Error` | `err` becomes a **nested object** with `op`/`kind`/`user_id` buried inside. No `error_chain` array. |
| B — `obsotel.LogErr` | `op` and `kind` surface **at the top level** alongside Meta fields. `error_chain` is a queryable JSON array of all three unwrapped errors. |
| C — shape drift | Three devs writing the same intent with bare slog produce three different field names (`err`/`error`/`e`). `LogErr` locks the shape. |
| D — `error_chain` only | The pipeline queries (LogQL / ELK / SQL) that only the structured array enables. |

The demo writes raw JSON to stdout so you can pipe it into `jq` for
further inspection. Lines are indented by the demo for readability; in
production each line is one log record that your collector ships as-is.

## Which demo to run first?

- **Learning the API** → `usage_guide/`
- **Learning why the contract exists** → `logerr_comparison/`
- **Both** → logerr_comparison to see the contrast, then usage_guide to see the whole surface.
