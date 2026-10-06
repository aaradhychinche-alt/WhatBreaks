package answer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type mockService struct {
	ans *answer.ImpactAnswer
	err error
}

func (m *mockService) AnalyzeImpact(ctx context.Context, req answer.ImpactRequest) (*answer.ImpactAnswer, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.ans, nil
}

func defaultValidPayload() []byte {
	return []byte(`{
		"target": {
			"provider": "kubernetes",
			"resource_type": "pod",
			"provider_id": "payments/payments-api"
		},
		"direction": "incoming",
		"max_depth": 5
	}`)
}

// ---------------------------------------------------------------------------
// Test 1: valid impact request
// ---------------------------------------------------------------------------
func TestHandler_ValidImpactRequest(t *testing.T) {
	svc := &mockService{
		ans: &answer.ImpactAnswer{
			Target: answer.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "pod",
				ProviderID:   "payments/payments-api",
			},
			Summary: answer.ImpactSummary{
				ImpactedCount: 0,
			},
		},
	}
	h := answer.NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	var res answer.ImpactAnswer
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if res.Target.ProviderID != "payments/payments-api" {
		t.Errorf("expected target payments/payments-api, got %s", res.Target.ProviderID)
	}
}

// ---------------------------------------------------------------------------
// Test 2: malformed JSON
// ---------------------------------------------------------------------------
func TestHandler_MalformedJSON(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader([]byte(`{"target": { "provider": "kubernetes"`)))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if errResp.Code != string(answer.CodeBadRequest) {
		t.Errorf("expected code %s, got %s", answer.CodeBadRequest, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 3: missing target
// ---------------------------------------------------------------------------
func TestHandler_MissingTarget(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	payload := []byte(`{"direction": "incoming", "max_depth": 5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeValidationError) {
		t.Errorf("expected code %s, got %s", answer.CodeValidationError, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 4: empty provider
// ---------------------------------------------------------------------------
func TestHandler_EmptyProvider(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	payload := []byte(`{
		"target": {"provider": "   ", "resource_type": "pod", "provider_id": "api"},
		"direction": "incoming",
		"max_depth": 5
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}
	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeValidationError) {
		t.Errorf("expected code %s, got %s", answer.CodeValidationError, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 5: empty resource_type
// ---------------------------------------------------------------------------
func TestHandler_EmptyResourceType(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	payload := []byte(`{
		"target": {"provider": "kubernetes", "resource_type": "", "provider_id": "api"},
		"direction": "incoming",
		"max_depth": 5
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}
	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeValidationError) {
		t.Errorf("expected code %s, got %s", answer.CodeValidationError, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 6: empty provider_id
// ---------------------------------------------------------------------------
func TestHandler_EmptyProviderID(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	payload := []byte(`{
		"target": {"provider": "kubernetes", "resource_type": "pod", "provider_id": " \t "},
		"direction": "incoming",
		"max_depth": 5
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}
	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeValidationError) {
		t.Errorf("expected code %s, got %s", answer.CodeValidationError, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 7: invalid direction
// ---------------------------------------------------------------------------
func TestHandler_InvalidDirection(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	payload := []byte(`{
		"target": {"provider": "kubernetes", "resource_type": "pod", "provider_id": "api"},
		"direction": "sideways",
		"max_depth": 5
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", rr.Code)
	}
	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeValidationError) {
		t.Errorf("expected code %s, got %s", answer.CodeValidationError, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 8: invalid max_depth
// ---------------------------------------------------------------------------
func TestHandler_InvalidMaxDepth(t *testing.T) {
	h := answer.NewHandler(&mockService{})

	// Depth 0 (below min 1)
	payload1 := []byte(`{
		"target": {"provider": "kubernetes", "resource_type": "pod", "provider_id": "api"},
		"direction": "incoming",
		"max_depth": 0
	}`)
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload1))
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for depth 0, got %d", rr1.Code)
	}

	// Depth 51 (above max 50)
	payload2 := []byte(`{
		"target": {"provider": "kubernetes", "resource_type": "pod", "provider_id": "api"},
		"direction": "incoming",
		"max_depth": 51
	}`)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(payload2))
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for depth 51, got %d", rr2.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 9: authentication failure
// ---------------------------------------------------------------------------
func TestHandler_AuthenticationFailure(t *testing.T) {
	authMw := auth.NewAuthMiddleware(nil, nil, logging.NewStandardLogger(nil, logging.LevelDebug), false)
	h := answer.NewHandler(&mockService{})

	// Wrap handler with authentication middleware
	protected := authMw.RequireAuth(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 10: workspace authorization failure
// ---------------------------------------------------------------------------
func TestHandler_WorkspaceAuthorizationFailure(t *testing.T) {
	mem := auth.NewMemoryMembershipProvider()
	mem.AddMember("user-1", "11111111-1111-1111-1111-111111111111", "member")
	authorizer := auth.NewWorkspaceAuthorizer(mem)

	h := answer.NewHandler(&mockService{}, answer.WithAuthorizer(authorizer, nil))

	// Authenticated user-1 tries to access workspace 22222222-2222-2222-2222-222222222222 (which they do not belong to)
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "user-1", Email: "user@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload())).WithContext(ctx)
	req.Header.Set("X-Workspace-ID", "22222222-2222-2222-2222-222222222222")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for unauthorized workspace, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeForbidden) {
		t.Errorf("expected code %s, got %s", answer.CodeForbidden, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 11: core unavailable
// ---------------------------------------------------------------------------
func TestHandler_CoreUnavailable(t *testing.T) {
	svc := &mockService{err: answer.ErrCoreUnavailable}
	h := answer.NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeCoreUnavailable) {
		t.Errorf("expected code %s, got %s", answer.CodeCoreUnavailable, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 12: core timeout
// ---------------------------------------------------------------------------
func TestHandler_CoreTimeout(t *testing.T) {
	svc := &mockService{err: answer.ErrCoreTimeout}
	h := answer.NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeCoreTimeout) {
		t.Errorf("expected code %s, got %s", answer.CodeCoreTimeout, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 13: context cancellation
// ---------------------------------------------------------------------------
func TestHandler_ContextCancellation(t *testing.T) {
	svc := &mockService{err: answer.ErrRequestCanceled}
	h := answer.NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != 499 {
		t.Fatalf("expected 499 Client Closed Request, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeRequestCanceled) {
		t.Errorf("expected code %s, got %s", answer.CodeRequestCanceled, errResp.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 14: malformed core response
// ---------------------------------------------------------------------------
func TestHandler_MalformedCoreResponse(t *testing.T) {
	svc := &mockService{err: answer.ErrMalformedCoreResponse}
	h := answer.NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error, got %d", rr.Code)
	}

	var errResp answer.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != string(answer.CodeInternalError) {
		t.Errorf("expected code %s, got %s", answer.CodeInternalError, errResp.Code)
	}
	if errResp.Error != "Internal translation failure" {
		t.Errorf("unexpected error message: %s", errResp.Error)
	}
}

// ---------------------------------------------------------------------------
// Test 15: successful structured response
// ---------------------------------------------------------------------------
func TestHandler_SuccessfulStructuredResponse(t *testing.T) {
	svc := &mockService{
		ans: &answer.ImpactAnswer{
			Target: answer.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "pod",
				ProviderID:   "payments/payments-api",
			},
			Summary: answer.ImpactSummary{
				ImpactedCount: 1,
				DirectCount:   1,
				IndirectCount: 0,
				MaxDepth:      1,
			},
			ImpactedResources: []answer.ImpactedResource{
				{
					Resource: answer.ResourceIdentity{
						Provider:     "kubernetes",
						ResourceType: "pod",
						ProviderID:   "payments/orders-api",
					},
					Depth: 1,
				},
			},
			Relationships: []answer.AnswerRelationship{
				{
					Relationship: answer.Relationship{
						Source: answer.ResourceIdentity{
							Provider:     "kubernetes",
							ResourceType: "pod",
							ProviderID:   "payments/orders-api",
						},
						Target: answer.ResourceIdentity{
							Provider:     "kubernetes",
							ResourceType: "pod",
							ProviderID:   "payments/payments-api",
						},
						Kind:     "DEPENDS_ON",
						Category: "Dependency",
					},
					State:       "supported",
					EvidenceIDs: []string{"550e8400-e29b-41d4-a716-446655440001"},
				},
			},
			Paths: []answer.ImpactPath{
				{
					Resources: []answer.ResourceIdentity{
						{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/payments-api"},
						{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/orders-api"},
					},
					Relationships: []answer.Relationship{
						{
							Source:   answer.ResourceIdentity{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/orders-api"},
							Target:   answer.ResourceIdentity{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/payments-api"},
							Kind:     "DEPENDS_ON",
							Category: "Dependency",
						},
					},
				},
			},
			Evidence: []answer.AnswerEvidence{
				{
					ID: "550e8400-e29b-41d4-a716-446655440001",
					Source: answer.EvidenceSource{
						Provider:  "kubernetes",
						Collector: "k8s-runtime",
					},
					ObservedAt:      "2026-10-04T12:00:00Z",
					ObservationType: "RUNTIME_CONNECTION",
				},
			},
			ExplanationFacts: []answer.ExplanationFact{
				{
					Path: answer.ImpactPath{
						Resources: []answer.ResourceIdentity{
							{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/payments-api"},
							{Provider: "kubernetes", ResourceType: "pod", ProviderID: "payments/orders-api"},
						},
					},
					EvidenceIDs: []string{"550e8400-e29b-41d4-a716-446655440001"},
				},
			},
		},
	}

	h := answer.NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(defaultValidPayload()))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}

	var res answer.ImpactAnswer
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.Summary.ImpactedCount != 1 {
		t.Errorf("expected impacted count 1, got %d", res.Summary.ImpactedCount)
	}
	if len(res.ImpactedResources) != 1 || res.ImpactedResources[0].Resource.ProviderID != "payments/orders-api" {
		t.Errorf("unexpected impacted resources: %+v", res.ImpactedResources)
	}
	if len(res.Relationships) != 1 || res.Relationships[0].State != "supported" {
		t.Errorf("unexpected relationships: %+v", res.Relationships)
	}
	if len(res.Paths) != 1 || len(res.Paths[0].Resources) != 2 {
		t.Errorf("unexpected paths: %+v", res.Paths)
	}
	if len(res.Evidence) != 1 || res.Evidence[0].ID != "550e8400-e29b-41d4-a716-446655440001" {
		t.Errorf("unexpected evidence: %+v", res.Evidence)
	}
	if len(res.ExplanationFacts) != 1 || len(res.ExplanationFacts[0].EvidenceIDs) != 1 {
		t.Errorf("unexpected facts: %+v", res.ExplanationFacts)
	}
}
