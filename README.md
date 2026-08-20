# task-id-sanitize

CLIProxyAPI C-ABI plugin. Strips hallucinated `task_id` values from
Kilo `Task` tool calls before the client schema validator runs.

Kilo requires `task_id` to start with `ses` (resume only). Some models
invent a UUID on every new Task call, so a subagent never starts. This
plugin deletes any `task_id` that is not a `ses…` session id, in both
JSON and SSE tool-call streams.

Current version: **0.1.2**

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

The plugin itself resets in-memory stream state on `plugin.register`,
`plugin.reconfigure`, and shutdown, and logs `plugin.upgrade` when the
loaded version changes.

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

Listed in [`requirements`](requirements). `install.sh` installs them
unless you pass `--skip-deps`.

Required:

- `git`, `curl`, `ca-certificates`, `python3`
- C toolchain: `gcc`, `make`, libc headers (`libc6-dev` / `glibc-devel` / `musl-dev`)
- Go **>= 1.22** (distro package if new enough, otherwise the official
  `go.dev` tarball)

Optional:

- `systemd` — restart `cli-proxy-api` after install
- `sudo` — write `/opt/cli-proxy-api` when not running as root

There is no Python pip/requirements.txt. Config edits use the Python 3
standard library only.

## Log

Default: `/opt/cli-proxy-api/logs/task-id-sanitize.log`

Override with `TASK_ID_SANITIZE_LOG`.

Events:

- `plugin.configure` — plugin loaded / reconfigured
- `plugin.upgrade` — in-process version change after an upgrade
- `plugin.reset` — stream state cleared (`register` / `reconfigure` / shutdown)
- `plugin.register.payload` — registration JSON returned to CPA
- `task_id.stripped` — UUID (or other non-`ses` value) removed

## Manual build

```bash
CGO_ENABLED=1 go test .
CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -buildmode=c-shared \
  -o task-id-sanitize-v0.1.2.so .
```

Copy the `.so` to the host plugin directory, for example:

`/opt/cli-proxy-api/plugins/linux/amd64/`

Enable it under `plugins.configs.task-id-sanitize` in CLIProxyAPI
`config.yaml` and restart the proxy.

## Tests

```bash
CGO_ENABLED=1 go test .
```

Against a running CLIProxyAPI (optional; skipped unless `CPA_API_KEY` is set):

```bash
CPA_API_KEY=... CPA_BASE_URL=http://127.0.0.1:8320/v1 CPA_MODEL=grok-4.6 \
  CGO_ENABLED=1 go test -tags live -count=1 -timeout 90s .
```
