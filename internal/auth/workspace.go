package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

var (
	ErrNotAuthenticated          = errors.New("auth: user not authenticated")
	ErrInvalidSession            = errors.New("auth: invalid session data")
	ErrEmailVerificationRequired = errors.New("auth: email verification required")
	ErrInvalidWorkspaceID        = errors.New("auth: invalid workspace identifier")
	ErrWorkspaceNotFound         = errors.New("auth: workspace not found")
	ErrWorkspaceForbidden        = errors.New("auth: access to workspace forbidden")
)

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsValidUUID checks if the string matches the standard 8-4-4-4-12 UUID format.
func IsValidUUID(id string) bool {
	return uuidRegex.MatchString(strings.TrimSpace(id))
}

// WorkspacePolicy encapsulates route-level workspace access rules,
// faithfully migrating infrastructure/auth/workspace-access-policy.js.
type WorkspacePolicy struct {
	HideExistence bool
}

// HideWorkspaceExistence creates an HTTP middleware attaching HideExistence=true to the context,
// causing unauthorized or forbidden workspace requests to respond with 404 Not Found instead of 403.
func HideWorkspaceExistence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithWorkspacePolicy(r.Context(), WorkspacePolicy{HideExistence: true})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WorkspaceMembershipProvider defines the interface for looking up a user's role within a workspace.
type WorkspaceMembershipProvider interface {
	GetWorkspaceRole(ctx context.Context, userID, workspaceID string) (role string, err error)
}

// MemoryMembershipProvider provides an in-memory workspace membership mapping for tests and isolation.
type MemoryMembershipProvider struct {
	memberships map[string]string // key: userID + ":" + workspaceID -> role
}

// NewMemoryMembershipProvider creates an empty MemoryMembershipProvider.
func NewMemoryMembershipProvider() *MemoryMembershipProvider {
	return &MemoryMembershipProvider{
		memberships: make(map[string]string),
	}
}

// AddMember adds a user to a workspace with the specified role.
func (m *MemoryMembershipProvider) AddMember(userID, workspaceID, role string) {
	key := userID + ":" + workspaceID
	m.memberships[key] = role
}

func (m *MemoryMembershipProvider) GetWorkspaceRole(ctx context.Context, userID, workspaceID string) (string, error) {
	key := userID + ":" + workspaceID
	role, ok := m.memberships[key]
	if !ok || role == "" {
		return "", nil
	}
	return role, nil
}

// WorkspaceAuthorizer enforces workspace boundary isolation, member roles, and deny-by-default rules.
type WorkspaceAuthorizer struct {
	membership WorkspaceMembershipProvider
}

// NewWorkspaceAuthorizer constructs a WorkspaceAuthorizer with the given membership provider.
func NewWorkspaceAuthorizer(membership WorkspaceMembershipProvider) *WorkspaceAuthorizer {
	return &WorkspaceAuthorizer{
		membership: membership,
	}
}

// AuthorizeWorkspaceAccess evaluates whether the context's authenticated identity can access workspaceID.
func (a *WorkspaceAuthorizer) AuthorizeWorkspaceAccess(ctx context.Context, workspaceID string) error {
	policy := WorkspacePolicyFromContext(ctx)

	// 1. Authenticated user required
	user, ok := UserFromContext(ctx)
	if !ok || user == nil {
		return ErrNotAuthenticated
	}

	// 2. Validate workspace identifier format
	cleanID := strings.TrimSpace(workspaceID)
	if cleanID == "" || !IsValidUUID(cleanID) {
		if policy.HideExistence {
			return ErrWorkspaceNotFound
		}
		return ErrInvalidWorkspaceID
	}

	// 3. Internal workers have administrative access across all workspaces
	if IsWorkerCallFromContext(ctx) || user.Role == WorkerRole {
		return nil
	}

	// 4. Look up membership role in the specified workspace
	if a.membership == nil {
		if policy.HideExistence {
			return ErrWorkspaceNotFound
		}
		return ErrWorkspaceForbidden
	}

	role, err := a.membership.GetWorkspaceRole(ctx, user.ID, cleanID)
	if err != nil || role == "" {
		// Deny-by-default: hide existence if policy is enabled to prevent workspace probing
		if policy.HideExistence {
			return ErrWorkspaceNotFound
		}
		return ErrWorkspaceForbidden
	}

	return nil
}

// RequireWorkspaceAccessMiddleware wraps an HTTP handler, extracting the workspace ID from the request
// and enforcing workspace access authorization.
func RequireWorkspaceAccessMiddleware(authorizer *WorkspaceAuthorizer, getWorkspaceID func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var wsID string
			if getWorkspaceID != nil {
				wsID = getWorkspaceID(r)
			}

			err := authorizer.AuthorizeWorkspaceAccess(r.Context(), wsID)
			if err != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				switch {
				case errors.Is(err, ErrNotAuthenticated):
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Not authenticated"})
				case errors.Is(err, ErrWorkspaceNotFound):
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Workspace not found"})
				case errors.Is(err, ErrInvalidWorkspaceID):
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid workspace ID"})
				default:
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Access to workspace forbidden"})
				}
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
