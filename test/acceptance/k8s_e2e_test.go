package acceptance_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

func getTestDatabaseConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()

	host := os.Getenv("TEST_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := 5432
	if portStr := os.Getenv("TEST_DB_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			port = p
		}
	}

	timeout := 200 * time.Millisecond
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		t.Skipf("PostgreSQL is not reachable at %s:%d (%v); skipping test", host, port, err)
	}
	_ = conn.Close()

	user := os.Getenv("TEST_DB_USER")
	if user == "" {
		user = "postgres"
	}
	dbName := os.Getenv("TEST_DB_NAME")
	if dbName == "" {
		dbName = "postgres"
	}
	password := os.Getenv("TEST_DB_PASSWORD")

	return &config.DatabaseConfig{
		Host:              host,
		Port:              port,
		Database:          dbName,
		User:              user,
		Password:          password,
		PoolMax:           5,
		PoolMin:           1,
		PoolIdleTimeout:   10 * time.Second,
		ConnectionTimeout: 2 * time.Second,
	}
}

// startKubectlProxy launches a local kubectl proxy subprocess and returns the base HTTP URL.
func startKubectlProxy(t *testing.T) (string, func()) {
	t.Helper()

	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skipf("kubectl CLI not found: %v", err)
	}

	// Verify cluster connectivity before launching proxy
	checkCmd := exec.Command("kubectl", "cluster-info")
	if out, err := checkCmd.CombinedOutput(); err != nil {
		t.Skipf("Kubernetes cluster is not reachable via kubectl: %v (out: %s)", err, string(out))
	}

	cmd := exec.Command("kubectl", "proxy", "--port=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe for kubectl proxy: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start kubectl proxy: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	var proxyURL string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Starting to serve on") {
			parts := strings.Split(line, "Starting to serve on ")
			if len(parts) == 2 {
				proxyURL = "http://" + strings.TrimSpace(parts[1])
				break
			}
		}
	}

	if proxyURL == "" {
		_ = cmd.Process.Kill()
		t.Fatalf("failed to parse kubectl proxy port from stdout")
	}

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			time.Sleep(50 * time.Millisecond)
			_ = cmd.Process.Kill()
		}
	}

	// Verify readyz
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(proxyURL + "/readyz")
	if err != nil || resp.StatusCode != http.StatusOK {
		cleanup()
		t.Fatalf("kubectl proxy failed readyz check: %v", err)
	}
	_ = resp.Body.Close()

	return proxyURL, cleanup
}

// deployDeterministicWorkload deploys backend and frontend workloads into the specified namespace.
func deployDeterministicWorkload(t *testing.T, namespace string) func() {
	t.Helper()

	manifest := `
apiVersion: v1
kind: Namespace
metadata:
  name: ` + namespace + `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend
  namespace: ` + namespace + `
  labels:
    app: backend
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
  template:
    metadata:
      labels:
        app: backend
    spec:
      containers:
      - name: backend
        image: nginx:alpine
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: backend
  namespace: ` + namespace + `
  labels:
    app: backend
spec:
  type: ClusterIP
  selector:
    app: backend
  ports:
  - name: http
    port: 8080
    targetPort: 8080
    protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frontend
  namespace: ` + namespace + `
  labels:
    app: frontend
spec:
  replicas: 1
  selector:
    matchLabels:
      app: frontend
  template:
    metadata:
      labels:
        app: frontend
    spec:
      containers:
      - name: frontend
        image: nginx:alpine
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: frontend
  namespace: ` + namespace + `
  labels:
    app: frontend
spec:
  type: ClusterIP
  selector:
    app: frontend
  ports:
  - name: http
    port: 80
    targetPort: 80
    protocol: TCP
`

	// If namespace is currently Terminating from a prior run, wait for it to be removed
	for i := 0; i < 40; i++ {
		checkCmd := exec.Command("kubectl", "get", "namespace", namespace, "-o", "jsonpath={.status.phase}")
		out, err := checkCmd.Output()
		if err != nil || string(out) != "Terminating" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	applyCmd := exec.Command("kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(manifest)
	out, err := applyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to apply test workload: %v (out: %s)", err, string(out))
	}

	// Wait up to 15s for pods to be created and running
	deadline := time.Now().Add(15 * time.Second)
	podsReady := false
	for time.Now().Before(deadline) {
		getCmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-o", "jsonpath={.items[*].status.phase}")
		out, err := getCmd.Output()
		if err == nil && strings.Contains(string(out), "Running") {
			podsReady = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !podsReady {
		t.Logf("warning: pods in namespace %s did not reach Running within deadline (still proceeding with API collection)", namespace)
	}

	cleanup := func() {
		delCmd := exec.Command("kubectl", "delete", "namespace", namespace, "--wait=true", "--timeout=20s")
		_ = delCmd.Run()
	}

	return cleanup
}

// ---------------------------------------------------------------------------
// Real End-to-End Test: Real K8s -> Collector -> State -> Postgres -> Rust -> Impact
// ---------------------------------------------------------------------------
func TestKubernetes_RealEndToEndFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	// 1. Start kubectl proxy connected to real Kubernetes cluster
	proxyURL, proxyCleanup := startKubectlProxy(t)
	defer proxyCleanup()

	// 2. Deploy small deterministic workload into dedicated namespace wb-e2e
	testNamespace := "wb-e2e"
	workloadCleanup := deployDeterministicWorkload(t, testNamespace)
	defer workloadCleanup()

	// 3. Connect to real PostgreSQL server
	dbCfg := getTestDatabaseConfig(t)
	db, err := database.New(ctx, dbCfg, database.WithLogger(logger))
	if err != nil {
		t.Fatalf("failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	if err := state.EnsureSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}
	pgStore := state.NewPostgresStore(db)

	workspaceID := "ws-k8s-real-e2e"
	clusterID := "k3d-cluster"

	// Clean any previous test run
	_ = pgStore.DeleteWorkspaceState(ctx, workspaceID)
	defer func() {
		_ = pgStore.DeleteWorkspaceState(context.Background(), workspaceID)
	}()

	// 4. Start live Rust Core Server
	addr, err := allocateFreeAddr()
	if err != nil {
		t.Fatalf("failed to allocate free address: %v", err)
	}
	server := startManagedServer(t, addr)
	defer server.Stop()

	coreClient, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect to Rust core engine: %v", err)
	}
	defer coreClient.Close()

	// 5. Instantiate real Kubernetes Collector
	restClient, err := k8s.NewRESTClient(k8s.ClientConfig{
		BaseURL: proxyURL,
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("failed to create RESTClient: %v", err)
	}

	collector, err := k8s.New(
		k8s.WithClusterID(clusterID),
		k8s.WithWorkspaceID(workspaceID),
		k8s.WithNamespaces(testNamespace),
		k8s.WithClient(restClient),
		k8s.WithCoreClient(coreClient),
		k8s.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to create Collector: %v", err)
	}

	// 6. Run Collection from the real Kubernetes API
	t.Logf("Collecting real Kubernetes resources from namespace %s via %s", testNamespace, proxyURL)
	evidenceList, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("collector.Collect failed: %v", err)
	}

	if len(evidenceList) == 0 {
		t.Fatalf("expected non-empty evidence from Kubernetes API, got 0")
	}
	t.Logf("Collected %d real evidence observations from Kubernetes API", len(evidenceList))

	// Verify real resources are represented in the observations
	observedDeployments := make(map[string]bool)
	observedServices := make(map[string]bool)
	observedPods := make(map[string]bool)

	for _, ev := range evidenceList {
		if ev.Subject == nil {
			continue
		}
		if ev.Subject.Provider != "kubernetes" {
			t.Errorf("expected provider 'kubernetes', got %q", ev.Subject.Provider)
		}
		switch ev.Subject.ResourceType {
		case "deployment":
			observedDeployments[ev.Subject.ProviderId] = true
		case "service":
			observedServices[ev.Subject.ProviderId] = true
		case "pod":
			observedPods[ev.Subject.ProviderId] = true
		}
	}

	// Assert that backend and frontend were genuinely observed from Kubernetes
	expectedBackendDeploy := fmt.Sprintf("%s/%s/backend", clusterID, testNamespace)
	expectedFrontendDeploy := fmt.Sprintf("%s/%s/frontend", clusterID, testNamespace)
	if !observedDeployments[expectedBackendDeploy] {
		t.Errorf("expected deployment %s to be observed by collector", expectedBackendDeploy)
	}
	if !observedDeployments[expectedFrontendDeploy] {
		t.Errorf("expected deployment %s to be observed by collector", expectedFrontendDeploy)
	}

	expectedBackendSvc := fmt.Sprintf("%s/%s/backend", clusterID, testNamespace)
	expectedFrontendSvc := fmt.Sprintf("%s/%s/frontend", clusterID, testNamespace)
	if !observedServices[expectedBackendSvc] {
		t.Errorf("expected service %s to be observed by collector", expectedBackendSvc)
	}
	if !observedServices[expectedFrontendSvc] {
		t.Errorf("expected service %s to be observed by collector", expectedFrontendSvc)
	}

	if len(observedPods) < 2 {
		t.Errorf("expected at least 2 pods to be observed, got %d (%v)", len(observedPods), observedPods)
	}

	// 7. State Assembly into real PostgreSQL
	materializer := state.NewMaterializer(pgStore, coreClient, logger)
	assembler := state.NewStateAssembler(pgStore, coreClient, materializer, logger)

	batch := state.ObservationBatch{
		WorkspaceID: workspaceID,
		Evidence:    evidenceList,
	}

	ingestResult, err := assembler.IngestAndMaterialize(ctx, batch)
	if err != nil {
		t.Fatalf("assembler.IngestAndMaterialize failed: %v", err)
	}

	t.Logf("Ingestion Result: evidence=%d, registered_resources=%d, discovered=%d, conflicts=%d",
		ingestResult.EvidenceCount, ingestResult.ResourcesRegistered,
		ingestResult.DiscoveredCount, ingestResult.ConflictCount)

	// 8. Verify Persistence in PostgreSQL
	// Query state_resources
	persistedResources, err := pgStore.ListResources(ctx, workspaceID)
	if err != nil {
		t.Fatalf("pgStore.ListResources failed: %v", err)
	}
	if len(persistedResources) == 0 {
		t.Fatalf("expected resources to be persisted in PostgreSQL, got 0")
	}

	persistedResourceMap := make(map[string]state.Resource)
	for _, r := range persistedResources {
		persistedResourceMap[r.Identity.ProviderID] = r
	}

	if _, ok := persistedResourceMap[expectedBackendSvc]; !ok {
		t.Errorf("expected backend service %s to be in PostgreSQL state_resources", expectedBackendSvc)
	}
	if _, ok := persistedResourceMap[expectedFrontendDeploy]; !ok {
		t.Errorf("expected frontend deployment %s to be in PostgreSQL state_resources", expectedFrontendDeploy)
	}

	// Query state_evidence in PostgreSQL
	persistedEvidence, err := pgStore.ListEvidence(ctx, workspaceID)
	if err != nil {
		t.Fatalf("pgStore.ListEvidence failed: %v", err)
	}
	if len(persistedEvidence) != len(evidenceList) {
		t.Errorf("expected %d evidence records in PostgreSQL, got %d", len(evidenceList), len(persistedEvidence))
	}

	// 9. Relationship Discovery & PostgreSQL Provenance Verification
	// Control-plane evidence (OWNERSHIP_REFERENCE) establishes legitimate ownership relationships:
	// Deployment -> ReplicaSet -> Pod (direct and transitive ownership chains).
	if ingestResult.DiscoveredCount == 0 {
		t.Fatalf("expected control-plane relationships to be discovered, got 0")
	}
	t.Logf("Discovery Engine v2 successfully discovered %d relationships from control-plane evidence",
		ingestResult.DiscoveredCount)

	persistedRelationships, err := pgStore.ListRelationships(ctx, workspaceID)
	if err != nil {
		t.Fatalf("pgStore.ListRelationships failed: %v", err)
	}
	if len(persistedRelationships) == 0 {
		t.Fatalf("expected relationships to be persisted in PostgreSQL, got 0")
	}

	persistedEvidenceMap := make(map[string]state.Evidence)
	for _, ev := range persistedEvidence {
		persistedEvidenceMap[ev.ID] = ev
	}

	// Verify specific ownership & dependency relationships and ensure full provenance
	var foundBackendDeployOwnsPod, foundFrontendDeployOwnsPod bool
	var foundBackendPodDependsOnNode, foundFrontendPodDependsOnNode bool
	var backendPodIdentity, frontendPodIdentity, targetNodeIdentity state.ResourceIdentity
	var foundAnyCallsRelationship bool

	for _, rel := range persistedRelationships {
		t.Logf("Persisted relationship: %s -[%s]-> %s (category=%s)",
			rel.Source.ProviderID, rel.Kind, rel.Target.ProviderID, rel.Category)

		// Rule: Never claim CALLS without runtime connection evidence
		if rel.Kind == "CALLS" || rel.Category == "NETWORK" {
			foundAnyCallsRelationship = true
		}

		// Verify provenance: every relationship MUST be backed by existing evidence IDs
		evIDs, err := pgStore.GetProvenanceForRelationship(ctx, workspaceID, rel.Key())
		if err != nil {
			t.Fatalf("failed to query provenance for %s: %v", rel.Key(), err)
		}
		if len(evIDs) == 0 {
			t.Fatalf("provenance violation: relationship %s has 0 supporting evidence IDs", rel.Key())
		}
		for _, evID := range evIDs {
			if _, ok := persistedEvidenceMap[evID]; !ok {
				t.Fatalf("provenance violation: evidence ID %s supporting %s does not exist in state_evidence", evID, rel.Key())
			}
		}

		// Check for Deployment -> Pod ownership
		if rel.Kind == "OWNS" && rel.Source.ResourceType == "deployment" && rel.Target.ResourceType == "pod" {
			if rel.Source.ProviderID == expectedBackendDeploy {
				foundBackendDeployOwnsPod = true
				t.Logf("Verified ownership: %s OWNS %s (supported by %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
			if rel.Source.ProviderID == expectedFrontendDeploy {
				foundFrontendDeployOwnsPod = true
				t.Logf("Verified ownership: %s OWNS %s (supported by %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}

		// Check for Pod -> Node DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "pod" && rel.Target.ResourceType == "node" {
			if strings.Contains(rel.Source.ProviderID, "/backend-") {
				foundBackendPodDependsOnNode = true
				backendPodIdentity = rel.Source
				targetNodeIdentity = rel.Target
				t.Logf("Verified dependency: %s DEPENDS_ON %s (supported by %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
			if strings.Contains(rel.Source.ProviderID, "/frontend-") {
				foundFrontendPodDependsOnNode = true
				frontendPodIdentity = rel.Source
				targetNodeIdentity = rel.Target
				t.Logf("Verified dependency: %s DEPENDS_ON %s (supported by %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}
	}

	if foundAnyCallsRelationship {
		t.Fatalf("architectural violation: discovered CALLS relationship without runtime network telemetry")
	}
	if !foundBackendDeployOwnsPod {
		t.Errorf("expected backend deployment to own backend pod in discovered relationships")
	}
	if !foundFrontendDeployOwnsPod {
		t.Errorf("expected frontend deployment to own frontend pod in discovered relationships")
	}
	if !foundBackendPodDependsOnNode {
		t.Fatalf("expected backend pod to depend on node in discovered relationships")
	}
	if !foundFrontendPodDependsOnNode {
		t.Fatalf("expected frontend pod to depend on node in discovered relationships")
	}
	if targetNodeIdentity.ProviderID == "" {
		t.Fatalf("expected target node identity to be populated from discovered DEPENDS_ON relationships")
	}

	// 10. Materialize into Rust Core Process
	matResp, err := materializer.Materialize(ctx, workspaceID)
	if err != nil {
		t.Fatalf("materializer.Materialize failed: %v", err)
	}
	if matResp.WorkspaceId != workspaceID {
		t.Errorf("expected workspace ID %q, got %q", workspaceID, matResp.WorkspaceId)
	}
	if int(matResp.EvidenceLoaded) != len(evidenceList) {
		t.Errorf("expected %d evidence loaded into Rust Core, got %d", len(evidenceList), matResp.EvidenceLoaded)
	}
	if int(matResp.RelationshipsLoaded) < len(persistedRelationships) {
		t.Errorf("expected at least %d relationships loaded into Rust Core, got %d",
			len(persistedRelationships), matResp.RelationshipsLoaded)
	}

	// 11. Run Real Impact Query ("What Breaks?") on the collected state
	answerSvc := answer.NewService(coreClient, logger)

	// A. Query impact on the real Kubernetes Node (Incoming traversal along DEPENDS_ON)
	// Because Pod DEPENDS_ON Node, incoming traversal from Node discovers upstream dependent Pods.
	// Since DEPENDS_ON propagates impact, this produces a real, non-zero blast radius!
	impactTargetNode := &answer.ResourceIdentity{
		Provider:     targetNodeIdentity.Provider,
		ResourceType: targetNodeIdentity.ResourceType,
		ProviderID:   targetNodeIdentity.ProviderID,
	}

	ansNode, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetNode,
		Direction:   "incoming",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on Node failed: %v", err)
	}
	if ansNode.Target.ProviderID != targetNodeIdentity.ProviderID {
		t.Errorf("expected target %s in ImpactAnswer, got %s", targetNodeIdentity.ProviderID, ansNode.Target.ProviderID)
	}

	t.Logf("Node Impact Answer: target=%s, impacted_count=%d, direct_count=%d, indirect_count=%d, max_depth=%d, paths=%d, evidence_refs=%d",
		ansNode.Target.ProviderID, ansNode.Summary.ImpactedCount, ansNode.Summary.DirectCount,
		ansNode.Summary.IndirectCount, ansNode.Summary.MaxDepth, len(ansNode.Paths), len(ansNode.Evidence))

	// Verify non-zero blast radius
	if ansNode.Summary.ImpactedCount == 0 {
		t.Fatalf("expected non-zero blast radius for Node with dependent Pods, got 0")
	}
	if ansNode.Summary.DirectCount < 2 {
		t.Errorf("expected at least 2 directly impacted pods (backend and frontend), got %d", ansNode.Summary.DirectCount)
	}

	// Verify that the impacted resources are indeed the real Kubernetes Pods
	impactedMap := make(map[string]answer.ImpactedResource)
	for _, ir := range ansNode.ImpactedResources {
		impactedMap[ir.Resource.ProviderID] = ir
		t.Logf("  Impacted Resource: %s (depth=%d)", ir.Resource.ProviderID, ir.Depth)
	}

	if ir, ok := impactedMap[backendPodIdentity.ProviderID]; !ok {
		t.Errorf("expected backend pod %s in impacted resources", backendPodIdentity.ProviderID)
	} else if ir.Depth != 1 {
		t.Errorf("expected backend pod depth=1, got %d", ir.Depth)
	}

	if ir, ok := impactedMap[frontendPodIdentity.ProviderID]; !ok {
		t.Errorf("expected frontend pod %s in impacted resources", frontendPodIdentity.ProviderID)
	} else if ir.Depth != 1 {
		t.Errorf("expected frontend pod depth=1, got %d", ir.Depth)
	}

	// Verify Paths: Pod -> DEPENDS_ON -> Node
	if len(ansNode.Paths) < 2 {
		t.Errorf("expected at least 2 paths in answer, got %d", len(ansNode.Paths))
	}
	for i, path := range ansNode.Paths {
		t.Logf("  Path %d: %v (rels: %v)", i, path.Resources, path.Relationships)
		if len(path.Resources) != 2 {
			t.Errorf("expected 2 resources in path, got %d", len(path.Resources))
			continue
		}
		if path.Resources[0].ResourceType != "pod" {
			t.Errorf("expected first path resource to be pod, got %s", path.Resources[0].ResourceType)
		}
		if path.Resources[1].ProviderID != targetNodeIdentity.ProviderID {
			t.Errorf("expected last path resource to be target node %s, got %s", targetNodeIdentity.ProviderID, path.Resources[1].ProviderID)
		}
		if len(path.Relationships) != 1 {
			t.Errorf("expected 1 relationship in path, got %d", len(path.Relationships))
			continue
		}
		rel := path.Relationships[0]
		if rel.Kind != "DEPENDS_ON" {
			t.Errorf("expected path relationship to be DEPENDS_ON, got %s", rel.Kind)
		}
		if rel.Source.ProviderID != path.Resources[0].ProviderID || rel.Target.ProviderID != path.Resources[1].ProviderID {
			t.Errorf("path relationship endpoints mismatch: rel=(%s -> %s), path=(%s -> %s)",
				rel.Source.ProviderID, rel.Target.ProviderID, path.Resources[0].ProviderID, path.Resources[1].ProviderID)
		}
	}

	// Verify Evidence in Answer: must match persisted PostgreSQL evidence records
	if len(ansNode.Evidence) == 0 {
		t.Errorf("expected supporting evidence in answer, got 0")
	}
	for _, ev := range ansNode.Evidence {
		t.Logf("  Answer Evidence: id=%s, type=%s, source=%s/%s",
			ev.ID, ev.ObservationType, ev.Source.Provider, ev.Source.Collector)
		if _, exists := persistedEvidenceMap[ev.ID]; !exists {
			t.Errorf("answer evidence %s does not exist in persisted PostgreSQL evidence", ev.ID)
		}
		if ev.Source.Provider != "kubernetes" || ev.Source.Collector != "k8s-collector" {
			t.Errorf("unexpected evidence source: %+v", ev.Source)
		}
	}

	// B. Query impact on backend Deployment (outgoing traversal)
	// Demonstrates that OWNS does NOT propagate through ImpactEngine (hard boundary).
	impactTargetDeploy := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "deployment",
		ProviderID:   expectedBackendDeploy,
	}

	ansDeploy, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetDeploy,
		Direction:   "outgoing",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on deployment failed: %v", err)
	}
	if ansDeploy.Target.ProviderID != expectedBackendDeploy {
		t.Errorf("expected target %s in ImpactAnswer, got %s", expectedBackendDeploy, ansDeploy.Target.ProviderID)
	}

	t.Logf("Deployment Impact Answer: target=%s, impacted_count=%d, direct_count=%d, relationships=%d",
		ansDeploy.Target.ProviderID, ansDeploy.Summary.ImpactedCount, ansDeploy.Summary.DirectCount, len(ansDeploy.Relationships))

	// Validate Impact v1 boundary guarantee: OWNS does not propagate impact
	if ansDeploy.Summary.ImpactedCount != 0 {
		t.Errorf("architectural violation: ImpactEngine propagated %d resources across OWNS boundary", ansDeploy.Summary.ImpactedCount)
	} else {
		t.Logf("Verified ImpactEngine v1 boundary: OWNS is a hard containment boundary and does not propagate impact")
	}

	// C. Query impact on backend Service (incoming traversal)
	// Without runtime network telemetry, no CALLS relationship exists between Service and clients
	impactTargetSvc := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderID:   expectedBackendSvc,
	}

	ansSvc, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetSvc,
		Direction:   "incoming",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on service failed: %v", err)
	}
	if ansSvc.Target.ProviderID != expectedBackendSvc {
		t.Errorf("expected target %s in ImpactAnswer, got %s", expectedBackendSvc, ansSvc.Target.ProviderID)
	}
	t.Logf("Service Impact Answer: target=%s, impacted_count=%d (honest: 0 runtime callers without network evidence)",
		ansSvc.Target.ProviderID, ansSvc.Summary.ImpactedCount)

	t.Logf("Real Kubernetes End-to-End flow verified successfully with DEPENDS_ON non-zero blast radius, control-plane ownership, and ZERO fake data.")
}
