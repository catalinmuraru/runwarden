# Runwarden

A guarded place to run scripts. Runwarden is a self-hosted web UI for running a catalogue of bash
scripts with typed parameters, live output, execution history and access control. It ships as a
single binary and container image and is meant to run on Kubernetes.

> **Status:** early development. The repository currently contains the project skeleton; features
> below are being built incrementally.

## Features

- **Script catalogue** grouped in the UI, defined by one JSON file per script.
- **Typed parameters**: text, int, list, multiselect, flag, file upload, server file, multiline
  text and IP, with defaults, required fields, min/max, regex and secure (masked) values. Values
  can be passed as arguments, environment variables or stdin.
- **Live output** streamed to an in-browser terminal over WebSocket, with stop and kill.
- **History**: execution logs and metadata kept on disk.
- **Authentication** via OIDC, GitLab or LDAP; per-script allowed users, groups and admins.
- **Audit log** of who ran what with which parameters.

## Script configuration

Each script is described by one JSON file in the runners directory (subdirectories are
scanned too). See [examples/runners/](examples/runners/) for complete examples.

```json
{
  "name": "Restart Service",
  "script_path": "scripts/restart.sh",
  "working_directory": ".",
  "group": "Maintenance",
  "description": "Restarts a service.",
  "allowed_users": ["@ops", "alice"],
  "parameters": [
    { "name": "Service", "param": "--service", "type": "list", "required": true,
      "values": ["api", "worker"] },
    { "name": "Dry run", "param": "--dry-run", "no_value": true }
  ]
}
```

**Script fields:** `name`, `script_path` (path plus optional fixed arguments, never run
through a shell), `working_directory`, `description`, `group`, `allowed_users` / `admin_users`
(user names, `@group` or `*`), `output_files` (globs offered for download), `requires_terminal`,
`scheduling.enabled`, `include` (merge parameters from other files), `hidden` (only usable as an
include).

**Parameter types:** `text` (default), `int`, `list`, `multiselect`, `editable_list`,
`multiline_text`, `file_upload`, `server_file`, `ip`, `ip4`, `ip6` (also `ipv4` / `ipv6`, any
case); flags use `no_value: true`. Unknown types are treated as `text`, with a warning.

**Validation:** `required`, `min` / `max`, `max_length`, `regex` (`{"pattern", "description"}`,
whole-value match), allowed `values` (static list or `{"script": "cmd ${Other}"}`), server file
`file_dir`, `file_recursive`, `file_type`, `file_extensions`, `excluded_files`. Every value is
validated on the server.

**How values reach the script:**

| Option | Effect |
|---|---|
| no `param` | positional argument |
| `param: "--opt"` | `--opt value` |
| `same_arg_param: true` | `--opt=value` as one argument (with `param: "--opt="`) |
| `no_value: true` | `--opt` only when true |
| `multiselect_argument_type` | `single_argument` (joined by `separator`, default `,`), `argument_per_value`, `repeat_param_value` |
| `pass_as` | `argument`, `env_variable` or `stdin`; default is argument **and** env variable |
| `env_var` | env variable name; default `PARAM_<NAME>` |
| `secure: true` | value masked as `******` in logs, history and audit |
| `constant: true` | always the `default`; user input ignored |

Every run also gets `EXECUTION_ID`. Booleans may be written as `true` or `"true"`.

Validate a runners directory before deploying it:

```bash
go run ./cmd/runwarden check-config path/to/runners
```

## Execution

- Scripts are started directly (`script_path` plus arguments), never through a shell. Each run
  gets its own process group; stop sends `SIGTERM` to the whole group and `SIGKILL` after a
  grace period. A run ends when its main process exits; leftover background children are
  terminated.
- stdout and stderr are combined in order. Output is stored per run (readers can follow it live)
  and every `secure` value is replaced by `******` before it is stored.
- stdin stays open for interactive answers; `pass_as: stdin` values are written at start or,
  with `stdin_expected_text`, once that text appears in the output. Scripts run without a
  terminal, so prompts printed by `read -p` are not shown; print prompts with `printf` instead.
- Environment: scripts never inherit Runwarden's environment. They get `PATH`, `HOME`, `LANG`,
  `TZ` (defaults `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`,
  `LANG=C.UTF-8`), every `RUNNER_ENV_<NAME>` variable as `<NAME>`, `EXECUTION_ID`, and their
  parameter variables. Parameters cannot set `PATH`, `HOME`, `IFS`, `ENV`, `BASH_ENV`,
  `LD_*` and similar variables.

| Limit | Default |
|---|---|
| Concurrent runs | 10 |
| Run time | 60 minutes |
| Grace between `SIGTERM` and `SIGKILL` | 10 seconds |
| Stored output per run | 10 MiB (the script keeps running; output is marked truncated) |

## Configuration

All settings come from environment variables. Secrets are never read from files.

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `RUNNERS_DIR` | `./runners` | directory with runner configs (scanned recursively) |
| `BASE_DIR` | working directory | base for relative paths inside runner configs |
| `DATA_DIR` | `./data` | run history and temporary uploads |
| `MAX_CONCURRENT_RUNS` | `10` | runs beyond this are rejected with `429` |
| `RUN_TIMEOUT` | `60m` | default time limit per run |
| `KILL_GRACE` | `10s` | wait between `SIGTERM` and `SIGKILL` |
| `MAX_OUTPUT_BYTES` | `10Mi` | stored output per run (`Ki`, `Mi`, `Gi` suffixes) |
| `MAX_UPLOAD_BYTES` | `100Mi` | maximum upload request size |
| `ALLOWED_ORIGINS` | – | extra hosts allowed to open WebSockets, comma-separated (`ops.example.com,*.example.org`); the server's own host is always allowed |
| `RUNNER_ENV_<NAME>` | – | passed to scripts as `<NAME>` |

Send `SIGHUP` to reload runner configs without a restart. `GET /healthz` is liveness;
`GET /readyz` reports ready once runner configs are loaded.

> **No authentication yet.** Until authentication is added, anyone who can reach the server can
> run every script. Only run it on localhost or a trusted network.

## API

JSON over HTTP. Errors look like `{"error": "...", "details": ["..."]}`; validation errors name
the parameter but never echo the submitted value.

| Method and path | Purpose |
|---|---|
| `GET /api/scripts` | list scripts |
| `GET /api/scripts/{name}` | script with its parameters (form description) |
| `GET /api/scripts/{name}/parameters/{param}/values?Other=value` | allowed values; for script-provided lists, pass the parameters it depends on |
| `POST /api/scripts/{name}/runs` | start a run: JSON `{"values": {...}}`, or `multipart/form-data` with a `values` JSON field plus one file per `file_upload` parameter. `201 {"id"}`, `400` invalid values, `429` busy |
| `GET /api/runs?script=&limit=` | run history, newest first |
| `GET /api/runs/{id}` | run status and details (secure values masked) |
| `GET /api/runs/{id}/output?offset=N` | output from byte `N`; `X-Next-Offset` and `X-Run-Finished` headers |
| `POST /api/runs/{id}/input` | `{"line": "yes"}` writes a line to stdin; `{"close": true}` closes stdin |
| `POST /api/runs/{id}/stop` / `kill` | stop (SIGTERM, then SIGKILL) or kill immediately |
| `GET /api/runs/{id}/files/{name}` | download a file listed in the run's `files` |
| `GET /api/runs/{id}/ws?offset=N` | WebSocket, see below |

WebSocket messages from the server: `{"type":"output","data":"...","offset":N}` (resume later
with `?offset=N`), then `{"type":"result","status":"finished","exit_code":0}`. While the run is
live, the client may send `{"type":"input","line":"..."}`, `{"type":"close_input"}`,
`{"type":"stop"}` or `{"type":"kill"}`.

Run statuses: `running`, `finished`, `failed`, `stopped`, `killed`, `timed_out`, and
`interrupted` (the server stopped while the run was in progress).

```bash
# Start a run
curl -X POST http://localhost:8080/api/scripts/Restart%20Service/runs \
  -H 'Content-Type: application/json' -d '{"values": {"Service": "api"}}'
# → {"id":"20261024-092457.815-0001","status":"running"}

# Follow it live
websocat ws://localhost:8080/api/runs/20261024-092457.815-0001/ws

# Upload a file for a file_upload parameter
curl -X POST http://localhost:8080/api/scripts/Backup%20Database/runs \
  -F 'values={"Database":"orders","Encryption key":"…"}' -F 'Manifest=@manifest.txt'
```

Every action (start, input, stop, kill) is also written to the log as a structured line with
`"audit": true`, the user, the script, the run ID and the masked parameter values. History is
stored under `DATA_DIR/history/<run-id>/` (`meta.json`, `output.log`, `files/`).

## Security model

- Scripts are executed directly with an argument array, never through a shell-built command line.
- Scripts receive an allowlisted environment only, so application secrets never reach them.
- Every parameter is validated server-side.
- Each run gets its own process group, so stopping a run stops all of its children.
- Limits on concurrent runs, output size and run time.
- Secrets are read from environment variables only and are masked in logs, history and audit.
- The container runs as non-root with a read-only root filesystem and no capabilities.

## Tech stack

- **Backend:** Go, standard library `net/http` and `log/slog`, minimal dependencies.
- **Frontend:** Vue 3, Vite and TypeScript, built and embedded into the binary with `go:embed`.
- **Deployment:** container image on GHCR and a Helm chart.
- **Maintenance:** all dependencies pinned and updated through Renovate.

## Development

Develop on Linux or WSL2: process groups and bash script execution are Linux features.

Prerequisites: Go (latest stable), Node.js (see `web/.nvmrc`), `golangci-lint`, `govulncheck`,
and Docker plus `kind` for cluster testing.

```bash
# Build the UI (output is embedded into the binary)
cd web && npm ci && npm run build && cd ..

# Run the server on :8080 with the example scripts
RUNNERS_DIR=examples/runners go run ./cmd/runwarden

# UI with hot reload, proxying /api to the server above
cd web && npm run dev
```

Checks run in CI on every pull request:

```bash
go test -race ./...
golangci-lint run
govulncheck ./...
cd web && npm ci && npm run build
```

Without a UI build the binary still compiles; `/` then answers 503 with a hint.

## Project layout

```
cmd/runwarden/     entry point and check-config command
internal/config/   runner config loader
internal/params/   parameter model, validation, argument/env/stdin building
internal/executor/ process lifecycle, output, stdin, limits
internal/history/  run records on disk
internal/api/      REST and WebSocket API
internal/web/      embedded UI (built into internal/web/dist/)
examples/          example runner configs and scripts
web/               Vue 3 + Vite + TypeScript source
.github/workflows/ CI
```

## Health checks

`GET /healthz` (liveness) and `GET /readyz` (readiness) return `200 ok`.
