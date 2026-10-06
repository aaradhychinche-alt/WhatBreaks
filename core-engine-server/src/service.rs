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

use std::sync::RwLock;
use wb_core_engine::answer::AnswerEngine;
use wb_core_engine::{Evidence, Graph, ProvenanceStore};
use wb_core_proto::core_v1::answer_service_server::AnswerService;
use wb_core_proto::core_v1::{AnalyzeImpactRequest, AnalyzeImpactResponse};

use crate::convert::{impact_answer_to_proto, proto_to_answer_request};

/// Concrete implementation of `wb.core.v1.AnswerService`.
///
/// Holds in-memory Graph, ProvenanceStore, and Evidence state and delegates
/// impact analysis to `AnswerEngine::analyze`.
#[derive(Clone, Default)]
pub struct AnswerServiceImpl {
    graph: Arc<RwLock<Graph>>,
    provenance: Arc<RwLock<ProvenanceStore>>,
    evidence: Arc<RwLock<Vec<Evidence>>>,
}

impl AnswerServiceImpl {
    /// Create a new `AnswerServiceImpl` wrapping the provided state.
    pub fn new(
        graph: Arc<RwLock<Graph>>,
        provenance: Arc<RwLock<ProvenanceStore>>,
        evidence: Arc<RwLock<Vec<Evidence>>>,
    ) -> Self {
        Self {
            graph,
            provenance,
            evidence,
        }
    }

    /// Create an `AnswerServiceImpl` initialized with specific domain state.
    pub fn with_state(graph: Graph, provenance: ProvenanceStore, evidence: Vec<Evidence>) -> Self {
        Self {
            graph: Arc::new(RwLock::new(graph)),
            provenance: Arc::new(RwLock::new(provenance)),
            evidence: Arc::new(RwLock::new(evidence)),
        }
    }

    /// Update the internal state atomically.
    pub fn set_state(&self, graph: Graph, provenance: ProvenanceStore, evidence: Vec<Evidence>) {
        if let Ok(mut g) = self.graph.write() {
            *g = graph;
        }
        if let Ok(mut p) = self.provenance.write() {
            *p = provenance;
        }
        if let Ok(mut e) = self.evidence.write() {
            *e = evidence;
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
        let domain_req = proto_to_answer_request(req)?;

        let graph = self
            .graph
            .read()
            .map_err(|_| Status::internal("graph lock poisoned"))?;
        let provenance = self
            .provenance
            .read()
            .map_err(|_| Status::internal("provenance lock poisoned"))?;
        let evidence = self
            .evidence
            .read()
            .map_err(|_| Status::internal("evidence lock poisoned"))?;

        let answer = AnswerEngine::analyze(&graph, &provenance, &evidence, &domain_req);
        let proto_resp = impact_answer_to_proto(answer);

        Ok(Response::new(proto_resp))
    }
}
