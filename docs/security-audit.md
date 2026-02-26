# Security Audit Report

**Date:** 2026-02-24
**Scope:** evc-mesh core backend (Sprint 10.8, before open-source release)
**Auditor:** Automated code review + manual analysis
**Codebase version:** Current HEAD on main

---

## Executive Summary

The evc-mesh codebase demonstrates generally sound security practices: parameterized SQL queries, bcrypt password hashing, JWT with explicit algorithm validation, and refresh token rotation with theft detection. However, several issues were identified that should be addressed before open-source release.

| Severity | Count | Status |
|----------|-------|--------|
| CRITICAL | 1     | Requires immediate fix |
| HIGH     | 2     | Should fix before release |
| MEDIUM   | 3     | Should fix or document |
| LOW      | 3     | Improvement recommended |

---

## Findings

### CRITICAL-01: SQL Injection in Custom Field JSONB Filtering

**File:** `internal/repository/postgres/task_repo.go` lines 235-263
**Vector:** The custom field slug (map key from `filter.CustomFields`) is interpolated directly into the SQL query using `fmt.Sprintf` without sanitization.

```go
// VULNERABLE: slug is user-controlled (from query params like "custom.{slug}=value")
conditions = append(conditions, fmt.Sprintf("custom_fields->>'%s' = $%d", slug, argIdx))
// ...
conditions = append(conditions, fmt.Sprintf("custom_fields ? '%s'", slug))
```

The slug originates from HTTP query parameter keys parsed in `handler/task_handler.go:parseCustomFieldFilters()`:
```go
fieldKey := strings.TrimPrefix(key, "custom.")
```

An attacker can craft a query parameter like:
```
GET /projects/{id}/tasks?custom.'; DROP TABLE tasks; --=value
```

The `'` in the slug breaks out of the JSON key string in the SQL query, enabling arbitrary SQL injection.

**Recommendation:**
1. Validate that slug matches `^[a-z][a-z0-9_]*$` (alphanumeric + underscore only).
2. Alternatively, use parameterized JSONB operators: `custom_fields->>$N = $M` where the key is passed as a parameter.
3. Cross-reference slugs against actual `custom_field_definitions` for the project before building the query.

**Fix priority:** IMMEDIATE -- this is exploitable via unauthenticated-level effort (any authenticated user can trigger it).

---

### CRITICAL-01b: SQL Injection in WorkspaceRLS Middleware

**File:** `internal/middleware/workspace.go` line 71
**Vector:** The UUID string is interpolated directly into a SET command via `fmt.Sprintf`.

```go
q := fmt.Sprintf("SET app.current_workspace_id = '%s'", wsID.String())
```

While `wsID` is a `uuid.UUID` (parsed by `uuid.Parse()`), meaning this is currently safe because UUID parsing rejects non-UUID strings, this pattern is fragile. If the code is ever refactored to accept the workspace ID from a different source, or if the UUID library behavior changes, this becomes injectable.

**Recommendation:** Use a parameterized query instead:
```go
_, err := db.ExecContext(ctx, "SELECT set_config('app.current_workspace_id', $1, false)", wsID.String())
```

**Fix priority:** HIGH -- not currently exploitable due to UUID parsing, but the pattern is dangerous.

---

### HIGH-01: CORS Allows All Origins

**File:** `cmd/api/main.go` line 161
**Current config:**
```go
AllowOrigins: []string{"*"},
```

This permits any website to make authenticated requests to the API using the user's browser session. While the API primarily uses `Authorization` headers (not cookies), the wildcard CORS still:
- Enables CSRF-like attacks if cookies are ever added.
- Allows data exfiltration from any malicious website.
- Is flagged by security scanners.

**Recommendation:**
1. Make `AllowOrigins` configurable via environment variable (`CORS_ORIGINS`).
2. Default to restrictive origins in production (e.g., `https://mesh.example.com`).
3. Allow `*` only when `NODE_ENV=development` or explicit opt-in.

```go
origins := strings.Split(getEnv("CORS_ORIGINS", "http://localhost:5173"), ",")
e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
    AllowOrigins: origins,
    // ...
}))
```

---

### HIGH-02: No Rate Limiting on Auth Endpoints

**Files:** `cmd/api/main.go` lines 192-195
**Affected endpoints:**
- `POST /api/v1/auth/register` -- account enumeration, mass registration
- `POST /api/v1/auth/login` -- brute-force password attacks
- `POST /api/v1/auth/refresh` -- token brute-force

There is no rate limiting applied to any endpoint, and auth endpoints are public (no JWT required).

**Recommendation:**
1. Add IP-based rate limiting via Echo's `middleware.RateLimiter` or a Redis-backed limiter.
2. Suggested limits:
   - `/auth/login`: 10 attempts per minute per IP.
   - `/auth/register`: 5 registrations per hour per IP.
   - `/auth/refresh`: 30 per minute per IP.
3. Return `429 Too Many Requests` with a `Retry-After` header.
4. Consider account lockout after N consecutive failed login attempts.

---

### MEDIUM-01: Default JWT Secret is Weak

**File:** `internal/config/config.go` line 117
```go
JWTSecret: getEnv("JWT_SECRET", "change-me-in-production"),
```

If `JWT_SECRET` is not set, the application starts with a well-known secret, enabling any attacker to forge valid JWT tokens.

**Recommendation:**
1. Refuse to start if `JWT_SECRET` is not set or equals the default value.
2. Log a FATAL error and exit:
```go
if cfg.Auth.JWTSecret == "change-me-in-production" || len(cfg.Auth.JWTSecret) < 32 {
    log.Fatal("JWT_SECRET must be set to a secure value (minimum 32 characters)")
}
```
3. Document the requirement in deployment docs and `.env.example`.

---

### MEDIUM-02: Comments Use Physical DELETE

**File:** `internal/repository/postgres/comment_repo.go` line 82
```go
const q = `DELETE FROM comments WHERE id = $1`
```

While tasks, agents, and workspaces use soft delete (`deleted_at` column), comments and custom field definitions use physical DELETE. This is inconsistent and means:
- Deleted comments cannot be recovered.
- Audit trail is incomplete for deleted comments.

**Recommendation:** Either:
1. Add `deleted_at` column to `comments` and `custom_field_definitions` tables and use soft delete consistently, OR
2. Document this as intentional behavior (GDPR: comments may need hard delete for data erasure requests).

---

### MEDIUM-03: No Input Length Limits on Text Fields

**Files:** `internal/handler/task_handler.go`, `internal/handler/comment_handler.go`

There are no server-side length limits on:
- Task title and description
- Comment body
- Project name and description
- Agent name
- Custom field definition name

An attacker could submit multi-megabyte strings, causing:
- Database bloat
- Memory exhaustion during JSON serialization
- Slow queries

**Recommendation:**
1. Add validation for maximum lengths:
   - `title`: max 500 characters
   - `description`: max 50,000 characters
   - `comment.body`: max 50,000 characters
   - `name` fields: max 255 characters
2. Enforce limits in handler validation (before passing to services).
3. Consider Echo's `BodyLimit` middleware for an overall request size cap.

---

### LOW-01: Password Hash Correctly Hidden in JSON

**File:** `internal/domain/user.go` line 13
```go
PasswordHash string `json:"-" db:"password_hash"`
```

**Status:** PASS. The `json:"-"` tag correctly prevents password hash from appearing in API responses. Verified in the Register and Me handler responses.

---

### LOW-02: Agent API Key Hash Correctly Hidden (PASS)

**File:** `internal/domain/agent.go` line 39
```go
APIKeyHash string `json:"-" db:"api_key_hash"`
```

**Status:** PASS. The `json:"-"` tag is correctly applied. Existing tests in `domain_test.go` verify this behavior (TestAgent_APIKeyHash_ExcludedFromJSON, TestAgent_APIKeyHash_NotRestoredFromJSON).

---

### LOW-03: Missing Request Timeout Configuration

**File:** `cmd/api/main.go`

While `ServerConfig` includes `ReadTimeout` and `WriteTimeout`, these are not applied to the Echo server:
```go
go func() {
    if err := e.Start(addr); err != nil { ... }
}()
```

**Recommendation:**
```go
server := &http.Server{
    Addr:         addr,
    ReadTimeout:  cfg.Server.ReadTimeout,
    WriteTimeout: cfg.Server.WriteTimeout,
}
go func() {
    if err := e.StartServer(server); err != nil { ... }
}()
```

---

## SQL Injection Analysis (Comprehensive)

### Parameterized Queries (PASS)

All repositories consistently use `$1, $2, ...` parameterized placeholders for user-supplied data:

| File | Method | Status |
|------|--------|--------|
| `task_repo.go` | Create, GetByID, Update, Delete | PASS |
| `task_repo.go` | List (standard filters) | PASS |
| `task_repo.go` | List (custom field filters) | **FAIL** -- slug interpolation |
| `project_repo.go` | All methods | PASS |
| `workspace_repo.go` | All methods | PASS |
| `agent_repo.go` | All methods | PASS |
| `comment_repo.go` | All methods | PASS |
| `custom_field_repo.go` | All methods | PASS |
| `event_bus_repo.go` | All methods | PASS |
| `user_repo.go` | All methods | PASS |
| `task_status_repo.go` | All methods | PASS |
| `task_dependency_repo.go` | All methods | PASS |

### Sort Column Injection Prevention (PASS)

**File:** `internal/repository/postgres/helpers.go`

The `orderClause` function uses an allowlist (`allowedSortColumns`) to prevent SQL injection via sort parameters. Only whitelisted column names are used in ORDER BY.

### Search/ILIKE (PASS)

Search filters use parameterized ILIKE (`$N`) with `%` patterns concatenated in Go code (not SQL), which is safe:
```go
pattern := "%" + filter.Search + "%"
conditions = append(conditions, fmt.Sprintf("(title ILIKE $%d)", argIdx))
args = append(args, pattern)
```

---

## Authentication & Authorization Analysis

### JWT Implementation (PASS with notes)

| Check | Status | Notes |
|-------|--------|-------|
| Signing algorithm | PASS | HS256 only, explicit validation with `WithValidMethods` |
| Algorithm confusion | PASS | Rejects non-HMAC methods via type assertion |
| Issuer validation | PASS | `jwt.WithIssuer("evc-mesh")` |
| Expiration | PASS | 15-minute TTL, standard `exp` claim |
| Token ID (jti) | PASS | UUID-based, enables future blacklisting |
| Secret strength | WARN | Default is weak (see MEDIUM-01) |

### Refresh Token Implementation (PASS)

| Check | Status | Notes |
|-------|--------|-------|
| Token format | PASS | `rt_{64_hex_chars}` (256 bits of entropy) |
| Storage | PASS | SHA-256 hash stored, not plaintext |
| Rotation | PASS | Old token revoked when new one issued |
| Theft detection | PASS | Reuse of revoked token revokes all user sessions |
| Expiration | PASS | 7-day TTL checked server-side |
| CSPRNG | PASS | Uses `crypto/rand` |

### Password Security (PASS)

| Check | Status | Notes |
|-------|--------|-------|
| Hashing | PASS | bcrypt with cost 10 |
| Min length | PASS | 8 characters minimum |
| Max length | PASS | 128 characters maximum (prevents bcrypt DoS) |
| Complexity | PASS | Requires upper + lower + digit |
| Timing attacks | PASS | `bcrypt.CompareHashAndPassword` is constant-time |

### Agent API Key Security (PASS)

| Check | Status | Notes |
|-------|--------|-------|
| Key format | PASS | `agk_{workspace_slug}_{random}` |
| Storage | PASS | bcrypt-hashed in DB |
| Key rotation | PASS | `regenerate-key` endpoint implemented |
| Prefix storage | PASS | Only prefix stored for lookup optimization |

---

## Secret Exposure Analysis

### Hardcoded Secrets

| Location | Finding | Status |
|----------|---------|--------|
| `config.go` | `JWT_SECRET` default `"change-me-in-production"` | WARN |
| `config.go` | MinIO default creds `minioadmin/minioadmin` | OK -- dev only |
| `config.go` | DB default creds `mesh/mesh` | OK -- dev only |
| `.env.example` | Not found in repo root | N/A |
| `docker-compose.yml` | Contains dev credentials | OK -- dev only |

### Git History

No API keys, JWT secrets, or production credentials were found in the reviewed source files. The `.gitignore` should include `.env`, `.env.local`, `.env.production`.

---

## Dependency Analysis

### Direct Dependencies

| Package | Version | Known Issues |
|---------|---------|-------------|
| `golang-jwt/jwt/v5` | v5.3.1 | None known |
| `google/uuid` | v1.6.0 | None known |
| `jmoiron/sqlx` | v1.4.0 | None known |
| `labstack/echo/v4` | v4.15.1 | None known |
| `lib/pq` | v1.11.2 | None known |
| `minio/minio-go/v7` | v7.0.98 | None known |
| `nats-io/nats.go` | v1.49.0 | None known |
| `redis/go-redis/v9` | v9.18.0 | None known |
| `golang.org/x/crypto` | v0.48.0 | None known |

**Recommendation:** Run `govulncheck` regularly in CI:
```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

---

## Recommendations Summary (Priority Order)

### Must Fix Before Release

1. **CRITICAL-01:** Sanitize custom field slugs in JSONB queries or use parameterized key extraction. This is a real SQL injection vulnerability.
2. **HIGH-01:** Configure CORS per environment. Do not ship with `AllowOrigins: ["*"]`.
3. **HIGH-02:** Add rate limiting to auth endpoints.

### Should Fix

4. **MEDIUM-01:** Refuse to start with default JWT secret.
5. **MEDIUM-03:** Add input length validation to all text fields.
6. **LOW-03:** Apply server read/write timeout to Echo.

### Nice to Have

7. **CRITICAL-01b:** Refactor RLS middleware to use `set_config()` instead of `fmt.Sprintf`.
8. **MEDIUM-02:** Decide on consistent soft/hard delete policy.
9. **LOW-02:** Agent `api_key_hash` has `json:"-"` tag -- PASS, no action needed.
10. Add `govulncheck` to CI pipeline.
11. Add security headers middleware (X-Content-Type-Options, X-Frame-Options, Strict-Transport-Security).
12. Add request body size limit via Echo's `BodyLimit` middleware.
