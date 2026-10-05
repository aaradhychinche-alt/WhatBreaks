package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceAuthorizer_AccessRules(t *testing.T) {
	const (
		validWS1 = "11111111-1111-1111-1111-111111111111"
		validWS2 = "22222222-2222-2222-2222-222222222222"
		userID   = "user-123"
	)

	memberships := NewMemoryMembershipProvider()
	memberships.AddMember(userID, validWS1, RoleMember)

	authorizer := NewWorkspaceAuthorizer(memberships)

	// 1. Authorized member access to validWS1
	ctxValid := WithUser(context.Background(), &User{ID: userID, Role: RoleMember})
	if err := authorizer.AuthorizeWorkspaceAccess(ctxValid, validWS1); err != nil {
		t.Fatalf("expected authorized access to validWS1, got error: %v", err)
	}

	// 2. Unauthenticated access
	ctxUnauth := context.Background()
	if err := authorizer.AuthorizeWorkspaceAccess(ctxUnauth, validWS1); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("expected ErrNotAuthenticated, got: %v", err)
	}

	// 3. Cross-workspace access attempt (accessing validWS2 without membership)
	if err := authorizer.AuthorizeWorkspaceAccess(ctxValid, validWS2); !errors.Is(err, ErrWorkspaceForbidden) {
		t.Errorf("expected ErrWorkspaceForbidden for cross-workspace access, got: %v", err)
	}

	// 4. Cross-workspace access with HideExistence policy enabled
	ctxHidden := WithWorkspacePolicy(ctxValid, WorkspacePolicy{HideExistence: true})
	if err := authorizer.AuthorizeWorkspaceAccess(ctxHidden, validWS2); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Errorf("expected ErrWorkspaceNotFound when HideExistence is true, got: %v", err)
	}

	// 5. Malformed workspace ID without HideExistence
	if err := authorizer.AuthorizeWorkspaceAccess(ctxValid, "not-a-valid-uuid"); !errors.Is(err, ErrInvalidWorkspaceID) {
		t.Errorf("expected ErrInvalidWorkspaceID for invalid uuid, got: %v", err)
	}

	// 6. Malformed workspace ID with HideExistence
	if err := authorizer.AuthorizeWorkspaceAccess(ctxHidden, "not-a-valid-uuid"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Errorf("expected ErrWorkspaceNotFound for invalid uuid with HideExistence, got: %v", err)
	}

	// 7. Worker call bypasses workspace membership checks
	ctxWorker := WithUser(context.Background(), &User{Role: WorkerRole, Email: WorkerEmail})
	ctxWorker = WithWorkerCall(ctxWorker, true)
	if err := authorizer.AuthorizeWorkspaceAccess(ctxWorker, validWS2); err != nil {
		t.Errorf("expected internal worker call to be granted workspace access: %v", err)
	}
}

func TestRequireWorkspaceAccessMiddleware(t *testing.T) {
	const (
		ws1 = "11111111-1111-1111-1111-111111111111"
		ws2 = "22222222-2222-2222-2222-222222222222"
	)

	memberships := NewMemoryMembershipProvider()
	memberships.AddMember("u1", ws1, RoleMember)
	authorizer := NewWorkspaceAuthorizer(memberships)

	extractor := func(r *http.Request) string {
		return r.Header.Get("X-Workspace-ID")
	}

	protectedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mw := RequireWorkspaceAccessMiddleware(authorizer, extractor)
	handler := mw(protectedHandler)

	// 1. Authorized request -> 200
	reqAuth := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	reqAuth.Header.Set("X-Workspace-ID", ws1)
	reqAuth = reqAuth.WithContext(WithUser(reqAuth.Context(), &User{ID: "u1"}))
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, reqAuth)
	if rr1.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr1.Code)
	}

	// 2. Unauthenticated -> 401
	reqNoUser := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	reqNoUser.Header.Set("X-Workspace-ID", ws1)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, reqNoUser)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr2.Code)
	}

	// 3. Cross-workspace forbidden -> 403
	reqCross := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	reqCross.Header.Set("X-Workspace-ID", ws2)
	reqCross = reqCross.WithContext(WithUser(reqCross.Context(), &User{ID: "u1"}))
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, reqCross)
	if rr3.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr3.Code)
	}

	// 4. Hide existence policy middleware -> 404
	hiddenHandler := HideWorkspaceExistence(handler)
	reqHidden := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	reqHidden.Header.Set("X-Workspace-ID", ws2)
	reqHidden = reqHidden.WithContext(WithUser(reqHidden.Context(), &User{ID: "u1"}))
	rr4 := httptest.NewRecorder()
	hiddenHandler.ServeHTTP(rr4, reqHidden)
	if rr4.Code != http.StatusNotFound {
		t.Errorf("expected 404 with HideWorkspaceExistence, got %d", rr4.Code)
	}
}
