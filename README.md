# task-id-sanitize

CLIProxyAPI C-ABI plugin. Strips hallucinated `task_id` values from
Kilo `Task` tool calls before the client schema validator runs.

Kilo requires `task_id` to start with `ses` (resume only). Some models
invent a UUID on every new Task call, so a subagent never starts. This
plugin deletes any `task_id` that is not a `ses…` session id, in both
JSON and SSE tool-call streams.

Current version: **0.1.4** (MIT licensed, see [LICENSE](LICENSE))

## How it works

The plugin registers two capabilities with CLIProxyAPI:
`response_interceptor` and `response_stream_interceptor`.

- Any `task_id` whose value does **not** start with `ses` is deleted:
  - from `Task` tool-call `arguments` (stringified JSON) and `input`
    objects in complete (non-stream) responses;
  - from SSE stream deltas, including arguments fragments that arrive
    cut mid-object (the fragment is rewritten so no dangling comma is
    left behind, whatever position `task_id` occupies);
  - from XML-style `<parameter name="task_id">` payloads, including
    XML embedded in JSON string values.
- `ses…` session ids are passed through untouched (resume flows keep
  working).
- Rewritten chunks are forwarded, never dropped: CPA treats a missing
  first payload as `empty_stream` and disables the upstream auth, so
  `DropChunk` is never set.
- Sanitization is stateless per chunk — the plugin keeps no data
  across calls and leaks nothing when a stream ends without `[DONE]`.

## Install

CLIProxyAPI must already be installed. The installer clones this repo
(or uses a local tree), installs build dependencies, builds the `.so`,
copies it into the CPA plugin directory, enables it in `config.yaml`,
and restarts the service.

```bash
curl -fsSL https://raw.githubusercontent.com/Xell79/task-id-sanitize/master/install.sh \
  | sudo bash
```

### Upgrade

Same command with `--upgrade`. Newer `.so` replaces the old one; previous
`task-id-sanitize-v*.so` files are removed. Same version is a no-op unless
you pass `--force`. A downgrade also needs `--force`.

```bash
curl -fsSL https://raw.githubusercontent.com/Xell79/task-id-sanitize/master/install.sh \
  | sudo bash -s -- --upgrade
```

From a git checkout:

```bash
sudo ./install.sh --src . --upgrade
```

The plugin is stateless across chunks: stream sanitization never holds
data between calls, and `plugin.register` / `plugin.reconfigure` /
shutdown log `plugin.upgrade` when the loaded version changes.

### Supply chain

The default install tracks `master`. For a reproducible install, pin
the ref to a release tag:

```bash
curl -fsSL https://raw.githubusercontent.com/Xell79/task-id-sanitize/master/install.sh \
  | sudo bash -s -- --ref v0.1.4
```

### install.sh flags

| Flag | Meaning |
|------|---------|
| `--repo URL` | Git clone URL (default: this GitHub repo) |
| `--ref REF` | Branch, tag, or commit (default: `master`) |
| `--src DIR` | Existing source tree (skip clone) |
| `--so PATH` | Prebuilt `.so` (skip clone/build) |
| `--cpa-dir DIR` | CLIProxyAPI working directory |
| `--config PATH` | Path to `config.yaml` |
| `--plugin-dir DIR` | Destination directory for the `.so` |
| `--service NAME` | systemd unit (default: `cli-proxy-api`) |
| `--priority N` | `plugins.configs.task-id-sanitize.priority` (default: `2`) |
| `--upgrade` | Replace an older install; prune previous `.so` files |
| `--force` | Reinstall the same version, or allow a downgrade |
| `--no-enable` | Copy the `.so` but leave the plugin disabled |
| `--no-restart` | Do not restart the CLIProxyAPI service |
| `--skip-deps` | Do not install OS packages / Go |
| `--test` | Run `go test` before installing |
| `--prune-old` | Remove older `task-id-sanitize-v*.so` files |
| `--dry-run` | Print actions; do not write files or restart |
| `-h`, `--help` | Show help |

Detection order for the CPA directory: `--cpa-dir` / `CPA_DIR`, then the
systemd unit working directory, then `/opt/cli-proxy-api`,
`/usr/local/cli-proxy-api`, `~/cli-proxy-api`, `~/CLIProxyAPI`, `$PWD`.

The plugin directory is `<plugins.dir>/<os>/<arch>`, for example
`/opt/cli-proxy-api/plugins/linux/amd64/`.

### Environment

| Variable | Same as |
|----------|---------|
| `TASK_ID_SANITIZE_REPO` | `--repo` |
| `TASK_ID_SANITIZE_REF` | `--ref` |
| `CPA_DIR` | `--cpa-dir` |
| `CPA_CONFIG` | `--config` |
| `CPA_SERVICE` | `--service` |
| `TASK_ID_SANITIZE_LOG` | Runtime log path (not an installer flag) |

### Dependencies

Listed in [`requirements.txt`](requirements.txt). `install.sh` installs
the OS packages unless you pass `--skip-deps`. The file follows the pip
requirements.txt format (comments and blank lines only): this plugin has
no PyPI dependencies, so `pip install -r requirements.txt` is a no-op.

Required:

- `git`, `curl`, `ca-certificates`, `python3`
- C toolchain: `gcc`, `make`, libc headers (`libc6-dev` / `glibc-devel` / `musl-dev`)
- Go **>= 1.27** (distro package if new enough, otherwise the official
  `go.dev` tarball; supported until 2027)

Optional:

- `systemd` — restart `cli-proxy-api` after install
- `sudo` — write `/opt/cli-proxy-api` when not running as root

Config edits use the Python 3 standard library only. Do not put OS
package names (`git`, `gcc`, …) on uncommented lines in
`requirements.txt`: pip would treat them as PyPI specifiers.

## Log

Default: `/opt/cli-proxy-api/logs/task-id-sanitize.log`

Override with `TASK_ID_SANITIZE_LOG`. The file is created with `0600`
permissions: it records removed `task_id` values and model names.

Events:

- `plugin.configure` — plugin loaded / reconfigured
- `plugin.upgrade` — in-process version change after an upgrade
- `plugin.reset` — lifecycle marker (`register` / `reconfigure` / shutdown)
- `plugin.register.payload` — registration JSON returned to CPA
- `task_id.stripped` — UUID (or other non-`ses` value) removed

## Manual build

```bash
CGO_ENABLED=1 go test .
CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -buildmode=c-shared \
  -o task-id-sanitize-v0.1.4.so .
```

Copy the `.so` to the host plugin directory, for example:

`/opt/cli-proxy-api/plugins/linux/amd64/`

Enable it under `plugins.configs.task-id-sanitize` in CLIProxyAPI
`config.yaml` and restart the proxy.

## Tests

```bash
CGO_ENABLED=1 go test .        # unit tests
CGO_ENABLED=1 go test -race .  # with the race detector
```

The suite covers comma placement in cut JSON fragments (including
duplicate `task_id` keys and bare fragments without an opening brace),
nested objects, XML (raw and JSON-escaped), SSE framing (CRLF,
multi-data-line collapse, keepalive), the plugin dispatch boundary, and
log file permissions.

Against a running CLIProxyAPI (optional; skipped unless `CPA_API_KEY` is set):

```bash
CPA_API_KEY=... CPA_BASE_URL=http://127.0.0.1:8320/v1 CPA_MODEL=grok-4.6 \
  CGO_ENABLED=1 go test -tags live -count=1 -timeout 90s .
```

## Changelog

- **0.1.4** — deploy fixes: the plugin log is tightened to `0600` even
  when the file already exists (files created by older versions kept
  their loose mode); `install.sh` removes its temporary clone
  directory on exit.
- **0.1.3** — correctness and security wave: `task_id` as the first key
  no longer produces invalid JSON (incl. duplicate keys and bare SSE
  fragments); XML strip is no longer discarded when the JSON rewrite
  also fires; JSON-escaped XML (`name=\"task_id\"`) is matched;
  case-insensitive keys (`Task_ID`) are stripped; stateless sanitizer
  (no per-stream map); cgo request-length guard; log written `0600`;
  race-free reconfigure; `install.sh --dry-run` no longer installs
  packages or Go; Go 1.27; MIT license.
- **0.1.2** — installer (`install.sh`) with upgrade/prune support,
  interceptor unit tests, optional live CPA smoke test, pip-format
  `requirements.txt`.
- **0.1.1** — never `DropChunk` on the first SSE payload (fixes
  `empty_stream` → upstream auth marked unavailable).
- **0.1.0** — initial import.

## License

[MIT](LICENSE) — Copyright (c) 2026 Xell79
