# Reuse Manifest

This document recorded the foundational infrastructure components copied from the TokenTimer repository into the WhatBreaks workspace for the MVP.

> **MIGRATION STATUS:** All components have been completely migrated to Go (Steps 5B.1 through 5B.9) and verified. The legacy JavaScript source files were deleted during the **Final JS→Go Cutover Audit**.

| Source File | Destination | Purpose | Why Needed | Dependencies | Status |
|-------------|-------------|---------|------------|--------------|--------|
| `apps/api/db/database.js` | `infrastructure/database/database.js` | Database connection pooling | Standardized PG connection setup | `log-scrub`, `logger.js` | MIGRATED TO GO (`internal/database`) & DELETED |
| `apps/api/utils/logger.js` | `infrastructure/utils/logger.js` | Centralized logging | Standardized structured logging | `log-scrub`, `runtime-labels` | MIGRATED TO GO (`internal/logging`) & DELETED |
| `apps/api/config/runtime-labels.js` | `infrastructure/config/runtime-labels.js` | Runtime configurations | Needed for logger setup | None | DISCARDED (Legacy TokenTimer branding) & DELETED |
| `packages/log-scrub/*` | `packages/log-scrub/*` | Sensitive data scrubbing | Standardized logging safety | None | MIGRATED TO GO (`internal/logging`) & DELETED |
| `packages/config/*` | `packages/config/*` | Shared config definitions | Standard configuration | None | MIGRATED TO GO (`internal/config`) & DELETED |
| `apps/api/middleware/auth.js` | `infrastructure/auth/auth-middleware.js` | Core authentication logic | Securing API routes | `logger.js`, `internal-worker-auth` | MIGRATED TO GO (`internal/auth`) & DELETED |
| `apps/api/middleware/internal-worker-auth.js` | `infrastructure/auth/internal-worker-auth.js` | Worker authentication | Backend service auth | None | MIGRATED TO GO (`internal/auth`) & DELETED |
| `apps/api/middleware/workspace-access-policy.js` | `infrastructure/auth/workspace-access-policy.js` | Workspace policies | Multi-tenancy support | None | MIGRATED TO GO (`internal/auth`) & DELETED |
| `apps/api/middleware/validation.js` | `infrastructure/auth/validation.js` | Request validation | Standard express validation | `logger.js` | DISCARDED (Legacy cert validation) & DELETED |
| `apps/api/session-cookie-options.js` | `infrastructure/auth/session-cookie-options.js` | Cookie configurations | Secure session management | None | MIGRATED TO GO (`internal/auth`) & DELETED |
| `apps/worker/src/*` | `workers/runtime/*` | Generic worker execution | Scheduling and running workers | None | MIGRATED TO GO (`internal/scheduler`) & DELETED |
| `apps/k8s-controller/src/*` | `kubernetes/controller/*` | Kubernetes controller setup | Managing K8s client and lifecycle | None | MIGRATED TO GO (`internal/lifecycle`, `internal/collector/k8s`) & DELETED |
| `apps/api/index.js` | `infrastructure/api/index.js` | Express app bootstrap | Minimal API initialization | middlewares, logger | MIGRATED TO GO (`internal/api`, `internal/platform`) & DELETED |
| `apps/api/middleware/rateLimit.js` | `infrastructure/api/middleware/rateLimit.js` | Global rate limiting | API endpoint protection | `logger.js` | MIGRATED TO GO (`internal/api`) & DELETED |
| `apps/api/middleware/csrf.js` | `infrastructure/api/middleware/csrf.js` | CSRF protection | Secure API interactions | `session-cookie-options` | MIGRATED TO GO (`internal/api`) & DELETED |
| `apps/api/routes/health.js` | `infrastructure/api/routes/health.js` | Liveness checks | Deployment readiness | `database.js`, `logger.js` | MIGRATED TO GO (`internal/health`) & DELETED |
