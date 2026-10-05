package auth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

func TestSecurity_SecretsDoNotLeakInErrorsOrLogs(t *testing.T) {
	const secretWorkerToken = "super-secret-worker-token-xyz-12345"
	workerAuth := NewWorkerAuthenticator(secretWorkerToken)

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "security-audit")

	mw := NewAuthMiddleware(workerAuth, nil, logger, false)

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Attempt with invalid token
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid-attempted-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Check HTTP response body
	body := rr.Body.String()
	if strings.Contains(body, secretWorkerToken) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: secret worker token leaked in response body!")
	}
	if strings.Contains(body, "invalid-attempted-token") {
		t.Fatalf("CRITICAL SECURITY VIOLATION: attempted token echoed in response body!")
	}

	// Check logs
	logContent := logBuf.String()
	if strings.Contains(logContent, secretWorkerToken) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: secret worker token leaked in logs!")
	}
	if strings.Contains(logContent, "invalid-attempted-token") {
		t.Fatalf("CRITICAL SECURITY VIOLATION: incoming bearer token value leaked in logs!")
	}
}

func TestSecurity_WorkspaceIsolationAndDenyByDefault(t *testing.T) {
	authorizer := NewWorkspaceAuthorizer(nil) // nil membership provider -> must deny by default

	ctx := WithUser(context.Background(), &User{ID: "attacker-user", Role: RoleMember})

	// 1. Nil membership provider denies access
	err := authorizer.AuthorizeWorkspaceAccess(ctx, "11111111-1111-1111-1111-111111111111")
	if err == nil {
		t.Fatalf("expected deny-by-default when membership provider is nil")
	}

	// 2. HideExistence policy converts forbidden to not found
	ctxHidden := WithWorkspacePolicy(ctx, WorkspacePolicy{HideExistence: true})
	errHidden := authorizer.AuthorizeWorkspaceAccess(ctxHidden, "11111111-1111-1111-1111-111111111111")
	if errHidden != ErrWorkspaceNotFound {
		t.Fatalf("expected ErrWorkspaceNotFound under HideExistence policy, got %v", errHidden)
	}
}

func TestSecurity_ConcurrentAuthenticationIsolation(t *testing.T) {
	workerAuth := NewWorkerAuthenticator("concurrent-worker-key")
	memberships := NewMemoryMembershipProvider()

	const numUsers = 50
	const iterations = 20

	for i := 0; i < numUsers; i++ {
		uID := fmt.Sprintf("user-%d", i)
		wsID := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		memberships.AddMember(uID, wsID, RoleMember)
	}

	authorizer := NewWorkspaceAuthorizer(memberships)

	sessionVal := SessionValidatorFunc(func(req *http.Request) (*User, error) {
		uID := req.Header.Get("X-User-ID")
		if uID == "" {
			return nil, nil
		}
		return &User{
			ID:            uID,
			Role:          RoleMember,
			Email:         uID + "@test.com",
			AuthMethod:    "sso",
			EmailVerified: true,
		}, nil
	})

	mw := NewAuthMiddleware(workerAuth, sessionVal, logging.NewJSONLogger(io.Discard, logging.LevelInfo, "test"), false)

	var wg sync.WaitGroup
	wg.Add(numUsers)

	for i := 0; i < numUsers; i++ {
		go func(workerIndex int) {
			defer wg.Done()
			ownUID := fmt.Sprintf("user-%d", workerIndex)
			ownWS := fmt.Sprintf("00000000-0000-0000-0000-%012d", workerIndex)
			otherWS := fmt.Sprintf("00000000-0000-0000-0000-%012d", (workerIndex+1)%numUsers)

			for iter := 0; iter < iterations; iter++ {
				// 1. Authorized access to own workspace
				reqOwn := httptest.NewRequest(http.MethodGet, "/resource", nil)
				reqOwn.Header.Set("X-User-ID", ownUID)
				rrOwn := httptest.NewRecorder()

				mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					u, ok := UserFromContext(r.Context())
					if !ok || u.ID != ownUID {
						t.Errorf("concurrent context contamination: got user %v, expected %s", u, ownUID)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if err := authorizer.AuthorizeWorkspaceAccess(r.Context(), ownWS); err != nil {
						t.Errorf("failed to authorize own workspace: %v", err)
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.WriteHeader(http.StatusOK)
				})).ServeHTTP(rrOwn, reqOwn)

				if rrOwn.Code != http.StatusOK {
					t.Errorf("expected 200 for user %s on own workspace, got %d", ownUID, rrOwn.Code)
				}

				// 2. Cross-workspace forbidden
				reqCross := httptest.NewRequest(http.MethodGet, "/resource", nil)
				reqCross.Header.Set("X-User-ID", ownUID)
				rrCross := httptest.NewRecorder()

				mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					err := authorizer.AuthorizeWorkspaceAccess(r.Context(), otherWS)
					if err == nil {
						t.Errorf("CRITICAL SECURITY FAILURE: user %s accessed other workspace %s!", ownUID, otherWS)
						w.WriteHeader(http.StatusOK)
						return
					}
					w.WriteHeader(http.StatusForbidden)
				})).ServeHTTP(rrCross, reqCross)

				if rrCross.Code != http.StatusForbidden {
					t.Errorf("expected 403 for cross-workspace access, got %d", rrCross.Code)
				}
			}
		}(i)
	}

	wg.Wait()
}
