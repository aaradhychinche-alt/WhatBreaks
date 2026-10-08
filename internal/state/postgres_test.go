package state_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
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
		t.Skipf("PostgreSQL is not reachable at %s:%d (%v); skipping integration test", host, port, err)
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

func TestPostgresStore_Integration(t *testing.T) {
	cfg := getTestDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logger := logging.NewJSONLogger(nil, logging.LevelDebug, "pg-state-test")
	db, err := database.New(ctx, cfg, database.WithLogger(logger))
	if err != nil {
		t.Skipf("Failed to initialize database pool: %v; skipping", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL Ping failed: %v; skipping", err)
	}

	pgStore := state.NewPostgresStore(db)

	// -----------------------------------------------------------------------
	// 1. Schema Creation & Verification
	// -----------------------------------------------------------------------
	t.Run("SchemaVerification", func(t *testing.T) {
		if err := state.EnsureSchema(ctx, db); err != nil {
			t.Fatalf("EnsureSchema failed: %v", err)
		}

		expectedTables := []string{
			"state_resources",
			"state_evidence",
			"state_relationships",
			"state_provenance",
			"state_conflicts",
		}

		for _, table := range expectedTables {
			query := `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`
			var exists int
			err := db.QueryRow(ctx, query, table).Scan(&exists)
			if err != nil || exists != 1 {
				t.Fatalf("expected table %s to exist in PostgreSQL: %v", table, err)
			}
		}
	})

	ws1 := "ws-pg-test-tenant-1"
	ws2 := "ws-pg-test-tenant-2"

	// Cleanup any previous run
	_ = pgStore.DeleteWorkspaceState(ctx, ws1)
	_ = pgStore.DeleteWorkspaceState(ctx, ws2)
	t.Cleanup(func() {
		_ = pgStore.DeleteWorkspaceState(context.Background(), ws1)
		_ = pgStore.DeleteWorkspaceState(context.Background(), ws2)
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	res1 := state.Resource{
		WorkspaceID: ws1,
		Identity: state.ResourceIdentity{
			Provider:     "aws",
			ResourceType: "rds_instance",
			ProviderID:   "db-prod-main",
		},
		FirstObservedAt: now,
		LastObservedAt:  now,
	}
	res2 := state.Resource{
		WorkspaceID: ws1,
		Identity: state.ResourceIdentity{
			Provider:     "aws",
			ResourceType: "ecs_service",
			ProviderID:   "order-api",
		},
		FirstObservedAt: now,
		LastObservedAt:  now,
	}

	evID := "22222222-2222-2222-2222-222222222222"
	initialData := []byte(`{"metric": "db_connections", "val": 45}`)
	ev := state.Evidence{
		WorkspaceID: ws1,
		ID:          evID,
		Source: state.EvidenceSource{
			Provider:  "aws",
			Collector: "aws-cloudwatch",
		},
		ObservedAt:      now,
		ObservationType: "METRIC_CORRELATION",
		Subject:         res2.Identity,
		Data:            initialData,
	}

	rel := state.Relationship{
		WorkspaceID:     ws1,
		Source:          res2.Identity,
		Target:          res1.Identity,
		Category:        "storage",
		Kind:            "queries",
		FirstObservedAt: now,
		LastObservedAt:  now,
	}

	prov := state.ProvenanceAssociation{
		WorkspaceID:  ws1,
		Relationship: rel.Key(),
		EvidenceID:   evID,
	}

	conflict := state.DiscoveryConflict{
		WorkspaceID: ws1,
		Description: "conflicting target port observed",
		ObservedAt:  now,
	}

	// -----------------------------------------------------------------------
	// 2. Persistence & Reconstruction
	// -----------------------------------------------------------------------
	t.Run("PersistenceAndReconstruction", func(t *testing.T) {
		// Save resources
		if err := pgStore.SaveResources(ctx, ws1, []state.Resource{res1, res2}); err != nil {
			t.Fatalf("SaveResources failed: %v", err)
		}
		// Save evidence
		if err := pgStore.SaveEvidence(ctx, ws1, []state.Evidence{ev}); err != nil {
			t.Fatalf("SaveEvidence failed: %v", err)
		}
		// Save relationship
		if err := pgStore.SaveRelationships(ctx, ws1, []state.Relationship{rel}); err != nil {
			t.Fatalf("SaveRelationships failed: %v", err)
		}
		// Save provenance
		if err := pgStore.SaveProvenance(ctx, ws1, []state.ProvenanceAssociation{prov}); err != nil {
			t.Fatalf("SaveProvenance failed: %v", err)
		}
		// Save conflict
		if err := pgStore.SaveConflict(ctx, conflict); err != nil {
			t.Fatalf("SaveConflict failed: %v", err)
		}

		// Reconstruct full AssembledState
		assembled, err := pgStore.LoadAssembledState(ctx, ws1)
		if err != nil {
			t.Fatalf("LoadAssembledState failed: %v", err)
		}
		if len(assembled.Resources) != 2 {
			t.Errorf("expected 2 resources in ws1, got %d", len(assembled.Resources))
		}
		if len(assembled.Evidence) != 1 {
			t.Errorf("expected 1 evidence in ws1, got %d", len(assembled.Evidence))
		}
		if len(assembled.Relationships) != 1 {
			t.Errorf("expected 1 relationship in ws1, got %d", len(assembled.Relationships))
		}
		if len(assembled.Provenance) != 1 {
			t.Errorf("expected 1 provenance entry in ws1, got %d", len(assembled.Provenance))
		}

		// Verify individual query lookups
		gotRes, err := pgStore.GetResource(ctx, ws1, res1.Identity)
		if err != nil || gotRes.Identity.ProviderID != res1.Identity.ProviderID {
			t.Errorf("GetResource failed: %v, got %+v", err, gotRes)
		}

		evList, err := pgStore.ListEvidence(ctx, ws1)
		if err != nil || len(evList) != 1 || evList[0].ID != evID {
			t.Errorf("ListEvidence failed: %v, got %+v", err, evList)
		}

		provEvIDs, err := pgStore.GetProvenanceForRelationship(ctx, ws1, rel.Key())
		if err != nil || len(provEvIDs) != 1 || provEvIDs[0] != evID {
			t.Errorf("GetProvenanceForRelationship failed: %v", err)
		}

		provRels, err := pgStore.GetRelationshipsForEvidence(ctx, ws1, evID)
		if err != nil || len(provRels) != 1 || provRels[0] != rel.Key() {
			t.Errorf("GetRelationshipsForEvidence failed: %v", err)
		}
	})

	// -----------------------------------------------------------------------
	// 3. Idempotency & Immutability
	// -----------------------------------------------------------------------
	t.Run("IdempotencyAndEvidenceImmutability", func(t *testing.T) {
		laterTime := now.Add(5 * time.Minute)

		// Repeated resource write with later timestamp
		updatedRes := res1
		updatedRes.LastObservedAt = laterTime
		if err := pgStore.SaveResources(ctx, ws1, []state.Resource{updatedRes}); err != nil {
			t.Fatalf("repeated SaveResources failed: %v", err)
		}

		storedRes, err := pgStore.GetResource(ctx, ws1, res1.Identity)
		if err != nil {
			t.Fatalf("GetResource failed: %v", err)
		}
		if !storedRes.LastObservedAt.Equal(laterTime) {
			t.Errorf("expected LastObservedAt updated to %v, got %v", laterTime, storedRes.LastObservedAt)
		}

		// Repeated relationship write with later timestamp
		updatedRel := rel
		updatedRel.LastObservedAt = laterTime
		if err := pgStore.SaveRelationships(ctx, ws1, []state.Relationship{updatedRel}); err != nil {
			t.Fatalf("repeated SaveRelationships failed: %v", err)
		}

		rels, err := pgStore.ListRelationships(ctx, ws1)
		if err != nil || len(rels) != 1 {
			t.Fatalf("expected 1 relationship, got %d (err: %v)", len(rels), err)
		}
		if !rels[0].LastObservedAt.Equal(laterTime) {
			t.Errorf("expected relationship LastObservedAt updated to %v, got %v", laterTime, rels[0].LastObservedAt)
		}

		// Repeated provenance associations: duplicate ignored
		if err := pgStore.SaveProvenance(ctx, ws1, []state.ProvenanceAssociation{prov}); err != nil {
			t.Fatalf("repeated SaveProvenance failed: %v", err)
		}
		provIDs, err := pgStore.GetProvenanceForRelationship(ctx, ws1, rel.Key())
		if err != nil || len(provIDs) != 1 {
			t.Fatalf("expected still exactly 1 provenance link after repeated write, got %d", len(provIDs))
		}

		// Evidence Immutability: attempting to overwrite evidence data MUST NOT change existing data
		mutatedEvidence := ev
		mutatedEvidence.Data = []byte(`{"metric": "TAMPERED_DATA", "val": 9999}`)
		if err := pgStore.SaveEvidence(ctx, ws1, []state.Evidence{mutatedEvidence}); err != nil {
			t.Fatalf("SaveEvidence duplicate write failed: %v", err)
		}

		evList, err := pgStore.ListEvidence(ctx, ws1)
		if err != nil || len(evList) != 1 {
			t.Fatalf("ListEvidence failed: %v", err)
		}
		if !bytes.Equal(evList[0].Data, initialData) {
			t.Fatalf("Evidence immutability VIOLATED! Data was overwritten: %s", string(evList[0].Data))
		}
	})

	// -----------------------------------------------------------------------
	// 4. Workspace Isolation & Identical Resource Identities
	// -----------------------------------------------------------------------
	t.Run("WorkspaceIsolationWithIdenticalIdentities", func(t *testing.T) {
		// Insert identical ResourceIdentity in ws2
		resInWs2 := state.Resource{
			WorkspaceID:     ws2,
			Identity:        res1.Identity, // exact same (aws, rds_instance, db-prod-main)
			FirstObservedAt: now.Add(1 * time.Hour),
			LastObservedAt:  now.Add(2 * time.Hour),
		}
		if err := pgStore.SaveResources(ctx, ws2, []state.Resource{resInWs2}); err != nil {
			t.Fatalf("SaveResources in ws2 failed: %v", err)
		}

		// Query ws1: must have 2 resources
		ws1Resources, err := pgStore.ListResources(ctx, ws1)
		if err != nil || len(ws1Resources) != 2 {
			t.Fatalf("expected 2 resources in ws1, got %d", len(ws1Resources))
		}

		// Query ws2: must have only 1 resource
		ws2Resources, err := pgStore.ListResources(ctx, ws2)
		if err != nil || len(ws2Resources) != 1 {
			t.Fatalf("expected 1 resource in ws2, got %d", len(ws2Resources))
		}

		// Query ws2 state: relationships, evidence, provenance must be completely empty
		ws2State, err := pgStore.LoadAssembledState(ctx, ws2)
		if err != nil {
			t.Fatalf("LoadAssembledState for ws2 failed: %v", err)
		}
		if len(ws2State.Relationships) != 0 || len(ws2State.Evidence) != 0 || len(ws2State.Provenance) != 0 {
			t.Fatalf("Cross-tenant leakage! ws2 contains ws1 relationships or evidence: %+v", ws2State)
		}

		// Delete ws1: ws2 resource must remain intact
		if err := pgStore.DeleteWorkspaceState(ctx, ws1); err != nil {
			t.Fatalf("DeleteWorkspaceState ws1 failed: %v", err)
		}

		ws2Remaining, err := pgStore.ListResources(ctx, ws2)
		if err != nil || len(ws2Remaining) != 1 {
			t.Fatalf("expected ws2 resource to survive ws1 deletion, got %d", len(ws2Remaining))
		}
	})

	// -----------------------------------------------------------------------
	// 5. Transaction Rollback Atomicity
	// -----------------------------------------------------------------------
	t.Run("TransactionRollbackAtomicity", func(t *testing.T) {
		wsTx := "ws-tx-rollback-test"
		t.Cleanup(func() {
			_ = pgStore.DeleteWorkspaceState(context.Background(), wsTx)
		})

		customErr := errors.New("simulated batch failure")
		err := db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO state_resources (workspace_id, provider, resource_type, provider_id, first_observed_at, last_observed_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				wsTx, "aws", "s3_bucket", "temp-bucket-tx", now, now)
			if err != nil {
				return err
			}
			// Intentionally abort the transaction
			return customErr
		})

		if !errors.Is(err, customErr) {
			t.Fatalf("expected customErr from transaction, got %v", err)
		}

		// Assert that the resource was not committed
		resources, err := pgStore.ListResources(ctx, wsTx)
		if err != nil {
			t.Fatalf("ListResources failed: %v", err)
		}
		if len(resources) != 0 {
			t.Fatalf("Transaction rollback failed: expected 0 resources, got %d", len(resources))
		}
	})

	// -----------------------------------------------------------------------
	// 6. Reconstruction & Materialization into Live Rust Core Engine
	// -----------------------------------------------------------------------
	t.Run("MaterializationToRustCoreEngine", func(t *testing.T) {
		client := startProdServer(t)

		wsMat := "ws-pg-materialize-test"
		t.Cleanup(func() {
			_ = pgStore.DeleteWorkspaceState(context.Background(), wsMat)
		})

		// Insert topology in Postgres: service-a -[DEPENDS_ON]-> service-b
		serviceA := state.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderID: "service-a"}
		serviceB := state.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderID: "service-b"}

		resA := state.Resource{WorkspaceID: wsMat, Identity: serviceA, FirstObservedAt: now, LastObservedAt: now}
		resB := state.Resource{WorkspaceID: wsMat, Identity: serviceB, FirstObservedAt: now, LastObservedAt: now}
		if err := pgStore.SaveResources(ctx, wsMat, []state.Resource{resA, resB}); err != nil {
			t.Fatalf("SaveResources failed: %v", err)
		}

		matEvID := "99999999-9999-9999-9999-999999999999"
		matEv := state.Evidence{
			WorkspaceID:     wsMat,
			ID:              matEvID,
			Source:          state.EvidenceSource{Provider: "kubernetes", Collector: "k8s-runtime"},
			ObservedAt:      now,
			ObservationType: "RUNTIME_CONNECTION",
			Subject:         serviceA,
			Data:            []byte(`{"target": "service-b"}`),
		}
		if err := pgStore.SaveEvidence(ctx, wsMat, []state.Evidence{matEv}); err != nil {
			t.Fatalf("SaveEvidence failed: %v", err)
		}

		matRel := state.Relationship{
			WorkspaceID:     wsMat,
			Source:          serviceA,
			Target:          serviceB,
			Category:        "Invocation",
			Kind:            "CALLS",
			FirstObservedAt: now,
			LastObservedAt:  now,
		}
		if err := pgStore.SaveRelationships(ctx, wsMat, []state.Relationship{matRel}); err != nil {
			t.Fatalf("SaveRelationships failed: %v", err)
		}

		matProv := state.ProvenanceAssociation{
			WorkspaceID:  wsMat,
			Relationship: matRel.Key(),
			EvidenceID:   matEvID,
		}
		if err := pgStore.SaveProvenance(ctx, wsMat, []state.ProvenanceAssociation{matProv}); err != nil {
			t.Fatalf("SaveProvenance failed: %v", err)
		}

		// Materialize from PostgreSQL into Rust Core Engine
		mat := state.NewMaterializer(pgStore, client, logger)
		resp, err := mat.Materialize(ctx, wsMat)
		if err != nil {
			t.Fatalf("Materialize from PostgreSQL failed: %v", err)
		}
		if resp.RelationshipsLoaded != 1 || resp.EvidenceLoaded != 1 || resp.ProvenanceLinksLoaded != 1 {
			t.Fatalf("unexpected Materialize response: %+v", resp)
		}

		// Execute impact query on Rust Core Engine
		answerSvc := answer.NewService(client, logger)
		ans, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
			WorkspaceID: wsMat,
			Target: &answer.ResourceIdentity{
				Provider:     serviceB.Provider,
				ResourceType: serviceB.ResourceType,
				ProviderID:   serviceB.ProviderID,
			},
			Direction: "incoming",
			MaxDepth:  5,
		})
		if err != nil {
			t.Fatalf("AnalyzeImpact failed: %v", err)
		}

		if ans.Summary.ImpactedCount != 1 {
			t.Errorf("expected 1 impacted resource, got %d", ans.Summary.ImpactedCount)
		}
		if len(ans.Relationships) != 1 || ans.Relationships[0].State != "supported" {
			t.Errorf("expected 1 supported relationship, got %+v", ans.Relationships)
		}
		if len(ans.Evidence) != 1 || ans.Evidence[0].ID != matEvID {
			t.Errorf("expected evidence %s, got %+v", matEvID, ans.Evidence)
		}
	})
}

func TestPostgresStore_ReconcilerLifecycle(t *testing.T) {
	cfg := getTestDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	logger := logging.NewJSONLogger(nil, logging.LevelDebug, "pg-reconciler-test")
	db, err := database.New(ctx, cfg, database.WithLogger(logger))
	if err != nil {
		t.Skipf("Failed to initialize database pool: %v; skipping", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL Ping failed: %v; skipping", err)
	}

	if err := state.EnsureSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	pgStore := state.NewPostgresStore(db)
	client := startProdServer(t)

	materializer := state.NewMaterializer(pgStore, client, logger)
	reconciler := state.NewReconciler(pgStore, client, materializer, logger)

	ws := "ws-pg-rec-lifecycle"
	_ = pgStore.DeleteWorkspaceState(ctx, ws)
	defer func() {
		_ = pgStore.DeleteWorkspaceState(ctx, ws)
	}()

	frontendSubj := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   "payments/frontend",
	}
	backendSubj := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   "payments/backend",
	}

	connEv := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440071", frontendSubj, "10.0.1.50", 8080)
	mapEv := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440072", backendSubj, "10.0.1.50", 8080)

	// 1. Initial Observation Sweep into PostgreSQL
	batch1 := state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{connEv, mapEv},
	}
	res1, err := reconciler.Reconcile(ctx, batch1)
	if err != nil {
		t.Fatalf("Cycle 1 Reconcile failed: %v", err)
	}
	if res1.ResourcesCreated != 2 || res1.RelationshipsCreated != 1 || res1.ProvenanceLinksAdded != 2 {
		t.Fatalf("unexpected Cycle 1 result: %+v", res1)
	}

	// 2. Repeated Sweep (Idempotency) into PostgreSQL
	res2, err := reconciler.Reconcile(ctx, batch1)
	if err != nil {
		t.Fatalf("Cycle 2 Reconcile failed: %v", err)
	}
	if res2.ResourcesCreated != 0 || res2.ResourcesUpdated != 2 {
		t.Errorf("expected 0 created, 2 updated on repeat; got %+v", res2)
	}
	if res2.RelationshipsCreated != 0 || res2.RelationshipsUpdated != 1 {
		t.Errorf("expected 0 rel created, 1 rel updated on repeat; got %+v", res2)
	}
	if res2.ResourcesTotal != 2 || res2.RelationshipsTotal != 1 {
		t.Errorf("expected 2 total resources, 1 total relationship; got %+v", res2)
	}

	// 3. Partial Gap Sweep (backend omitted) into PostgreSQL
	// CRITICAL: backend and relationship must NOT be deleted!
	connEvLater := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440073", frontendSubj, "10.0.1.50", 8080)
	batch3 := state.ObservationBatch{
		WorkspaceID: ws,
		Evidence:    []*corev1.Evidence{connEvLater},
	}
	res3, err := reconciler.ReconcileAndMaterialize(ctx, batch3)
	if err != nil {
		t.Fatalf("Cycle 3 ReconcileAndMaterialize failed: %v", err)
	}
	if res3.ResourcesCreated != 0 || res3.ResourcesUpdated != 1 {
		t.Errorf("expected 0 created, 1 updated on partial sweep; got %+v", res3)
	}
	if res3.ResourcesTotal != 2 || res3.RelationshipsTotal != 1 {
		t.Errorf("CRITICAL REGRESSION: unobserved resources or relationships were deleted from PostgreSQL! got %+v", res3)
	}

	// Verify in database that backend is still present
	backendResource, err := pgStore.GetResource(ctx, ws, state.ResourceIdentity{
		Provider:     backendSubj.Provider,
		ResourceType: backendSubj.ResourceType,
		ProviderID:   backendSubj.ProviderId,
	})
	if err != nil {
		t.Fatalf("backend resource was deleted from PostgreSQL: %v", err)
	}
	if backendResource == nil {
		t.Fatalf("backend resource must not be nil in PostgreSQL")
	}

	// 4. Verify Rust Core Engine impact remains intact after materialization
	answerSvc := answer.NewService(client, logger)
	ans, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: ws,
		Target: &answer.ResourceIdentity{
			Provider:     backendSubj.Provider,
			ResourceType: backendSubj.ResourceType,
			ProviderID:   backendSubj.ProviderId,
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed: %v", err)
	}
	if ans.Summary.ImpactedCount != 1 {
		t.Errorf("expected 1 impacted resource (frontend) in Rust Core Engine, got %d", ans.Summary.ImpactedCount)
	}
}
