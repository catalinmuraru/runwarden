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

# Run the server on :8080 (override with LISTEN_ADDR)
go run ./cmd/runwarden

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
internal/web/      embedded UI (built into internal/web/dist/)
examples/          example runner configs and scripts
web/               Vue 3 + Vite + TypeScript source
.github/workflows/ CI
```

## Health checks

`GET /healthz` (liveness) and `GET /readyz` (readiness) return `200 ok`.
