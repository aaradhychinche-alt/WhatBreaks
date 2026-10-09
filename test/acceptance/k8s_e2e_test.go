package acceptance_test

import (
	"bufio"
	"context"
	"encoding/json"
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

// deployDeterministicWorkload deploys a realistic Kubernetes topology into the specified namespace.
func deployDeterministicWorkload(t *testing.T, namespace string) func() {
	t.Helper()

	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: backend-sa
  namespace: %[1]s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: backend-config
  namespace: %[1]s
data:
  APP_ENV: production
  LOG_LEVEL: info
---
apiVersion: v1
kind: Secret
metadata:
  name: backend-secret
  namespace: %[1]s
type: Opaque
stringData:
  DB_PASSWORD: supersecretpassword
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: backend-pvc
  namespace: %[1]s
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 10Mi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend
  namespace: %[1]s
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
      serviceAccountName: backend-sa
      containers:
      - name: backend
        image: nginx:alpine
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080
        env:
        - name: APP_ENV
          valueFrom:
            configMapKeyRef:
              name: backend-config
              key: APP_ENV
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: backend-secret
              key: DB_PASSWORD
        volumeMounts:
        - name: data-vol
          mountPath: /data
      volumes:
      - name: data-vol
        persistentVolumeClaim:
          claimName: backend-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: backend
  namespace: %[1]s
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
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: backend-ingress
  namespace: %[1]s
spec:
  rules:
  - http:
      paths:
      - path: /api
        pathType: Prefix
        backend:
          service:
            name: backend
            port:
              number: 8080
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frontend
  namespace: %[1]s
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
  namespace: %[1]s
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
`, namespace)

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

	// Wait up to 30s for pods to be created and running
	deadline := time.Now().Add(30 * time.Second)
	podsReady := false
	for time.Now().Before(deadline) {
		getCmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-o", "jsonpath={.items[*].status.phase}")
		out, err := getCmd.Output()
		phases := strings.Fields(string(out))
		if err == nil && len(phases) >= 2 {
			allRunning := true
			for _, p := range phases {
				if p != "Running" {
					allRunning = false
					break
				}
			}
			if allRunning {
				podsReady = true
				break
			}
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	observedConfigMaps := make(map[string]bool)
	observedSecrets := make(map[string]bool)
	observedPVCs := make(map[string]bool)
	observedServiceAccounts := make(map[string]bool)
	observedIngresses := make(map[string]bool)
	observedEvidenceTypes := make(map[string]int)
	observedRefTypes := make(map[string]int)

	for _, ev := range evidenceList {
		observedEvidenceTypes[ev.ObservationType]++
		if ev.ObservationType == "OWNERSHIP_REFERENCE" {
			observedRefTypes["owner_ref"]++
		}
		if len(ev.Data) > 0 {
			var d map[string]any
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				if refType, ok := d["reference_type"].(string); ok {
					observedRefTypes[refType]++
				}
			}
		}
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
		case "configmap":
			observedConfigMaps[ev.Subject.ProviderId] = true
		case "secret":
			observedSecrets[ev.Subject.ProviderId] = true
		case "persistentvolumeclaim":
			observedPVCs[ev.Subject.ProviderId] = true
		case "serviceaccount":
			observedServiceAccounts[ev.Subject.ProviderId] = true
		case "ingress":
			observedIngresses[ev.Subject.ProviderId] = true
		}
	}

	// Assert that all workload resources were genuinely observed from Kubernetes
	expectedBackendDeploy := fmt.Sprintf("%s/%s/backend", clusterID, testNamespace)
	expectedFrontendDeploy := fmt.Sprintf("%s/%s/frontend", clusterID, testNamespace)
	expectedBackendSvc := fmt.Sprintf("%s/%s/backend", clusterID, testNamespace)
	expectedFrontendSvc := fmt.Sprintf("%s/%s/frontend", clusterID, testNamespace)
	expectedBackendConfig := fmt.Sprintf("%s/%s/backend-config", clusterID, testNamespace)
	expectedBackendSecret := fmt.Sprintf("%s/%s/backend-secret", clusterID, testNamespace)
	expectedBackendPVC := fmt.Sprintf("%s/%s/backend-pvc", clusterID, testNamespace)
	expectedBackendSA := fmt.Sprintf("%s/%s/backend-sa", clusterID, testNamespace)
	expectedBackendIngress := fmt.Sprintf("%s/%s/backend-ingress", clusterID, testNamespace)

	if !observedDeployments[expectedBackendDeploy] {
		t.Errorf("expected deployment %s to be observed by collector", expectedBackendDeploy)
	}
	if !observedDeployments[expectedFrontendDeploy] {
		t.Errorf("expected deployment %s to be observed by collector", expectedFrontendDeploy)
	}
	if !observedServices[expectedBackendSvc] {
		t.Errorf("expected service %s to be observed by collector", expectedBackendSvc)
	}
	if !observedServices[expectedFrontendSvc] {
		t.Errorf("expected service %s to be observed by collector", expectedFrontendSvc)
	}
	if !observedConfigMaps[expectedBackendConfig] {
		t.Errorf("expected configmap %s to be observed by collector", expectedBackendConfig)
	}
	if !observedSecrets[expectedBackendSecret] {
		t.Errorf("expected secret %s to be observed by collector", expectedBackendSecret)
	}
	if !observedPVCs[expectedBackendPVC] {
		t.Errorf("expected pvc %s to be observed by collector", expectedBackendPVC)
	}
	if !observedServiceAccounts[expectedBackendSA] {
		t.Errorf("expected serviceaccount %s to be observed by collector", expectedBackendSA)
	}
	if !observedIngresses[expectedBackendIngress] {
		t.Errorf("expected ingress %s to be observed by collector", expectedBackendIngress)
	}

	if len(observedPods) < 2 {
		t.Errorf("expected at least 2 pods to be observed, got %d (%v)", len(observedPods), observedPods)
	}

	// Verify all required evidence types are generated
	for _, requiredType := range []string{
		"config_map_ref",
		"secret_ref",
		"pvc_mount_ref",
		"service_account_ref",
		"ingress_backend_ref",
		"pod_scheduled_node",
		"owner_ref",
	} {
		if count := observedRefTypes[requiredType]; count == 0 {
			t.Errorf("expected evidence of type %q to be generated, got 0", requiredType)
		} else {
			t.Logf("Generated %d evidence records of type %q", count, requiredType)
		}
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
	if _, ok := persistedResourceMap[expectedBackendConfig]; !ok {
		t.Errorf("expected backend configmap %s to be in PostgreSQL state_resources", expectedBackendConfig)
	}
	if _, ok := persistedResourceMap[expectedBackendSecret]; !ok {
		t.Errorf("expected backend secret %s to be in PostgreSQL state_resources", expectedBackendSecret)
	}
	if _, ok := persistedResourceMap[expectedBackendPVC]; !ok {
		t.Errorf("expected backend pvc %s to be in PostgreSQL state_resources", expectedBackendPVC)
	}
	if _, ok := persistedResourceMap[expectedBackendSA]; !ok {
		t.Errorf("expected backend serviceaccount %s to be in PostgreSQL state_resources", expectedBackendSA)
	}
	if _, ok := persistedResourceMap[expectedBackendIngress]; !ok {
		t.Errorf("expected backend ingress %s to be in PostgreSQL state_resources", expectedBackendIngress)
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
	var (
		foundBackendDeployOwnsPod       bool
		foundFrontendDeployOwnsPod      bool
		foundBackendPodDependsOnNode    bool
		foundFrontendPodDependsOnNode   bool
		foundBackendPodDependsOnCM      bool
		foundBackendPodDependsOnSecret  bool
		foundBackendPodDependsOnPVC     bool
		foundBackendPodDependsOnSA      bool
		foundIngressDependsOnBackendSvc bool
		backendPodIdentity              state.ResourceIdentity
		frontendPodIdentity             state.ResourceIdentity
		targetNodeIdentity              state.ResourceIdentity
		foundAnyCallsRelationship       bool
	)

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

		// Check for Pod -> ConfigMap DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "pod" && rel.Target.ResourceType == "configmap" {
			if strings.Contains(rel.Source.ProviderID, "/backend-") && rel.Target.ProviderID == expectedBackendConfig {
				foundBackendPodDependsOnCM = true
				t.Logf("Verified dependency: %s DEPENDS_ON %s (ConfigMap, %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}

		// Check for Pod -> Secret DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "pod" && rel.Target.ResourceType == "secret" {
			if strings.Contains(rel.Source.ProviderID, "/backend-") && rel.Target.ProviderID == expectedBackendSecret {
				foundBackendPodDependsOnSecret = true
				t.Logf("Verified dependency: %s DEPENDS_ON %s (Secret, %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}

		// Check for Pod -> PVC DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "pod" && rel.Target.ResourceType == "persistentvolumeclaim" {
			if strings.Contains(rel.Source.ProviderID, "/backend-") && rel.Target.ProviderID == expectedBackendPVC {
				foundBackendPodDependsOnPVC = true
				t.Logf("Verified dependency: %s DEPENDS_ON %s (PVC, %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}

		// Check for Pod -> ServiceAccount DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "pod" && rel.Target.ResourceType == "serviceaccount" {
			if strings.Contains(rel.Source.ProviderID, "/backend-") && rel.Target.ProviderID == expectedBackendSA {
				foundBackendPodDependsOnSA = true
				t.Logf("Verified dependency: %s DEPENDS_ON %s (ServiceAccount, %d evidence records)",
					rel.Source.ProviderID, rel.Target.ProviderID, len(evIDs))
			}
		}

		// Check for Ingress -> Service DEPENDS_ON relationship
		if rel.Kind == "DEPENDS_ON" && rel.Source.ResourceType == "ingress" && rel.Target.ResourceType == "service" {
			if rel.Source.ProviderID == expectedBackendIngress && rel.Target.ProviderID == expectedBackendSvc {
				foundIngressDependsOnBackendSvc = true
				t.Logf("Verified dependency: %s DEPENDS_ON %s (Ingress->Service, %d evidence records)",
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
	if !foundBackendPodDependsOnCM {
		t.Fatalf("expected backend pod to depend on ConfigMap %s in discovered relationships", expectedBackendConfig)
	}
	if !foundBackendPodDependsOnSecret {
		t.Fatalf("expected backend pod to depend on Secret %s in discovered relationships", expectedBackendSecret)
	}
	if !foundBackendPodDependsOnPVC {
		t.Fatalf("expected backend pod to depend on PVC %s in discovered relationships", expectedBackendPVC)
	}
	if !foundBackendPodDependsOnSA {
		t.Fatalf("expected backend pod to depend on ServiceAccount %s in discovered relationships", expectedBackendSA)
	}
	if !foundIngressDependsOnBackendSvc {
		t.Fatalf("expected Ingress %s to depend on Service %s in discovered relationships", expectedBackendIngress, expectedBackendSvc)
	}
	if targetNodeIdentity.ProviderID == "" {
		t.Fatalf("expected target node identity to be populated from discovered DEPENDS_ON relationships")
	}

	// Negative Test: Zero Secret Custody Check
	// Verify that secret payload (data / stringData / "supersecretpassword") is NEVER stored in evidence
	for _, ev := range persistedEvidence {
		evJSON, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("failed to marshal evidence: %v", err)
		}
		if strings.Contains(string(evJSON), "supersecretpassword") {
			t.Fatalf("ZERO SECRET CUSTODY VIOLATION: secret data found in persisted evidence: %s", string(evJSON))
		}
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

	// Verify non-zero blast radius on Node
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

	// B. Query impact on ConfigMap backend-config (Incoming traversal along DEPENDS_ON)
	// Because backend Pod DEPENDS_ON backend-config, incoming traversal from backend-config
	// discovers the dependent backend Pod!
	impactTargetCM := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "configmap",
		ProviderID:   expectedBackendConfig,
	}
	ansCM, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetCM,
		Direction:   "incoming",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on ConfigMap failed: %v", err)
	}
	if ansCM.Summary.ImpactedCount == 0 {
		t.Fatalf("expected non-zero blast radius for ConfigMap with dependent Pod, got 0")
	}
	t.Logf("ConfigMap Impact Answer: target=%s, impacted_count=%d, direct_count=%d",
		ansCM.Target.ProviderID, ansCM.Summary.ImpactedCount, ansCM.Summary.DirectCount)
	var foundBackendPodInCMImpact bool
	for _, ir := range ansCM.ImpactedResources {
		t.Logf("  ConfigMap Impacted Resource: %s (depth=%d, type=%s)", ir.Resource.ProviderID, ir.Depth, ir.Resource.ResourceType)
		if ir.Depth != 1 {
			t.Errorf("expected direct impact at depth=1, got depth=%d for %s", ir.Depth, ir.Resource.ProviderID)
		}
		if ir.Resource.ProviderID == backendPodIdentity.ProviderID {
			foundBackendPodInCMImpact = true
		}
	}
	if !foundBackendPodInCMImpact {
		t.Errorf("expected backend pod %s to be impacted by ConfigMap change", backendPodIdentity.ProviderID)
	}

	// Audit check: Verify all paths from ConfigMap are exclusively direct DEPENDS_ON (no OWNS traversal)
	for i, path := range ansCM.Paths {
		t.Logf("  ConfigMap Impact Path %d: resources=%v, rels=%v", i, path.Resources, path.Relationships)
		for _, rel := range path.Relationships {
			if rel.Kind != "DEPENDS_ON" {
				t.Fatalf("audit violation: ConfigMap impact path used %s instead of DEPENDS_ON", rel.Kind)
			}
		}
	}

	// C. Query impact on backend Service (Incoming traversal along DEPENDS_ON)
	// Ingress backend-ingress DEPENDS_ON backend Service, so incoming traversal finds backend-ingress!
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
	if ansSvc.Summary.ImpactedCount == 0 {
		t.Fatalf("expected non-zero blast radius for Service with dependent Ingress, got 0")
	}
	t.Logf("Service Impact Answer: target=%s, impacted_count=%d, direct_count=%d",
		ansSvc.Target.ProviderID, ansSvc.Summary.ImpactedCount, ansSvc.Summary.DirectCount)
	var foundIngressInSvcImpact bool
	for _, ir := range ansSvc.ImpactedResources {
		if ir.Resource.ProviderID == expectedBackendIngress {
			foundIngressInSvcImpact = true
			break
		}
	}
	if !foundIngressInSvcImpact {
		t.Errorf("expected ingress %s to be impacted by Service change", expectedBackendIngress)
	}

	// D. Query impact on frontend Service (which has NO ingress pointing to it)
	// Without runtime network telemetry, no CALLS relationship exists between Service and clients
	impactTargetFrontendSvc := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderID:   expectedFrontendSvc,
	}
	ansFrontendSvc, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetFrontendSvc,
		Direction:   "incoming",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on frontend service failed: %v", err)
	}
	if ansFrontendSvc.Summary.ImpactedCount != 0 {
		t.Errorf("expected 0 callers for frontend service without runtime evidence, got %d", ansFrontendSvc.Summary.ImpactedCount)
	}
	t.Logf("Frontend Service Impact Answer: target=%s, impacted_count=0 (honest: 0 runtime callers without network evidence)",
		ansFrontendSvc.Target.ProviderID)

	// E. Query impact on frontend Deployment (outgoing traversal)
	// Demonstrates that OWNS does NOT propagate through ImpactEngine (hard boundary).
	// Frontend Deployment OWNS ReplicaSet and Pod, with NO secret/configmap dependencies.
	impactTargetFrontendDeploy := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "deployment",
		ProviderID:   expectedFrontendDeploy,
	}
	ansFrontendDeploy, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceID,
		Target:      impactTargetFrontendDeploy,
		Direction:   "outgoing",
		MaxDepth:    5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact on frontend deployment failed: %v", err)
	}
	if ansFrontendDeploy.Target.ProviderID != expectedFrontendDeploy {
		t.Errorf("expected target %s in ImpactAnswer, got %s", expectedFrontendDeploy, ansFrontendDeploy.Target.ProviderID)
	}
	t.Logf("Frontend Deployment Impact Answer: target=%s, impacted_count=%d, direct_count=%d, relationships=%d",
		ansFrontendDeploy.Target.ProviderID, ansFrontendDeploy.Summary.ImpactedCount, ansFrontendDeploy.Summary.DirectCount, len(ansFrontendDeploy.Relationships))

	// Validate Impact v1 boundary guarantee: OWNS does not propagate impact
	if ansFrontendDeploy.Summary.ImpactedCount != 0 {
		t.Errorf("architectural violation: ImpactEngine propagated %d resources across OWNS boundary", ansFrontendDeploy.Summary.ImpactedCount)
	} else {
		t.Logf("Verified ImpactEngine v1 boundary: OWNS is a hard containment boundary and does not propagate impact")
	}

	t.Logf("Real Kubernetes End-to-End flow verified successfully with DEPENDS_ON non-zero blast radius, control-plane ownership, and ZERO fake data.")
}

// ---------------------------------------------------------------------------
// Real Kubernetes Acceptance Test: Reconciliation & Lifecycle
// ---------------------------------------------------------------------------

func TestRealKubernetes_ReconciliationLifecycle(t *testing.T) {
	// 1. Prerequisites: kubectl proxy & PostgreSQL
	proxyURL, stopProxy := startKubectlProxy(t)
	defer stopProxy()

	dbCfg := getTestDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := logging.NewJSONLogger(nil, logging.LevelInfo, "k8s-rec-acceptance")

	db, err := database.New(ctx, dbCfg, database.WithLogger(logger))
	if err != nil {
		t.Skipf("Failed to initialize database pool: %v; skipping", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL Ping failed: %v; skipping", err)
	}

	if err := state.EnsureSchema(ctx, db); err != nil {
		t.Fatalf("state.EnsureSchema failed: %v", err)
	}

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
	pgStore := state.NewPostgresStore(db)

	workspaceID := "k8s-rec-lifecycle-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_ = pgStore.DeleteWorkspaceState(ctx, workspaceID)
	defer func() {
		_ = pgStore.DeleteWorkspaceState(ctx, workspaceID)
	}()

	clusterID := "k8s-rec-cluster"
	testNamespace := "whatbreaks-test"

	k8sClient, err := k8s.NewRESTClient(k8s.ClientConfig{
		BaseURL: proxyURL,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to initialize k8s REST client: %v", err)
	}

	collector, err := k8s.New(
		k8s.WithClient(k8sClient),
		k8s.WithClusterID(clusterID),
		k8s.WithWorkspaceID(workspaceID),
		k8s.WithNamespaces(testNamespace),
		k8s.WithClusterWide(true),
	)
	if err != nil {
		t.Fatalf("failed to initialize k8s collector: %v", err)
	}

	materializer := state.NewMaterializer(pgStore, coreClient, logger)
	reconciler := state.NewReconciler(pgStore, coreClient, materializer, logger)

	// Step 1: Initial Sweep — creates state in PostgreSQL
	evidence1, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("collector.Collect sweep 1 failed: %v", err)
	}
	if len(evidence1) == 0 {
		t.Fatalf("expected evidence from sweep 1, got 0")
	}

	batch1 := state.ObservationBatch{
		WorkspaceID: workspaceID,
		Evidence:    evidence1,
	}

	res1, err := reconciler.ReconcileAndMaterialize(ctx, batch1)
	if err != nil {
		t.Fatalf("reconciler.ReconcileAndMaterialize sweep 1 failed: %v", err)
	}

	if res1.ResourcesCreated == 0 {
		t.Errorf("expected resources created in sweep 1, got 0")
	}
	initialResourcesTotal := res1.ResourcesTotal
	initialRelsTotal := res1.RelationshipsTotal
	t.Logf("Sweep 1 Complete: created=%d, total_resources=%d, total_relationships=%d, evidence=%d",
		res1.ResourcesCreated, initialResourcesTotal, initialRelsTotal, res1.EvidenceCount)

	// Step 2: Second Observation Sweep — updates/reconciles state idempotently
	evidence2, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("collector.Collect sweep 2 failed: %v", err)
	}

	batch2 := state.ObservationBatch{
		WorkspaceID: workspaceID,
		Evidence:    evidence2,
	}

	res2, err := reconciler.ReconcileAndMaterialize(ctx, batch2)
	if err != nil {
		t.Fatalf("reconciler.ReconcileAndMaterialize sweep 2 failed: %v", err)
	}

	t.Logf("Sweep 2 Complete: created=%d, updated=%d, total_resources=%d, total_relationships=%d",
		res2.ResourcesCreated, res2.ResourcesUpdated, res2.ResourcesTotal, res2.RelationshipsTotal)

	// Idempotency check: resources should not double; updated count should reflect seen resources
	if res2.ResourcesTotal < initialResourcesTotal {
		t.Errorf("expected total resources >= %d, got %d", initialResourcesTotal, res2.ResourcesTotal)
	}
	if res2.ResourcesUpdated == 0 {
		t.Errorf("expected resources to be updated on second sweep, got 0")
	}

	// Verify evidence history is preserved immutably in PostgreSQL
	persistedEvidence, err := pgStore.ListEvidence(ctx, workspaceID)
	if err != nil {
		t.Fatalf("pgStore.ListEvidence failed: %v", err)
	}
	if len(persistedEvidence) < len(evidence1) {
		t.Errorf("expected at least %d evidence records preserved, got %d", len(evidence1), len(persistedEvidence))
	}

	// Step 3: Temporary Observation Gap — partial sweep must NOT delete state
	// Submit a partial batch containing only the first evidence record
	partialBatch := state.ObservationBatch{
		WorkspaceID: workspaceID,
		Evidence:    evidence2[:1],
	}

	res3, err := reconciler.ReconcileAndMaterialize(ctx, partialBatch)
	if err != nil {
		t.Fatalf("reconciler.ReconcileAndMaterialize partial gap failed: %v", err)
	}

	// Invariant: Total resources and relationships must remain unchanged
	if res3.ResourcesTotal != res2.ResourcesTotal {
		t.Fatalf("CRITICAL REGRESSION: temporary gap deleted resources! expected %d, got %d",
			res2.ResourcesTotal, res3.ResourcesTotal)
	}
	if res3.RelationshipsTotal != res2.RelationshipsTotal {
		t.Fatalf("CRITICAL REGRESSION: temporary gap deleted relationships! expected %d, got %d",
			res2.RelationshipsTotal, res3.RelationshipsTotal)
	}

	// Query store to confirm resources are still intact
	persistedAfterGap, err := pgStore.ListResources(ctx, workspaceID)
	if err != nil {
		t.Fatalf("pgStore.ListResources failed: %v", err)
	}
	if len(persistedAfterGap) != res2.ResourcesTotal {
		t.Fatalf("resources missing in PostgreSQL after gap: expected %d, got %d",
			res2.ResourcesTotal, len(persistedAfterGap))
	}

	t.Logf("Reconciliation Lifecycle Acceptance Test passed: state preserved across multiple sweeps and temporary gaps.")
}
