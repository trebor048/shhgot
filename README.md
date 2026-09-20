<div align="center">

# shhgit

**Find secrets in code — before they find their way into a breach.**

A fast, single-binary secret scanner for GitHub, Gists and local directories.
Live terminal UI, web dashboard, Discord/Telegram alerts, and optional AI triage.

[![CI](https://github.com/trebor048/shhgot/workflows/CI/badge.svg)](https://github.com/trebor048/shhgot/actions)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/trebor048/shhgot)](go.mod)

</div>

---

## Contents

- [What it does](#what-it-does)
- [Requirements](#requirements)
- [Install](#install)
- [First run](#first-run)
- [Running shhgit](#running-shhgit)
- [Command-line reference](#command-line-reference)
- [Configuration reference](#configuration-reference)
- [Log styles](#log-styles)
- [Web dashboard](#web-dashboard)
- [Webhooks](#webhooks)
- [Performance tuning](#performance-tuning)
- [AI review](#ai-review)
- [Docker](#docker)
- [Troubleshooting](#troubleshooting)
- [Security](#security)
- [Development](#development)
- [Known limitations](#known-limitations)
- [Credits and license](#credits-and-license)

---

## What it does

shhgit watches code for credentials as it is committed, and tells you the moment
something looks like a secret.

| | |
|---|---|
| **309 built-in signatures** | API keys, tokens, private keys, connection strings and webhook URLs across AWS, OpenAI, Anthropic, Google, Stripe, Slack, Discord, GitHub, Telegram and many more. |
| **Live GitHub monitoring** | Polls the public event firehose (commits, Gists, comments) with your token, at a rate you control. |
| **Local scanning** | `--local ./some/dir` walks a directory tree. No GitHub token needed. |
| **Terminal UI** | The default mode: a live, colour-coded match feed as findings arrive. |
| **Web dashboard** | `--web` serves a self-contained dashboard — live feed, filters, log tail, activity view, full-file viewer, AI review workspace, provider settings. |
| **Alerts** | Discord or Telegram, with separate routing for AI-token and crypto findings. |
| **Live credential checks** | Optional provider auth-checks (OpenAI, Anthropic, AWS, Stripe, GitHub, Discord) to confirm a found key actually works. |
| **AI review** | One click on any hit gets a streamed assessment — what the credential is, what it grants, how exploitable it is, how to fix it — then answers follow-up questions. DeepSeek, OpenAI, any OpenAI-compatible endpoint, or local Ollama. |
| **Entropy and allowlists** | Cuts noise with `blacklisted_strings`, extension/path filters and Shannon-entropy scoring. |
| **Zero dependencies** | One static Go binary. No runtime, no cgo, no external services. |

---

## Requirements

- **Go 1.26 or newer** — only to build; the installer checks this for you.
- **git** — shhgit clones repositories to scan them.
- **A GitHub token** — only for GitHub/Gist monitoring. Local scanning needs none.

Nothing else. `CGO_ENABLED=0` throughout, so the same source cross-compiles
cleanly to Windows, Linux and macOS on both amd64 and arm64.

---

## Install

### Option 1 — installer script (Linux and macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/trebor048/shhgot/main/install.sh | bash
```

Or from a checkout:

```bash
git clone https://github.com/trebor048/shhgot
cd shhgot
./install.sh
```

The script detects your OS and architecture, installs Go if it is missing or too
old (Homebrew on macOS, the official tarball on Linux), builds `./shhgit`, creates
`config.yaml` from the example with `chmod 600`, and smoke-tests the result.

```bash
INSTALL_DIR=~/tools/shhgot ./install.sh   # install somewhere else
./install.sh --no-build                   # set up source and config only
./install.sh --docker                     # print the Docker route instead
```

On Windows, use WSL2 or the [Docker](#docker) route.

### Option 2 — build from source

```bash
git clone https://github.com/trebor048/shhgot
cd shhgot
cp config.yaml.example config.yaml        # then add your GitHub token(s)
go build -o shhgit .
./shhgit
```

Or drive it through the Makefile:

```bash
make build        # binary for this platform
make build-all    # cross-compile Windows/Linux/macOS, amd64 + arm64, into dist/
make test         # run the test suite
make help         # list every target
```

### Option 3 — Docker

```bash
cp config.yaml.example config.yaml        # must exist before you start
cp .env.example .env                      # optional
docker compose up -d
# dashboard: http://localhost:8080
```

See [Docker](#docker) for details.

---

## First run

**1. Create your config**

```bash
cp config.yaml.example config.yaml
chmod 600 config.yaml      # it will hold tokens
```

**2. Add a GitHub token** — skip this if you only scan local code.

Put one or more tokens in `github_access_tokens`. Fine-grained tokens with
public-repo read access are enough. More tokens means a higher API rate limit,
which is the usual reason to add more than one.

Tokens GitHub reports as revoked (`HTTP 401`) are pruned from `config.yaml`
automatically, at startup and during a scan, with a `config.yaml.bak` backup
written first — so a dead token is never retried.

**3. Run it**

```bash
./shhgit                 # terminal UI
./shhgit --web           # web dashboard on http://127.0.0.1:8080
./shhgit --local ./code  # scan a directory, no token required
```

`config.yaml` is looked for in the directory given by `--config-path`, then next
to the binary, then the current directory. Set `--config-path` explicitly when
running from elsewhere, e.g. `./shhgit --config-path /etc/shhgit`.

> **Secrets never belong in git.** `config.yaml`, `.env`, `logs/` and the AI
> review state directory are all gitignored. Keep it that way.

---

## Running shhgit

### Terminal UI (default)

```bash
./shhgit
```

Matches stream in, colour-coded by priority. The visual style comes from
`logFormat` in `config.yaml` — see [Log styles](#log-styles).

### Web dashboard

```bash
./shhgit --web                      # http://127.0.0.1:8080
./shhgit --web --web-port 9000      # different port
./shhgit --web --web-host 0.0.0.0   # reachable from other machines
```

The same process both scans and serves the dashboard, so there is nothing else to
start. Your browser opens automatically.

> The dashboard has **no authentication**. Leave it on `127.0.0.1` unless you put
> a firewall, an authenticating reverse proxy or Cloudflare Access in front of
> it. Binding `0.0.0.0` publishes every secret it has found to your network.

### Scanner only

```bash
./shhgit --scanner
```

No UI at all — for systemd units, containers, or CI where you only want webhook
alerts and log files.

### Scanning local code

```bash
./shhgit --local /path/to/repo
./shhgit --local ./src --csv-path findings.csv
./shhgit --scan-keys /path/to/repo     # find API keys and validate them
```

Local mode scans recursively, needs no GitHub token, and stays off the network
unless you enable webhooks or credential checks.

### Validate tokens from your config

```bash
./shhgit --test-tokens
```

Tests the Claude/OpenAI tokens in `config.yaml` and writes the valid ones to
`logs/priority3.log`.

---

## Command-line reference

| Flag | Default | Description |
|---|---|---|
| `--web` | off | Run the web dashboard mode. |
| `--tui`, `--terminal` | on | Run the terminal UI mode (the default). |
| `--scanner` | off | Run the scanner with no UI. |
| `--web-port` | `8080` | Dashboard port. |
| `--web-host` | `127.0.0.1` | Dashboard bind address. |
| `-threads` | `0` (one per logical CPU) | Concurrent worker count, capped by the `max_*_threads` keys below. |
| `--local` | – | Scan this directory recursively instead of GitHub. |
| `--live` | – | Your shhgit live endpoint. |
| `--config-path` | – | Directory to look for `config.yaml` in. |
| `--silent` | `false` | Suppress everything except errors. |
| `--debug` | `false` | Verbose logging. |
| `--maximum-repository-size` | `5120` KB | Skip repositories larger than this. |
| `--maximum-file-size` | `256` KB | Skip files larger than this. |
| `--clone-repository-timeout` | `30` s | Per-repository clone timeout; raise it on a slow link. |
| `--entropy-threshold` | `5.0` | Shannon-entropy score a string must reach. `0` disables entropy checks. |
| `--minimum-stars` | `0` | Only process repositories with at least this many stars. |
| `--path-checks` | `true` | Set `false` to match file contents only and ignore path rules. |
| `--process-gists` | `true` | Set `false` to skip Gists. |
| `--temp-directory` | `<system temp>/shhgit` | Where clones and matches are staged. |
| `--csv-path` | – | Append findings to this CSV file. Blank disables it. |
| `--search-query` | – | Regex; restrict scanning to files containing this string. |
| `--test-tokens` | `false` | Validate config tokens; write valid ones to `logs/priority3.log`. |
| `--scan-keys` | – | Scan a directory for API keys and validate them. |

Both `-flag` and `--flag` forms work.

---

## Configuration reference

Everything except three [environment overrides](#environment-variables) lives in
`config.yaml`. The example file is heavily commented — treat it as the primary
reference, and this section as the map.

### Top-level keys

| Key | Purpose |
|---|---|
| `github_access_tokens` | One or more GitHub tokens; revoked ones are pruned automatically. |
| `signatures` | The detection rules — 309 in the example. |
| `performance` | Thread counts, API pacing, worker-pool and queue sizing. |
| `webhook`, `webhook_ai_tokens`, `webhook_crypto` | Alert destinations ([details](#webhooks)). |
| `webhook_payload` | Body template POSTed to the webhook. |
| `logFormat` | Terminal UI style ([details](#log-styles)). |
| `ai_review` | AI backend and model ([details](#ai-review)). |
| `ai_tokens` | Patterns for recognising AI-provider tokens. |
| `match_log_dir`, `match_log_enabled` | Per-signature append logs (default `logs/matches`). |
| `blacklisted_strings` | Values never reported, e.g. documented example keys. |
| `blacklisted_extensions` | File extensions to ignore. |
| `blacklisted_paths` | Path fragments to ignore; `{sep}` expands to the OS separator. |
| `blacklisted_entropy_extensions` | Skip entropy scoring for these (keys, certs, logs). |

### Signatures

A signature matches either a file's **contents** (regex) or its **path**,
**filename** or **extension** (exact string):

```yaml
signatures:
  - part: 'contents'                 # contents | path | filename | extension
    regex: 'sk-[a-zA-Z0-9]{48}'      # use `regex`, or `match` for an exact string
    name: 'OpenAI API Key'
    verifier: 'openai'               # optional live auth-check
    priority: 3                      # 3 = critical, 2 = high, 1 = medium, 0 = info
    color: '#d53d46'
    exclude_in_modes: ['local']      # optional: skip this rule in these modes
```

Adding your own is just another list entry: the dashboard's priority filters and
the priority colouring pick it up automatically.

### Performance keys

| Key | Default | Meaning |
|---|---|---|
| `max_repository_threads` | `20` | Repositories cloned/processed concurrently. |
| `max_gist_threads` | `5` | Gists processed concurrently. |
| `max_comment_threads` | `3` | Comment streams processed concurrently. |
| `api_pages_per_cycle` | `3` | API pages fetched per polling cycle. |
| `api_per_page` | `100` | Results per page (GitHub caps this at 100). |
| `api_sleep_seconds` | `5` | Pause between polling cycles. |
| `worker_pool_size` | `20` | Concurrent event processors. |
| `queue_buffer_size` | `0` | Internal queue depth. Unset keeps the built-in `1000`/`100`/`1000` sizes. |
| `max_file_count` | `10000` | Files processed per repository before skipping it. |

Disk usage is capped by `max_disk_usage_mb` (default 5120) plus
`cleanup_threshold_mb` and `cleanup_interval_secs`, which evict stale clones so a
long run cannot fill the disk.

### Environment variables

The AI provider is configured from the dashboard's Settings tab first; these
environment variables act as per-field fallbacks below it (see
[AI review](#ai-review)):

| Variable | Effect |
|---|---|
| `SHHGIT_AI_PROVIDER` | Provider id: `deepseek`, `openai`, `custom` or `ollama`. |
| `SHHGIT_AI_API_KEY` | API key for whichever provider is selected. |
| `SHHGIT_AI_BASE_URL` | Endpoint base URL, e.g. a self-hosted gateway. |
| `SHHGIT_AI_MODEL` | Model name. |
| `SHHGIT_AI_<PROVIDER>_API_KEY` | Per-provider key, e.g. `SHHGIT_AI_OLLAMA_API_KEY`. The same pattern works for `_BASE_URL` and `_MODEL`. |
| `DEEPSEEK_API_KEY`, `OPENAI_API_KEY` | Convenience aliases for the keys above, so an existing setup keeps working. |

Every variable is a **per-field fallback below the Settings tab**: a value saved
in the dashboard wins, and the environment is consulted only for fields the saved
settings leave blank. So an env-provided key is used until you save one in the
UI, after which the UI's value takes precedence.

The Ollama endpoint is set through `SHHGIT_AI_BASE_URL` (or the Settings tab);
there is no separate `OLLAMA_URL` variable.

Deployment-only variables, read by the tooling around the binary rather than by
shhgit itself:

| Variable | Effect |
|---|---|
| `SHHGIT_PORT` | Host port published by `docker compose`. |
| `CLOUDFLARE_TUNNEL_TOKEN` | Cloudflare Tunnel token for the optional compose profile. |

Config values go through `os.ExpandEnv`, so secrets can stay out of the file:

```yaml
webhook: '$SHHGIT_DISCORD_WEBHOOK'
```

---

## Log styles

Five terminal styles ship with shhgit. Set `logFormat` in `config.yaml`:

```yaml
logFormat: "fancy"     # minimal | fancy | ultra fancy | even more fancy | neon
```

| Style | Look |
|---|---|
| `minimal` | One plain line per match. Best for files and CI logs. |
| `fancy` | Default. Colour-coded panels with signature, repository and secret. |
| `ultra fancy` | Adds banners and extra framing around each match. |
| `even more fancy` | Higher-contrast presentation for wall displays. |
| `neon` | Maximum-glow palette for demos and screenshots. |

---

## Web dashboard

```bash
./shhgit --web
```

One self-contained HTML page served by the scanning process itself — no build
step, no separate frontend server, nothing extra to deploy.

**What is in it**

- **Live match feed** — delivered over Server-Sent Events (`/api/events`), with
  priority, source and signature filters, plus a **pause** control that freezes
  the table while buffering new hits.
- **Stats panel** — totals, per-source and per-signature breakdowns, top
  signatures.
- **Full-file viewer** — click a match to read the file it came from with the
  secret highlighted. Content is captured at scan time because cloned
  repositories are deleted afterwards.
- **Tokens tab** — live credential-validation results.
- **Logs and activity tabs** — the scanner log tail and an activity view.
- **Review tab** — the AI security-review workspace: a job list of current and
  past reviews, the streamed assessment for the selected finding, and a
  follow-up chat (see [AI review](#ai-review)).
- **Settings tab** — pick the AI provider and key from inside the UI; no config
  file edit or restart needed.

**HTTP endpoints**

| Endpoint | Purpose |
|---|---|
| `/` | The dashboard. |
| `/health` | Liveness probe — `{"status":"healthy"}`. |
| `/api/events` | SSE stream of live events, including review status changes. |
| `/api/stats`, `/api/matches`, `/api/logs`, `/api/tokens`, `/api/signatures`, `/api/activity` | JSON data for the UI. |
| `/api/ws` | JSON snapshot of matches and stats (despite the name, it is not a WebSocket). |
| `/api/file?id=<match-id>` | Stored file content behind a match. |
| `/api/push` | `POST` a match in from an external tool. |
| `/api/review` | `GET` the review history, `POST` to start a review of a finding. |
| `/api/review/<id>` | `GET` one review with its chat, `DELETE` it. |
| `/api/review/<id>/stream` | SSE: the assessment as the model writes it. |
| `/api/review/<id>/chat` | `POST` a follow-up question; streams the reply. |
| `/api/settings` | `GET` the AI configuration (the key is never sent back), `PUT` to save it. |
| `/api/settings/test` | `POST` to make one real call against the configured provider. |

`PUT /api/settings` applies a partial update, so a client can rotate one field
without sending the rest: an omitted `base_url`, `model` or `api_key` keeps the
stored value, while sending one as an explicit empty string means "use the
provider default" (which is what the Settings tab sends when a field is cleared).
This matters for a self-hosted gateway — a save that carried only a new key used to
replace the endpoint and model with the vendor defaults, quietly redirecting the
next review, and the finding in it, to a different host.

A mistyped `/api/…` path gets a real `404` with a JSON body, so a broken client
request is obvious instead of silently returning dashboard HTML. Other unknown
paths get a plain `404`.

### Web security notes

- **No authentication.** Bind to `127.0.0.1` (the default) unless something
  authenticating sits in front of it.
- **Same-origin only.** Cross-origin requests are rejected with `403`, so a page
  you happen to visit cannot read `/api/matches` or inject findings via
  `/api/push`. Requests with no `Origin` header (curl, the CLI) still work.
- **Every route that can reveal captured material is additionally local-only**
  (`/api/matches`, `/api/file`, `/api/logs`, `/api/tokens`, `/api/stats`,
  `/api/signatures`, `/api/activity`, `/api/events`, `/api/push`, `/api/review`,
  `/api/settings` and the review sub-routes). While the dashboard is bound to a
  loopback address they also require a loopback `Host`, which defeats DNS
  rebinding — an attacker hostname resolving to `127.0.0.1` would otherwise look
  same-origin, and `Origin == Host` alone would have let it read your findings.
  `/health` and the dashboard page itself stay reachable by any hostname.
- **Binding a routable address** (`--web-host 0.0.0.0`) disables that Host check,
  leaving only the same-origin comparison. Treat it as an explicit opt-in to
  exposure, and only do it behind something that authenticates. **This is the
  Docker default**, because a container cannot be reached on `127.0.0.1` from
  outside its network namespace: even though `docker-compose.yml` publishes the
  port on the host's loopback only, the container itself binds `0.0.0.0` and the
  rebinding check is off. The same applies to the Cloudflare Tunnel profile. A
  page you visit could then read your findings through `127.0.0.1:8080`, so put
  Cloudflare Access (or any authenticating proxy) in front of either deployment.
  Running the binary directly on the host keeps the check, which is the
  recommended way to use the dashboard locally.
- **The feed contains live secrets.** Treat the dashboard as a secrets console:
  do not screen-share it, do not put it on a public interface.

---

## Webhooks

shhgit posts to Discord or Telegram when it finds something. Three options let
you route by finding type:

```yaml
webhook: 'https://discord.com/api/webhooks/<numeric-id>/<token>'    # everything
webhook_ai_tokens: 'https://discord.com/api/webhooks/<id>/<token>'  # AI/LLM tokens
webhook_crypto: 'https://discord.com/api/webhooks/<id>/<token>'     # crypto keys/seeds
```

If a class-specific webhook is unset, that class falls back to `webhook`.

**Payload template** — `%s` is replaced with the match text:

```yaml
webhook_payload: |
  {
    "text": "%s"
  }
```

For Telegram, point `webhook` at
`https://api.telegram.org/bot<TOKEN>/sendMessage`, or use the
`TELEGRAM_BOT_TOKEN:` form.

Discord URLs are validated at startup and the webhook ID must be **numeric** — a
placeholder like `YOUR_WEBHOOK_HERE` makes shhgit refuse to start, so keep the
line commented out until you have a real URL.

Delivery is queued and rate-limited (1000 slots, 500 ms spacing, 300 s dedupe),
so a burst of matches will not get you throttled. Those numbers are currently
fixed in code; the commented-out `webhook_queue` block in the example documents
them but does not change them.

---

## Performance tuning

shhgit is I/O bound: most of its time goes to cloning repositories and waiting on
the GitHub API. Tune in this order.

1. **Tokens first.** Throughput comes from GitHub's per-token rate limit, so more
   tokens means more throughput — almost always the biggest single win.
2. **`performance.max_*_threads` and `worker_pool_size`.** Raise them for more
   concurrency; raise them too far and you are only fighting the API limit.
3. **Safety rails.** `--maximum-repository-size`, `--maximum-file-size`,
   `--clone-repository-timeout` and `--minimum-stars` stop pathological
   repositories from eating bandwidth. On a fast link, lower
   `--clone-repository-timeout` so a hung clone cannot stall a worker.
4. **`--entropy-threshold`.** Higher reduces false positives at the cost of
   missing weak-but-real secrets; `0` disables the check.
5. **Trim the signature set.** Every rule is a regex pass over every candidate
   file. If you only care about cloud credentials, deleting the rules you do not
   need is the cheapest speedup available.

---

## AI review

Optional. Configure a provider and a **Review** action appears on every match:
clicking it sends that finding to the model, which explains what the credential
is, what an attacker could do with it, how reachable it really is, and how to fix
it — then answers your follow-up questions about it.

Reviews live in the dashboard's **Review** tab: a job list of everything you have
reviewed, the streamed assessment for the selected finding, and a chat box.

### Configure it in the UI (recommended)

```bash
./shhgit --web
```

Open the **Settings** tab, choose a provider, paste a key, and press **Test
Connection**. Saving writes `ai_review/settings.json` (gitignored, and mode
`0600` on Unix; Windows applies its own ACLs) and takes effect immediately — no
restart. The API key is never sent back to the browser; the UI only learns
whether one is stored.

### Configure it in `config.yaml`

```yaml
ai_review:
  provider: deepseek              # deepseek | openai | custom | ollama
  api_key: ''                     # prefer the environment variables instead
  base_url: ''                    # blank = the provider's default
  model: ''                       # blank = the provider's default
  review_dir: ai_review           # where reviews and settings are stored
```

These values only **seed** the settings the first time shhgit runs. After that the
Settings tab owns them, so switching provider in the UI is not undone on restart.

### Providers

| Provider | Endpoint | Key | Default model |
|---|---|---|---|
| `deepseek` | `https://api.deepseek.com/v1` | required | `deepseek-chat` |
| `openai` | `https://api.openai.com/v1` | required | `gpt-4o-mini` |
| `custom` | whatever you set as `base_url` | optional | you set it (required) |
| `ollama` | `http://localhost:11434` | none | `llama3.1` |

All four go through one OpenAI-compatible client; `ollama` talks to the native
`/api/chat` endpoint unless you point `base_url` at a `/v1` route. `custom` is for
any other OpenAI-compatible gateway (vLLM, LiteLLM, OpenRouter, a company proxy).

`base_url` must be an absolute `http://` or `https://` URL. A bare
`localhost:11434` is rejected when you save it (rather than failing later with an
opaque "unsupported protocol scheme" on the first review), and it is not restricted
to loopback — an Ollama on another machine is fine.

### Environment variables

Read as per-field fallbacks, used only for fields the saved settings leave blank.
Note that `config.yaml` *seeds* those saved settings on first run, so a value set
there also outranks the environment.

```bash
SHHGIT_AI_PROVIDER=deepseek        # deepseek | openai | custom | ollama
SHHGIT_AI_API_KEY=...
SHHGIT_AI_BASE_URL=https://api.deepseek.com/v1
SHHGIT_AI_MODEL=deepseek-chat

# per provider, same three fields
SHHGIT_AI_OPENAI_API_KEY=...
SHHGIT_AI_OLLAMA_BASE_URL=http://localhost:11434

# vendor aliases, lowest precedence of the key names
DEEPSEEK_API_KEY=...      OPENAI_API_KEY=...
```

Prefer these for keys so no secret sits in a file.

### What the model is sent

The signature name, the repository and file, the **detected value**, and the
**full contents of the file it was found in** (captured at scan time, because
cloned repositories are deleted afterwards). Reviews are stored as JSON in
`review_dir`, so that directory is gitignored by default.

### Privacy — read this before enabling

With any cloud provider, **real credentials and the surrounding file contents
leave your machine**. The model is instructed never to repeat the value back, and
that is all that can be promised for a third-party API. If that is not acceptable,
use `provider: ollama` so nothing leaves the host, or leave AI review
unconfigured — scanning, the dashboard and webhooks all work fully without it.

### Failure behaviour

A review that fails (bad key, wrong model, provider down) is stored with
`status: failed` and the provider's own error message, visible in the Review tab
and replayed on its stream endpoint. A failed review never blocks the dashboard,
and setting `review_dir` to a read-only location only disables AI review — the
scanner keeps running.

---

## Docker

```bash
cp config.yaml.example config.yaml    # MUST exist before `docker compose up`
cp .env.example .env
docker compose up -d
```

The image is a multi-stage build: a Go builder stage, then a small `alpine`
runtime holding just the static binary, git and CA certificates. It runs as a
non-root user.

- **Dashboard** — the container starts in `--web` mode on port 8080, published on
  `127.0.0.1` only. To reach it from elsewhere, change the port mapping in
  `docker-compose.yml` *and* put authentication in front of it.
- **`config.yaml` must exist first.** Otherwise Docker creates a *directory* with
  that name and the container cannot read it. It is mounted read-write because
  shhgit prunes revoked tokens from it.
- **Optional tunnel** — `docker compose --profile tunnel up -d` publishes the
  dashboard through Cloudflare Tunnel using `CLOUDFLARE_TUNNEL_TOKEN`. A tunnel
  exposes it to the internet; put Cloudflare Access in front of it. Note that the
  tunnel forwards its public hostname as `Host`, and that the compose file binds
  `0.0.0.0` inside the container, so the DNS-rebinding check described under
  [Web security notes](#web-security-notes) does not apply here — an
  authenticating proxy is required, not optional.

Plain Docker works too:

```bash
docker build -t shhgit .
docker run --rm -p 127.0.0.1:8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  -v "$PWD/logs:/app/logs" \
  shhgit
```

---

## Troubleshooting

**`You need to provide at least one GitHub Access Token`**
Add a token to `github_access_tokens`, or use `--local` to scan a directory.

**shhgit refuses to start after I edited the webhook**
Discord URLs are validated at startup and the ID must be numeric. Comment the
line out until you have a real URL.

**The terminal UI finds nothing**
Check that `--minimum-stars` and `--search-query` are not filtering everything
out, and that `blacklisted_paths`/`blacklisted_extensions` are not excluding your
files. `--debug` shows what is being skipped.

**Clones time out**
Raise `--clone-repository-timeout`, or lower `--maximum-repository-size` so big
repositories are skipped instead of stalling a worker.

**The dashboard shows no matches**
The scanner feeds the dashboard in-process, so matches appear only while a scan is
running. Confirm the scanner started (watch the logs tab) and that your filters
are not hiding rows.

**AI review returns a 400**
The model name is wrong for your account, or no API key is set. The chat window
says which.

**Port already in use**
`--web-port 9000`, or set `SHHGIT_PORT` for Docker.

---

## Security

- The dashboard is **unauthenticated**. Keep it on loopback, or behind an
  authenticating proxy.
- `config.yaml` holds live credentials. It is gitignored — keep it `chmod 600`
  and never commit it.
- `.env`, `logs/`, the AI review state directory and build output are gitignored
  too.
- Cross-origin requests to the dashboard API are rejected, so an arbitrary web
  page cannot read your findings through the browser.
- Treat everything the dashboard shows as a live production credential: rotate a
  key rather than pasting it into a ticket.
- If a secret does get committed, **rotate it**. Deleting the commit is not
  enough — assume it was already scraped.

Found a vulnerability? Please open a private security advisory rather than a
public issue.

---

## Development

```bash
make help          # every target
make build         # local binary
make build-all     # cross-compile all six OS/arch targets into dist/
make test          # test suite
make test-race     # test suite under the race detector
make vet fmt       # static checks and formatting
make run-web       # run the dashboard
```

Layout:

| Path | Contents |
|---|---|
| `main.go` | Startup, terminal UI, log styles, scan loop. |
| `modes.go` | Mode selection (`--web`, `--tui`, `--scanner`). |
| `web.go` | Web server, HTTP API, stats, SSE feed, origin and loopback guards. |
| `dashboard.go`, `dashboard/index.html` | The embedded dashboard (one self-contained file, compiled in with `go:embed`). |
| `web_review.go` | AI security review: routes, prompt assembly, in-flight review broker. |
| `web_settings.go` | The `/api/settings` and `/api/settings/test` routes. |
| `aiproviders/` | One client for DeepSeek, OpenAI, any OpenAI-compatible endpoint and Ollama, plus the settings store. |
| `reviewstore/` | On-disk review and chat history (one JSON file per review). |
| `core/` | Scanner, signatures, validators, config, webhooks. |
| `cmd/apikey-check/` | Standalone API-key checking utility. |

To change the dashboard, edit `dashboard/index.html` and rebuild — there is no
build step, package manager or bundler, and no network access is needed at
runtime.

CI (`go.yml`) checks formatting, runs `go vet`, runs the tests including under
`-race`, and builds all six OS/arch combinations. Releases attach binaries for
Windows, Linux and macOS on amd64 and arm64.

---

## Known limitations

Recorded honestly, so nobody has to rediscover them:

- **No authentication on the dashboard.** It is protected by loopback binding,
  a same-origin check and a local-only guard on the AI routes — not by a login.
  Put it behind an authenticating proxy before exposing it.
- **Reviews need a reachable provider.** With no provider configured the Review
  tab works, but every review fails with the provider's error; the Settings tab's
  **Test Connection** tells you why.
- **`webhook_queue` settings are ignored** — the live queue uses fixed values in
  `core/validators.go`.
- **Token pruning needs a writable `config.yaml`.** With a read-only mount,
  pruning is skipped.
- **`--local` scans once.** The directory is scanned and the results are fed to
  the dashboard; there is no watch mode and no follow-up GitHub scan in that mode.

---

## Credits and license

shhgit builds on the original [shhgit](https://github.com/eth0izzle/shhgit) by
[eth0izzle](https://github.com/eth0izzle) and the signature library built around
it. This is an independent, heavily extended fork.

MIT licensed — see [LICENSE](LICENSE).
