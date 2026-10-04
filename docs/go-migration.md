# WhatBreaks — Go Platform Architecture & JavaScript Migration Inventory

**Document Status:** Accepted Architecture Plan (Step 5A)  
**Target Platform:** Go 1.26+ (Orchestration, Ingestion, API, Collectors, Workers, DB) ↔ Rust Core Engine (gRPC Boundary)

---

## 1. Executive Summary & Migration Context

WhatBreaks inherits a set of 31 JavaScript source files from the legacy TokenTimer platform. The core analysis engine has been formally extracted and validated in Rust (`core-engine`), communicating with Go over a gRPC boundary (`wb.core.v1.DiscoveryService`).

This document records the exhaustive audit of all 31 JavaScript source files, defines their classification, establishes the target Go package structure, records observable behavioral contracts that must be preserved, explicitly marks TokenTimer legacy baggage for exclusion, and outlines the dependency-derived migration order.

---

## 2. Exhaustive JavaScript Inventory & Classification

Every single JavaScript file in the repository has been inspected, analyzed, and classified into one of the following categories:
- **MUST MIGRATE**: Core operational capability, security constraint, or data infrastructure required by WhatBreaks.
- **SHOULD MIGRATE**: Platform services (API middleware, rate limiting, scheduling) required in WhatBreaks but adapted to Go idioms.
- **REPLACE / REDESIGN**: Architecture components where the JS version is scaffolding or tied to legacy assumptions (e.g., K8s collector stubs, HTTP server framing).
- **DISCARD**: TokenTimer-specific domain baggage, Node.js-only runtime utilities, or redundant duplicate code.
- **DEFER**: Non-essential features deferred to future milestone phases.

| # | Path | Responsibility | Dependencies | Consumers / Callers | Security Sensitivity | Go Destination | Priority | Classification |
|---|------|----------------|--------------|---------------------|----------------------|----------------|----------|----------------|
| 1 | `kubernetes/controller/config.js` | Controller environment validation, forbidden var checks, token file permission check, API URL parsing | `node:fs`, `node:url`, `./ports.js`, `@tokentimer/config` | `kubernetes/controller/index.js` | **HIGH** (Token file security, SSRF check) | `internal/config` | P1 | **MUST MIGRATE** |
| 2 | `kubernetes/controller/health-server.js` | HTTP liveness (`/healthz`) and readiness (`/readyz`) probe server | `node:http` | `kubernetes/controller/index.js`, `lifecycle.js` | **LOW** (Liveness/Readiness probes) | `internal/health` | P1 | **MUST MIGRATE** |
| 3 | `kubernetes/controller/lifecycle.js` | State management (`starting`, `running`, `stopping`, `stopped`), signal handling (SIGTERM, SIGINT), graceful timeout drain | None | `kubernetes/controller/index.js` | **MEDIUM** (Clean teardown, resource leak prevention) | `internal/lifecycle` | P1 | **MUST MIGRATE** |
| 4 | `kubernetes/controller/runtime.js` | In-flight execution tracking (`trackWork`) and active task completion barrier | None | `kubernetes/controller/index.js` | **LOW** (Work state) | `internal/lifecycle` | P1 | **MUST MIGRATE** |
| 5 | `kubernetes/controller/logger.js` | Controller JSON logger wrapping `@tokentimer/log-scrub` | `@tokentimer/log-scrub` | Controller modules | **HIGH** (Secret scrubbing) | `internal/logging` | P1 | **MUST MIGRATE** |
| 6 | `kubernetes/controller/ports.js` | Integer port parsing and range validation `[1, 65535]` | None | `kubernetes/controller/config.js` | **LOW** (Config validation) | `internal/config` | P1 | **MUST MIGRATE** |
| 7 | `kubernetes/controller/index.js` | Controller bootstrapper; contains dummy stub objects for `kubernetesClient` & `reporter` | Controller submodules | Process execution | **MEDIUM** (Process entrypoint) | `cmd/wb` + `internal/platform` | P2 | **REPLACE / REDESIGN** |
| 8 | `packages/log-scrub/index.js` | Field-name redaction rules and deep value sanitization | `./secret-material.js` | Loggers | **CRITICAL** (Zero Secret Custody enforcement) | `internal/logging` | P1 | **MUST MIGRATE** |
| 9 | `packages/log-scrub/secret-material.js` | Content-based cryptographic secret/key detection (PEM, DER, PKCS#1/#8, SEC1, JKS magic, PFX) | `node:crypto`, `node:zlib` | `packages/log-scrub/index.js` | **CRITICAL** (Zero Secret Custody enforcement) | `internal/logging` | P1 | **MUST MIGRATE** |
| 10 | `packages/config/src/database.js` | PostgreSQL connection config parsing, SSL mode flags, and connection pool parameters | None | `packages/config/src/index.js`, `workers/runtime/db.js` | **HIGH** (DB credentials & TLS) | `internal/config`, `internal/database` | P1 | **MUST MIGRATE** |
| 11 | `packages/config/src/network.js` | Network allowlist validation, private IP and loopback blocking against SSRF | None | `kubernetes/controller/config.js` | **HIGH** (SSRF prevention) | `internal/config` | P1 | **MUST MIGRATE** |
| 12 | `packages/config/src/index.js` | Aggregates DB/Network config, but also includes SMTP email and TokenTimer cert expiration alerts | `./database.js`, `./network.js` | Platform consumers | **HIGH** (Credentials) | `internal/config` (core only) | P1 | **REPLACE / REDESIGN** |
| 13 | `workers/runtime/db.js` | PostgreSQL connection pool, query wrapper, and advisory locking (`hash32` + `pg_try_advisory_lock`) | `pg`, `@tokentimer/config` | `workers/runtime/runner.js` | **HIGH** (Advisory locks & DB access) | `internal/database` | P1 | **MUST MIGRATE** |
| 14 | `workers/runtime/is-node-entrypoint.js` | Checks `process.argv[1]` vs `import.meta.url` for CLI execution | `node:process`, `node:url` | `workers/runtime/runner.js` | **NONE** (Runtime glue) | None | N/A | **DISCARD** |
| 15 | `workers/runtime/logger.js` | Worker runtime JSON logging with log scrubbing | `@tokentimer/log-scrub` | Worker modules | **HIGH** (Secret scrubbing) | `internal/logging` | P1 | **MUST MIGRATE** |
| 16 | `workers/runtime/metrics.js` | Prometheus metrics for TokenTimer certificate alert queues and digests | `prom-client` | Worker runtime | **LOW** (Telemetry) | `internal/metrics` (future) | P3 | **REPLACE / REDESIGN** |
| 17 | `workers/runtime/proxy-compat-check.js` | Warns if Node.js runtime does not support `NODE_USE_ENV_PROXY=1` | `@tokentimer/node-compat` | `workers/runtime/runner.js` | **NONE** (Runtime glue) | None (Go stdlib handles proxies) | N/A | **DISCARD** |
| 18 | `workers/runtime/runner.js` | 5-field cron parser, lookahead calendar calculation, interval timer, overlap prevention, `--once` mode | `./db.js`, `./logger.js`, `./is-node-entrypoint.js` | Worker runners | **MEDIUM** (Task concurrency & execution) | `internal/scheduler` | P2 | **SHOULD MIGRATE / REPLACE** |
| 19 | `infrastructure/api/index.js` | Express HTTP server setup (Helmet security headers, CORS, body size limits, error handling) | `express`, `cors`, `helmet`, `./routes/health.js` | API entrypoint | **HIGH** (API boundary security) | `internal/api` | P2 | **REPLACE / REDESIGN** |
| 20 | `infrastructure/api/middleware/csrf.js` | Double-submit cookie CSRF protection | `csrf-csrf`, `session-cookie-options.js` | `infrastructure/api/index.js` | **HIGH** (Web CSRF defense) | `internal/api/middleware` | P2 | **SHOULD MIGRATE / REPLACE** |
| 21 | `infrastructure/api/middleware/rateLimit.js` | Global, slowdown, and authenticated user/IP rate limiters | `express-rate-limit`, `express-slow-down` | `infrastructure/api/index.js` | **HIGH** (Abuse prevention) | `internal/api/middleware` | P2 | **SHOULD MIGRATE / REPLACE** |
| 22 | `infrastructure/api/routes/health.js` | API health routes: `GET /` and `GET /health` (`SELECT 1` ping, uptime) | `express`, `database.js` | `infrastructure/api/index.js` | **LOW** (Service health) | `internal/api` / `internal/health` | P2 | **SHOULD MIGRATE** |
| 23 | `infrastructure/auth/auth-middleware.js` | Request auth: bearer token for worker calls, session auth, email verification | `./internal-worker-auth.js`, `logger.js` | Express routes | **CRITICAL** (API Access control) | `internal/auth` | P2 | **SHOULD MIGRATE / REPLACE** |
| 24 | `infrastructure/auth/auth.js` | 100% duplicate copy of `auth-middleware.js` | `./internal-worker-auth.js` | Legacy imports | **CRITICAL** (Redundant code) | None (Single auth package) | N/A | **DISCARD** |
| 25 | `infrastructure/auth/internal-worker-auth.js` | Internal worker Bearer token authentication with timing-safe comparison | `node:crypto` | `auth-middleware.js` | **CRITICAL** (Timing attack protection) | `internal/auth` | P1 | **MUST MIGRATE** |
| 26 | `infrastructure/auth/session-cookie-options.js` | Cookie security flags (HttpOnly, SameSite, Secure, `__Host-` prefix) & CORS origin generator | None | `infrastructure/api/middleware/csrf.js` | **HIGH** (Session security) | `internal/auth` / `internal/api` | P2 | **SHOULD MIGRATE / REPLACE** |
| 27 | `infrastructure/auth/validation.js` | Express-validator schemas for TokenTimer SSL/TLS certs, licenses, and renewal fields | `express-validator` | Legacy TokenTimer routes | **MEDIUM** (Input validation) | None (Not WhatBreaks domain) | N/A | **DISCARD** |
| 28 | `infrastructure/auth/workspace-access-policy.js` | `hideWorkspaceExistence` policy returning 404 instead of 403 on denied workspaces | None | Route handlers | **MEDIUM** (Workspace enumeration prevention) | `internal/auth` | P2 | **SHOULD MIGRATE / REPLACE** |
| 29 | `infrastructure/config/runtime-labels.js` | Reads `.tokentimer-variant`, `TT_MODE`, `TT_VARIANT` for SaaS/OSS labeling | `node:fs`, `node:path` | `logger.js` | **NONE** (Branding) | None | N/A | **DISCARD** |
| 30 | `infrastructure/database/database.js` | PostgreSQL pool creation, TLS configuration (`TLSv1.3`), `waitForDatabase`, query instrumentation | `pg`, `prom-client`, `logger.js` | Infrastructure repos | **HIGH** (Postgres connectivity & TLS) | `internal/database` | P1 | **MUST MIGRATE** |
| 31 | `infrastructure/utils/logger.js` | Winston logger with sensitive key redaction, value scrubbing, JSON ordering | `winston`, `prom-client`, `log-scrub` | Infrastructure modules | **HIGH** (Zero Secret Custody in logs) | `internal/logging` | P1 | **MUST MIGRATE** |

---

## 3. Observable Semantics of MUST MIGRATE Components

To ensure zero behavioral regression during future translation steps, the following observable contracts are frozen and documented:

### 3.1 Configuration Semantics (`kubernetes/controller/config.js`, `packages/config/src/`)
- **Forbidden Environment Variables:**
  - `WB_API_TOKEN` / `TT_API_TOKEN`: Startup must immediately fail if present. Raw tokens in environment variables are strictly forbidden to prevent accidental disclosure via `/proc` or crash dumps.
  - `KUBECONFIG`: Startup must immediately fail if explicitly set in the controller environment. The controller must use standard in-cluster service account authentication or default config locations.
- **Accepted Token File:**
  - Environment variable: `WB_API_TOKEN_FILE` (legacy fallback: `TT_API_TOKEN_FILE`).
  - Validation: File must exist, be a regular file, non-empty when trimmed.
  - File permissions: File mode (`stat.mode & 0777`) must strictly be `0600` (read/write owner only) or `0400` (read owner only). Any other permission must cause startup abort unless explicitly overridden by `ALLOW_INSECURE_TOKEN_FILE_PERMISSIONS=true`.
- **API URL Validation:**
  - Environment variable: `WB_API_URL` (fallback `API_URL`, default `http://localhost:4000`).
  - Validation: Must parse as valid URL with protocol `http:` or `https:`. Must satisfy network allowlist policy (no loopback/private IP in production unless explicitly permitted).
- **Timeouts & Numeric Defaults:**
  - `HEALTH_PORT`: integer in `[1, 65535]`, default `8081`.
  - `CONTROLLER_INTERVAL_MS`: positive integer, default `60000` (60s).
  - `SHUTDOWN_TIMEOUT_MS`: positive integer, default `30000` (30s).
  - `KUBERNETES_CLIENT_TIMEOUT_MS`: positive integer, default `10000` (10s).
  - `LOG_LEVEL`: case-insensitive `debug`, `info`, `warn`, `error` (default `info`).

### 3.2 Health Server Semantics (`kubernetes/controller/health-server.js`)
- **HTTP Endpoints:**
  - `GET /healthz`:
    - Status `200 OK`, body text: `ok` when healthy.
    - Status `503 Service Unavailable`, body text: `unavailable` when unhealthy.
  - `GET /readyz`:
    - Status `200 OK`, body text: `ready` when ready.
    - Status `503 Service Unavailable`, body text: `not_ready` when not ready.
- **Error Handling:**
  - Any other path: Status `404 Not Found`, body text: `not_found`.
  - Any non-GET method: Status `405 Method Not Allowed`, body text: `method_not_allowed`.
- **State Management:** Thread-safe boolean toggles for `healthy` and `ready`.

### 3.3 Lifecycle Semantics (`kubernetes/controller/lifecycle.js`)
- **Lifecycle Phases:**
  1. `starting`: initialization, health server start, dependency verification.
  2. `running`: active processing, periodic cycles.
  3. `stopping`: graceful shutdown initiated, readiness probe immediately flipped to `false`.
  4. `stopped`: all in-flight work drained, resources released, health server stopped.
  5. `failed`: fatal error encountered during operation.
- **Signals:** Intercepts `SIGTERM` and `SIGINT`.
- **Shutdown Sequence:**
  1. Transition state to `stopping`.
  2. Set readiness probe to `not_ready` so external ingress/kube-proxy stops routing traffic.
  3. Stop accepting new tasks or work loops.
  4. Wait for in-flight tasks to complete up to `shutdownTimeoutMs` (30s).
  5. Close health server and disconnect database/gRPC channels.
  6. Transition state to `stopped` and exit (exit code `0` on clean shutdown, `1` on timeout).

### 3.4 Database & Advisory Lock Semantics (`infrastructure/database/`, `workers/runtime/db.js`)
- **TLS Configuration:**
  - `DB_SSL=verify` or CA certificate provided via `PGSSLROOTCERT`: TLS enabled, CA verified, `minVersion: TLSv1.3`.
  - `DB_SSL=require`: TLS enabled; identity verified in production; `minVersion: TLSv1.3`.
  - `DB_SSL=require-no-verify`: TLS enabled; server certificate identity not verified (explicit opt-in).
  - `DB_SSL=false` or unset: Plaintext connection (dev/local testing).
- **PostgreSQL Connection Pool Defaults:**
  - `max`: 10 connections, `min`: 2 connections.
  - `idleTimeout`: 30,000 ms.
  - `connectionTimeout`: 30,000 ms.
  - `acquireTimeout`: 60,000 ms.
- **Advisory Lock Hashing Algorithm:**
  - Mathematical 32-bit signed integer hash:
    $$h_{i} = ((h_{i-1} \ll 5) - h_{i-1} + \text{char}(s_i)) \pmod{2^{32}}$$
    Signed two's-complement cast ensures exact compatibility with Postgres `pg_try_advisory_lock(bigint/int)`.
  - Verified test vectors:
    - `""` $\to$ `0`
    - `"test-lock"` $\to$ `-1226527354`
    - `"whatbreaks:discovery"` $\to$ `-2033551410`
- **Advisory Lock Operations:**
  - `SELECT pg_try_advisory_lock($1) AS locked`
  - `SELECT pg_advisory_unlock($1) AS unlocked`

### 3.5 Worker Authentication Semantics (`infrastructure/auth/internal-worker-auth.js`)
- **Header:** `Authorization: Bearer <token>`
- **Secret Resolution:** `WORKER_API_KEY` (fallback `SESSION_SECRET`).
- **Timing-Safe Equality:** Must check length equality first, then execute constant-time comparison (`crypto/subtle.ConstantTimeCompare` in Go).
- **Context Identity:** Successful worker authentication injects system worker identity (`role: admin`, `email: worker@internal`).

### 3.6 Log Scrubbing & Zero Secret Custody (`packages/log-scrub/`, `infrastructure/utils/logger.js`)
- **Field Name Redaction:** Any field matching `/password|secret|api[-_]?key|access[-_]?key|authorization|cookie|credential|private[-_]?key|token|role[-_]?id/i` is unconditionally replaced with `"[REDACTED]"`.
- **Content Scrubbing:** Any freeform log message or payload is scrubbed of:
  - PEM private key blocks (`-----BEGIN ... PRIVATE KEY-----` to `-----END ... PRIVATE KEY-----`).
  - Base64-encoded PKCS#1, PKCS#8, and SEC1 keys.
  - Hex-encoded JKS keystores (magic header `0xFEEDFEED`).
  - Bearer tokens and JWT patterns.
- **JSON Field Ordering:** Standard log records emit fields in deterministic order: `["level", "message", "service", "timestamp", ...metadata]`.

---

## 4. TokenTimer Legacy Baggage (Explicitly Excluded)

The following components from TokenTimer are **NOT** part of the WhatBreaks domain and must be completely discarded:

1. **`infrastructure/auth/validation.js`**: TokenTimer token/cert validation (validating `ssl_cert`, `tls_cert`, `certopsApiTokenId`, `renewal_url`, `renewal_date`, `serial_number`, `algorithm`, `license_type`). WhatBreaks models Kubernetes resources and dependency relationships, not SSL certificate renewals.
2. **`infrastructure/auth/auth.js`**: Exact 100% byte-for-byte duplicate of `auth-middleware.js`. Discarded in favor of a single Go `internal/auth` package.
3. **`infrastructure/config/runtime-labels.js`**: TokenTimer variant detection (`.tokentimer-variant`, `TT_MODE=oss|enterprise|cloud`). Discarded.
4. **`workers/runtime/is-node-entrypoint.js`**: Node.js specific module check (`import.meta.url === process.argv[1]`). Go uses standard entrypoint `main.main()`.
5. **`workers/runtime/proxy-compat-check.js`**: Checks Node.js runtime compatibility with `NODE_USE_ENV_PROXY=1`. Go standard library handles HTTP proxies natively.
6. **`packages/config/src/index.js` (Alerts & Email portions)**: `getEmailConfig` (SMTP) and `getAlertConfig` (warning thresholds of 30, 14, 7, 1, 0 days for certificate expiration). Discarded.
7. **Certificate Management / Secret Extraction**: Any capability that extracts TLS private keys, secret values, or certificate payloads. WhatBreaks strictly enforces **Zero Secret Custody**.

---

## 5. Kubernetes Collector Architectural Decision

- **JS Controller Status:** `kubernetes/controller/index.js` lines 21-36 explicitly contain dummy stub objects for `kubernetesClient` and `reporter`. There is **NO** real Kubernetes collector implementation in JavaScript.
- **Decision:** The existing JS collector files serve only as a behavioral/contract reference for lifecycle, configuration, health probes, and graceful shutdown.
- **Implementation Plan:** The real Kubernetes collector will be newly implemented in Go using `client-go` in a subsequent migration phase. No stub or fake collector is created in Step 5A.

---

## 6. Target Go Architecture & Dependency Direction

The Go platform is organized to ensure a strict unidirectional dependency graph. Lower-level packages know nothing about callers; circular dependencies are forbidden.

```
                  cmd/wb
                    │
                    ▼
            internal/platform (Orchestration)
                    │
   ┌────────────────┼────────────────┬────────────────┐
   ▼                ▼                ▼                ▼
internal/config  internal/health  internal/lifecycle internal/logging
   │                │                │                │
   └────────────────┴────────┬───────┴────────────────┘
                             ▼
         internal/database      internal/auth
                    │                │
                    └────────┬───────┘
                             ▼
                    internal/scheduler
                             │
                             ▼
                    internal/coreclient (gRPC Client)
                             │
                             ▼ (Protobuf / gRPC)
                    Rust Core Engine
```

### Dependency Rules:
1. `cmd/wb`: Sole binary entrypoint; parses CLI arguments and invokes `internal/platform`.
2. `internal/config`: Self-contained; does not import any other `internal/` packages.
3. `internal/logging`: Self-contained; provides structured logging and Zero Secret Custody scrubbing.
4. `internal/health`: Implements HTTP probes; depends only on standard library.
5. `internal/lifecycle`: Manages process state transitions and shutdown signaling; decoupled from specific worker types.
6. `internal/database`: Provides Postgres connection pooling and advisory lock primitives; depends only on `config`.
7. `internal/auth`: Implements timing-safe token authentication; depends only on standard library `crypto/subtle`.
8. `internal/coreclient`: Transport adapter to Rust Core Engine; depends only on generated gRPC/protobuf code (`gen/go`).
9. `internal/platform`: Root composition root; wires config, logging, health, lifecycle, database, and coreclient.

---

## 7. Recommended Safe Migration Order

Based on the dependency analysis, the safest implementation sequence for future migration steps is:

1. **Step 5B — Logging & Secret Scrubbing (`internal/logging`)**: Port `secret-material.js` and `log-scrub` into Go. All other packages require secure logging.
2. **Step 5C — Configuration & Validation (`internal/config`)**: Port environment parsing, token file permissions enforcement, and network allowlist validation.
3. **Step 5D — Health Server & Probes (`internal/health`)**: Port `/healthz` and `/readyz` HTTP server.
4. **Step 5E — Lifecycle & Graceful Drain (`internal/lifecycle`)**: Port state machine, signal listeners, and in-flight work tracker.
5. **Step 5F — Database & Advisory Locks (`internal/database`)**: Implement PostgreSQL pool and `hash32` advisory locking with `pgx`.
6. **Step 5G — Authentication & Worker Auth (`internal/auth`)**: Port timing-safe worker token verification and session security helpers.
7. **Step 5H — Scheduler & Task Runner (`internal/scheduler`)**: Implement cron/interval job scheduler and overlap prevention.
8. **Step 5I — API Layer & Middleware (`internal/api`)**: Implement HTTP API, security headers, rate limiting, and route handlers.
9. **Step 5J — Kubernetes Collector (`internal/collector/k8s`)**: Build new Go-native Kubernetes collector using `client-go` and feed evidence into `internal/coreclient`.
