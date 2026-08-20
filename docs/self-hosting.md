# Self-Hosting Guide

## Prerequisites

- **Docker** and **Docker Compose v2+**
- **Go 1.22+** (for building the API server)
- **Node.js 20+** and **pnpm** (for the frontend; CI builds on Node 22)
- 2 GB RAM minimum
- 10 GB disk space
- **Outbound HTTPS (port 443) from the `api` container**, if you intend to use
  the Telegram notification channel — it calls `api.telegram.org` directly. See
  [Telegram notifications](#telegram-notifications). Nothing else in Mesh needs
  outbound internet access, so a locked-down network is otherwise fine.

> Running a **production** install? Skip to
> [Production Deployment](#production-deployment) — it is a different compose
> file with a different env file, and the two are easy to mix up.

## Quick Start

This is the **development** path: infrastructure in Docker, API and frontend
running from source on your machine.

```bash
# 1. Clone the repository
git clone https://github.com/entire-vc/evc-mesh && cd evc-mesh

# 2. Start infrastructure (PostgreSQL, Redis, NATS, MinIO)
cd deploy/docker/mesh && docker compose up -d
#    or from the repo root: make docker-up

# 3. Start the API server -- from the repo root, see the note below
cd ../../..
JWT_SECRET=$(openssl rand -base64 32) go run ./cmd/api

# 4. In a separate terminal, start the frontend
cd web && pnpm install && pnpm dev
```

> **The API server does not read a `.env` file.** It has no dotenv loader —
> `config.Load()` reads the process environment and nothing else. Creating a
> root `.env` has no effect on `go run ./cmd/api`; export the variables, put
> them on the command line, or use the Docker Compose path where compose reads
> the env file for you. `.env.example` is a reference list of names and
> defaults, not a file the server loads.

> **Run the API from the repo root.** It applies database migrations at startup
> and resolves `migrations/` relative to the working directory. Started
> elsewhere it exits with `Failed to run migrations: migrations directory does
> not exist`.

A fresh install has **no users and no default password** — open
http://localhost:3000/register and create the first account, or see
[Seeding the first admin](#seeding-the-first-admin) to have the server create it
for you.

The services will be available at:

| Service | URL | Description |
|---------|-----|-------------|
| Frontend | http://localhost:3000 | Web UI (React) |
| API | http://localhost:8005 | REST API + WebSocket |
| MCP (SSE) | http://localhost:8081 | MCP over SSE (optional) |
| MinIO Console | http://localhost:9003 | Object storage UI |

---

## Environment Variables Reference

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | `0.0.0.0` | API server bind host |
| `SERVER_PORT` | `8005` | API server bind port |
| `SERVER_READ_TIMEOUT` | `30s` | HTTP read timeout |
| `SERVER_WRITE_TIMEOUT` | `30s` | HTTP write timeout |
| `MESH_METRICS_TOKEN` | _(empty)_ | Gates `GET /metrics` behind `Authorization: Bearer <token>`. Empty leaves it open — correct when you gate it at the network layer. The Compose stack generates one for you; see [the security checklist](#security-checklist). |
| `MESH_METRICS_TOKEN_FILE` | _(empty)_ | File to read `MESH_METRICS_TOKEN` from when that variable is empty. How the Compose stack shares a generated token with Prometheus. |

### PostgreSQL

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5437` | PostgreSQL port (mapped from container 5432) |
| `DB_USER` | `mesh` | PostgreSQL user |
| `DB_PASSWORD` | `mesh` | PostgreSQL password |
| `DB_NAME` | `mesh` | Database name |
| `DB_SSL_MODE` | `disable` | SSL mode (`disable`, `require`, `verify-full`) |

### Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_HOST` | `localhost` | Redis host |
| `REDIS_PORT` | `6383` | Redis port (mapped from container 6379) |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_DB` | `0` | Redis database number |

### NATS JetStream

| Variable | Default | Description |
|----------|---------|-------------|
| `NATS_URL` | `nats://localhost:4223` | NATS connection URL |

### S3 / MinIO

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | `localhost:9002` | S3-compatible endpoint |
| `S3_ACCESS_KEY_ID` | `minioadmin` | S3 access key |
| `S3_SECRET_ACCESS_KEY` | `minioadmin` | S3 secret key |
| `S3_BUCKET` | `mesh-artifacts` | Bucket name for artifacts and workspace icons. Created automatically at API startup if missing. |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_USE_SSL` | `false` | Use SSL for S3 connections |
| `S3_PUBLIC_URL` | *(empty)* | Public base URL for **artifact** downloads (e.g. `https://mesh.example.com/s3`). Leave empty to use presigned S3 URLs. Not needed for workspace icons — those are served by the API itself. |

See [File storage](#file-storage) below for what the bucket holds and how to
check it.

### Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `change-me-in-production` | JWT signing secret (use 32+ chars) |
| `CASDOOR_ENDPOINT` | *(empty)* | Casdoor SSO endpoint (optional) |
| `CASDOOR_CLIENT_ID` | *(empty)* | Casdoor client ID (optional) |
| `AGENT_KEY_PREFIX` | `agk` | Prefix for agent API keys |
| `MESH_ALLOW_REGISTRATION` | `true` | Whether `POST /auth/register` accepts new signups. See [Closing registration](#closing-registration) below. |

### First admin

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_SEED_ADMIN` | `false` | Create the first admin at boot. Runs **only** when the database has zero users. Must be the exact string `true`; anything else (including `1` or `TRUE`) is treated as off. **`docker-compose.prod.yml` overrides this default to `true`** — under production compose the seed is armed unless you set it to `false` yourself. |
| `MESH_ADMIN_EMAIL` | `admin@localhost` | Email for the seeded admin |
| `MESH_ADMIN_PASSWORD` | *(generated)* | Password for the seeded admin. If unset, a strong one is generated and printed **once** at boot. |
| `MESH_ADMIN_NAME` | `Admin` | Display name for the seeded admin |

See [Seeding the first admin](#seeding-the-first-admin) below.

### CORS

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_CORS_ORIGINS` | `*` | Comma-separated list of allowed origins (e.g. `https://mesh.example.com,https://app.example.com`). Use `*` for development only. |
| `MESH_COOKIE_INSECURE` | unset | Set to `true` **only** when serving Mesh over plain HTTP on a trusted network. It removes the `Secure` attribute from the refresh-token cookie, which over HTTPS would put a 7-day refresh token on the wire in cleartext. Localhost development does not need this — loopback requests are detected automatically. |

> **Note on reverse proxies.** Mesh does not read `X-Forwarded-Proto` when
> deciding whether to mark the refresh cookie `Secure`, because that header
> reflects only the last hop: a proxy chain that terminates TLS at the edge and
> forwards over plain HTTP will commonly rewrite it to `http`, which would
> silently strip `Secure` on a genuinely HTTPS site. The cookie is marked
> `Secure` unless the request is to loopback or you set the variable above.

> **Topology constraint: the frontend must be same-site with the API.** The
> refresh cookie is `SameSite=Strict`, so browsers send it only on same-site
> requests. "Same-site" is judged on the registrable domain, not the origin, so
> `mesh.example.com` and `app.example.com` are same-site and work normally —
> different origins, one site. What does *not* work is serving the frontend from
> a genuinely different site (say `mesh-ui.net` against an API on
> `example.com`): the browser withholds the cookie, refresh fails, and adding
> the origin to `MESH_CORS_ORIGINS` will not help, because the restriction is
> the cookie's, not CORS's. Keep the UI and the API under one registrable
> domain.

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_RATE_LIMIT_ENABLED` | `true` | Enable or disable rate limiting globally |
| `MESH_RATE_LIMIT_AUTH_RPM` | `5` | Maximum requests per minute for auth endpoints (per IP). Deliberately tight — raise it if a whole office shares one NAT'd egress IP. |
| `MESH_RATE_LIMIT_REFRESH_RPM` | `60` | Maximum requests per minute for token refresh (per IP) |
| `MESH_RATE_LIMIT_API_RPM` | `600` | Maximum requests per minute for API endpoints (per authenticated actor) |

### Spark Catalog

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_SPARK_URL` | *(empty)* | Spark agent catalog API base URL. Required if you enable Spark. |
| `MESH_SPARK_ENABLED` | `false` | Enable Spark catalog routes (`/api/v1/spark/...`) |

### Outbound email

**Email is not configured by default, and that is a supported way to run Mesh.**
With `SMTP_HOST` unset nothing is emailed; instead every invite hands you its
link directly in the UI, with a copy button, for you to pass on however you
like. The invite itself is completely normal — it is only the delivery that is
manual.

The API says which mode it is in once at startup:

```
Email is not configured (SMTP_HOST is empty) — no invitation emails will be sent. ...
Email ready: sending via smtp.example.com:587 as "mesh@example.com"
```

Only set the variables below if you want Mesh to send the mail itself.

| Variable | Default | Description |
|----------|---------|-------------|
| `SMTP_HOST` | *(empty)* | SMTP server host. Empty disables sending. |
| `SMTP_PORT` | `587` | SMTP server port |
| `SMTP_USER` | *(empty)* | SMTP username |
| `SMTP_PASSWORD` | *(empty)* | SMTP password |
| `SMTP_FROM` | *(empty)* | From address on outbound mail. Set this if you set `SMTP_HOST` — a configured server with no From address will have outbound mail rejected by most providers. |
| `MESH_BASE_URL` | `http://localhost:5173` | Public base URL of the web UI. Used to build invite links — set this or invitees get a `localhost` link. |

> **Email addresses are canonicalized.** Registration, login and invites trim
> surrounding whitespace and lowercase the address, and the database enforces
> uniqueness on `lower(email)`. `Carol@Example.COM` and `carol@example.com` are
> the same account.

### Telegram notifications

The Telegram channel is configured per workspace from the **Integrations**
page, not through environment variables. It has one infrastructure
requirement, and it is easy to miss because nothing else in Mesh has it:

> **The `api` container must be able to reach `api.telegram.org` on port 443.**
> Either allow outbound HTTPS to that host, or give the container an
> `HTTPS_PROXY` / `HTTP_PROXY` that can.

Check from the host running the container:

```bash
curl -sS --max-time 10 https://api.telegram.org
# healthy: {"ok":false,"error_code":404,...}  <- a reply at all is the point
# blocked: curl: (28) Operation timed out
```

and from inside the container itself, which is the answer that actually
matters — host and container do not always have the same egress:

```bash
docker compose exec api curl -sS --max-time 10 https://api.telegram.org
```

Without that route the channel fails in a way that looks like nothing is
wrong: the bot is configured, the settings page offers the connect link, users
bind their accounts successfully — and no message is ever delivered, because
every send times out. Mesh reports this rather than leaving you to guess:

- **Notification settings** shows *"Telegram notifications cannot be delivered
  right now"* with the reason, above the Telegram controls.
- **The API log** names the host and the fix on every failed call, e.g.
  `telegram sendMessage: cannot reach https://api.telegram.org — check that
  this host has outbound HTTPS (443) access to it, or set HTTPS_PROXY ...`

If you do not use the Telegram channel, none of this applies — leave it
unconfigured and Mesh never calls out.

### MCP server

Read by the `mesh-mcp` binary (`cmd/mcp`), not by the API. In SSE mode
`docker-compose.prod.yml` sets the transport, host and port for you, and also
proxies it under `/mcp/` on the same origin as the web UI (`nginx.conf`) —
`https://<your-host>/mcp/sse` works out of the box once `MESH_BASE_URL` is set,
with no separate port to open.

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_MCP_TRANSPORT` | `stdio` | `stdio` or `sse`. The `--transport` flag overrides it. Any other value is fatal at startup. |
| `MESH_API_URL` | `http://localhost:8005` | Mesh REST API the MCP server proxies to |
| `MESH_AGENT_KEY` | *(none)* | Agent key for stdio mode. **Required** — stdio mode exits without it. Ignored in SSE mode, where each connection carries its own key. |
| `MESH_MCP_HOST` | `0.0.0.0` | SSE listen host |
| `MESH_MCP_PORT` | `8081` | SSE listen port |
| `MESH_MCP_PUBLIC_URL` | *(empty — the binary's own default; see note)* | Public base URL of the SSE server (e.g. `https://mesh.example.com/mcp`). Empty means the message endpoint is advertised as a path relative to whatever URL the client connected to — correct for localhost and a directly-published container port, but **not** for a reverse proxy that strips a path prefix (like the bundled nginx's `/mcp/` route), which needs the prefix included explicitly. |
| `MESH_MCP_PROFILE` | `full` | `full` (49 tools) or `core` (21 tools). Applies to stdio mode; in SSE mode the profile is chosen by which endpoint the client connects to (`/sse` vs `/core/sse`). |

See [Agent onboarding](agent-onboarding.md) for issuing keys and connecting a
client.

### Embeddings (semantic memory recall)

Unset, the API runs a no-op embedder and memory recall is keyword-only. Nothing
fails; results are just less good. `docker-compose.prod.yml` does not pass these
through, so setting them requires editing the compose file's `environment:`
block as well.

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBEDDING_PROVIDER` | `none` | `none`, `ollama` or `openai` |
| `EMBEDDING_MODEL` | *(provider default)* | `nomic-embed-text` for ollama, `text-embedding-3-small` for openai |
| `EMBEDDING_ENDPOINT` | *(provider default)* | `http://localhost:11434` for ollama, `https://api.openai.com` for openai |
| `EMBEDDING_API_KEY` | *(empty)* | Bearer token for the openai provider |
| `EMBEDDING_DIMENSIONS` | `0` | Expected vector length (`0` = whatever the model returns) |
| `EMBEDDING_BATCH_SIZE` | `32` | Texts per batch embed call |
| `EMBEDDING_CONCURRENCY` | `0` | Concurrent embed calls (`0` = unbounded) |
| `EMBEDDING_HTTP_TIMEOUT_SECS` | `30` | Embedder HTTP timeout |

### Memory recall tuning

Scoring knobs for `recall`. The defaults are the tuned values; change them only
with a benchmark in hand.

| Variable | Default | Description |
|----------|---------|-------------|
| `MEMORY_RECALL_HALF_LIFE_DAYS` | `30` | Half-life of the recency decay applied to recall scores |
| `MEMORY_RECALL_RRF_VECTOR_WEIGHT` | `0.7` | Weight of the vector arm in reciprocal-rank fusion (`0` disables it) |
| `MEMORY_RECALL_RRF_TEXT_WEIGHT` | `0.3` | Weight of the keyword arm |
| `RECONCILER_EPOCH` | *(build time)* | RFC3339 cutoff; only memories created after it are eligible for stale-marking. Guards against a cold start marking your whole history stale. Unparseable values are logged and ignored. |

### Browser push notifications

Set all three or leave all three empty. With the keypair empty push is
disabled — the API logs `[push] VAPID keys not set — browser push disabled
(safe for local dev)` and carries on. `MESH_VAPID_SUBJECT` is not part of that
check, but push services reject a VAPID token whose `sub` claim is missing, so
keys without a subject give you a service that looks enabled and delivers
nothing.

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_VAPID_PUBLIC_KEY` | *(empty)* | VAPID public key |
| `MESH_VAPID_PRIVATE_KEY` | *(empty)* | VAPID private key |
| `MESH_VAPID_SUBJECT` | *(empty)* | `sub` claim of the VAPID JWT (a `mailto:` address or URL). Required if you set the VAPID keys above. |

### Integrations

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_INTEGRATION_ENCRYPTION_KEY` | *(empty)* | Base64 32-byte AES-256-GCM key encrypting stored integration credentials. **Empty means those credentials are stored in plaintext** — the API logs a warning at startup and exports `mesh_integration_encryption_active 0`. See the [Security Checklist](#security-checklist). Generate with `openssl rand -base64 32`. |
| `MESH_INTEGRATION_ENCRYPTION_REQUIRED` | `false` | Refuse to start, and refuse to store a credential, when the key above is missing or malformed. Recommended once you hold real credentials: a mistyped key otherwise degrades to plaintext storage and nothing fails. |
| `MESH_GITHUB_WEBHOOK_SECRET` | *(empty)* | HMAC-SHA256 secret for inbound GitHub webhooks. Empty disables signature validation, i.e. anyone who can reach the webhook endpoint can post to it. |
| `MESH_TEAMRELAY_TRANSPORT_ENABLED` | `false` | Push artifacts to Team Relay. Must be the exact string `true`. |
| `MESH_TEAMRELAY_RELAY_URL` | *(empty)* | Team Relay API base URL. Empty makes relay search and preview return `503`. |
| `MESH_TEAMRELAY_WEB_BASE_URL` | *(falls back to `MESH_TEAMRELAY_RELAY_URL`)* | Base URL used to build relay preview links |

None of these are passed through by `docker-compose.prod.yml`; add them to the
`api` service's `environment:` block if you need them.

### Escape hatches

| Variable | Default | Description |
|----------|---------|-------------|
| `MESH_ALLOW_REVIEW_AT_CREATE` | *(off)* | Set to `true` to let `POST /tasks` create a task directly in a review-category status. Intended for bulk imports and migrations; leave off otherwise. |

---

## File storage

Two features write files to object storage: **task artifacts** and the
**workspace icon** (the PNG shown in the sidebar and used as the browser
favicon). Both live in a single bucket, `S3_BUCKET` (default `mesh-artifacts`).

### What you have to do

Nothing. The bundled `minio` service is started by both compose files, and the
API **creates the bucket at startup if it does not exist**. You do not need to
open the MinIO console or run `mc mb` by hand.

Confirm it on the first boot — the API logs one line either way:

```bash
cd deploy/docker/mesh
docker compose -f docker-compose.prod.yml --env-file .env logs api | grep -i "object storage"
```

```
Object storage ready: bucket "mesh-artifacts" on minio:9000
```

If the bucket could not be created you get a warning naming the bucket, the
endpoint and the reason, and uploads will fail until it is fixed:

```
WARNING: object storage bucket "mesh-artifacts" on minio:9000 is not usable —
artifact and workspace-icon uploads will fail until this is fixed: ...
```

The API keeps serving everything else; it retries the create on the next
upload, so fixing credentials or starting the storage service is enough — no
API restart required.

### Using an external S3 bucket

Point `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_BUCKET`,
`S3_REGION` and `S3_USE_SSL` at it, and drop the `minio` service. If the
credentials are not allowed to create buckets, create `S3_BUCKET` yourself
first — the startup log will tell you if this is the case.

### How files are served back

| | Served by | Needs `S3_PUBLIC_URL`? |
|---|---|---|
| Workspace icon | The API streams the bytes (`GET /api/v1/workspaces/{id}/icon`) | No |
| Task artifacts | Redirect to a presigned storage URL | Yes, if storage is not reachable from the browser |

Under the bundled compose stack storage is *never* reachable from the browser —
`minio:9000` is an internal name on an unpublished port — so artifact downloads
need `S3_PUBLIC_URL`. The bundled nginx ships a `/s3/` location that proxies
through to MinIO for exactly this, so setting
`S3_PUBLIC_URL=https://your-domain.com/s3` is all that is required. That
location deliberately does not override the `Host` header: the presigned
signature is computed against `S3_ENDPOINT`, and MinIO validates it against the
`Host` it receives, so nginx must pass the upstream host through unchanged.

Note that `/s3/` fronts the MinIO endpoint as a whole, not just the artifact
bucket. Every operation behind it still requires a valid SigV4 signature —
unsigned requests get `403` — but if you would rather not expose it at all,
leave `S3_PUBLIC_URL` empty and put artifacts on an external bucket the browser
can reach directly.

The workspace icon is deliberately **not** served via a presigned URL. A
presigned URL is built from `S3_ENDPOINT`, which under compose is the internal
host `minio:9000` — a name that does not resolve in a browser and a port that is
not published. Streaming it through the API means the icon works behind any
reverse proxy, tunnel or path prefix with no extra configuration, and the
storage backend is never exposed. The icon is capped at 500 KB and carries an
`ETag`, so repeat requests are answered `304 Not Modified`.

`GET /api/v1/workspaces/{id}/icon` is **unauthenticated** — it has to be, since
the browser loads it as a plain `<img>` and `<link rel="icon">`, neither of
which sends an `Authorization` header. Reaching it requires knowing the
workspace UUID, which is only ever handed out in authenticated responses; a
workspace with no icon and a workspace that does not exist both answer `404`.
No other workspace data is exposed by this route.

### Checking what is in the bucket

```bash
cd deploy/docker/mesh
docker compose -f docker-compose.prod.yml --env-file .env exec minio \
  sh -c 'mc alias set l http://localhost:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc ls --recursive l/mesh-artifacts'
```

Workspace icons are stored under `workspaces/<workspace-id>/icon.png`.

---

## Seeding the first admin

A fresh install has **no users and no default password**. Until an account
exists, every login attempt correctly returns `401 invalid email or password`.

There are two ways to create that first account.

### Option 1 — register in the web UI (simplest)

Open `/register` and sign up. The first account you register is yours and gets
its own workspace. Registration is a public endpoint by default, so nothing
needs to be configured first — see [Closing registration](#closing-registration)
once you have that first account and want to lock the door behind you.

Passwords must be 8+ characters with an uppercase letter, a lowercase letter,
and a digit — a weaker one is rejected with `400`, not `401`.

### Option 2 — seed at boot (scripted or headless installs)

Start the API once with `MESH_SEED_ADMIN=true`:

```bash
MESH_SEED_ADMIN=true \
MESH_ADMIN_EMAIL=you@example.com \
MESH_ADMIN_PASSWORD='<strong-password>' \
docker compose -f docker-compose.prod.yml up -d
```

Omit `MESH_ADMIN_PASSWORD` and the server generates a strong one and prints it
once:

```
[bootstrap] ────────────────────────────────────────────────────────
[bootstrap] First admin created: admin@localhost
[bootstrap] Generated password:  XaSfco7rmK4jSkhSGZETGM6a
[bootstrap] This password is shown ONCE and is not stored anywhere.
```

Copy it before the log scrolls away, then change it after logging in.

### Two rules worth knowing

**The seed only runs on a database with zero users.** This is deliberate — it
means an accidental `MESH_SEED_ADMIN=true` on a running instance can never
overwrite or re-create accounts. If you registered a user first and *then* set
the flag, the seed will not run.

**The seed only runs at API startup.** Setting the environment variable on an
already-running container does nothing; restart the API for it to take effect.

### Reading the boot log

The API states what it did on every boot, so you never have to guess:

| Log line | Meaning | What to do |
|---|---|---|
| `First admin created: <email>` | Seed succeeded | Log in with that account |
| `MESH_SEED_ADMIN=true, but the database already has N user(s)` | Skipped by design | Log in with an existing account |
| `The database has no users yet, so nobody can log in.` | No account, no seed requested | Register at `/register`, or restart with `MESH_SEED_ADMIN=true` |
| `first-admin seed FAILED: ...` | Seed errored | Fix the reported cause and restart |

### Lost the admin password?

A self-hosted install has no password-reset email. Passwords are stored as
bcrypt hashes, so the fix is to write a new hash directly into the `users` row.

Generate a bcrypt hash (any bcrypt tool works; Python's `bcrypt` package is the
shortest path):

```bash
python3 -c "import bcrypt; print(bcrypt.hashpw(b'<new-password>', bcrypt.gensalt()).decode())"
```

Then set it for the account, and log in with the new password:

```bash
docker compose exec postgres psql -U mesh -d mesh \
  -c "UPDATE users SET password_hash = '<hash-from-above>' WHERE email = 'you@example.com';"
```

The new password must satisfy the same rules as registration (8+ characters,
upper, lower, digit) or you will not be able to change it later from the UI.

If no account is recoverable at all and the install holds nothing you need, you
can wipe the users and let the seed run again on the next boot:

```bash
docker compose exec postgres psql -U mesh -d mesh \
  -c 'TRUNCATE users, workspaces, workspace_members CASCADE;'
# then restart the API with MESH_SEED_ADMIN=true
```

⚠️ This deletes **all** workspace content — tasks, projects, comments,
artifacts — owned by those users. Back up first (see
[Backup & Restore](#backup--restore)).

### Closing registration

By default `POST /auth/register` is a **public, unauthenticated endpoint** —
anyone who can reach the instance can create an account. Each new account gets
its own workspace, and every route that names workspace-owned data enforces
membership (see [Security Checklist](#security-checklist), item 7) — with
one **known exception documented there**, which is why closing registration is
recommended rather than optional. On an internet-facing self-host instance the
open endpoint also means: unlimited accounts consuming your
database/S3/JetStream, and behavior most operators running a small team (3-10
people) do not expect from a "closed" tool.

Set `MESH_ALLOW_REGISTRATION=false` and restart the API to close it:

```bash
MESH_ALLOW_REGISTRATION=false docker compose -f docker-compose.prod.yml up -d
```

With the flag off:

- `POST /auth/register` returns `403` with
  `"registration is closed on this instance — ask an admin for an invite"`.
- The web UI's login page stops showing the "Register" link (via
  `GET /auth/config`), and `/register` shows a "Registration is closed" notice
  instead of the signup form.
- **The very first user can always register**, regardless of the flag — the
  server checks `COUNT(*) FROM users` and only enforces the flag once at least
  one account exists. This is deliberate: it is the same bootstrap invariant
  [Seeding the first admin](#seeding-the-first-admin) relies on, and without it
  a closed instance could never be stood up in the first place. So the safe
  sequence for a brand-new install you intend to keep closed is: deploy with
  the default (`MESH_ALLOW_REGISTRATION` unset/`true`) → register the first
  admin account → **then** set `MESH_ALLOW_REGISTRATION=false` and restart.

**Closed registration does not close the instance to new people** — it closes
the walk-up path. An existing member with `PermManageMembers` can still invite
anyone from a workspace's Members page (or `POST /workspaces/:ws_id/invites`),
which is a **separate code path** from `/auth/register` and is unaffected by
this flag either way:

```bash
curl -X POST https://mesh.yourdomain.com/api/v1/workspaces/<ws_id>/invites \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"email":"newperson@example.com","role":"member"}'
```

The invited person gets a link to `/accept-invite/<token>`, sets their own
password there, and is added straight to that workspace — no open `/register`
needed. `GET /api/v1/invites/:token` and `POST /api/v1/invites/:token/accept`
stay public precisely so this path works on a fully closed instance.

The response tells you whether the invite was actually emailed — do not read the
`201` as delivery:

```json
{
  "id": "...", "email": "newperson@example.com", "token": "...",
  "email_sent": false,
  "delivery_status": "not_configured",
  "invite_url": "https://mesh.example.com/accept-invite/<token>"
}
```

`delivery_status` is `sent`, `not_configured` (no `SMTP_HOST`), or `failed`
(configured, but the send did not work — `delivery_error` says why). `invite_url`
is always present, so whenever `email_sent` is `false` you have the link to pass
along yourself. The UI shows it with a copy button in exactly those cases.

**The shipped templates default to closed; the binary still defaults to open.**
`.env.example` and `deploy/docker/mesh/.env.prod.example` both ship
`MESH_ALLOW_REGISTRATION=false`, so a new install that starts from a template is
closed from the first boot — and thanks to the zero-users exception above, you
can still register your first admin normally. The binary's own fallback stays
`true` so that existing instances upgrading to this version do not have
registration disappear underneath them. A future release may flip that too, in
its own CHANGELOG entry.

Why closed is the right starting point: on our own instance, one day of open
registration produced eight accounts nobody had invited, and one of them used
the cross-workspace user lookup described in the
[Security Checklist](#security-checklist) to find an administrator and add them
to a workspace of their own. Nothing there was a bug — it is what an open
`/register` on a public address means.

---

## Production Deployment

### Docker Compose (Production)

For production, use `deploy/docker/mesh/docker-compose.prod.yml` instead of `deploy/docker/mesh/docker-compose.yml`. It builds all services from source and adds nginx, Prometheus, and Grafana.

```bash
cd deploy/docker/mesh

# 1. Create the env file. The name matters -- see the warning below.
cp .env.prod.example .env
#    nano .env   -- fill in every variable under the "# Required" comment near
#                   the top of the file. Treat that comment as authoritative,
#                   not this sentence: deploy/docker/mesh/check-required-env.sh
#                   enforces in CI that it lists exactly what
#                   docker-compose.prod.yml requires, so it cannot go stale the
#                   way this paragraph twice has. As of this writing that's
#                   POSTGRES_PASSWORD, REDIS_PASSWORD, JWT_SECRET,
#                   MINIO_ACCESS_KEY, MINIO_SECRET_KEY, GRAFANA_PASSWORD.

# 2. Build and start all services
docker compose -f docker-compose.prod.yml --env-file .env up -d --build
# or from the repo root: make docker-prod-up

# 3. Verify all services are healthy
docker compose -f docker-compose.prod.yml --env-file .env ps
```

> ⚠️ **The file must be called `.env`, in `deploy/docker/mesh/`.** The template
> is named `.env.prod.example`, which makes `.env.prod` the obvious name to copy
> it to — but Compose only auto-loads `.env` from the directory holding the
> compose file, and it never reads the repo-root `.env`. With a `.env.prod`
> sitting right there, `docker compose -f docker-compose.prod.yml config` still
> fails with `required variable POSTGRES_PASSWORD is missing a value`, which
> reads like a missing variable rather than an unread file.
>
> Pass `--env-file .env` explicitly on every command, as above. It costs
> nothing when the name is already right and it turns a silent misread into an
> explicit one. Verify what Compose actually resolved before starting anything:
>
> ```bash
> docker compose -f docker-compose.prod.yml --env-file .env config | \
>   grep -E 'MESH_BASE_URL|MESH_ALLOW_REGISTRATION|MESH_SEED_ADMIN'
> ```

Services included in `docker-compose.prod.yml`:

| Service | Port | Description |
|---------|------|-------------|
| `postgres` | *(internal)* | PostgreSQL 16 — required env: `POSTGRES_PASSWORD` |
| `redis` | *(internal)* | Redis 7 with password — required env: `REDIS_PASSWORD` |
| `nats` | *(internal)* | NATS 2.10 with JetStream enabled |
| `minio` | *(internal)* | MinIO object storage — required env: `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` |
| `api` | `${API_PORT:-8005}` | Mesh API server (Go binary, runs DB migrations on startup) |
| `mcp` | `${MCP_PORT:-8081}` | MCP server in SSE mode for remote agents |
| `nginx` | `${HTTP_PORT:-80}` | Nginx serving the React SPA, proxying `/api`, `/ws` and `/mcp` (SSE, see [Agent onboarding](agent-onboarding.md)) |
| `prometheus` | `${PROMETHEUS_PORT:-9090}` | Prometheus scraping `/metrics` from the API |
| `grafana` | `${GRAFANA_PORT:-3001}` | Grafana dashboards — password set by `GRAFANA_PASSWORD` (required, no default) |

Required environment variables for production:

```bash
POSTGRES_PASSWORD=your-strong-db-password
REDIS_PASSWORD=your-redis-password
JWT_SECRET=your-32-char-minimum-secret
MINIO_ACCESS_KEY=your-minio-access-key
MINIO_SECRET_KEY=your-minio-secret-key
GRAFANA_PASSWORD=your-grafana-admin-password
# Optional:
MESH_CORS_ORIGINS=https://mesh.yourdomain.com   # CORS_ORIGINS is also accepted
MESH_ALLOW_REGISTRATION=false                   # close self-registration
```

**Not every variable in the reference above reaches the container.**
`docker-compose.prod.yml` passes through the core set — server, database, Redis,
NATS, S3, auth, CORS, rate limits, first-admin seed, SMTP, `MESH_BASE_URL`,
`MESH_ALLOW_REGISTRATION` — and setting those in `.env` is enough. The
following groups are **not** in the `api` service's `environment:` block, so
setting them in `.env` does nothing; add them to the compose file too if you
need them:

- `EMBEDDING_*` — production runs the no-op embedder, i.e. keyword-only recall
- `MESH_VAPID_*` — browser push is off
- `MESH_TEAMRELAY_*` — Team Relay routes answer `503`
- `MEMORY_RECALL_*`, `MESH_ALLOW_REVIEW_AT_CREATE`

`MESH_INTEGRATION_ENCRYPTION_KEY` and `MESH_GITHUB_WEBHOOK_SECRET` used to be
on that list, and both fail silently when unset — plaintext integration
credentials and unverified webhook signatures respectively. A security control
you must first edit the compose file to enable is one most people will not
enable, so they are now passed through: set them in `.env` and they take
effect.

`DB_PASSWORD` and the S3 credentials work differently on purpose: under compose
they are supplied by `POSTGRES_PASSWORD` and `MINIO_ACCESS_KEY` /
`MINIO_SECRET_KEY`, so one value configures both the server and its client.
These are the names that differ — everything else in `.env` reaches the API
under the same name it has in the file:

| In `.env` | What the API reads | Also configures |
|---|---|---|
| `POSTGRES_PASSWORD` | `DB_PASSWORD` | the `postgres` server's own password |
| `DB_USER` | `DB_USER` | `POSTGRES_USER` — one knob, both sides |
| `DB_NAME` | `DB_NAME` | `POSTGRES_DB` — one knob, both sides |
| `MINIO_ACCESS_KEY` | `S3_ACCESS_KEY_ID` | MinIO's `MINIO_ROOT_USER` |
| `MINIO_SECRET_KEY` | `S3_SECRET_ACCESS_KEY` | MinIO's `MINIO_ROOT_PASSWORD` |
| `CORS_ORIGINS` | `MESH_CORS_ORIGINS` | *(legacy spelling, still accepted)* |
| `GRAFANA_PASSWORD` | *(not the API)* | Grafana's `GF_SECURITY_ADMIN_PASSWORD` |

⚠️ **Editing `POSTGRES_PASSWORD` after the first boot breaks the API.** The
Postgres image applies that variable only when it initialises an empty data
directory; on every later start it is ignored. Change it in `.env`, restart,
and the API dutifully connects with the new value while the server still wants
the old one — `password authentication failed for user "mesh"`. Verified on a
running stack:

```bash
# from a throwaway client on the compose network, after rotating .env
psql -h postgres -U mesh -d mesh -c 'select 1'
#   old password → 1
#   new password → FATAL: password authentication failed for user "mesh"
```

Rotate it in both places instead — `ALTER USER mesh PASSWORD '<new>';` on the
server, then the same value in `.env`.

(Test this yourself from *outside* the container: `docker exec … psql -U mesh`
connects over the local socket, and `127.0.0.1` inside the container is
`trust` in the image's `pg_hba.conf`. Both accept any password whatsoever, so
either one will tell you a rotation "worked" when it did not.)

When in doubt, ask Compose rather than the docs:

```bash
docker compose -f docker-compose.prod.yml --env-file .env config | \
  sed -n '/^  api:/,/^  [a-z]/p'
```

### Security Checklist

1. **JWT_SECRET** -- Generate a strong random secret (32+ characters):
   ```bash
   openssl rand -base64 32
   ```

2. **Database password** -- Change it from the default. Under
   `docker-compose.prod.yml` one variable configures both the server and the
   API client:
   ```bash
   # deploy/docker/mesh/.env
   POSTGRES_PASSWORD=your-strong-db-password
   ```
   Running the API against an external Postgres instead? Set `DB_PASSWORD`
   (plus `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SSL_MODE`) directly.

3. **MinIO credentials** -- Change from defaults. Same pattern: under compose
   these configure both MinIO and the API's S3 client:
   ```bash
   # deploy/docker/mesh/.env
   MINIO_ACCESS_KEY=your-access-key
   MINIO_SECRET_KEY=your-secret-key
   ```
   Against an external S3/R2 bucket, set `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`,
   `S3_SECRET_ACCESS_KEY`, `S3_BUCKET`, `S3_REGION` and `S3_USE_SSL` instead.

4. **Redis password** -- Set a password:
   ```bash
   REDIS_PASSWORD=your-redis-password
   # The production compose file already passes this to redis-server
   ```

5. **CORS** -- Configure allowed origins (the default allows `*`):
   ```bash
   # deploy/docker/mesh/.env
   MESH_CORS_ORIGINS=https://mesh.yourdomain.com
   ```

6. **Registration** -- Keep `MESH_ALLOW_REGISTRATION=false` (the value both env
   templates ship). The first account on an empty database can register
   regardless of the flag, so this costs you nothing during setup and closes the
   door for everyone after. Add people afterward via workspace invites, not the
   open `/register` endpoint. See
   [Closing registration](#closing-registration) — including what happened on
   our own instance when it was left open.

7. **Workspace isolation** -- Every endpoint that names workspace-owned data
   requires the caller to be a member of that workspace (agents: to belong to
   it), enforced for the whole authenticated API group rather than per route.
   A logged-in user from another workspace gets `403` on all of them.

   This covers any route whose path carries `:ws_id`, `:proj_id`, `:task_id`,
   `:artifact_id`, `:agent_id`, `:field_id` or `:init_id` — the tenant is
   resolved from whichever of those the route has, and membership is required
   for the workspace that comes out. A route whose parameter resolves to nothing
   (an unknown or deleted id) answers `403` rather than `404`: the guard runs
   before the handler, and it does not confirm which ids exist.

   **One registered exception:** `GET`/`HEAD /workspaces/:ws_id/icon` is
   readable without authentication, because the browser fetches the workspace
   icon with an `<img>` tag and a `<link rel="icon">`, and neither can send an
   `Authorization` header. Knowing the workspace id is the only requirement, and
   ids are handed out only in authenticated responses; a workspace with no icon
   and a workspace that does not exist answer byte-identically, so this cannot
   be used to discover workspaces. Uploading an icon (`PUT`) is unaffected and
   stays owner/admin only. The exception is registered in `wsPublicRoutes`
   (`tests/integration/cross_tenant_test.go`) — a workspace route that leaves
   the guard without being registered there fails the build.

   The live event stream (`/ws`) applies the same rule. It is upgraded before any
   API middleware runs, so it checks membership itself: the `workspace=` query
   parameter is verified during the handshake, and every later
   `{"action":"subscribe"}` is authorised too — a connection may subscribe only
   to its own workspace channel, to projects inside that workspace, and to its
   own user's mention feed. Agent connections take their workspace from the agent
   key, never from the query string.

   Two consequences worth knowing:

   - Looking people up by name or email
     (`GET /workspaces/:ws_id/users/search`) searches the whole instance, not
     just the workspace — that is what makes "add an existing user" work. It is
     therefore restricted to roles that can manage members (owner/admin). On a
     shared instance hosting unrelated tenants, treat workspace admin as
     "can see that an account exists"; invite by email if that is not
     acceptable.
   - Renaming a workspace or changing its slug
     (`PATCH /workspaces/:ws_id`) is owner/admin only, because the slug appears
     in every `/w/<slug>/...` URL your team has saved.

   ⚠️ **Known gap — do not read the above as "isolation is complete".** The
   guard resolves the tenant from a **path parameter**. A route that takes the
   workspace from the **request body** instead has no path parameter to resolve,
   so the guard never runs on it. `POST /api/v1/rules/evaluate` is such a route
   today: it reads `workspace_id` from the JSON body and does not check that the
   caller belongs to it, so any authenticated account can evaluate rules against
   another workspace. Composite routes that name a child object
   (`.../statuses/:status_id`, `.../auto-transition-rules/:rule_id`,
   `/tasks/:task_id/dependencies/:dep_id`) verify the parent but not that the
   child belongs to it.

   Note what this does and does not mean: closing registration stops a **new**
   stranger from obtaining credentials, but it does **not** protect you from
   accounts that already exist on the instance. **If you host unrelated tenants
   on one instance, do not treat it as a security boundary until this is
   closed.** Tracked, with reproductions, as the follow-up to the tenancy work
   in `#416`/`#419`/`#420`.

8. **TLS** -- Set up a reverse proxy (nginx or Caddy) with TLS termination:

   **Caddy example (`Caddyfile`):**
   ```
   mesh.yourdomain.com {
     reverse_proxy /api/* localhost:8005
     reverse_proxy /ws localhost:8005
     reverse_proxy /* localhost:3000
   }
   ```

   **nginx example:**
   ```nginx
   server {
     listen 443 ssl;
     server_name mesh.yourdomain.com;

     ssl_certificate /etc/ssl/certs/mesh.pem;
     ssl_certificate_key /etc/ssl/private/mesh.key;

     location /api/ {
       proxy_pass http://localhost:8005;
     }

     location /ws {
       proxy_pass http://localhost:8005;
       proxy_http_version 1.1;
       proxy_set_header Upgrade $http_upgrade;
       proxy_set_header Connection "upgrade";
     }

     location / {
       proxy_pass http://localhost:3000;
     }
   }
   ```

9. **`/metrics` on both the API and the MCP server is token-gated**, by the
   same `MESH_METRICS_TOKEN`. Compose generates the token on first start
   into `deploy/docker/mesh/volumes/secrets/metrics_token` and gives the
   same file to the API (`MESH_METRICS_TOKEN_FILE`), the MCP service
   (`MESH_METRICS_TOKEN_FILE`), and Prometheus (`bearer_token_file`, see
   `monitoring/prometheus.yml`) — there is nothing to set, and the value is
   not in `docker inspect` output on any of the three. Set
   `MESH_METRICS_TOKEN` in `.env` to choose it yourself; that value is
   written to the same file and wins for both services. Scrape either
   endpoint from outside the stack with
   `curl -H "Authorization: Bearer $(cat deploy/docker/mesh/volumes/secrets/metrics_token)" http://localhost:8005/metrics`
   (API) or the same against `http://localhost:${MCP_PORT:-8081}/metrics`
   (MCP — note the bundled Prometheus does not scrape this one; nothing in
   the stack consumes it, the gate exists so exposing the port doesn't leak
   route names, traffic volumes and workspace/task counts to anyone who can
   reach it). Outside Compose both binaries read `MESH_METRICS_TOKEN`
   directly, and leaving it unset keeps the endpoint open for deployments
   that gate it at the network layer instead (e.g. the internal prod
   install, fronted by Caddy). If you'd rather not expose the MCP metrics
   port at all, bind it to loopback instead:

   ```yaml
   # deploy/docker/mesh/docker-compose.prod.yml, mcp service
   ports:
     - "127.0.0.1:${MCP_PORT:-8081}:8081"
   ```

   Don't do this as the shipped default, though — the mcp service exists so
   remote agents can reach `/sse` and `/message` on this same port; binding
   it to loopback closes those along with `/metrics`.

   The same publish-by-default caveat applies to the `prometheus` (`9090`) and `grafana` (`3001`)
   services: `docker-compose.prod.yml` publishes both on all interfaces.
   Grafana requires `GRAFANA_PASSWORD` to be set (Compose refuses to start
   otherwise) — but the port is still open to the network, so bind it to
   `127.0.0.1` as above, or drop the two services entirely if you are not
   using them.

10. **Do not publish the API port when a proxy fronts it** -- Compose publishes
    `${API_PORT:-8005}` on every interface. With nginx or Caddy terminating TLS
    in front, that port is a plaintext bypass around it, rate limits and all.
    Bind it to `127.0.0.1` as above, or remove the mapping.

    Note that Docker's published ports are inserted into iptables ahead of most
    host firewall rules, so a `ufw`/`firewalld` "deny" you added afterwards
    likely is not covering them. Check from another machine, not from the host.

11. **Set `MESH_INTEGRATION_ENCRYPTION_KEY`** -- Without it, credentials for
    configured integrations are written to the database **in plaintext**; the
    API notes this in a startup warning and continues. It is not passed through
    by `docker-compose.prod.yml`, so it needs adding to the `api` service's
    `environment:` block as well as to `.env`:

    ```bash
    openssl rand -base64 32
    ```

    Then set `MESH_INTEGRATION_ENCRYPTION_REQUIRED=true`, which turns "the key
    is missing or malformed" from a warning into a refusal to start. The two
    settings answer different questions: the key makes encryption *possible*,
    the flag makes a broken key *visible*. A typo in the key alone produces a
    perfectly healthy-looking instance that stores every credential in the
    clear.

    Watch `mesh_integration_encryption_active` — it is `1` when credentials are
    actually being encrypted and `0` when they are not. It is worth an alert:
    the failure mode has no other symptom, because writes succeed either way.

    **Credentials stored before you set the key stay in plaintext.** Encryption
    happens on write, so existing rows are untouched by turning it on. Rewrite
    them with the one-off tool, which verifies each value round-trips before it
    replaces it and does the whole set in one transaction:

    ```bash
    go run ./cmd/encrypt-integration-keys --dry-run   # report only
    go run ./cmd/encrypt-integration-keys             # rewrite
    ```

    From this version a database trigger refuses any *new* unencrypted
    credential in `project_integrations.agent_key`, whatever wrote it — the API,
    `psql`, or an ops script. An instance running without an encryption key is
    still supported and unaffected: the API grants that exemption explicitly for
    the write it knows it cannot encrypt.

12. **Set `MESH_BASE_URL`** -- Not a confidentiality issue, but the one people
    hit: unset, it falls back to `http://localhost:5173` and every workspace
    invite link points at the invitee's own machine. Since this version the API
    warns about it at startup:

    ```
    [config] WARNING: MESH_BASE_URL is not set — workspace invite links will point at http://localhost:5173
    ```

---

## Add your teammates

Registration ships closed, so the admin account seeded at first boot is the only
way in until you invite someone. Nothing below needs SMTP — without it Mesh hands
you the invite link to pass on yourself, which is enough to get a second person
working today.

**From the UI:** workspace → Members → Invite. With no mail server configured the
dialog stays open and shows the invite link with a copy button — that is the
normal flow, not an error. The same link is available from the Resend button on
any pending invite.

**From the API**, using the admin's access token:

```bash
TOKEN=$(curl -s -X POST http://localhost:${HTTP_PORT}/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@localhost","password":"<the bootstrap password>"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["tokens"]["access_token"])')

WS=$(curl -s http://localhost:${HTTP_PORT}/api/v1/workspaces \
  -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])')

curl -s -X POST http://localhost:${HTTP_PORT}/api/v1/workspaces/$WS/invites \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"teammate@example.com","role":"member"}'
```

With `SMTP_HOST` unset, the response says so and carries the link:

```json
{
  "email_sent": false,
  "delivery_status": "not_configured",
  "invite_url": "http://localhost:8080/accept-invite/<token>"
}
```

The link is deliberately **not** written to the API log. It is a bearer
credential for joining the workspace, and logs are routinely shipped to
collectors that more people can read than the workspace has members. The log
records only that no mail was sent, and to whom.

The invitee opens that link, picks their own password, and is signed in — they
do **not** need the `/register` page, which is exactly why closing it costs you
nothing. Their account is created and joined to the workspace in one step.

`MESH_BASE_URL` is what makes the link openable: unset, it falls back to
`http://localhost:5173` and your teammate gets a link to their own machine. The
API warns about this at startup.

---

## Data Persistence

Docker Compose uses bind mounts rooted in `deploy/docker/mesh/volumes/`:

| Host path | Container | Path | Description |
|-----------|-----------|------|-------------|
| `deploy/docker/mesh/volumes/postgres/data/` | postgres | `/var/lib/postgresql/data` | Database storage |
| `deploy/docker/mesh/volumes/redis/data/` | redis | `/data` | Redis persistence (RDB/AOF) |
| `deploy/docker/mesh/volumes/nats/data/` | nats | `/data` | NATS JetStream storage |
| `deploy/docker/mesh/volumes/minio/data/` | minio | `/data` | Object storage (artifacts) |
| `deploy/docker/mesh/volumes/prometheus/data/` | prometheus | `/prometheus` | Prometheus TSDB |
| `deploy/docker/mesh/volumes/grafana/data/` | grafana | `/var/lib/grafana` | Grafana state |

To inspect the bind mount directories:
```bash
find deploy/docker/mesh/volumes -maxdepth 2 -type d
```

---

## Backup & Restore

### PostgreSQL

**Backup:**
```bash
cd deploy/docker/mesh && docker compose exec postgres pg_dump -U mesh mesh > ../../../backup_$(date +%Y%m%d).sql
```

**Restore:**
```bash
cd deploy/docker/mesh && docker compose exec -T postgres psql -U mesh mesh < ../../../backup_20250224.sql
```

### MinIO (Artifacts)

**Backup:**
```bash
# Install mc (MinIO client) if not already installed
# brew install minio/stable/mc

# Configure MinIO alias
mc alias set local http://localhost:9002 minioadmin minioadmin

# Mirror to local directory
mc mirror local/mesh-artifacts ./backup-artifacts/
```

**Restore:**
```bash
mc mirror ./backup-artifacts/ local/mesh-artifacts
```

### NATS JetStream

JetStream stores data on disk in `deploy/docker/mesh/volumes/nats/data/`. For backup:
```bash
cd deploy/docker/mesh
docker compose stop nats
tar czf ../../../nats_backup.tar.gz volumes/nats/data
docker compose start nats
```

### Full Backup Script

```bash
#!/bin/bash
BACKUP_DIR="./backups/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# PostgreSQL
(
  cd deploy/docker/mesh
  docker compose exec -T postgres pg_dump -U mesh mesh
) > "$BACKUP_DIR/postgres.sql"

# MinIO
mc mirror local/mesh-artifacts "$BACKUP_DIR/artifacts/"

# NATS (stop briefly)
(
  cd deploy/docker/mesh
  docker compose stop nats
  tar czf "../../../$BACKUP_DIR/nats.tar.gz" volumes/nats/data
  docker compose start nats
)

echo "Backup complete: $BACKUP_DIR"
```

---

## Health Checks

All infrastructure containers have built-in health checks. Additionally:

| Service | Health Check | Expected |
|---------|-------------|----------|
| API | `curl http://localhost:8005/health` | `{"status":"ok","service":"evc-mesh-api"}` |
| API version | `curl http://localhost:8005/api/version` | `{"commit":"abc1234","build_time":"...","version":"dev","environment":"dev","service":"evc-mesh-api"}` |
| PostgreSQL | `cd deploy/docker/mesh && docker compose exec postgres pg_isready -U mesh` | `accepting connections` |
| Redis | `cd deploy/docker/mesh && docker compose exec redis redis-cli ping` | `PONG` |
| NATS | `curl http://localhost:8223/healthz` | `ok` |
| MinIO | `cd deploy/docker/mesh && docker compose exec minio mc ready local` | exit code 0 |

Check all containers at once:
```bash
cd deploy/docker/mesh && docker compose ps
```

All services should show `healthy` status.

---

## Troubleshooting

### PostgreSQL connection refused

**Symptom:** API fails to start with `connection refused` on port 5437.

**Solution:** Wait for PostgreSQL to fully initialize:
```bash
cd deploy/docker/mesh
docker compose up -d postgres
# Wait for health check
until docker compose exec postgres pg_isready -U mesh; do sleep 1; done
# Then start the API
cd ../..
go run ./cmd/api
```

### Icon or artifact upload fails

**Symptom:** Uploading a workspace icon or a task artifact returns an error.

The API names the cause in the response and in its log — read it first, the
three cases have different fixes:

| Message | Cause | Fix |
|---------|-------|-----|
| *…bucket does not exist and could not be created…* | The credentials may not create buckets | Create `S3_BUCKET` manually (below), or grant `CreateBucket` |
| *…rejected the credentials…* | Wrong keys | Check `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` (under compose: `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY`) |
| *…storage is unreachable…* | Storage is down or `S3_ENDPOINT` is wrong | Check the `minio` service is healthy and `S3_ENDPOINT` points at it |

The bucket is created automatically at API startup and re-created on demand at
upload time (see [File storage](#file-storage)). To create it by hand anyway:

```bash
cd deploy/docker/mesh
docker compose -f docker-compose.prod.yml --env-file .env exec minio \
  sh -c 'mc alias set l http://localhost:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc mb l/mesh-artifacts'
```

### Workspace icon uploads but does not appear

Hard-refresh once — the icon is cached for 5 minutes. If it is still missing,
check that your reverse proxy forwards `/api/` to the API without rewriting it;
`GET /api/v1/workspaces/<id>/icon` must return `200` with `Content-Type:
image/png`:

```bash
curl -i http://localhost/api/v1/workspaces/<workspace-id>/icon | head -5
```

### NATS JetStream not available

**Symptom:** Events fail with "JetStream not available".

**Solution:** Ensure NATS started with JetStream enabled:
```bash
cd deploy/docker/mesh && docker compose logs nats | grep "JetStream"
# Should show: "JetStream is ready"
```

### Port conflicts

**Symptom:** `address already in use` errors.

**Solution:** Check what is using the ports:
```bash
lsof -i :5437  # PostgreSQL
lsof -i :6383  # Redis
lsof -i :4223  # NATS
lsof -i :9002  # MinIO S3
lsof -i :9003  # MinIO Console
lsof -i :8005  # API
lsof -i :3000  # Frontend
```

Adjust ports in `deploy/docker/mesh/docker-compose.yml` and `deploy/docker/mesh/.env` if needed.

### `429 Rate limit exceeded` while logging in or testing

**Symptom:** login, register or invite calls start answering
`{"code":429,"message":"Rate limit exceeded"}` after a handful of attempts.

**Cause:** `MESH_RATE_LIMIT_AUTH_RPM` defaults to **5 requests per minute, per
IP** — deliberately tight against credential stuffing. Behind one NAT'd office
egress, or while scripting a setup, five is easy to spend: a login, a couple of
retries and an invite acceptance will do it. The window is a minute; waiting
clears it.

**Solution:** raise it in `.env` if a whole team shares one egress IP:

```bash
MESH_RATE_LIMIT_AUTH_RPM=30
```

Do not raise it for a public instance without thinking about why it is 5.

### `403 registration is closed on this instance`

**Not a misconfiguration.** Both env templates ship
`MESH_ALLOW_REGISTRATION=false`, so `/register` is closed to walk-ups by design.
The exception is the *first* account on an empty database, which can always
register — so this never locks you out of a fresh install.

Add people with [workspace invites](#add-your-teammates) instead. To reopen
public registration anyway, set `MESH_ALLOW_REGISTRATION=true` and read
[Closing registration](#closing-registration) first — it describes what happened
on our own instance when it was left open.

### The invitee cannot open their invite link

**Symptom:** the link points at `http://localhost:5173` (or any host that is not
your instance), so it opens nothing on their machine.

**Cause:** `MESH_BASE_URL` was unset when the invite was created — links are
built from it, and it falls back to a localhost dev URL. The API says so at
startup: `[config] WARNING: MESH_BASE_URL is not set`.

**Solution:** set `MESH_BASE_URL` to the URL your team actually types, restart
the API, and issue a **new** invite. Existing invite links keep the old host
baked in; the token is still valid, so the invitee can also just replace the
scheme+host in the link by hand.

### Frontend build fails

**Symptom:** `pnpm install` or `pnpm dev` fails.

**Solution:**
```bash
cd web
rm -rf node_modules .next
pnpm install
pnpm dev
```

### WebSocket connection drops

**Symptom:** Real-time updates stop working in the frontend.

**Solution:** If behind a reverse proxy, ensure WebSocket upgrade headers are forwarded (see the nginx config above). Also check that `SERVER_READ_TIMEOUT` and `SERVER_WRITE_TIMEOUT` are not too short for long-lived connections.

---

## Upgrading

### Docker Compose

1. Pull the latest code:
   ```bash
   git pull origin main
   ```

2. **Compare your `.env` against the example.** New releases add variables,
   and a release that adds one you do not have can stop the upgrade before
   anything starts:
   ```bash
   cd deploy/docker/mesh
   diff <(grep -oE '^[A-Z_]+' .env.prod.example | sort -u) \
        <(grep -oE '^[A-Z_]+' .env | sort -u)
   ```
   Lines marked `<` are in the example but not in your `.env`. Most are
   optional and documented in
   [the reference above](#environment-variables-reference) — read what each
   one does and set the ones you want. None of them are required for the
   upgrade to proceed; see the note below.

3. Rebuild and restart:
   ```bash
   docker compose -f docker-compose.prod.yml --env-file .env up -d --build
   ```
   Migrations are applied automatically at API startup.

4. Verify health and version:
   ```bash
   curl http://localhost:8005/health
   curl http://localhost:8005/api/version
   # → {"commit":"abc1234","build_time":"2026-05-20T...","version":"v1.2.3","environment":"prod","service":"evc-mesh-api"}
   ```
   `commit` should be the revision you just pulled. If it still shows the
   old one, the containers were restarted but not rebuilt — check that
   `--build` was passed.

#### If the upgrade fails before anything starts

An error of this shape comes from Docker Compose, not from Mesh:

```
error while interpolating services.api.environment.SOME_VAR:
required variable SOME_VAR is missing a value
```

Compose refuses to parse the file, so nothing is rebuilt and nothing is
stopped — your instance keeps serving on the old containers while you fix
it. Set the named variable in `deploy/docker/mesh/.env` (step 2 above shows
what you are missing) and re-run.

This should not happen on an upgrade. The variables the stack requires
without a default are fixed — `POSTGRES_PASSWORD`, `REDIS_PASSWORD`,
`JWT_SECRET`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `GRAFANA_PASSWORD` —
and all of them have shipped in `.env.prod.example` since the first
installable release, so any working install already has them. Anything
added later gets a default or is generated by the stack instead;
`deploy/docker/mesh/check-required-env.sh` enforces that in CI. If you hit
this error on a released version, it is a bug worth reporting.

> **Upgrading from a release before 2026-07-29?** One version briefly
> required `MESH_METRICS_TOKEN` with no default and broke exactly this way.
> It is no longer required — the stack generates the token into
> `deploy/docker/mesh/volumes/secrets/metrics_token` on first start. If you
> worked around it by adding `MESH_METRICS_TOKEN` to your `.env`, that still
> works and takes precedence; you can also drop the line.

### Building from source

1. Pull, then run `make build-prod` for the API (cross-compilation with all
   ldflags) and `cd web && pnpm install && pnpm build` for the frontend.
2. Restart your service manager's units. Migrations apply at API startup.
3. Verify with the same `/health` and `/api/version` checks as above.
