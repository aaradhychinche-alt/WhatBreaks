//! Implementation of the generated DiscoveryService gRPC trait.

use std::sync::Arc;
use tonic::{Request, Response, Status};
use wb_core_engine::DiscoveryEngine;
use wb_core_proto::core_v1::discovery_service_server::DiscoveryService;
use wb_core_proto::core_v1::{RunDiscoveryRequest, RunDiscoveryResponse};

use crate::convert::{discovery_result_to_proto, proto_to_evidence};

/// Concrete implementation of `wb.core.v1.DiscoveryService`.
///
/// Owns an `Arc<DiscoveryEngine>` and delegates discovery logic to it.
/// Does not implement or duplicate discovery rules — acts solely as an adapter
/// between protobuf transport and the domain engine.
#[derive(Clone)]
pub struct DiscoveryServiceImpl {
    engine: Arc<DiscoveryEngine>,
}

impl DiscoveryServiceImpl {
    /// Create a new `DiscoveryServiceImpl` wrapping the provided engine.
    pub fn new(engine: Arc<DiscoveryEngine>) -> Self {
        Self { engine }
    }

    /// Create a `DiscoveryServiceImpl` with the default v1 rule set.
    pub fn with_default_engine() -> Self {
        Self::new(Arc::new(DiscoveryEngine::default_v1()))
    }

    /// Return a reference to the inner `DiscoveryEngine`.
    pub fn engine(&self) -> &DiscoveryEngine {
        &self.engine
    }
}

#[tonic::async_trait]
impl DiscoveryService for DiscoveryServiceImpl {
    async fn run_discovery(
        &self,
        request: Request<RunDiscoveryRequest>,
    ) -> Result<Response<RunDiscoveryResponse>, Status> {
        let req = request.into_inner();

        // 1. Convert and validate all evidence observations.
        // Fails fast with Status::invalid_argument on any malformed input.
        let evidence: Vec<wb_core_engine::Evidence> = req
            .evidence
            .into_iter()
            .map(proto_to_evidence)
            .collect::<Result<Vec<_>, Status>>()?;

        // 2. Delegate to the authoritative DiscoveryEngine.
        let results = self.engine.run(&evidence);

        // 3. Convert domain results to protobuf responses.
        let proto_results = results.into_iter().map(discovery_result_to_proto).collect();

        Ok(Response::new(RunDiscoveryResponse {
            results: proto_results,
        }))
    }
}

// ---------------------------------------------------------------------------
// AnswerService Implementation
// ---------------------------------------------------------------------------

use std::collections::HashMap;
use std::sync::RwLock;
use wb_core_engine::answer::AnswerEngine;
use wb_core_engine::{Evidence, Graph, ProvenanceStore};
use wb_core_proto::core_v1::answer_service_server::AnswerService;
use wb_core_proto::core_v1::{
    AnalyzeImpactRequest, AnalyzeImpactResponse, LoadStateRequest, LoadStateResponse,
};

use crate::convert::{
    impact_answer_to_proto, proto_to_answer_request, proto_to_relationship,
    proto_to_relationship_evidence,
};

/// Tenant state encapsulating the authoritative Graph, ProvenanceStore, and Evidence.
#[derive(Clone, Default)]
pub struct TenantState {
    pub graph: Graph,
    pub provenance: ProvenanceStore,
    pub evidence: Vec<Evidence>,
}

/// Concrete implementation of `wb.core.v1.AnswerService`.
///
/// Holds in-memory Graph, ProvenanceStore, and Evidence state per workspace (tenant)
/// and delegates impact analysis to `AnswerEngine::analyze`.
#[derive(Clone, Default)]
pub struct AnswerServiceImpl {
    tenants: Arc<RwLock<HashMap<String, TenantState>>>,
}

impl AnswerServiceImpl {
    /// Create a new `AnswerServiceImpl` wrapping the provided tenant map.
    pub fn new(tenants: Arc<RwLock<HashMap<String, TenantState>>>) -> Self {
        Self { tenants }
    }

    /// Create an `AnswerServiceImpl` initialized with specific domain state in the default tenant ("").
    pub fn with_state(graph: Graph, provenance: ProvenanceStore, evidence: Vec<Evidence>) -> Self {
        let mut map = HashMap::new();
        map.insert(
            "".to_string(),
            TenantState {
                graph,
                provenance,
                evidence,
            },
        );
        Self {
            tenants: Arc::new(RwLock::new(map)),
        }
    }

    /// Update the default tenant state atomically.
    pub fn set_state(&self, graph: Graph, provenance: ProvenanceStore, evidence: Vec<Evidence>) {
        self.set_workspace_state("", graph, provenance, evidence);
    }

    /// Update a specific workspace tenant state atomically.
    pub fn set_workspace_state(
        &self,
        workspace_id: &str,
        graph: Graph,
        provenance: ProvenanceStore,
        evidence: Vec<Evidence>,
    ) {
        if let Ok(mut map) = self.tenants.write() {
            map.insert(
                workspace_id.to_string(),
                TenantState {
                    graph,
                    provenance,
                    evidence,
                },
            );
        }
    }
}

#[tonic::async_trait]
impl AnswerService for AnswerServiceImpl {
    async fn analyze_impact(
        &self,
        request: Request<AnalyzeImpactRequest>,
    ) -> Result<Response<AnalyzeImpactResponse>, Status> {
        let req = request.into_inner();
        let ws_id = req.workspace_id.clone();
        let domain_req = proto_to_answer_request(req)?;

        let tenants = self
            .tenants
            .read()
            .map_err(|_| Status::internal("tenants lock poisoned"))?;

        let empty_state = TenantState::default();
        let tenant_state = tenants.get(&ws_id).unwrap_or(&empty_state);

        let answer = AnswerEngine::analyze(
            &tenant_state.graph,
            &tenant_state.provenance,
            &tenant_state.evidence,
            &domain_req,
        );
        let proto_resp = impact_answer_to_proto(answer);

        Ok(Response::new(proto_resp))
    }

    async fn load_state(
        &self,
        request: Request<LoadStateRequest>,
    ) -> Result<Response<LoadStateResponse>, Status> {
        let req = request.into_inner();
        let workspace_id = req.workspace_id;

        let mut graph = Graph::new();
        let mut provenance = ProvenanceStore::new();

        let mut rel_count = 0u32;
        for rel_proto in req.relationships {
            let rel = proto_to_relationship(rel_proto)?;
            graph.add_relationship(rel.clone());
            provenance.add_relationship(rel);
            rel_count += 1;
        }

        let mut prov_count = 0u32;
        for assoc in req.associations {
            let (rel, ev_ids) = proto_to_relationship_evidence(assoc)?;
            graph.add_relationship(rel.clone());
            provenance.add_relationship(rel.clone());
            for ev_id in ev_ids {
                provenance.add_evidence(&rel, ev_id);
                prov_count += 1;
            }
        }

        let mut ev_count = 0u32;
        let mut evidence = Vec::with_capacity(req.evidence.len());
        for ev_proto in req.evidence {
            let ev = proto_to_evidence(ev_proto)?;
            evidence.push(ev);
            ev_count += 1;
        }

        self.set_workspace_state(&workspace_id, graph, provenance, evidence);

        Ok(Response::new(LoadStateResponse {
            workspace_id,
            relationships_loaded: rel_count,
            evidence_loaded: ev_count,
            provenance_links_loaded: prov_count,
        }))
    }
}
