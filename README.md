# Runwarden

A guarded place to run scripts. Runwarden is a self-hosted web UI for running a catalogue of bash
scripts with typed parameters, live output, execution history and access control. It ships as a
single binary and container image and is meant to run on Kubernetes.

> **Status:** early development. Server, web UI, container image and Helm chart work; no release
> has been tagged yet.

## Features

- **Script catalogue** grouped in the UI, defined by one JSON file per script.
- **Typed parameters**: text, int, list, multiselect, flag, file upload, server file, multiline
  text and IP, with defaults, required fields, min/max, regex and secure (masked) values. Values
  can be passed as arguments, environment variables or stdin.
- **Live output** streamed to an in-browser terminal over WebSocket, with stop and kill.
- **History**: execution logs and metadata kept on disk.
- **Authentication** via GitLab, OIDC or LDAP, with server-side sessions.
- **Access control** by roles (guest, developer, consultant, devops and your own), environments
  and script tags: rules give a role view or run access per script and environment, and
  time-limited exceptions give one user more for a while. All of it is managed in the Admin area.
- **Audit log** of who ran what with which parameters, also browsable by admins in the UI.
- **Admin overview**: everyone's runs, who is signed in, and the activity log.
- **Browser notifications** when your runs finish.

## Web UI

The page has two parts: the script list on the left, always visible, and the selected page on
the right.

- **Script list**: the scripts you may see, by group (click a group to open or close it), with a
  search box (name, group, description or tag) and links to Running, History and Admin below.
  Hovering a name shows its description. On narrow screens the list opens from the ☰ button.
- **Script page**: description and a form generated from the parameters (lists that depend on
  other fields reload as you type, server files are browsed, uploads attach files); server-side
  validation errors appear next to the fields. Environments you may only view are marked
  "view only", and Execute is disabled (with the reason) while one is selected. Execute shows the
  run's output under the form: live terminal with colours, an input line for scripts that ask
  questions, Stop and Kill, and downloadable output files. Users without a role are told to ask
  an administrator for access.
- **Run page** (from History or a run's Details link): the same output plus the masked arguments
  and values, and "Run again" with the same values. Reloading or losing the connection resumes
  the output where it stopped.
- **Running**: runs in progress that you may see, with Stop on your own (devops: on every run).
  The Running link shows how many are running.
- **History**: runs you may see (your own, and every run of a script in an environment where you
  have view access; everything for devops), filterable by script, status and user.
- **Admin** (devops only): **Users** — everyone who signed in, their roles (changed here), groups
  from the identity provider (for reference), status, last activity, address, runs and sessions,
  and users given roles before their first sign-in; **Roles**; **Rules** per role;
  **Exceptions** (active and ended); **Environments** (with a box to test which environment a
  value belongs to); **Secrets** — which declared secrets each environment has or misses (names
  only); **Access** — what a user or role may do, script by environment, and which
  rule grants it; **Activity** — the audit trail (runs, input, stop/kill, sign-ins, policy
  changes, reloads, clean-ups), filterable by user, action, result and script; **Appearance** —
  the colour theme for everyone, the sign-in page included: System (follows each user's
  operating system, the default), Light or Dark. It is kept in `DATA_DIR/settings.json`; other
  users get a new theme on their next page load. **Export policy** downloads the policy as YAML;
  **Import policy** replaces it after showing what changes.
- **Notifications**: the bell under the script list turns on browser notifications for your own runs
  (needs HTTPS or localhost). They arrive while a Runwarden tab is open, even in the background.

The UI is a Vue 3 single-page app embedded in the binary and served with a strict
Content-Security-Policy.

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
  "tags": ["developer", "devops"],
  "environment_parameters": { "Environment": "target" },
  "parameters": [
    { "name": "Environment", "type": "list", "required": true,
      "values": ["development", "acceptance", "production"] },
    { "name": "Service", "param": "--service", "type": "list", "required": true,
      "values": ["api", "worker"] },
    { "name": "Dry run", "param": "--dry-run", "no_value": true }
  ]
}
```

**Script fields:** `name`, `script_path` (path plus optional fixed arguments, never run
through a shell), `working_directory`, `description`, `group`, `tags` (the script's audience, used
by access rules as `tag:<tag>`), `environment_parameters` (see *Access control*), `secrets` (names of
the secrets a run receives, see *Secrets*), `kubernetes` (`true`: the run gets its environment's
cluster, see *Kubernetes clusters per environment*), `output_files` (globs offered for download), `timeout` (this
script's time limit, e.g. `"4h"` or a number of minutes; default `RUN_TIMEOUT`, at most
`RUN_TIMEOUT_MAX`), `requires_terminal`,
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
- **Terminal**: scripts with `requires_terminal: true` (the default) run on a pseudo-terminal
  (120×40, `TERM=xterm-256color`), so `read -p` prompts, colours and tools that need a TTY work.
  With `requires_terminal: false` they get pipes: stdout and stderr combined in order.
- Output is stored per run (readers can follow it live). Every `secure` value is replaced by
  `******` before it is stored, and so is every match of the script's `redact_patterns` (RE2
  regexes, applied per line) — use them for secrets a script prints itself, e.g.
  `"redact_patterns": ["ghp_[A-Za-z0-9]+", "(?i)password: \\S+"]`.
- stdin stays open for interactive answers. Input sent as hidden (the UI's "hide" box, or
  `"secret": true` in the API) is masked wherever it appears in the output, including the
  terminal's echo. `pass_as: stdin` values are written at start or, with `stdin_expected_text`,
  once that text appears in the output.
- Each run gets a private, empty `TMPDIR`, removed when the run ends, so concurrent runs don't
  collide on temporary files (use `mktemp` / `$TMPDIR` instead of fixed names in `/tmp`).
- **Private HOME** (`RUN_HOME=per-run`, the default): each run, and each call of a dropdown
  helper script, also gets its own empty `HOME`, with `KUBECONFIG=$HOME/.kube/config` and
  `AZURE_CONFIG_DIR=$HOME/.azure`, removed when it ends. A kube context, `az` login or git
  credentials written by one run never reach another run, e.g. one user working on production
  while another works on development. Scripts therefore log in per run (`az login`); runners
  with `"kubernetes": true` get their cluster from Runwarden (see *Kubernetes clusters per
  environment*) instead of `az aks get-credentials`; files that must survive go to `$RUNWARDEN_WORKSPACE`. `HOME_SKELETON`
  is copied into every new HOME (e.g. `.gitconfig`, `.ssh/known_hosts`). `RUN_HOME=shared`
  keeps one HOME for all runs, for scripts that are not migrated yet. All runs share one user ID;
  *Isolation between runs* below keeps them apart.
- **Isolation between runs** (`ISOLATION`, default `auto`): on Linux with Landlock (kernel 5.13+),
  each run and each dropdown helper is sandboxed without privileges. It may read the system
  (`/usr`, `/etc`, `/proc`, ...), `BASE_DIR`, `ISOLATION_READ_PATHS` (add `file_dir` directories
  of `server_file` parameters that lie elsewhere) and its own uploaded files, and write only its own
  `HOME`/`TMPDIR`, its user's workspace and `ISOLATION_WRITE_PATHS`. It cannot read
  `DATA_DIR` (history, sessions, policy), `SECRETS_DIR`, `KUBE_DIR`, another run's files or
  another user's workspace, nor another run's `/proc/<pid>/environ` or memory; with kernel 6.12+
  (Landlock ABI 6) it cannot signal other runs either. Still visible to other runs: their
  command lines, so `check-config` warns about `secure` parameters passed as arguments (use
  `"pass_as": "env_variable"`). Memory, CPU and process count are shared by all runs
  (`RUN_MAX_PROCS` sets `RLIMIT_NPROC`, which counts all processes of the user ID: a guard, not
  a quota). At startup Runwarden checks that no private directory lies below a path every run can
  access and runs a sandboxed self-test in the run and workspace directories (Landlock does not
  work on every filesystem, e.g. 9p). `auto` runs scripts unsandboxed with a warning in the log
  when any of this fails, or when Runwarden runs as root; `required` refuses to start; `off`
  disables the sandbox.
- **Workspace per user** (`WORKSPACE_DIR`): a run gets `RUNWARDEN_WORKSPACE=<dir>/<user>`
  (created on first use, `0700`), for files that must survive between runs, and
  `RUNWARDEN_SHARED=<dir>/shared`, read-only for runs (team tools, filled by operators). Dropdown
  helpers get only `RUNWARDEN_SHARED`. User names with characters other than letters, digits,
  `.`, `-` and `_` get a hash suffix.
- **One run per user** (`MAX_RUNS_PER_USER`, default 1, admins included): a second start answers
  `429` with the ID of the run in progress; other users are not affected.
- Dropdown helper scripts run at most `VALUES_MAX_PER_USER` (2) at a time per user and
  `VALUES_MAX_GLOBAL` (10) in total (`429` beyond), and their output is reused for
  `VALUES_CACHE_TTL` (60 s), so forms stay fast and cannot be used to flood the server.
- Environment: scripts never inherit Runwarden's environment. They get `PATH`, `HOME`, `LANG`,
  `TZ` (defaults `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`,
  `LANG=C.UTF-8`), every `RUNNER_ENV_<NAME>` variable as `<NAME>`, `EXECUTION_ID`,
  `RUNWARDEN_USER` (who started the run), `TMPDIR`, `TERM` on a terminal,
  `RUNWARDEN_WORKSPACE` and `RUNWARDEN_SHARED` with `WORKSPACE_DIR`, their parameter
  variables and the secrets they declare (see *Secrets*). Parameters cannot set `PATH`, `HOME`,
  `IFS`, `ENV`, `BASH_ENV`, `LD_*`, `RUNWARDEN_USER`, `TMPDIR`, `TERM`, a declared secret and
  similar variables.

| Limit | Default |
|---|---|
| Concurrent runs | 10 |
| Run time | 60 minutes, or the script's `timeout` |
| Grace between `SIGTERM` and `SIGKILL` | 10 seconds |
| Stored output per run | 10 MiB (the script keeps running; output is marked truncated) |

## Configuration

All settings come from environment variables. Secrets are never read from files.

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `RUNNERS_DIR` | `./runners` | directory with runner configs (scanned recursively) |
| `BASE_DIR` | working directory | base for relative paths inside runner configs |
| `DATA_DIR` | `./data` | run history, temporary uploads, policy, sessions and UI settings (`settings.json`) |
| `MAX_CONCURRENT_RUNS` | `10` | runs beyond this are rejected with `429` |
| `RUN_TIMEOUT` | `60m` | default time limit per run |
| `RUN_TIMEOUT_MAX` | `24h` | largest `timeout` a runner config may set (larger values are a config error) |
| `KILL_GRACE` | `10s` | wait between `SIGTERM` and `SIGKILL` |
| `MAX_OUTPUT_BYTES` | `10Mi` | stored output per run (`Ki`, `Mi`, `Gi` suffixes) |
| `MAX_UPLOAD_BYTES` | `100Mi` | maximum upload request size |
| `ALLOWED_ORIGINS` | – | extra hosts allowed to open WebSockets, comma-separated (`ops.example.com,*.example.org`); the server's own host is always allowed |
| `RUNNER_ENV_<NAME>` | – | passed to every script as `<NAME>`: for settings, **not secrets** (the server warns about names that look like secrets) |
| `SECRETS_DIR` | `/runwarden/secrets` | secrets per environment, see *Secrets*; not below `/etc` or another path runs can read |
| `KUBE_DIR` | `/runwarden/kube` | kubeconfig per environment, see *Kubernetes clusters per environment* |
| `ISOLATION` | `auto` | Landlock sandbox per run: `auto`, `required` or `off`, see *Isolation between runs* |
| `ISOLATION_READ_PATHS`, `ISOLATION_WRITE_PATHS` | – | more absolute paths every sandboxed run may read, or write (shared by all runs), comma-separated |
| `RUN_MAX_PROCS` | `0` | `RLIMIT_NPROC` of sandboxed runs (`0`: unchanged) |
| `WORKSPACE_DIR` | – | per-user workspaces, see *Workspace per user* |
| `TRUSTED_PROXIES` | – | CIDRs of your ingress / load balancer; only they may set `X-Forwarded-For` (client address in audit logs and the sign-in rate limit) |
| `HISTORY_MAX_AGE` | `90d` | remove finished runs and activity-log days older than this (`d` or Go durations; `0` keeps them) |
| `HISTORY_MAX_RUNS` | `0` | keep at most this many runs (`0`: no limit); running runs are never removed |
| `HEALTH_ADDR` | `:8081` | listener for `/healthz` and `/readyz` (not served on `LISTEN_ADDR`) |
| `MAX_RUNS_PER_USER` | `1` | runs one user may have in progress (`0`: no limit) |
| `RUN_HOME` | `per-run` | `per-run`: private HOME per run; `shared`: one HOME for all runs |
| `HOME_SKELETON` | – | directory copied into every private HOME |
| `VALUES_MAX_PER_USER`, `VALUES_MAX_GLOBAL` | `2`, `10` | concurrent dropdown helper scripts |
| `VALUES_CACHE_TTL` | `60s` | reuse helper script output (`0`: no cache) |
| `ACTIVITY_MAX_DAY_BYTES` | `50Mi` | after this, a day's activity-log file no longer records failed sign-ins |
| `LOGIN_MAX_FAILURES_PER_IP`, `LOGIN_MAX_FAILURES_GLOBAL` | `20`, `100` | failed sign-ins per address (15 min) and in total (1 min) before refusing (`0`: no limit) |

`TRUSTED_PROXIES` entries wider than `/8` (IPv4) or `/32` (IPv6) are refused, so a typo cannot let
every client choose its own address.

Send `SIGHUP` to reload runner configs without a restart. `GET /healthz` (on `HEALTH_ADDR`) is
liveness; `GET /readyz` reports ready once runner configs are loaded.

## Logs and audit trail

Runwarden writes one JSON object per line to stdout in
[Elastic Common Schema](https://www.elastic.co/guide/en/ecs/current/index.html) (ECS 8.11):
`@timestamp`, `log.level`, `message`, `ecs.version`, `service.name`/`service.version`, and
`event.dataset` — `runwarden.audit` for the audit trail, `runwarden.app` for everything else.
Custom fields live under `runwarden.*`.

Audit events answer who did what and with which result:

| `event.action` | When | `event.outcome` |
|---|---|---|
| `run-start` | a run was started — or refused (`event.reason`: not allowed, unknown script, invalid parameters, too many runs) | success / failure |
| `run-end` | a run ended: status, exit code, duration, who started it | success if it finished with exit code 0 |
| `run-input`, `run-stop`, `run-kill` | a user sent input to, stopped or killed a run | success / failure |
| `login`, `logout` | sign-in (with the failure reason) and sign-out | success / failure |
| `config-reload` | runner configs reloaded (`SIGHUP`) | success / failure |
| `history-cleanup` | old runs removed by the retention settings | success |

Fields: `user.name`, `user.roles` (groups), `source.ip`, `user_agent.original`,
`event.category`/`event.type`/`event.duration` (ns), `process.exit_code`, `process.args`
(masked), `runwarden.run.id`, `runwarden.run.status`, `runwarden.run.parameters` (masked values
as one JSON string, so arbitrary parameter names can't explode the index mapping) and
`runwarden.script.name`. Secure values, hidden input and passwords never appear.

```json
{"@timestamp":"2026-10-25T04:03:11.765Z","log.level":"info","message":"run 20261025-040310.753-0001 of \"Restart Service\" by alice: finished (exit code 0)","ecs.version":"8.11.0","service":{"name":"runwarden","version":"0.2.0"},"event":{"kind":"event","category":["process"],"type":["end"],"action":"run-end","outcome":"success","dataset":"runwarden.audit","duration":1010455273},"user":{"name":"alice","roles":["ops"]},"source":{"ip":"198.51.100.23"},"process":{"exit_code":0,"args":["--service","api","--timeout","30"]},"runwarden":{"run":{"id":"20261025-040310.753-0001","status":"finished","parameters":"{\"Service\":\"api\",\"Timeout seconds\":\"30\"}"},"script":{"name":"Restart Service"}}}
```

The same audit events are also kept in `DATA_DIR/activity/` (one JSON-lines file per day, plus
`users.json` for the users index) for the Admin pages, and removed after `HISTORY_MAX_AGE`. The
log on stdout stays the system of record.

### Filebeat

The lines are ready for Filebeat's `ndjson` parser; no ingest pipeline is needed. On
Kubernetes, with autodiscover:

```yaml
filebeat.autodiscover:
  providers:
    - type: kubernetes
      node: ${NODE_NAME}
      templates:
        - condition:
            equals:
              kubernetes.labels.app.kubernetes.io/name: runwarden
          config:
            - type: filestream
              id: runwarden-${data.kubernetes.container.id}
              paths:
                - /var/log/containers/*-${data.kubernetes.container.id}.log
              prospector.scanner.symlinks: true
              parsers:
                - container: ~
                - ndjson:
                    target: ""
                    overwrite_keys: true
                    expand_keys: true
                    add_error_key: true
```

Outside Kubernetes, point a `filestream` input with the same `ndjson` parser at the file stdout
is written to. Search the audit trail in Kibana with `event.dataset : "runwarden.audit"`.

## Authentication and authorization

`AUTH_METHODS` lists the sign-in methods, in the order the sign-in page shows them; the server
refuses to start without one. Several can be enabled at once (e.g. `gitlab,ldap`).

| Variable | Default | Meaning |
|---|---|---|
| `AUTH_METHODS` | **required** | `gitlab`, `oidc`, `ldap` (comma-separated), or `none` alone for local development (no sign-in, everyone is an admin; also needs `ALLOW_NO_AUTH=true`). `AUTH_TYPE` (one method) is still read, with a deprecation warning |
| `INITIAL_ADMIN` | `false` | built-in administrator `admin@local`, see below |
| `PUBLIC_URL` | – | external URL, e.g. `https://runwarden.example.com` (gitlab, oidc); the redirect URI to register is `PUBLIC_URL/auth/callback` for every provider |
| `SESSION_KEY` | – | 32 random bytes, base64 (`openssl rand -base64 32`); required unless `none` |
| `SESSION_TTL` | `12h` | how long a sign-in lasts |
| `COOKIE_SECURE` | `true` (`false` if `PUBLIC_URL` is `http://`) | `Secure` flag on cookies |
| `GITLAB_URL`, `GITLAB_CLIENT_ID`, `GITLAB_CLIENT_SECRET` | `https://gitlab.com` | GitLab application (scopes `openid profile email`); user name from `nickname`, groups (full paths) from `groups`, falling back to `groups_direct` |
| `GITLAB_REQUIRED_GROUP` | – | only members of this group or its subgroups (full path, e.g. `company/ops`) may sign in |
| `GITLAB_LABEL` | `GitLab` | button text on the sign-in page |
| `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | – | OpenID Connect client (confidential, authorization code flow) |
| `OIDC_SCOPES` | `openid profile email` | extra scopes, space-separated (e.g. `groups`) |
| `OIDC_USERNAME_CLAIM`, `OIDC_GROUPS_CLAIM` | `preferred_username`, `groups` | claims read from the ID token, then userinfo |
| `OIDC_REQUIRED_GROUP`, `OIDC_LABEL` | –, `Single sign-on` | as for GitLab |
| `LDAP_URL` | – | `ldaps://host:636` or `ldap://host:389` with `LDAP_START_TLS=true`; plain `ldap://` is refused unless `LDAP_INSECURE=true` (local testing) |
| `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD` | – | service account used to find users |
| `LDAP_USER_BASE_DN`, `LDAP_USER_FILTER` | –, `(uid={username})` | where and how to find a user |
| `LDAP_GROUP_BASE_DN`, `LDAP_GROUP_FILTER`, `LDAP_GROUP_NAME_ATTR` | –, `(member={dn})`, `cn` | group search |
| `LDAP_USER_GROUPS_ATTR` | – | instead of a group search, read group DNs from this user attribute (e.g. `memberOf`) |
| `LDAP_LABEL` | `Directory` | button text on the sign-in page |

Sign-in:

- **Without a session, the web port offers only the sign-in page**: every page path shows it,
  only the files it needs are served (the app's code is not), and the API answers `401`. The
  page lists the enabled methods; the visitor picks one, then is sent to the provider (GitLab,
  OIDC) or gets a user name and password form (LDAP, built-in administrator). After signing in
  they return to the page they asked for. Provider errors (refused, not in the required group,
  expired) come back to the sign-in page as `?sign_in_error=` and are explained there.
- API: `GET /auth/config` lists the methods (`{"methods": [{"id", "label", "kind"}]}`);
  `GET /auth/login?method=gitlab&next=/path` starts a provider sign-in (authorization code flow
  with PKCE, nonce and state checks; the provider returns to `/auth/callback`);
  `POST /auth/login` with `{"method": "ldap", "username": "...", "password": "..."}` signs in
  with a password. After 5 failed attempts for a user name from one address, further attempts
  are refused for 15 minutes.
- Flood protection (all methods): after `LOGIN_MAX_FAILURES_PER_IP` (20) failed sign-ins from one
  address in 15 minutes, or `LOGIN_MAX_FAILURES_GLOBAL` (100) from everywhere in one minute,
  sign-ins are refused with `429` without asking the directory. Only the first refusal per
  address and window is audited, and once a day's activity-log file reaches
  `ACTIVITY_MAX_DAY_BYTES` (50Mi) failed sign-ins are kept only in the application log.
- `GET /api/me` returns the signed-in user; `POST /auth/logout` signs out (and returns the
  provider's logout URL when there is one). Audit events record the method as
  `runwarden.auth.method`.

**Sessions** are kept on the server (`DATA_DIR/sessions.json`, stored as hashes of the cookie
tokens, so the file cannot be used to sign in) and survive restarts. The cookie holds only a
random token (`HttpOnly`, `Secure`, `SameSite=Lax`). Signing out ends the session on the server,
so a copied cookie stops working; admins can sign a user out of every browser from the Admin
page ("Sign out everywhere", audited as `session-revoke`). API requests without a session get
`401`; state-changing requests from other sites are refused with `403`.

**Built-in administrator** (`INITIAL_ADMIN=true`, Helm `auth.initialAdmin`), for setting up the
first admins and for emergencies:

- the sign-in page offers "Administrator"; the account is `admin@local` (no other method may use
  that name) and has the devops role;
- at every start a new random password is written to `DATA_DIR/initial-admin-password` (mode
  0600). It is never logged; the log names only the file:
  `kubectl exec deploy/runwarden -- cat /var/lib/runwarden/initial-admin-password`;
- it is the only way to make the first admin: in the Admin area it gives the devops role to the
  first user (who may not have signed in yet), who then sets up roles and rules for everyone else;
- set `INITIAL_ADMIN=false` (or remove it) once a devops user can sign in: the file is deleted,
  the method disappears and every session of the built-in administrator ends. While it is off,
  the last devops user cannot lose the role (so the Admin area cannot be locked), and the server
  warns at start if nobody has it;
- it may be the only method (`AUTH_METHODS` empty) to set up a new installation.

The signed-in user is passed to scripts as `RUNWARDEN_USER`.

### Local identity providers

`deploy/dev/compose.yaml` starts Keycloak (`http://localhost:8180`, realm `runwarden`) and
OpenLDAP (`ldap://localhost:1389`) with test users `alice` (group `ops`) and `bob`.
`deploy/dev/test-auth.sh` starts them, runs the auth integration tests and stops them again.

```bash
docker compose -f deploy/dev/compose.yaml up -d
AUTH_METHODS=oidc PUBLIC_URL=http://localhost:8080 SESSION_KEY=$(openssl rand -base64 32) \
  OIDC_ISSUER_URL=http://localhost:8180/realms/runwarden OIDC_CLIENT_ID=runwarden \
  OIDC_CLIENT_SECRET=dev-client-secret RUNNERS_DIR=examples/runners go run ./cmd/runwarden
# open http://localhost:8080, choose "Single sign-on" and sign in as alice / alice-password
```

## Access control

Who may do what is kept in an **access policy** on the data volume (`DATA_DIR/policy.json`,
mode 0600), edited in the Admin area. Every change is audited (`policy-change`, with what
changed), and the policy can be exported and imported as YAML.

- **Roles**: built-in `guest` (every user without a role: signed in, sees nothing), `developer`,
  `consultant` and `devops` (every script, every environment, every run and the Admin area), plus
  any roles you add. Roles are assigned to users by hand in the Admin area; groups from GitLab,
  OIDC or LDAP are shown but **never grant rights**. A user may have several roles. Names match
  case-insensitively.
- **Environments** (e.g. development, acceptance, production) group the values that scripts'
  environment parameters can take. A value belongs to the first environment whose name or
  pattern matches (`dev*`, `uat?`, case-insensitive); values that match nothing count as the
  *unknown environment* (by default the last, most sensitive one).
- **Environment parameters**: a runner config says which of its parameters select an
  environment, and how: `target` (the run changes it: needs **run** access there) or `source`
  (the run only reads from it, e.g. the source of a copy: needs **view** access there).
  `check-config` warns about parameters that look like environments but are not declared.
  Scripts without environment parameters belong to the pseudo environment `-` ("scripts without
  environment").
- **Rules** give a role `view` or `run` access to scripts (`*`, names, or `tag:<tag>`) in
  environments (`*` or names). Rules only grant; when several match, the highest access wins.
  Nothing matching means no access.
- **Exceptions** give one user `view` or `run` access to scripts in environments until a date,
  with a mandatory reason (at most `POLICY_MAX_EXCEPTION_DAYS`). They end by themselves
  (audited as `exception-expired`) or early from the Admin area, and stay listed for 30 days.

What each access allows:

| | view | run |
|---|---|---|
| see the script, its form and dropdown values for that environment | ✓ | ✓ |
| see every run of the script there, with its output and files | ✓ | ✓ |
| start the script with that environment as target | | ✓ |

Users always see and control (input, stop, kill) their own runs; viewers cannot control other
users' runs (`403`); devops can. A run's environments are recorded with it (and in its audit
events as `runwarden.environments`). Scripts and runs a user may not see answer `404`; a start
refused by the rules answers `403` with the reason. Changes apply at once, without signing in
again.

Example: developers run their scripts on development and look at the other environments,
consultants run theirs everywhere, and one developer may run an upload on acceptance for a while:

| Who | Scripts | Environments | Access |
|---|---|---|---|
| role developer | tag:developer | development | run |
| role developer | tag:developer | acceptance, production | view |
| role consultant | tag:consultant | * | run |
| exception for jdoe until 2026-11-30, "INC-123" | Upload SQL | acceptance | run |

| Variable | Default | Meaning |
|---|---|---|
| `POLICY_ENVIRONMENTS` | development `dev*,test*`, acceptance `acc*,uat*`, production `prod*,prd*` | environments of a **new** policy, as JSON: `[{"name":"development","match":["dev*"]}]`; afterwards they are managed in the Admin area |
| `POLICY_UNKNOWN_ENVIRONMENT` | the last environment | environment of values that match nothing, for a new policy |
| `POLICY_MAX_EXCEPTION_DAYS` | `90` | longest exception that can be granted (`0`: no limit) |

**Upgrading from `allowed_users` / `admin_users`**: these runner fields and `ADMIN_GROUPS` no
longer grant anything (the configs still load, with a warning), and `ADMIN_USERS` is ignored too.
After the upgrade everyone is a guest: enable the built-in administrator, give the devops role to
the first user, turn the built-in administrator off, and let that user give roles and add rules
(tag the scripts in their configs first).

## Secrets

Secrets for scripts (API tokens, passwords) are bound to an **environment** and to the **scripts
that declare them**. Runwarden's own process environment holds none of them.

- **Where**: `SECRETS_DIR/<environment>/<NAME>` and `SECRETS_DIR/common/<NAME>`, one file per
  secret: the layout of a mounted Kubernetes Secret (Helm `secrets.environments` and
  `secrets.common`). Create the Secrets outside the chart, e.g. with External Secrets or sealed
  secrets. Directory names are the environment names of the access policy; the Admin area's
  **Secrets** tab shows directories that match no environment. One trailing newline is removed.
- **Who gets what**: a runner config lists the names it needs, e.g.
  `"secrets": ["DB_PASSWORD", "API_TOKEN"]`. A run receives exactly those, as environment
  variables, from the environment it runs in (its `target` environment parameter), falling back
  to `common`. Scripts that declare nothing receive no secrets. Starting a run needs run access to
  that environment (see *Access control*), so a developer never receives production secrets
  unless a rule or an exception allows running there.
- **Two environments**: a run with a `source` environment (e.g. copy production → development)
  also gets the source's own values as `SOURCE_<NAME>` (values from `common` are not repeated).
  Starting it needs only *view* access to the source, so give view on sensitive environments only
  to roles that may hold their secrets, or let such scripts declare no secrets.
- **Missing**: if a declared secret exists neither in the environment nor in `common`, the run is
  refused (`409`, audited) instead of starting without credentials; the Secrets tab lists such
  gaps per script and environment. A run that targets several environments at once cannot get
  secrets.
- **Masking and records**: every secret value is masked in the run's output, history and live
  stream (even when printed), and in dropdown helpers' output. History and audit events record the
  names only (`runwarden.secrets`); the run page lists them.
- **Dropdown helpers** of a script get its secrets for the environment their inputs select (view
  access to it is required anyway).
- **Rotation**: files are read at every start, so updated Kubernetes Secrets apply without a
  restart (after the kubelet refreshes the volume).
- `RUNNER_ENV_<NAME>` still reaches every script in every environment: use it for settings, not
  secrets. The server warns at start about such variables whose names look like secrets.

The example runner *Check Credentials* shows which secrets a run receives (lengths and
fingerprints only).

## Kubernetes clusters per environment

Runwarden, not the script, chooses the cluster a run talks to: for a runner with
`"kubernetes": true`, choosing the environment *is* choosing the cluster. Concurrent runs in
different environments never share or overwrite a kube context.

- **Where**: `KUBE_DIR/<environment>/config`, one kubeconfig per environment (Helm
  `kube.environments: {<environment>: <Secret name>}`, the Secret holding the key `config`).
  The environments with a cluster are simply the directories that exist: add one with a Secret,
  a Helm values entry and the environment in the runner configs, no release. Several
  environments may share a cluster, each with its own namespace in the context.
- **File**: YAML or JSON, at most 256 KiB; its `current-context` must exist and is the one
  pinned (other contexts in the file are ignored). Paths in it (certificate files, exec plugin
  command) must be absolute or a command on `PATH`. Runwarden treats the file as opaque and never
  logs it; whatever its exec plugin needs (e.g. `kubelogin` with a service principal's client id
  and secret) comes from the same environment's *Secrets* as ordinary environment variables, so
  declare them in the runner's `secrets`.
- **Run**: Runwarden writes the kubeconfig to the run's private HOME (`$KUBECONFIG`, mode 0600)
  with the context renamed to the environment name, and sets
  `RUNWARDEN_KUBE_CONTEXT=<environment>`. A run with a `source` environment gets both contexts,
  the target current, and `RUNWARDEN_KUBE_SOURCE_CONTEXT=<source>`. Dropdown helpers get the
  cluster of the environment their inputs select (none before one is chosen). Runners without the
  flag get no kubeconfig. Needs `RUN_HOME=per-run`.
- **Missing**: an environment without a usable kubeconfig refuses such runs (`409`, audited); a
  run that targets several environments at once cannot get a cluster. The Admin area's
  **Secrets** tab lists each environment's cluster server, namespace and context (never
  credentials), unusable files, and runners that miss a cluster somewhere; `check-config` warns
  about environment values without a kubeconfig when `KUBE_DIR` is set.
- **Rotation**: the file is read at every start, so an updated Secret applies without a restart.
- **Isolation**: this pins the cluster; it does not contain a misused script. With the sandbox
  (*Isolation between runs*) a run reads only the copy in its own HOME, never `KUBE_DIR`;
  without it all runs share one user ID and can read each other's files. Either way, give each
  environment's credentials only the rights its scripts need.

Migrating a script: drop `az aks get-credentials` (and the `az login` it needed), set
`"kubernetes": true` in its runner config and use `kubectl` as is, or
`kubectl --context "$RUNWARDEN_KUBE_SOURCE_CONTEXT"` for the source cluster. Calling
`get-credentials` still works, but it only changes the run's private copy; it is deprecated.

```bash
# Ops, per environment: a kubeconfig that logs in with the environment's service principal
az aks get-credentials -g rg-prod -n aks-prod -f config
kubelogin convert-kubeconfig -l spn --kubeconfig config   # reads AAD_SERVICE_PRINCIPAL_CLIENT_ID/_SECRET
kubectl create secret generic runwarden-kube-production --from-file=config
```

## API

JSON over HTTP. Errors look like `{"error": "...", "details": ["..."]}`; validation errors name
the parameter but never echo the submitted value.

| Method and path | Purpose |
|---|---|
| `GET /api/scripts` | list the scripts you may see (with their `tags`) |
| `GET /api/scripts/{name}` | script with its parameters (form description) and your `access` per environment; values of environment parameters carry their `environment` and your `access` |
| `GET /api/scripts/{name}/parameters/{param}/values?Other=value` | allowed values; for script-provided lists, pass the parameters it depends on (`403` when they select an environment you may not view) |
| `POST /api/scripts/{name}/runs` | start a run: JSON `{"values": {...}}`, or `multipart/form-data` with a `values` JSON field plus one file per `file_upload` parameter. `201 {"id"}`, `400` invalid values, `403` no run access to the environment, `429` busy |
| `GET /api/runs?script=&user=&status=&limit=` | runs you may see, newest first; `status=running` lists runs in progress |
| `GET /api/admin/users` | devops: known users, their roles, status and run counts |
| `GET /api/admin/activity?user=&action=&outcome=&script=&since=&until=&limit=` | devops: audit events, newest first (`since`/`until` in RFC 3339) |
| `GET /api/admin/policy` | devops: the access policy, warnings, scripts with their tags and environment parameters |
| `PUT /api/admin/policy/environments`, `POST`/`PUT`/`DELETE /api/admin/policy/roles[/{name}]`, `PUT /api/admin/policy/users/{name}`, `POST`/`PUT`/`DELETE /api/admin/policy/rules[/{id}]`, `POST`/`DELETE /api/admin/policy/exceptions[/{id}]` | devops: change the policy. Every change sends the `revision` it is based on (`DELETE`: `?revision=`); a stale one answers `409` |
| `GET /api/admin/secrets` | devops: secret names per environment and `common`, and per script the declared secrets missing in each environment (never values) |
| `GET /api/admin/policy/effective?user=` or `?role=` | devops: resulting access per script and environment, with the rule or exception that grants it |
| `GET /api/admin/policy/export`, `POST /api/admin/policy/import[?dry_run=true]` | devops: the policy as YAML; import replaces it (`dry_run` only validates and summarizes) |
| `GET /api/runs/{id}` | run status and details (secure values masked), its `environments`, and `can_control` |
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

The examples below assume `AUTH_METHODS=none`; otherwise sign in first and send the session
cookie (for LDAP: `curl -c cookies.txt -X POST .../auth/login -d '{"method":"ldap","username":..,"password":..}'`,
then `-b cookies.txt` on every call).

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

## Deployment

### Images

`ghcr.io/catalinmuraru/runwarden` is the generic base image (`linux/amd64`, `linux/arm64`):
Debian slim with `bash`, the Runwarden binary (UI included) and `tini` as PID 1. It runs as uid
10001 and expects:

| Path | Purpose |
|---|---|
| `/runwarden/runners` | runner configs (`RUNNERS_DIR`), not readable by sandboxed runs |
| `/opt/runwarden` | scripts; relative paths in configs resolve here (`BASE_DIR`) |
| `/var/lib/runwarden` | run history (`DATA_DIR`) — mount a volume |
| `/home/runwarden`, `/tmp` | writable scratch space when the root filesystem is read-only |

Put your scripts, the tools they need and the runner configs into an image built on top of it,
so tool and script updates stay separate from Runwarden updates
([examples/Dockerfile](examples/Dockerfile)):

```dockerfile
FROM ghcr.io/catalinmuraru/runwarden:0.1.0
USER root
RUN apt-get update && apt-get install -y --no-install-recommends curl jq && rm -rf /var/lib/apt/lists/*
USER 10001:10001
COPY runners/ /runwarden/runners/
COPY scripts/ /opt/runwarden/scripts/
```

### Helm chart

```bash
kubectl create secret generic runwarden-env \
  --from-literal=SESSION_KEY="$(openssl rand -base64 32)" \
  --from-literal=GITLAB_CLIENT_SECRET=...
# Script secrets: one Secret per environment (see "Secrets"), e.g.
kubectl create secret generic runwarden-secrets-production --from-literal=DB_PASSWORD=...

helm install runwarden oci://ghcr.io/catalinmuraru/charts/runwarden \
  --set image.repository=registry.example.com/my-scripts --set image.tag=1.0.0 \
  --set auth.publicUrl=https://runwarden.example.com \
  --set auth.gitlab.url=https://gitlab.example.com --set auth.gitlab.clientId=... \
  --set existingSecret=runwarden-env \
  --set secrets.environments.production=runwarden-secrets-production   --set kube.environments.production=runwarden-kube-production \
  --set ingress.enabled=true --set ingress.host=runwarden.example.com
```

See [values.yaml](deploy/helm/runwarden/values.yaml) for every setting. Highlights:

- **One replica, `Recreate` strategy**: running scripts live in the Runwarden process.
- **Secrets only from `existingSecret`**: the chart never renders secret values.
- **Hardened pod**: non-root, read-only root filesystem, all capabilities dropped,
  `RuntimeDefault` seccomp, no service account token unless `serviceAccount.automountToken`.
- **Run history** on a PersistentVolumeClaim (kept on uninstall by default).
- **Runner configs** come from the image, or from `runners.configs` in the values (rendered into a
  ConfigMap; changing them restarts the pod).
- **Ingress**: the controller must allow WebSocket upgrades and long read timeouts (runs may last
  as long as `limits.runTimeout`); add the annotations your controller needs.
- **Audit**: set `trustedProxies` to your ingress/load-balancer networks so audit events record
  the real client address; ship the pod log with Filebeat (see *Logs and audit trail*).
- **Retention**: `history.maxAge` (default `90d`) and `history.maxRuns`.
- **Workspace**: `workspace.enabled` adds a persistent volume at `/var/lib/runwarden-work`
  (`WORKSPACE_DIR`) for flows that keep files between runs: one private directory per user
  (`$RUNWARDEN_WORKSPACE`) and `shared` (`$RUNWARDEN_SHARED`, read-only for runs).
- **Isolation**: `isolation.mode` (default `required`: the pod does not start without a working
  Landlock sandbox; `auto` falls back to unsandboxed runs with a warning), `isolation.readPaths`,
  `isolation.writePaths` and `isolation.maxProcs` (see *Isolation between runs*). The Admin
  area's **Secrets** tab and the readiness probe show the state.
- **Probes** on a separate container port (`health`, 8081); the Service exposes only the web port.
- **Per-run HOME**: `runs.home` (`per-run` / `shared`) and `runs.homeSkeletonConfigMap` (a
  ConfigMap copied into every HOME; use `items`/`path` for files in sub-directories such as
  `.ssh/known_hosts`).
- **NetworkPolicy** (off by default): `networkPolicy.enabled`, `ingressFrom` (who may reach the web
  port, e.g. the ingress controller's namespace), `egress.dns` and `egress.rules` (what scripts may
  reach: GitLab, Azure, the API server...). Plain NetworkPolicy matches IP ranges only; to allow
  hosts by name use your CNI's FQDN policies (Cilium, Calico) or an egress proxy.
- **Sign-in**: `auth.methods` (default `[gitlab]`; e.g. `[gitlab, ldap]`), `auth.gitlab.url`
  for your GitLab, `auth.gitlab.requiredGroup`, `auth.initialAdmin`. `[none]` must be confirmed with
  `auth.allowNoAuth: true`. (`auth.type` was replaced by `auth.methods`; the chart says so.)
- **Secrets**: `secrets.common` and `secrets.environments` (environment → Secret name), mounted
  read-only (0440) under `/runwarden/secrets/`; kubeconfigs under `/runwarden/kube/`.
- **Access**: `policy.environments`, `policy.unknownEnvironment` and `policy.maxExceptionDays`.
  The first devops user is made by the built-in administrator (`auth.initialAdmin`);
  `auth.adminUsers` and `auth.adminGroups` were removed (the chart fails with a hint).
- `extraEnv`, `extraEnvFrom`, `extraVolumes`, `extraVolumeMounts` for anything scripts need
  (e.g. an SSH key from a Secret).

#### Upgrading to the run sandbox

The chart turns on `isolation.mode: required` and moves its mounts out of `/etc`, because
Landlock has no deny rules and every run may read `/etc`:

- Runner configs, secrets, kubeconfigs and the HOME skeleton are now under `/runwarden/`
  (`RUNNERS_DIR=/runwarden/runners`, `SECRETS_DIR=/runwarden/secrets`, `KUBE_DIR=/runwarden/kube`).
  Images built on the base image must `COPY` their runner configs to `/runwarden/runners/`;
  values that mount files under `/etc/runwarden/...` through `extraVolumeMounts` must move them.
  Runwarden refuses to sandbox runs (and with `required`, to start) while `DATA_DIR`,
  `SECRETS_DIR`, `KUBE_DIR` or `WORKSPACE_DIR` lies below a path every run can read.
- The workspace is per user: `$RUNWARDEN_WORKSPACE` is `/var/lib/runwarden-work/<user>`. Move
  files scripts still need into the users' directories, and team tools into
  `/var/lib/runwarden-work/shared` (e.g. with `kubectl exec`); runs can only read `shared`.
- Scripts may write only their HOME, `$TMPDIR` and their workspace: replace hard-coded `/tmp`
  paths with `$TMPDIR`, or allow a path for all runs with `isolation.writePaths`.
- A node kernel without Landlock (before 5.13, or with the LSM disabled) fails the start with
  `ISOLATION=required: ...`; set `isolation.mode: auto` to run unsandboxed until it is upgraded.

### Local cluster

`deploy/kind/test.sh` builds both images, installs the chart into a kind cluster and checks that
scripts run, output streams, the security context is enforced, a sandboxed run cannot reach a
concurrent run's files, environment or memory, and history survives a pod restart. `KEEP=1 deploy/kind/test.sh` leaves it running.

### Releases

Pushing a tag `vX.Y.Z` on `main` builds and publishes the multi-arch image
(`X.Y.Z`, `X.Y`, `latest`, with SBOM and provenance) and the Helm chart
(`oci://ghcr.io/catalinmuraru/charts/runwarden`, version `X.Y.Z`).

## Security model

- Scripts are executed directly with an argument array, never through a shell-built command line.
- Scripts receive an allowlisted environment only, so application secrets never reach them.
- Every parameter is validated server-side.
- Each run gets its own process group, so stopping a run stops all of its children.
- Limits on concurrent runs, output size and run time.
- Script secrets are bound to environments and scripts: a run gets only the secrets its config
  declares, of the environment it may run in; they are masked in output, history and audit, which
  record only their names.
- Runs get the kubeconfig of their environment only, chosen by Runwarden.
- With Landlock (`ISOLATION`), runs cannot read Runwarden's data, secrets or kubeconfigs, other
  runs' files, environment or memory, or other users' workspaces; with kernel 6.12+ they cannot
  signal other runs. They still see other runs' command lines and share memory and CPU.
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

# Run the server on :8080 with the example scripts, without sign-in (local only)
AUTH_METHODS=none ALLOW_NO_AUTH=true RUNNERS_DIR=examples/runners go run ./cmd/runwarden

# UI with hot reload on :5173, proxying /api and /auth to the server above
cd web && npm run dev
```

Checks run in CI on every pull request:

```bash
go test -race ./...
golangci-lint run
govulncheck ./...
cd web && npm ci && npm test && npm run build
helm lint --strict deploy/helm/runwarden --set 'auth.methods={none}',auth.allowNoAuth=true
docker build -t runwarden .   # CI also scans the image with Trivy
```

Security checks in CI: CodeQL, govulncheck, gosec (in golangci-lint), Trivy (image and
Dockerfile/Helm misconfigurations), dependency review on pull requests, zizmor for the workflows,
and a weekly OpenSSF Scorecard. Secret scanning with push protection is enabled on the repository.

Without a UI build the binary still compiles; `/` then answers 503 with a hint.

## Project layout

```
cmd/runwarden/     entry point and check-config command
internal/config/   runner config loader
internal/params/   parameter model, validation, argument/env/stdin building
internal/executor/ process lifecycle, output, stdin, limits
internal/history/  run records on disk
internal/activity/ in-app activity log and users index (admin pages)
internal/audit/    audit events (ECS)
internal/api/      REST and WebSocket API
internal/auth/     sign-in (OIDC, GitLab, LDAP) and sessions
internal/policy/   access policy: roles, environments, rules, exceptions (stored as JSON)
internal/secrets/  secrets per environment, read from mounted Secrets
internal/authz/    who may see and run scripts and see and control runs
internal/web/      embedded UI (built into internal/web/dist/)
web/               Vue 3 + Vite + TypeScript source
examples/          example runner configs, scripts and scripts image (Dockerfile)
deploy/helm/       Helm chart
deploy/kind/       local cluster test
deploy/dev/        local Keycloak and OpenLDAP for development
Dockerfile         base image
.github/workflows/ CI and release
```

## Health checks

`GET /healthz` (liveness) and `GET /readyz` (readiness) return `200 ok` on `HEALTH_ADDR`
(`:8081`); the readiness body adds one line on the run sandbox (`isolation: sandboxed (Landlock
ABI 7, signals scoped)`, or why runs are not sandboxed). The health port is a separate listener that the Helm chart does not expose through the Service or
ingress. On the web port they answer `404`: without a session, the web port offers only sign-in
and the UI shell.

## License

Runwarden is free software, licensed under the GNU General Public License v3.0 or later. See
[LICENSE](LICENSE). Security issues: see [SECURITY.md](SECURITY.md).
