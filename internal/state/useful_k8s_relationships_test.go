package state_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

func newReferenceEvidence(id, sourceProvider, sourceResType, sourceProviderID, refType string, target *corev1.ResourceIdentity, extraData map[string]any, observedAt time.Time) *corev1.Evidence {
	dataMap := map[string]any{
		"reference_type": refType,
	}
	if target != nil {
		dataMap["target"] = target
	}
	for k, v := range extraData {
		dataMap[k] = v
	}
	dataBytes, _ := json.Marshal(dataMap)

	return &corev1.Evidence{
		Id: k8s.DeterministicEvidenceID(id),
		Source: &corev1.EvidenceSource{
			Provider:  sourceProvider,
			Collector: "k8s-collector",
		},
		ObservedAt:      observedAt.Format(time.RFC3339Nano),
		ObservationType: k8s.ObservationResourceReference,
		Subject: &corev1.ResourceIdentity{
			Provider:     sourceProvider,
			ResourceType: sourceResType,
			ProviderId:   sourceProviderID,
		},
		Data: dataBytes,
	}
}

func newOwnershipEvidence(id, childProvider, childResType, childProviderID string, owner *corev1.ResourceIdentity, isController bool, observedAt time.Time) *corev1.Evidence {
	dataMap := map[string]any{
		"controller": isController,
	}
	if owner != nil {
		dataMap["owner"] = owner
	}
	dataBytes, _ := json.Marshal(dataMap)

	return &corev1.Evidence{
		Id: k8s.DeterministicEvidenceID(id),
		Source: &corev1.EvidenceSource{
			Provider:  childProvider,
			Collector: "k8s-collector",
		},
		ObservedAt:      observedAt.Format(time.RFC3339Nano),
		ObservationType: k8s.ObservationOwnershipReference,
		Subject: &corev1.ResourceIdentity{
			Provider:     childProvider,
			ResourceType: childResType,
			ProviderId:   childProviderID,
		},
		Data: dataBytes,
	}
}

// ---------------------------------------------------------------------------
// 1. Focused Tests for Every Required Kubernetes Relationship Rule
// ---------------------------------------------------------------------------

func TestKubernetesRelationships_PodToConfigMap(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)
	reconciler := state.NewReconciler(store, client, nil, logger)

	ws := "ws-cm-test"
	t0 := time.Now().UTC()

	targetCM := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "config_map",
		ProviderId:   "prod/app-config",
	}

	// 1. Valid reference -> Discovered
	evValid := newReferenceEvidence("ev-cm-1", "kubernetes", "pod", "prod/web-pod-1", "config_map_ref", targetCM, nil, t0)
	res1, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res1.DiscoveredCount != 1 || res1.RelationshipsCreated != 1 {
		t.Fatalf("expected 1 relationship discovered/created, got disc=%d, created=%d",
			res1.DiscoveredCount, res1.RelationshipsCreated)
	}

	rels, err := store.ListRelationships(ctx, ws)
	if err != nil || len(rels) != 1 {
		t.Fatalf("expected 1 stored relationship, got %d (%v)", len(rels), err)
	}
	rel := rels[0]
	if rel.Kind != "DEPENDS_ON" {
		t.Errorf("expected kind DEPENDS_ON, got %s", rel.Kind)
	}
	if rel.Category != "Dependency" {
		t.Errorf("expected category Dependency, got %s", rel.Category)
	}
	if rel.Source.ProviderID != "prod/web-pod-1" || rel.Target.ProviderID != "prod/app-config" {
		t.Errorf("unexpected endpoints: %s -> %s", rel.Source.ProviderID, rel.Target.ProviderID)
	}

	// Verify provenance
	prov, err := store.GetProvenanceForRelationship(ctx, ws, rel.Key())
	if err != nil || len(prov) != 1 || prov[0] != k8s.DeterministicEvidenceID("ev-cm-1") {
		t.Errorf("expected provenance [ev-cm-1], got %v", prov)
	}

	// 2. Repeated observation / idempotency: preserves FirstObservedAt, advances LastObservedAt
	t1 := t0.Add(5 * time.Minute)
	evRepeat := newReferenceEvidence("ev-cm-2", "kubernetes", "pod", "prod/web-pod-1", "config_map_ref", targetCM, nil, t1)
	res2, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evRepeat},
	})
	if err != nil {
		t.Fatalf("repeat Reconcile failed: %v", err)
	}
	if res2.RelationshipsUpdated != 1 || res2.RelationshipsCreated != 0 {
		t.Errorf("expected 1 updated, 0 created, got updated=%d, created=%d", res2.RelationshipsUpdated, res2.RelationshipsCreated)
	}
	updatedRels, _ := store.ListRelationships(ctx, ws)
	if len(updatedRels) != 1 {
		t.Fatalf("expected still 1 relationship, got %d", len(updatedRels))
	}
	if !updatedRels[0].FirstObservedAt.Equal(rel.FirstObservedAt) {
		t.Errorf("FirstObservedAt changed on repeated observation")
	}

	// Provenance should now have both evidence IDs without duplication
	prov2, _ := store.GetProvenanceForRelationship(ctx, ws, rel.Key())
	if len(prov2) != 2 {
		t.Errorf("expected 2 provenance records, got %d (%v)", len(prov2), prov2)
	}

	// 3. Missing target -> Insufficient
	evMissing := newReferenceEvidence("ev-cm-missing", "kubernetes", "pod", "prod/web-pod-2", "config_map_ref", nil, nil, t0)
	resMissing, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evMissing},
	})
	if err != nil {
		t.Fatalf("missing target Reconcile failed: %v", err)
	}
	if resMissing.InsufficientCount != 1 || resMissing.DiscoveredCount != 0 {
		t.Errorf("expected InsufficientCount=1, DiscoveredCount=0, got insufficient=%d, disc=%d",
			resMissing.InsufficientCount, resMissing.DiscoveredCount)
	}

	// 4. Malformed target -> Invalid
	dataMalformed, _ := json.Marshal(map[string]any{
		"reference_type": "config_map_ref",
		"target": map[string]any{
			"provider": "kubernetes",
			// missing resource_type and provider_id
		},
	})
	evMalformed := &corev1.Evidence{
		Id: k8s.DeterministicEvidenceID("ev-cm-malformed"),
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-collector",
		},
		ObservedAt:      t0.Format(time.RFC3339Nano),
		ObservationType: k8s.ObservationResourceReference,
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "prod/web-pod-3",
		},
		Data: dataMalformed,
	}
	resMalformed, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evMalformed},
	})
	if err != nil {
		t.Fatalf("malformed target Reconcile failed: %v", err)
	}
	if resMalformed.InvalidCount != 1 || resMalformed.DiscoveredCount != 0 {
		t.Errorf("expected InvalidCount=1, DiscoveredCount=0, got invalid=%d, disc=%d",
			resMalformed.InvalidCount, resMalformed.DiscoveredCount)
	}

	// 5. Workspace isolation: wsB must have 0 relationships
	relsB, _ := store.ListRelationships(ctx, "ws-other")
	if len(relsB) != 0 {
		t.Errorf("expected 0 relationships in isolated workspace ws-other, got %d", len(relsB))
	}
}

func TestKubernetesRelationships_PodToSecret(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-sec-test"
	t0 := time.Now().UTC()

	targetSec := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "secret",
		ProviderId:   "prod/db-credentials",
	}

	evValid := newReferenceEvidence("ev-sec-1", "kubernetes", "pod", "prod/api-pod", "secret_ref", targetSec, nil, t0)
	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Kind != "DEPENDS_ON" || rels[0].Category != "Dependency" {
		t.Errorf("expected DEPENDS_ON (Dependency), got %s (%s)", rels[0].Kind, rels[0].Category)
	}
	if rels[0].Target.ProviderID != "prod/db-credentials" {
		t.Errorf("unexpected target provider ID: %s", rels[0].Target.ProviderID)
	}
}

func TestKubernetesRelationships_PodToPVC(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-pvc-test"
	t0 := time.Now().UTC()

	targetPVC := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "persistent_volume_claim",
		ProviderId:   "prod/data-volume-claim",
	}

	evValid := newReferenceEvidence("ev-pvc-1", "kubernetes", "pod", "prod/db-pod", "pvc_mount_ref", targetPVC, nil, t0)
	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Kind != "DEPENDS_ON" || rels[0].Category != "Dependency" {
		t.Errorf("expected DEPENDS_ON (Dependency), got %s (%s)", rels[0].Kind, rels[0].Category)
	}
}

func TestKubernetesRelationships_PodToServiceAccount(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-sa-test"
	t0 := time.Now().UTC()

	targetSA := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service_account",
		ProviderId:   "prod/workload-identity-sa",
	}

	evValid := newReferenceEvidence("ev-sa-1", "kubernetes", "pod", "prod/worker-pod", "service_account_ref", targetSA, nil, t0)
	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Kind != "DEPENDS_ON" || rels[0].Category != "Dependency" {
		t.Errorf("expected DEPENDS_ON (Dependency), got %s (%s)", rels[0].Kind, rels[0].Category)
	}
}

func TestKubernetesRelationships_IngressToService(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-ing-test"
	t0 := time.Now().UTC()

	targetSvc := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderId:   "prod/frontend-svc",
	}

	evValid := newReferenceEvidence("ev-ing-1", "kubernetes", "ingress", "prod/frontend-ing", "ingress_backend_ref", targetSvc, nil, t0)
	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Kind != "DEPENDS_ON" || rels[0].Category != "Dependency" {
		t.Errorf("expected DEPENDS_ON (Dependency), got %s (%s)", rels[0].Kind, rels[0].Category)
	}
	if rels[0].Source.ResourceType != "ingress" || rels[0].Target.ResourceType != "service" {
		t.Errorf("unexpected endpoints: %s -> %s", rels[0].Source, rels[0].Target)
	}
}

func TestKubernetesRelationships_PodToNode(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-node-test"
	t0 := time.Now().UTC()

	targetNode := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "node",
		ProviderId:   "node-worker-pool-1",
	}

	evValid := newReferenceEvidence("ev-node-1", "kubernetes", "pod", "prod/checkout-pod", "pod_scheduled_node", targetNode, nil, t0)
	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evValid},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Kind != "DEPENDS_ON" || rels[0].Category != "Dependency" {
		t.Errorf("expected DEPENDS_ON (Dependency), got %s (%s)", rels[0].Kind, rels[0].Category)
	}
}

func TestKubernetesRelationships_Ownership(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-owner-test"
	t0 := time.Now().UTC()

	depOwner := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "deployment",
		ProviderId:   "prod/payments-deploy",
	}
	rsOwner := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "replica_set",
		ProviderId:   "prod/payments-rs-123",
	}

	// 1. ReplicaSet owned by Deployment
	evRS := newOwnershipEvidence("ev-own-rs", "kubernetes", "replica_set", "prod/payments-rs-123", depOwner, true, t0)
	// 2. Pod owned by ReplicaSet
	evPod := newOwnershipEvidence("ev-own-pod", "kubernetes", "pod", "prod/payments-pod-abc", rsOwner, true, t0)

	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evRS, evPod},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Direct: Deployment -> RS, RS -> Pod
	// Transitive: Deployment -> Pod
	if res.DiscoveredCount != 3 {
		t.Fatalf("expected 3 ownership relationships (direct + transitive), got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 3 {
		t.Fatalf("expected 3 stored relationships, got %d", len(rels))
	}

	var foundDeployOwnsRS, foundRSOwnsPod, foundDeployOwnsPod bool
	for _, r := range rels {
		if r.Kind != "OWNS" || r.Category != "Ownership" {
			t.Errorf("expected OWNS (Ownership), got %s (%s)", r.Kind, r.Category)
		}
		if r.Source.ProviderID == "prod/payments-deploy" && r.Target.ProviderID == "prod/payments-rs-123" {
			foundDeployOwnsRS = true
		}
		if r.Source.ProviderID == "prod/payments-rs-123" && r.Target.ProviderID == "prod/payments-pod-abc" {
			foundRSOwnsPod = true
		}
		if r.Source.ProviderID == "prod/payments-deploy" && r.Target.ProviderID == "prod/payments-pod-abc" {
			foundDeployOwnsPod = true
		}
	}

	if !foundDeployOwnsRS || !foundRSOwnsPod || !foundDeployOwnsPod {
		t.Errorf("missing expected ownership relationships: deploy->rs=%v, rs->pod=%v, deploy->pod=%v",
			foundDeployOwnsRS, foundRSOwnsPod, foundDeployOwnsPod)
	}
}

// ---------------------------------------------------------------------------
// 2. Focused Negative Tests
// ---------------------------------------------------------------------------

func TestNegative_ServiceSelectorNeverCreatesCallsOrDependency(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-neg-selector"
	t0 := time.Now().UTC()

	// Service with selector
	svcData, _ := json.Marshal(map[string]any{
		"type":     "ClusterIP",
		"selector": map[string]string{"app": "checkout"},
	})
	evSvc := &corev1.Evidence{
		Id: k8s.DeterministicEvidenceID("ev-svc-cfg"),
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-collector",
		},
		ObservedAt:      t0.Format(time.RFC3339Nano),
		ObservationType: k8s.ObservationConfiguration,
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderId:   "prod/checkout-svc",
		},
		Data: svcData,
	}

	// Pod with matching labels
	podData, _ := json.Marshal(map[string]any{
		"labels": map[string]string{"app": "checkout"},
	})
	evPod := &corev1.Evidence{
		Id: k8s.DeterministicEvidenceID("ev-pod-cfg"),
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-collector",
		},
		ObservedAt:      t0.Format(time.RFC3339Nano),
		ObservationType: k8s.ObservationConfiguration,
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "prod/checkout-pod",
		},
		Data: podData,
	}

	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evSvc, evPod},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// INVARIANT: zero relationships must be created!
	if res.DiscoveredCount != 0 || res.RelationshipsCreated != 0 {
		t.Fatalf("CRITICAL INVARIANT VIOLATION: Service selector created relationship! disc=%d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	if len(rels) != 0 {
		t.Fatalf("expected 0 relationships in store, got %d", len(rels))
	}
}

func TestNegative_NoRuntimeRelationshipWithoutRuntimeEvidence(t *testing.T) {
	client := startProdServer(t)
	ctx := context.Background()
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, client, nil, nil)

	ws := "ws-neg-runtime"
	t0 := time.Now().UTC()

	// Only declarative infrastructure references (e.g. Ingress -> Service)
	targetSvc := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderId:   "prod/api-svc",
	}
	evIng := newReferenceEvidence("ev-ing-1", "kubernetes", "ingress", "prod/api-ing", "ingress_backend_ref", targetSvc, nil, t0)

	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{evIng},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.DiscoveredCount != 1 {
		t.Fatalf("expected 1 discovered relationship, got %d", res.DiscoveredCount)
	}

	rels, _ := store.ListRelationships(ctx, ws)
	for _, r := range rels {
		if strings.EqualFold(r.Kind, "CALLS") {
			t.Fatalf("CRITICAL INVARIANT VIOLATION: CALLS relationship discovered without RUNTIME_CONNECTION evidence")
		}
	}
}
