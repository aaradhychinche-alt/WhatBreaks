package corev1_test

import (
	"testing"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"google.golang.org/protobuf/proto"
)

func TestResourceIdentityRoundTrip(t *testing.T) {
	orig := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   "cluster/prod/namespace/payments/pod/api-abc",
	}

	data, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("failed to marshal ResourceIdentity: %v", err)
	}

	decoded := &corev1.ResourceIdentity{}
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatalf("failed to unmarshal ResourceIdentity: %v", err)
	}

	if decoded.GetProvider() != "kubernetes" {
		t.Errorf("expected provider 'kubernetes', got %q", decoded.GetProvider())
	}
	if decoded.GetResourceType() != "pod" {
		t.Errorf("expected resource_type 'pod', got %q", decoded.GetResourceType())
	}
	if decoded.GetProviderId() != "cluster/prod/namespace/payments/pod/api-abc" {
		t.Errorf("expected provider_id match, got %q", decoded.GetProviderId())
	}
}

func TestEvidenceRoundTrip(t *testing.T) {
	orig := &corev1.Evidence{
		Id: "550e8400-e29b-41d4-a716-446655440000",
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-runtime",
		},
		ObservedAt:      "2026-10-04T12:00:00Z",
		ObservationType: "RUNTIME_CONNECTION",
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "default/frontend-xyz",
		},
		Data: []byte(`{"remote_ip":"10.0.0.1","remote_port":5432}`),
	}

	data, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("failed to marshal Evidence: %v", err)
	}

	decoded := &corev1.Evidence{}
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatalf("failed to unmarshal Evidence: %v", err)
	}

	if decoded.GetId() != orig.GetId() {
		t.Errorf("id mismatch: got %q, want %q", decoded.GetId(), orig.GetId())
	}
	if decoded.GetSource().GetCollector() != "k8s-runtime" {
		t.Errorf("collector mismatch: got %q", decoded.GetSource().GetCollector())
	}
	if string(decoded.GetData()) != `{"remote_ip":"10.0.0.1","remote_port":5432}` {
		t.Errorf("data mismatch: got %s", string(decoded.GetData()))
	}
}

func TestDiscoveryResultOneof(t *testing.T) {
	// Test Discovered variant
	discovered := &corev1.DiscoveryResult{
		Outcome: &corev1.DiscoveryResult_Discovered{
			Discovered: &corev1.DiscoveredRelationship{
				Relationship: &corev1.Relationship{
					Source: &corev1.ResourceIdentity{
						Provider:     "kubernetes",
						ResourceType: "pod",
						ProviderId:   "default/web",
					},
					Target: &corev1.ResourceIdentity{
						Provider:     "aws",
						ResourceType: "rds",
						ProviderId:   "arn:aws:rds:us-east-1:123:db:main",
					},
					Kind:     "DEPENDS_ON",
					Category: "Dependency",
				},
				SupportingEvidenceIds: []string{"ev-1", "ev-2"},
			},
		},
	}

	data, err := proto.Marshal(discovered)
	if err != nil {
		t.Fatalf("failed to marshal DiscoveryResult: %v", err)
	}

	decoded := &corev1.DiscoveryResult{}
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatalf("failed to unmarshal DiscoveryResult: %v", err)
	}

	res := decoded.GetDiscovered()
	if res == nil {
		t.Fatalf("expected Discovered outcome, got nil")
	}
	if res.GetRelationship().GetKind() != "DEPENDS_ON" {
		t.Errorf("expected kind DEPENDS_ON, got %q", res.GetRelationship().GetKind())
	}
	if len(res.GetSupportingEvidenceIds()) != 2 {
		t.Errorf("expected 2 supporting evidence ids, got %d", len(res.GetSupportingEvidenceIds()))
	}

	// Test Conflict variant
	conflict := &corev1.DiscoveryResult{
		Outcome: &corev1.DiscoveryResult_Conflict{
			Conflict: &corev1.Conflict{
				Description: "Ambiguous mapping between resources",
			},
		},
	}

	cData, err := proto.Marshal(conflict)
	if err != nil {
		t.Fatalf("failed to marshal conflict: %v", err)
	}

	cDecoded := &corev1.DiscoveryResult{}
	if err := proto.Unmarshal(cData, cDecoded); err != nil {
		t.Fatalf("failed to unmarshal conflict: %v", err)
	}

	if cDecoded.GetConflict() == nil {
		t.Fatalf("expected Conflict outcome, got nil")
	}
	if cDecoded.GetConflict().GetDescription() != "Ambiguous mapping between resources" {
		t.Errorf("unexpected description: %q", cDecoded.GetConflict().GetDescription())
	}
}

func TestRunDiscoveryRequestResponse(t *testing.T) {
	req := &corev1.RunDiscoveryRequest{
		Evidence: []*corev1.Evidence{
			{
				Id:              "ev-1",
				ObservedAt:      "2026-10-04T12:00:00Z",
				ObservationType: "TEST",
			},
		},
	}

	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal RunDiscoveryRequest: %v", err)
	}

	decodedReq := &corev1.RunDiscoveryRequest{}
	if err := proto.Unmarshal(data, decodedReq); err != nil {
		t.Fatalf("failed to unmarshal RunDiscoveryRequest: %v", err)
	}

	if len(decodedReq.GetEvidence()) != 1 {
		t.Fatalf("expected 1 evidence item, got %d", len(decodedReq.GetEvidence()))
	}

	resp := &corev1.RunDiscoveryResponse{
		Results: []*corev1.DiscoveryResult{
			{
				Outcome: &corev1.DiscoveryResult_Insufficient{
					Insufficient: &corev1.Insufficient{},
				},
			},
		},
	}

	respData, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal RunDiscoveryResponse: %v", err)
	}

	decodedResp := &corev1.RunDiscoveryResponse{}
	if err := proto.Unmarshal(respData, decodedResp); err != nil {
		t.Fatalf("failed to unmarshal RunDiscoveryResponse: %v", err)
	}

	if len(decodedResp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(decodedResp.GetResults()))
	}
	if decodedResp.GetResults()[0].GetInsufficient() == nil {
		t.Errorf("expected Insufficient outcome, got %v", decodedResp.GetResults()[0].GetOutcome())
	}
}

// Compile-time interface checks for gRPC client and server stubs
var (
	_ corev1.DiscoveryServiceServer = (*corev1.UnimplementedDiscoveryServiceServer)(nil)
	_ corev1.AnswerServiceServer    = (*corev1.UnimplementedAnswerServiceServer)(nil)
)

func TestDiscoveryServiceClientInterface(t *testing.T) {
	var client corev1.DiscoveryServiceClient = corev1.NewDiscoveryServiceClient(nil)
	if client == nil {
		t.Fatal("expected non-nil client wrapper")
	}
}

func TestAnswerServiceClientInterface(t *testing.T) {
	var client corev1.AnswerServiceClient = corev1.NewAnswerServiceClient(nil)
	if client == nil {
		t.Fatal("expected non-nil client wrapper")
	}
}
