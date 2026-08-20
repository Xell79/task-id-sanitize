# AGENTS.md

Guidance for AI coding agents (Kilo, Claude Code, Codex, etc.) working
in this repository.

## What this is

A CLIProxyAPI (CPA) plugin, built as a Go `c-shared` library (`.so`),
that strips hallucinated `task_id` values from Kilo `Task` tool calls
before the client schema validator runs. Kilo only accepts `task_id`
values starting with `ses` (resume); anything else — typically an
invented UUID — is deleted from JSON bodies, SSE stream deltas, and
XML-style `<parameter name="task_id">` payloads.

Public repo: <https://github.com/Xell79/task-id-sanitize> — MIT licensed.
Treat everything here as publishable: no secrets, no personal paths,
no internal hostnames.

## Layout

| File | Role |
|------|------|
| `plugin.go` | cgo boundary (`//export` ABI), method dispatch, lifecycle, logging. Pure-Go dispatch lives in `pluginCall(method, request, declaredLen)` — keep the cgo shim thin. |
| `sanitize.go` | All sanitization logic: JSON walk, incomplete-fragment regex rewriting, XML strip, SSE framing (`splitSSE`, `sseDataPayload`, `rebuildSSE`). |
| `sanitize_test.go` | Core logic tests, incl. the comma-placement table for cut JSON fragments. |
| `interceptor_test.go` | Envelope-path tests (`interceptJSON` / `interceptStream`) and configure/lifecycle tests. |
| `plugin_call_test.go` | Pure-Go boundary tests for `pluginCall` (method validation, size guard, register/intercept round trips). |
| `live_cpa_test.go` | Optional smoke test against a running CPA; build tag `live`, skipped unless `CPA_API_KEY` is set. Never run it by default. |
| `install.sh` | Root installer: deps, Go toolchain, build, config.yaml edit, systemd restart. Uses embedded Python 3 (stdlib only) for YAML edits and version parsing. |
| `go.mod` | Module `github.com/Xell79/task-id-sanitize`. **No external dependencies — keep it that way** (no `go.sum` needed). |

## Build and test

```bash
CGO_ENABLED=1 go test .        # unit tests
CGO_ENABLED=1 go test -race .  # must stay clean
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -buildmode=c-shared \
  -o task-id-sanitize-v<X.Y.Z>.so .
```

- `CGO_ENABLED=1` is mandatory for every go command (package uses cgo).
- Toolchain: Go **1.27** (pinned in `go.mod`; auto-downloaded via
  GOTOOLCHAIN on older installs). Do not downgrade below 1.27.
- `install.sh` checks: `bash -n install.sh` and `shellcheck install.sh`.

## Hard invariants — do not break

1. **Never `DropChunk` on a payload.** CPA treats a missing first
   payload as `empty_stream`, retries, and marks the upstream auth
   unavailable. Always forward a (possibly rewritten) body.
2. **Stateless per chunk.** No state may be kept across interceptor
   calls (the v0.1.0 per-stream map leaked memory and was removed).
   Do not reintroduce keyed stream state.
3. **`ses…` prefix = keep; everything else = strip.** `isSessionID` is
   the single source of truth for this decision.
4. **Cut-JSON fragments must stay parseable.** The regex consumes the
   member's *leading* comma; when the member is the first *visible*
   key (last non-space byte emitted to the output buffer is `{`, or
   nothing has been emitted yet for bare fragments), the *trailing*
   comma is swallowed instead. Duplicate `task_id` keys chain through
   this rule. Guard against match overlap before slicing
   (`args[last:start]` with `start < last` panics).
5. **No cgo in `_test.go` files.** The toolchain rejects
   `import "C"` in tests ("use of cgo in test not supported"). Test
   the boundary through the pure-Go `pluginCall` instead.
6. **Validate `requestLen` before `C.GoBytes`.** The declared length
   is a host-provided `size_t`; values above `math.MaxInt32` truncate
   to a negative `C.int` and corrupt memory. The guard in
   `cliproxyPluginCall`/`pluginCall` must survive any refactor.
7. **Logging discipline.** `logPath` is only read/written under
   `logMu`; the log file is created `0600` (it contains removed
   `task_id` values). Never log full request bodies or secrets.
8. **`install.sh --dry-run` must not mutate the system** — no package
   installs, no Go tarball, no config writes, no service restarts.

## Conventions

- Versioning: `pluginVersion` in `plugin.go` is the single source of
  truth; `install.sh` parses it out of the source (`source_version`).
  On release: bump `pluginVersion`, update README (version, manual
  build filename, changelog), tag `v<X.Y.Z>` to match.
- Commit style: imperative one-line subjects ("Never DropChunk on
  first SSE payload (v0.1.1).").
- Tests: prefer table-driven subtests (see
  `TestStripIncomplete_CommaPlacement`); when touching
  `stripTaskIDFromIncomplete`, add the new case to that table and
  assert the exact rewritten fragment.
- Rebuild artifacts (`.so`, `.h`, `task-id-sanitize-v*`) are gitignored
  on purpose — never commit binaries.
- `requirements.txt` is pip-format with comments only; do not list OS
  packages as pip specifiers.

## Before you finish

Run and keep green: `gofmt -l .` (empty), `go vet`, `go test -race`.
If you touched `install.sh`, run `bash -n` + `shellcheck` and a
`--dry-run` against a temp `--cpa-dir` with a stub `config.yaml`.
