# task-id-sanitize

CLIProxyAPI C-ABI plugin. Strips hallucinated `task_id` values from
Kilo `Task` tool calls before the client schema validator runs.

Kilo requires `task_id` to start with `ses` (resume only). Some models
invent a UUID on every new Task call, so a subagent never starts. This
plugin deletes any `task_id` that is not a `ses…` session id, in both
JSON and SSE tool-call streams.

## Log

Default: `/opt/cli-proxy-api/logs/task-id-sanitize.log`

Override with `TASK_ID_SANITIZE_LOG`.

Events:

- `plugin.configure` — plugin loaded / reconfigured
- `task_id.stripped` — UUID (or other non-`ses` value) removed

## Deploy

```bash
CGO_ENABLED=1 go test .
CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -buildmode=c-shared -o task-id-sanitize-v0.1.1.so .
```

Copy the `.so` to the host plugin directory, for example:

`/opt/cli-proxy-api/plugins/linux/amd64/`

Enable it under `plugins.configs.task-id-sanitize` in CLIProxyAPI
`config.yaml` and restart the proxy.
