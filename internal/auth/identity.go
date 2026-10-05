package auth

import (
	"context"
	"net"
	"net/http"
	"strings"
)

const (
	RoleAdmin  = "admin"
	RoleOwner  = "owner"
	RoleMember = "member"
)

// User represents an authenticated principal.
type User struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	Email         string `json:"email"`
	AuthMethod    string `json:"auth_method"`
	EmailVerified bool   `json:"email_verified"`
}

type (
	userContextKey     struct{}
	workerContextKey   struct{}
	workspacePolicyKey struct{}
)

// WithUser returns a context carrying the authenticated User.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userContextKey{}, u)
}

// UserFromContext extracts the authenticated User from the context.
func UserFromContext(ctx context.Context) (*User, bool) {
	if ctx == nil {
		return nil, false
	}
	u, ok := ctx.Value(userContextKey{}).(*User)
	return u, ok && u != nil
}

// WithWorkerCall marks the context as an authenticated internal worker invocation.
func WithWorkerCall(ctx context.Context, isWorker bool) context.Context {
	return context.WithValue(ctx, workerContextKey{}, isWorker)
}

// IsWorkerCallFromContext reports whether the context represents an internal worker call.
func IsWorkerCallFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(workerContextKey{}).(bool)
	return ok && v
}

// WithWorkspacePolicy attaches route-level workspace access rules to the context.
func WithWorkspacePolicy(ctx context.Context, policy WorkspacePolicy) context.Context {
	return context.WithValue(ctx, workspacePolicyKey{}, policy)
}

// WorkspacePolicyFromContext retrieves the workspace access policy from context, defaulting to empty.
func WorkspacePolicyFromContext(ctx context.Context) WorkspacePolicy {
	if ctx == nil {
		return WorkspacePolicy{}
	}
	p, ok := ctx.Value(workspacePolicyKey{}).(WorkspacePolicy)
	if !ok {
		return WorkspacePolicy{}
	}
	return p
}

// ResolveClientIP extracts the client IP matching infrastructure/utils/logger.js resolveClientIp:
// Checks X-Forwarded-For (first comma-separated entry), falling back to RemoteAddr.
func ResolveClientIP(req *http.Request) string {
	if req == nil {
		return ""
	}

	xff := req.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}

	if req.RemoteAddr != "" {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err == nil && host != "" {
			return host
		}
		return req.RemoteAddr
	}

	return ""
}
