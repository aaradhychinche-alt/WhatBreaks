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
