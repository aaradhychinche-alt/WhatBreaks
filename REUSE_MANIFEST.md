# Reuse Manifest

This document records the foundational infrastructure components copied from the TokenTimer repository into the WhatBreaks workspace for the MVP.

| Source File | Destination | Purpose | Why Needed | Dependencies | Status |
|-------------|-------------|---------|------------|--------------|--------|
| `apps/api/db/database.js` | `infrastructure/database/database.js` | Database connection pooling | Standardized PG connection setup | `log-scrub`, `logger.js` | REUSED |
| `apps/api/utils/logger.js` | `infrastructure/utils/logger.js` | Centralized logging | Standardized structured logging | `log-scrub`, `runtime-labels` | REUSED |
| `apps/api/config/runtime-labels.js` | `infrastructure/config/runtime-labels.js` | Runtime configurations | Needed for logger setup | None | REUSED |
| `packages/log-scrub/*` | `packages/log-scrub/*` | Sensitive data scrubbing | Standardized logging safety | None | REUSED |
| `packages/config/*` | `packages/config/*` | Shared config definitions | Standard configuration | None | REUSED |
| `apps/api/middleware/auth.js` | `infrastructure/auth/auth-middleware.js` | Core authentication logic | Securing API routes | `logger.js`, `internal-worker-auth` | REUSED |
| `apps/api/middleware/internal-worker-auth.js` | `infrastructure/auth/internal-worker-auth.js` | Worker authentication | Backend service auth | None | REUSED |
| `apps/api/middleware/workspace-access-policy.js` | `infrastructure/auth/workspace-access-policy.js` | Workspace policies | Multi-tenancy support | None | REUSED |
| `apps/api/middleware/validation.js` | `infrastructure/auth/validation.js` | Request validation | Standard express validation | `logger.js` | REUSED |
| `apps/api/session-cookie-options.js` | `infrastructure/auth/session-cookie-options.js` | Cookie configurations | Secure session management | None | REUSED |
| `apps/worker/src/*` | `workers/runtime/*` | Generic worker execution | Scheduling and running workers | None | REUSED |
| `apps/k8s-controller/src/*` | `kubernetes/controller/*` | Kubernetes controller setup | Managing K8s client and lifecycle | None | REUSED |
| `apps/api/index.js` | `infrastructure/api/index.js` | Express app bootstrap | Minimal API initialization | middlewares, logger | REUSED |
| `apps/api/middleware/rateLimit.js` | `infrastructure/api/middleware/rateLimit.js` | Global rate limiting | API endpoint protection | `logger.js` | REUSED |
| `apps/api/middleware/csrf.js` | `infrastructure/api/middleware/csrf.js` | CSRF protection | Secure API interactions | `session-cookie-options` | REUSED |
| `apps/api/routes/health.js` | `infrastructure/api/routes/health.js` | Liveness checks | Deployment readiness | `database.js`, `logger.js` | REUSED |
