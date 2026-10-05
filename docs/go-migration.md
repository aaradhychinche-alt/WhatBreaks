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
| 1 | `kubernetes/controller/config.js` | Controller environment validation, forbidden var checks, token file permission check, API URL parsing | `node:fs`, `node:url`, `./ports.js`, `@tokentimer/config` | `kubernetes/controller/index.js` | **HIGH** (Token file security, SSRF check) | `internal/config` | P1 | **MIGRATED (Step 5B.1)** |
| 2 | `kubernetes/controller/health-server.js` | HTTP liveness (`/healthz`) and readiness (`/readyz`) probe server | `node:http` | `kubernetes/controller/index.js`, `lifecycle.js` | **LOW** (Liveness/Readiness probes) | `internal/health` | P1 | **MIGRATED (Step 5B.3)** |
| 3 | `kubernetes/controller/lifecycle.js` | State management (`starting`, `running`, `stopping`, `stopped`), signal handling (SIGTERM, SIGINT), graceful timeout drain | None | `kubernetes/controller/index.js` | **MEDIUM** (Clean teardown, resource leak prevention) | `internal/lifecycle` | P1 | **MIGRATED (Step 5B.4)** |
| 4 | `kubernetes/controller/runtime.js` | In-flight execution tracking (`trackWork`) and active task completion barrier | None | `kubernetes/controller/index.js` | **LOW** (Work state) | `internal/lifecycle` | P1 | **MIGRATED (Step 5B.4)** |
| 5 | `kubernetes/controller/logger.js` | Controller JSON logger wrapping `@tokentimer/log-scrub` | `@tokentimer/log-scrub` | Controller modules | **HIGH** (Secret scrubbing) | `internal/logging` | P1 | **MIGRATED (Step 5B.2)** |
| 6 | `kubernetes/controller/ports.js` | Integer port parsing and range validation `[1, 65535]` | None | `kubernetes/controller/config.js` | **LOW** (Config validation) | `internal/config` | P1 | **MIGRATED (Step 5B.1)** |
| 7 | `kubernetes/controller/index.js` | Controller bootstrapper; contains dummy stub objects for `kubernetesClient` & `reporter` | Controller submodules | Process execution | **MEDIUM** (Process entrypoint) | `cmd/wb` + `internal/platform` | P2 | **REPLACE / REDESIGN** |
| 8 | `packages/log-scrub/index.js` | Field-name redaction rules and deep value sanitization | `./secret-material.js` | Loggers | **CRITICAL** (Zero Secret Custody enforcement) | `internal/logging` | P1 | **MIGRATED (Step 5B.2)** |
| 9 | `packages/log-scrub/secret-material.js` | Content-based cryptographic secret/key detection (PEM, DER, PKCS#1/#8, SEC1, JKS magic, PFX) | `node:crypto`, `node:zlib` | `packages/log-scrub/index.js` | **CRITICAL** (Zero Secret Custody enforcement) | `internal/logging` | P1 | **MIGRATED (Step 5B.2)** |
| 10 | `packages/config/src/database.js` | PostgreSQL connection config parsing, SSL mode flags, and connection pool parameters | None | `packages/config/src/index.js`, `workers/runtime/db.js` | **HIGH** (DB credentials & TLS) | `internal/config` | P1 | **MIGRATED (Step 5B.1)** |
| 11 | `packages/config/src/network.js` | Network allowlist validation, private IP and loopback blocking against SSRF | None | `kubernetes/controller/config.js` | **HIGH** (SSRF prevention) | `internal/config` | P1 | **MIGRATED (Step 5B.1)** |
| 12 | `packages/config/src/index.js` | Aggregates DB/Network config, but also includes SMTP email and TokenTimer cert expiration alerts | `./database.js`, `./network.js` | Platform consumers | **HIGH** (Credentials) | `internal/config` (core only) | P1 | **MIGRATED (Step 5B.1)** |
| 13 | `workers/runtime/db.js` | PostgreSQL connection pool, query wrapper, and advisory locking (`hash32` + `pg_try_advisory_lock`) | `pg`, `@tokentimer/config` | `workers/runtime/runner.js` | **HIGH** (Advisory locks & DB access) | `internal/database` | P1 | **MIGRATED (Step 5B.5)** |
| 14 | `workers/runtime/is-node-entrypoint.js` | Checks `process.argv[1]` vs `import.meta.url` for CLI execution | `node:process`, `node:url` | `workers/runtime/runner.js` | **NONE** (Runtime glue) | None | N/A | **DISCARD** |
| 15 | `workers/runtime/logger.js` | Worker runtime JSON logging with log scrubbing | `@tokentimer/log-scrub` | Worker modules | **HIGH** (Secret scrubbing) | `internal/logging` | P1 | **MIGRATED (Step 5B.2)** |
| 16 | `workers/runtime/metrics.js` | Prometheus metrics for TokenTimer certificate alert queues and digests | `prom-client` | Worker runtime | **LOW** (Telemetry) | `internal/metrics` (future) | P3 | **REPLACE / REDESIGN** |
| 17 | `workers/runtime/proxy-compat-check.js` | Warns if Node.js runtime does not support `NODE_USE_ENV_PROXY=1` | `@tokentimer/node-compat` | `workers/runtime/runner.js` | **NONE** (Runtime glue) | None (Go stdlib handles proxies) | N/A | **DISCARD** |
| 18 | `workers/runtime/runner.js` | 5-field cron parser, lookahead calendar calculation, interval timer, overlap prevention, `--once` mode | `./db.js`, `./logger.js`, `./is-node-entrypoint.js` | Worker runners | **MEDIUM** (Task concurrency & execution) | `internal/scheduler` | P2 | **SHOULD MIGRATE / REPLACE** |
| 19 | `infrastructure/api/index.js` | Express HTTP server setup (Helmet security headers, CORS, body size limits, error handling) | `express`, `cors`, `helmet`, `./routes/health.js` | API entrypoint | **HIGH** (API boundary security) | `internal/api` | P2 | **REPLACE / REDESIGN** |
| 20 | `infrastructure/api/middleware/csrf.js` | Double-submit cookie CSRF protection | `csrf-csrf`, `session-cookie-options.js` | `infrastructure/api/index.js` | **HIGH** (Web CSRF defense) | `internal/api/middleware` | P2 | **SHOULD MIGRATE / REPLACE** |
| 21 | `infrastructure/api/middleware/rateLimit.js` | Global, slowdown, and authenticated user/IP rate limiters | `express-rate-limit`, `express-slow-down` | `infrastructure/api/index.js` | **HIGH** (Abuse prevention) | `internal/api/middleware` | P2 | **SHOULD MIGRATE / REPLACE** |
| 22 | `infrastructure/api/routes/health.js` | API health routes: `GET /` and `GET /health` (`SELECT 1` ping, uptime) | `express`, `database.js` | `infrastructure/api/index.js` | **LOW** (Service health) | `internal/health` | P2 | **MIGRATED (Step 5B.3)** |
| 23 | `infrastructure/auth/auth-middleware.js` | Request auth: bearer token for worker calls, session auth, email verification | `./internal-worker-auth.js`, `logger.js` | Express routes | **CRITICAL** (API Access control) | `internal/auth` | P2 | **SHOULD MIGRATE / REPLACE** |
| 24 | `infrastructure/auth/auth.js` | 100% duplicate copy of `auth-middleware.js` | `./internal-worker-auth.js` | Legacy imports | **CRITICAL** (Redundant code) | None (Single auth package) | N/A | **DISCARD** |
| 25 | `infrastructure/auth/internal-worker-auth.js` | Internal worker Bearer token authentication with timing-safe comparison | `node:crypto` | `auth-middleware.js` | **CRITICAL** (Timing attack protection) | `internal/auth` | P1 | **MUST MIGRATE** |
| 26 | `infrastructure/auth/session-cookie-options.js` | Cookie security flags (HttpOnly, SameSite, Secure, `__Host-` prefix) & CORS origin generator | None | `infrastructure/api/middleware/csrf.js` | **HIGH** (Session security) | `internal/auth` / `internal/api` | P2 | **SHOULD MIGRATE / REPLACE** |
| 27 | `infrastructure/auth/validation.js` | Express-validator schemas for TokenTimer SSL/TLS certs, licenses, and renewal fields | `express-validator` | Legacy TokenTimer routes | **MEDIUM** (Input validation) | None (Not WhatBreaks domain) | N/A | **DISCARD** |
| 28 | `infrastructure/auth/workspace-access-policy.js` | `hideWorkspaceExistence` policy returning 404 instead of 403 on denied workspaces | None | Route handlers | **MEDIUM** (Workspace enumeration prevention) | `internal/auth` | P2 | **SHOULD MIGRATE / REPLACE** |
| 29 | `infrastructure/config/runtime-labels.js` | Reads `.tokentimer-variant`, `TT_MODE`, `TT_VARIANT` for SaaS/OSS labeling | `node:fs`, `node:path` | `logger.js` | **NONE** (Branding) | None | N/A | **DISCARD** |
| 30 | `infrastructure/database/database.js` | PostgreSQL pool creation, TLS configuration (`TLSv1.3`), `waitForDatabase`, query instrumentation | `pg`, `prom-client`, `logger.js` | Infrastructure repos | **HIGH** (Postgres connectivity & TLS) | `internal/database` | P1 | **MIGRATED (Step 5B.5)** |
| 31 | `infrastructure/utils/logger.js` | Winston logger with sensitive key redaction, value scrubbing, JSON ordering | `winston`, `prom-client`, `log-scrub` | Infrastructure modules | **HIGH** (Zero Secret Custody in logs) | `internal/logging` | P1 | **MIGRATED (Step 5B.2)** |

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

1. **Step 5B.1 — Configuration Subsystem (`internal/config`)**: **COMPLETED ✅**
2. **Step 5B.2 — Logging & Secret Scrubbing (`internal/logging`)**: **COMPLETED ✅**
3. **Step 5B.3 — Health Server & Probes (`internal/health`)**: **COMPLETED ✅**
4. **Step 5B.4 — Lifecycle & Graceful Drain (`internal/lifecycle`)**: **COMPLETED ✅**
5. **Step 5B.5 — Database & Advisory Locks (`internal/database`)**: **COMPLETED ✅**
6. **Step 5B.6 — Authentication & Worker Auth (`internal/auth`)**: Port timing-safe worker token verification and session security helpers.
7. **Step 5B.7 — Scheduler & Task Runner (`internal/scheduler`)**: Implement cron/interval job scheduler and overlap prevention.
8. **Step 5B.8 — API Layer & Middleware (`internal/api`)**: Implement HTTP API, security headers, rate limiting, and route handlers.
9. **Step 5B.9 — Kubernetes Collector (`internal/collector/k8s`)**: Build new Go-native Kubernetes collector using `client-go` and feed evidence into `internal/coreclient`.

---

## 8. Subsystem Migration Status: Step 5B.1 Configuration Subsystem

- **Status:** **MIGRATED & VERIFIED**
- **Go Destination:** `internal/config/`
  - `internal/config/controller.go`: Migrates `kubernetes/controller/config.js` (controller environment variables, forbidden variable checks, token file security, cluster ID, workspace UUID, watch namespaces, intervals, mode).
  - `internal/config/ports.go`: Migrates port range validation `[1, 65535]` from `kubernetes/controller/ports.js` / `config.js`.
  - `internal/config/database.go`: Migrates `packages/config/src/database.js` (Postgres connection parameters, connection pooling configuration, TLS/SSL modes, safe connection string generation).
  - `internal/config/network.go`: Migrates `packages/config/src/network.js` (offline mode, allowlists with exact/wildcard/CIDR matching, webhook provider host allowlists, loopback/private IP detection).
  - `internal/config/config.go`: Top-level composition migrating `packages/config/src/index.js` (general platform config, application security parameters, production session secret validation).
  - `internal/config/env.go`: Zero-side-effect environment abstraction (`EnvLookup`, `MapEnv`, `OsEnv`) enabling deterministic unit testing.
- **Tests Added:**
  - `internal/config/controller_test.go`: Tests forbidden env vars, token file validation (permissions, format, null bytes, private keys), cluster ID RFC1123, workspace UUID, watch namespace policies, interval parsing, mode parsing, API URL validation.
  - `internal/config/ports_test.go`: Tests boundary values 1, 65535, 0, 65536, negative, malformed string, and default fallback.
  - `internal/config/database_test.go`: Tests defaults, custom overrides, port errors, SSL modes (verify, require, require-no-verify), and secret safety (password redacted in `GetSafeConnectionString`).
  - `internal/config/network_test.go`: Tests offline mode, empty allowlist blocking, wildcard matching, CIDR 32-bit bitmask logic, webhook allowlists, and loopback/private IP checks.
  - `internal/config/config_test.go`: Tests top-level `Load`, production requirements, and error safety.
- **Intentionally Removed TokenTimer Functionality:**
  - Discarded SMTP email configuration (`getEmailConfig`).
  - Discarded certificate expiration warning alert thresholds (30, 14, 7, 1, 0 days) (`getAlertConfig`).
  - Discarded TokenTimer variant branding (`brandName: TokenTimer`, `.tokentimer-variant`).
- **Semantic Differences:** None. All observable behaviors, error codes, and security invariants from the JavaScript source have been preserved. Forward-compatible WhatBreaks environment variables (`WB_*`) are supported with full fallback to legacy `TOKENTIMER_*` / `CERTOPS_*` names.

---

## 9. Subsystem Migration Status: Step 5B.2 Logging & Secret Scrubbing Subsystem

- **Status:** **MIGRATED & VERIFIED**
- **Files Inspected:**
  1. `kubernetes/controller/logger.js`: Controller JSON logger wrapping `@tokentimer/log-scrub` with service identity `tokentimer-k8s-controller`.
  2. `workers/runtime/logger.js`: Worker runtime logging with Winston format pipeline, colorized dev output, timestamped staging output, error counter metrics, and `@tokentimer/log-scrub` redaction.
  3. `infrastructure/utils/logger.js`: Winston infrastructure logger with field ordering `LOG_FIELD_ORDER = ["level", "message", "service", "timestamp"]`, `REDACT_FIELDS` list, `isSensitiveKey` pattern matching, client IP normalization `resolveClientIp`, and `safeErrorName`.
  4. `packages/log-scrub/index.js`: Main scrubbing facade providing `isSensitiveKey`, `scrubLogString`, `scrubBuffer`, `redactSensitiveFields` (depth limit 8, circular pointer tracking `[REDACTED:circular]`), and `sanitizeLogRecord`.
  5. `packages/log-scrub/secret-material.js`: Core cryptographic detector and redactor covering PEM private keys, PKCS#12 bundles, DER private keys, JKS keystore magic `0xfeedfeed`, base64/base64url/hex unwrapping, fail-closed gzip and zlib decompression (1 MiB input cap, 4 MiB output cap, ratio 4, preset dictionary rejection, nested compression fail-closed rejection), Authorization headers, Cookies, and generic secret assignments.
- **Go Destination:** `internal/logging/`
  - `internal/logging/secret_material.go`: Complete cryptographic secret detection and redaction engine. Faithfully implements all PEM/DER/JKS/PKCS#12 patterns, base64/base64url/hex unwrappers, and bounded RFC 1950 zlib / gzip inspection with fail-closed rejection.
  - `internal/logging/scrub.go`: Deep structure scrubbing, field-name sensitivity checks, circular reference protection, error sanitization, client IP resolution, and root log record sanitization.
  - `internal/logging/logger.go`: Thread-safe `JSONLogger` implementing deterministic field serialization (`level`, `message`, `service`, `timestamp`, then sorted extra fields) with automatic secret scrubbing interceptor before emission. Exposes `With`, `Named`, `Level`, `WithContext`, and `FromContext`.
- **Tests Added:**
  - `internal/logging/secret_material_test.go`:
    - PEM private key variants (RSA, EC, PKCS#8) detected and redacted.
    - Public certificates and public keys remain intact without modification.
    - Base64, base64url, and hex-encoded private keys detected and redacted.
    - DER structures: PKCS#12 bundle, DER private key, and JKS keystores (`0xfeedfeed`).
    - Compressed buffers: gzip, zlib, and nested compression fail-closed rejection.
    - Generic secrets: Authorization (Bearer/Basic), Cookie, Set-Cookie, X-API-Key, password quotes/unquoted, client secrets, and AWS secret access keys.
    - Sensitive vs non-sensitive field names.
  - `internal/logging/scrub_test.go`:
    - `IsSensitiveKey` pattern and fragment matching.
    - `ScrubLogString` free-form text redaction.
    - `ScrubBuffer` fail-closed binary handling.
    - `ResolveClientIP` parsing and extraction.
    - `RedactSensitiveFields` map, slice, struct, and primitive traversal.
    - `ErrorSerialization` error message scrubbing and `SafeErrorName`.
    - `CircularReferences` cycle protection returning `[REDACTED:circular]`.
    - `MaxDepth` nesting limit at depth 8.
    - `SanitizeLogRecord` message preserving with content scrubbing vs sensitive metadata keys redacted outright.
  - `internal/logging/logger_test.go`:
    - Log levels and level filtering (`debug`, `info`, `warn`, `error`).
    - Deterministic field ordering in JSON output.
    - Service and component tagging via `Named` and `With`.
    - Security Invariant: raw secrets in message, metadata, or error structs NEVER appear in output.
    - Public certificates preserved in log output.
    - High-concurrency logging across 50 goroutines.
    - Context propagation (`WithContext`, `FromContext`).
- **Security Invariants Preserved:**
  - **Zero Secret Custody in Logs:** The logger intercepts all records through `SanitizeLogRecord`. Raw passwords, API tokens, bearer authorization credentials, session cookies, and private key material never reach log output.
  - **Conservative Fail-Closed Handling:** Binary buffers, nested compressed payloads, malformed DER blobs, and oversized compressed payloads fail closed and are redacted/rejected.
  - **Deterministic JSON Line Output:** Log records are consistently ordered with keys `level`, `message`, `service`, `timestamp` followed by alphabetically sorted extra metadata keys.
- **Intentionally Removed TokenTimer Functionality:**
  - Discarded TokenTimer-specific Prometheus counter metrics `cLogError` on `tokentimer-worker` queues.
  - Discarded `.tokentimer-variant` file reading and SaaS mode branching (`runtime-labels.js`).
  - Discarded Winston console colorizers and simple text formatters in favor of structured JSON lines.
- **Semantic Differences:** None in security or log record semantics. All redaction rules, regex patterns, delimiters, and placeholders (`[REDACTED]`, `[PRIVATE_KEY_REDACTED]`, `[REDACTED:circular]`) exactly mirror the JavaScript source.
- **Untouched Source Files:**
  - `kubernetes/controller/logger.js` (UNTOUCHED)
  - `workers/runtime/logger.js` (UNTOUCHED)
  - `infrastructure/utils/logger.js` (UNTOUCHED)
  - `packages/log-scrub/index.js` (UNTOUCHED)
  - `packages/log-scrub/secret-material.js` (UNTOUCHED)

---

## 10. Subsystem Migration Status: Step 5B.3 Health Subsystem

- **Status:** **MIGRATED & VERIFIED**
- **Files Inspected:**
  1. `kubernetes/controller/health-server.js`: HTTP probe server for Kubernetes controller liveness (`/healthz`) and readiness (`/readyz`) probes.
  2. `infrastructure/api/routes/health.js`: Application API health routes exposing `GET /` and `GET /health` with PostgreSQL database ping.
- **Go Destination:** `internal/health/`
  - `internal/health/types.go`: Core types and interfaces (`ControllerStatus`, `StatusProvider`, `Checker`, `PortChecker`, `State`, `ProbeResponse`, `ControllerLifecycleChecker`).
  - `internal/health/server.go`: Full controller probe HTTP server and handler (`ControllerHealthHandler`, `WritePublicResponse`, `NewServer`, `Server.Start`, `Server.Listen`, `Server.Shutdown`, `Server.Close`).
  - `internal/health/api_health.go`: API health handler (`NewAPIHealthHandler`, `DBPinger`, `PingFunc`, `APIHealthConfig`, `APIHealthResponse`).
- **Exact Liveness Semantics (`GET /healthz`):**
  - Healthy (200 OK): `{"status":"ok"}`. Evaluated when controller phase is `"running"` and dependent ports (client, reporter) are alive.
  - Unhealthy (503 Service Unavailable): `{"status":"unavailable"}`. Evaluated if controller is starting, stopped, failed, or dependent ports are not alive.
- **Exact Readiness Semantics (`GET /readyz`):**
  - Ready (200 OK): `{"status":"ready"}`. Evaluated when controller is healthy, `acceptingWork` is true, and dependent ports are ready.
  - Not ready (503 Service Unavailable): `{"status":"not_ready"}`. Evaluated if controller is not healthy, not accepting work, or dependent ports are not ready.
- **Exact API Health Semantics (`infrastructure/api/routes/health.js`):**
  - `GET /`: returns 200 OK with plain text `"API running"` (`text/plain; charset=utf-8`).
  - `GET /health`: executes database connectivity check (`Ping(ctx)`).
    - On success: 200 OK, `{"status":"healthy","timestamp":"...","uptime":12.34,"environment":"production"}`.
    - On error: logs `logger.Error("Health check failed", "error", err.Error())` and returns 503 Service Unavailable, `{"status":"unhealthy","timestamp":"...","error":"..."}`.
- **Routing & Method Semantics:**
  - Non-GET requests on probe endpoints return 404 Not Found with `{"status":"not_found"}`.
  - Unknown routes on probe server return 404 Not Found with `{"status":"not_found"}`.
  - Header `Content-Type: application/json; charset=utf-8` and exact `Content-Length` set on all probe responses.
- **Lifecycle & Dependency Interfaces:**
  - Decoupled `StatusProvider` interface (`Status() ControllerStatus`) prevents circular dependencies on `internal/lifecycle`.
  - `ControllerLifecycleChecker` accurately models the controller lifecycle and port dependencies (`ClientPort`, `ReporterPort`, `AcceptingWork`, `PhaseFn`).
  - Backward-compatible `State` and `Checker` abstractions preserved for `internal/platform`.
  - `DBPinger` interface decouples API health checks from specific database drivers.
- **Tests Added:**
  - `internal/health/server_test.go`:
    - `TestControllerHealthHandler_Endpoints`: verifies 200/503 status transitions on `/healthz` and `/readyz`.
    - `TestControllerHealthHandler_MethodAndPathRejection`: verifies 404 `not_found` on non-GET methods (POST, PUT, DELETE, PATCH, HEAD) and unknown paths.
    - `TestControllerLifecycleChecker_Integration`: verifies lifecycle phase (`starting`, `running`) and dependent port failures.
    - `TestServer_LifecycleAndHTTP`: tests real network listener startup, dynamic port binding, real HTTP requests, and graceful shutdown.
    - `TestServer_PortAlreadyInUse`: tests port collision handling and error propagation.
    - `TestHealthResponses_NoSecretsOrSensitiveData`: verifies responses contain strictly the `status` field with zero credentials or config leaks.
  - `internal/health/api_health_test.go`:
    - `TestAPIHealthHandler_RootRoute`: tests `GET /` returning `"API running"`.
    - `TestAPIHealthHandler_HealthSuccess`: tests `GET /health` with DB ping, timestamp, uptime, environment.
    - `TestAPIHealthHandler_HealthFailureAndLogging`: tests 503 error on DB failure and verifies logger error emission.
    - `TestAPIHealthHandler_MethodAndPathRejection`: tests 404 rejection on unsupported methods and unknown paths.
    - `TestAPIHealthResponses_ResponseSafety`: asserts that API health responses do not leak sensitive database configuration.
- **Untouched Source Files:**
  - `kubernetes/controller/health-server.js` (UNTOUCHED)
  - `infrastructure/api/routes/health.js` (UNTOUCHED)

---

## 11. Subsystem Migration Status: Step 5B.4 Lifecycle + Runtime Subsystem

- **Status:** **MIGRATED & VERIFIED**
- **Files Inspected:**
  1. `kubernetes/controller/lifecycle.js`: Controller lifecycle coordinator (startup, running phase, signal handling, graceful shutdown sequence, error handling, exit code management, health status reporting).
  2. `kubernetes/controller/runtime.js`: Work tracker and port orchestrator (in-flight tracking via `activeWorkCount`, `acceptingWork` barrier, dependent port startup and shutdown sequencing, idle barrier waiting with timeout).
  3. `kubernetes/controller/ports.js`: Port lifecycle defaults (`UnavailablePort` with `isAlive=true`, `isReady=false`).
- **Go Destination:** `internal/lifecycle/`
  - `internal/lifecycle/phase.go`: Typed `Phase` enum constants (`starting`, `running`, `stopping`, `stopped`, `failed`) and state transition validation (`IsValidTransition`, `IsTerminal`).
  - `internal/lifecycle/errors.go`: Coded errors (`CodedError`) with `Code()` matching exact JavaScript error codes: `CONTROLLER_STARTUP_FAILED`, `CONTROLLER_SHUTDOWN_FAILED`, `CONTROLLER_STOPPING`, `INVALID_CONFIG`.
  - `internal/lifecycle/ports.go`: `Port` interface (`Start`, `StopAcceptingWork`, `Close`, `IsAlive`, `IsReady`), `UnavailablePort` (matching JS default port behavior), and `BasePort` for tests.
  - `internal/lifecycle/runtime.go`: `Runtime` controller (`NewRuntime`, `Start`, `StopAcceptingWork`, `TrackWork`, `WaitForIdle`, `Close`, `IsAlive`, `IsReady`, `IsAcceptingWork`, `IsStarted`, `ActiveCount`), along with backward-compatible `SimpleTracker`.
  - `internal/lifecycle/lifecycle.go`: `ControllerLifecycle` (`NewControllerLifecycle`, `Start`, `Shutdown`, `Status`, `Phase`, `InstallSignalHandlers`, `ExitCode`, `HasExited`, `IsShutdownRequested`), and interfaces `HealthServer` and `RuntimeController`.
  - `internal/lifecycle/manager.go`: Platform `Manager` interface preserved for skeleton compatibility.
- **Lifecycle States & Transitions:**
  - Expected states: `starting`, `running`, `stopping`, `stopped`, `failed`.
  - Valid transitions:
    - `starting` -> `running`, `stopping`, `failed`
    - `running` -> `stopping`, `failed`
    - `stopping` -> `stopped`, `failed`
    - Terminal states: `stopped`, `failed`
  - Invalid transitions (enforced and tested):
    - `stopped` -> `running`, `starting`, `stopping`, `failed`
    - `failed` -> `running`, `starting`, `stopping`, `stopped`
    - `running` -> `starting`
    - `stopping` -> `running`
- **Startup Sequence:**
  1. Process begins in `starting` phase.
  2. `healthServer.Listen()`. If error, transition to `failed`, run concurrent cleanup (`runtime.StopAcceptingWork()`, `healthServer.Close()`, `runtime.Close()`), log `controller-startup-failed` with code, invoke `exitProcess(1)`, and return error.
  3. Check if shutdown was requested concurrently; if so, return early.
  4. `runtime.Start()`:
     a. Starts reporter port (`reporter.Start()`). If error, reset `acceptingWork = false` and return error.
     b. Sets `acceptingWork = true`.
     c. Starts client port (`client.Start()`). If error, reset `acceptingWork = false` and return error.
     d. Sets `started = true`.
  5. Check if shutdown was requested concurrently; if so, return early.
  6. Phase becomes `running`.
  7. Log info `controller-started`.
- **Shutdown Sequence:**
  1. Idempotency guard: if shutdown was already requested, await completion channel and return cached exit code & error.
  2. Set `shutdownRequested = true`, phase becomes `stopping`.
  3. Unregister signal handlers immediately to avoid signal loops.
  4. Log info `controller-stopping` with triggering signal.
  5. Await in-flight startup if startup was initiated.
  6. Sequential teardown steps:
     a. `runtime.StopAcceptingWork(ctx)`: sets `acceptingWork = false`, calls `client.StopAcceptingWork()` and `reporter.StopAcceptingWork()` concurrently.
     b. `runtime.WaitForIdle(ctx, shutdownTimeout)`: waits for active work count to drop to 0. If timeout expires, log warn `controller-shutdown-timeout` with `shutdownTimeoutMs`, but proceed with cleanup.
     c. `healthServer.Close()`.
     d. `runtime.Close(ctx)`: sets `acceptingWork = false`, calls `client.Close()` and `reporter.Close()` concurrently.
  7. Determine outcome: if any teardown step failed, phase becomes `failed`, exitCode = 1, log error `controller-shutdown-failed`. If all succeeded, phase becomes `stopped`, exitCode = 0, log info `controller-stopped`.
  8. Call `exitOnce(exitCode)`.
- **acceptingWork Semantics:**
  - Initially `false` before startup.
  - Set to `true` during `runtime.Start()` strictly after reporter starts and before client starts.
  - Reset to `false` if startup fails or when `StopAcceptingWork` is invoked.
  - Monotonically `false` once stopping begins.
  - Any attempt to track work when `acceptingWork == false` immediately fails with `ErrControllerStopping` (`"CONTROLLER_STOPPING"`).
- **Work Tracking & waitForIdle:**
  - `TrackWork(fn)` increments `activeWorkCount`, executes `fn()`, and decrements `activeWorkCount` on exit. When the count drops to 0, `idleCh` is closed.
  - `WaitForIdle(ctx, timeout)` returns `true` immediately if `activeWorkCount == 0`. Otherwise waits on `idleCh`, timeout timer, or context cancellation without goroutine leaks.
- **Signal Handling:**
  - `InstallSignalHandlers` captures `SIGINT` and `SIGTERM`.
  - Signal triggers `Shutdown(ctx, signalName)`.
  - Handlers are immediately removed upon entering `Shutdown` to avoid repeated triggers.
  - Returns a cleanup function allowing clean unregistration.
- **Health Integration:**
  - `ControllerLifecycle.Status()` returns `health.ControllerStatus` (`Healthy`, `Ready`, `Phase`).
  - Implements `health.StatusProvider` without introducing circular dependencies.
  - Controller is healthy only when `Phase == PhaseRunning` and `runtime.IsAlive()`.
  - Controller is ready only when healthy and `runtime.IsReady()`.
- **Concurrency & Race Safety:**
  - Full race safety verified via `go test -race ./internal/lifecycle/...` (0 races detected).
  - Mutexes guard state transitions; cached completion channels guard idempotency.
- **Tests Added:**
  - `internal/lifecycle/phase_test.go` (embedded in `lifecycle_test.go`): `TestPhase_StateTransitions` (all valid, invalid, and terminal transitions).
  - `internal/lifecycle/lifecycle_test.go`:
    - `TestControllerLifecycle_NormalStartup`: validates ordering, status, phase transition, and logs.
    - `TestControllerLifecycle_StartupFailure_Health`: validates failure phase, cleanup execution, exit code 1, and error logging.
    - `TestControllerLifecycle_StartupFailure_Runtime`: validates failure phase, cleanup execution, exit code 1.
    - `TestControllerLifecycle_ShutdownSequence`: validates exact 4-step teardown ordering, phase, exit code 0, and logs.
    - `TestControllerLifecycle_ShutdownTimeout`: validates warning log on timeout and continued teardown.
    - `TestControllerLifecycle_ShutdownFailure`: validates teardown error handling, exit code 1, and failure logging.
    - `TestControllerLifecycle_ShutdownIdempotency`: validates sequential calls return identical results and execute teardown once.
    - `TestControllerLifecycle_ShutdownConcurrent`: validates 20 concurrent shutdown callers with zero race conditions.
    - `TestControllerLifecycle_ShutdownWhileStartInFlight`: validates graceful shutdown synchronization when start is blocked.
    - `TestControllerLifecycle_SignalHandling`: validates signal interception, shutdown trigger, and handler unregistration.
  - `internal/lifecycle/runtime_test.go`:
    - `TestRuntime_StartupOrder`: validates reporter starts before client.
    - `TestRuntime_StartupFailure_Reporter`: validates error propagation and acceptingWork reset.
    - `TestRuntime_StartupFailure_Client`: validates error propagation and acceptingWork reset.
    - `TestRuntime_WorkTrackingAndStopping`: validates rejection when stopping and active count tracking.
    - `TestRuntime_WaitForIdle`: validates timeout vs release behavior.
    - `TestRuntime_ConcurrentCallers`: validates 50 goroutines executing work concurrently.
    - `TestRuntime_CloseConcurrently`: validates concurrent closing of ports.
- **Untouched Source Files:**
  - `kubernetes/controller/lifecycle.js` (UNTOUCHED)
  - `kubernetes/controller/runtime.js` (UNTOUCHED)

---

## 12. Subsystem Migration Status: Step 5B.5 Database Runtime Subsystem

- **Status:** **MIGRATED & VERIFIED**
- **Files Inspected:**
  1. `infrastructure/database/database.js`: PostgreSQL connection pool creation, pool options, TLS configuration (`TLSv1.3`), `waitForDatabase`, `testConnection`, query error classification (`isConnectionError`), connection error handling.
  2. `workers/runtime/db.js`: Worker connection pool, `withClient` connection wrapper, advisory lock helpers (`tryAdvisoryLock`, `advisoryUnlock`), `hash32` algorithm.
  3. `packages/config/src/database.js`: Reference configuration parameters and connection string generation migrated in Step 5B.1.
- **Go Destination:** `internal/database/`
  - `internal/database/types.go`: Core interfaces and structs (`DB`, `Connection`, `Tx`, `SessionLock`, `PoolStat`, `AdvisoryLocker`, `Client`).
  - `internal/database/errors.go`: Typed errors (`ErrLockAcquisitionFailed`, `ErrDatabaseNotReady`, `ErrDatabaseClosed`, `ErrTransactionRolledBack`), `IsConnectionError`, and `RedactError`.
  - `internal/database/lock.go`: Legacy JavaScript-compatible UTF-16 `Hash32`, 64-bit non-negative `AdvisoryLockKey`, `TryAdvisoryLock`, and `AdvisoryUnlock`.
  - `internal/database/config.go`: Mapping `config.DatabaseConfig` to `pgxpool.Config`, TLS configuration, and connection usage recycling hook (`maxUses: 7500`).
  - `internal/database/database.go`: `Database` runtime implementation wrapping `pgxpool.Pool`, with `WithConnection`, `WithTransaction`, `AcquireLock`, `TestConnection`, `WaitForDatabase`, `Ping`, `Close`, and `Stat`.
- **Exact Pool Defaults:**
  - `PoolMax`: 10 (default in `infrastructure/database/database.js` and `workers/runtime/db.js`)
  - `PoolMin`: 2 (default in `infrastructure/database/database.js` and `packages/config/src/database.js`)
  - `PoolIdleTimeout`: 30,000 ms (30 seconds across all JS sources)
  - `ConnectionTimeout`: 5,000 ms (config default) / 30,000 ms (infrastructure default)
  - `AcquireTimeout`: 60,000 ms (60 seconds)
  - `MaxUses`: 7,500 connections (enforced via `AfterRelease` hook destroying connection after 7500 queries)
- **Driver Selected:**
  - `github.com/jackc/pgx/v5` and `github.com/jackc/pgx/v5/pgxpool`.
  - Native Go PostgreSQL driver with high-performance binary protocol support, connection pool hooks (`BeforeAcquire`, `AfterRelease`, `BeforeClose`), connection lifetime controls, native advisory locking, and direct SSL/TLS integration.
- **SSL/TLS Semantics:**
  - `verify` (or CA root certificate provided): encrypted, server identity verified (`RejectUnauthorized: true`), `RootCAs` cert pool populated, `MinVersion: TLSv1.3`.
  - `require`: encrypted, server identity verified in production (`RejectUnauthorized: isProduction`), `MinVersion: TLSv1.3`.
  - `require-no-verify`: encrypted, server identity NOT verified (`RejectUnauthorized: false`), `MinVersion: TLSv1.3`.
  - `disable`: unencrypted plaintext connection.
- **Advisory Lock Hashing Compatibility:**
  - `Hash32(s string) int32`: decodes UTF-8 string into UTF-16 code units (`utf16.Encode([]rune(s))`) to exactly reproduce JavaScript's UTF-16 `s.charCodeAt(i)` and `(h << 5) - h + code; h |= 0`.
  - Tested and verified byte-for-byte against Node.js output:
    - Empty string: `0`
    - `"test-lock"`: `-1226527354`
    - `"whatbreaks:discovery"`: `-2033551410`
    - `"café"`: `3045921`
    - `"日本語"`: `25921943`
    - `"worker:🚀"` (Astral emoji with UTF-16 surrogate pair): `1097726815`
    - Long string: `-2047572317`
  - `AdvisoryLockKey(key string) int64`: computes `Math.abs(hash32(key))` as a 64-bit integer, ensuring that when `hash32` produces `math.MinInt32` (`-2147483648`), the value is cleanly negated to positive `2147483648` without signed 32-bit overflow, matching JavaScript `Number` behavior when passed to PostgreSQL `pg_try_advisory_lock($1)`.
- **Connection Affinity:**
  - PostgreSQL advisory locks are session-scoped. Connection affinity is strictly preserved:
    1. `WithConnection`: acquires dedicated `*pgxpool.Conn`, passes `Connection` to callback, releases connection in `defer`.
    2. `AcquireLock`: acquires dedicated `*pgxpool.Conn`, runs `pg_try_advisory_lock($1)`. If lock fails, connection is released immediately. If acquired, returns `SessionLock` holding that exact connection until `Unlock(ctx)` is called, which calls `pg_advisory_unlock($1)` on that connection and releases it to the pool.
- **Connection Error Classification:**
  - `IsConnectionError(err error) bool`: detects SQLSTATE Class `08*` (`pgconn.PgError`), `net.OpError`, `syscall.ECONNREFUSED`, `syscall.ECONNRESET`, and error messages containing `"connection terminated"`, `"could not connect"`, `"connection reset"`, `"broken pipe"`.
- **Secret Redaction:**
  - `RedactError(err error)`: scrubs any DSN passwords (`postgresql://user:[REDACTED]@host...`) before error messages can be logged or returned.
  - Safe DSN generation in `internal/config/database.go` replaces passwords with `[REDACTED]`.
- **Health Integration:**
  - `*Database` implements `Ping(ctx context.Context) error`, satisfying `internal/health.DBPinger` without cyclic package dependencies.
- **Tests Added:**
  - `internal/database/database_test.go`:
    - `TestHash32_ExactJavaScriptCompatibility`: validates exact matching across ASCII, UTF-8, multi-byte Unicode, and emojis.
    - `TestAdvisoryLockKey_MinInt32Boundary`: validates Math.abs boundary behavior.
    - `TestIsConnectionError`: validates SQLSTATE Class 08, network errors, syscalls, and message parsing.
    - `TestRedactError`: validates credential scrubbing.
    - `TestBuildPgxPoolConfig`: validates connection pool bounds, TLS settings, and MaxUses hook.
    - `TestDatabase_ClosedPoolGuards`: validates closed pool guard on all DB methods and idempotent Close.
    - `TestDatabase_ConcurrentHashingAndLockKeys`: validates concurrent hashing across 50 goroutines.
    - `TestSessionLock_IdempotentUnlock`: validates idempotency of SessionLock unlock.
  - `internal/database/integration_test.go`:
    - `TestIntegration_RealPostgreSQL`: comprehensive integration test covering ping, query, transaction commit, transaction rollback, and advisory lock connection affinity & contention against live PostgreSQL (conditionally skipped when PostgreSQL is unreachable).
- **Untouched Source Files:**
  - `infrastructure/database/database.js` (UNTOUCHED)
  - `workers/runtime/db.js` (UNTOUCHED)




