//! Server setup and execution for WhatBreaks Discovery Service.

use std::net::SocketAddr;
use std::sync::Arc;
use tonic::transport::Server;
use wb_core_engine::DiscoveryEngine;
use wb_core_proto::core_v1::answer_service_server::AnswerServiceServer;
use wb_core_proto::core_v1::discovery_service_server::DiscoveryServiceServer;

use crate::service::{AnswerServiceImpl, DiscoveryServiceImpl};

/// Create a `DiscoveryServiceServer` wrapping a `DiscoveryServiceImpl` with the given engine.
pub fn create_service(
    engine: Arc<DiscoveryEngine>,
) -> DiscoveryServiceServer<DiscoveryServiceImpl> {
    DiscoveryServiceServer::new(DiscoveryServiceImpl::new(engine))
}

/// Create an `AnswerServiceServer` wrapping an `AnswerServiceImpl`.
pub fn create_answer_service(service: AnswerServiceImpl) -> AnswerServiceServer<AnswerServiceImpl> {
    AnswerServiceServer::new(service)
}

/// Run the gRPC server on the specified address with default v2 DiscoveryEngine and default AnswerServiceImpl.
pub async fn run_server(addr: SocketAddr) -> Result<(), Box<dyn std::error::Error>> {
    let engine = Arc::new(DiscoveryEngine::default_v2());
    let discovery_svc = create_service(engine);
    let answer_svc = create_answer_service(AnswerServiceImpl::default());

    Server::builder()
        .add_service(discovery_svc)
        .add_service(answer_svc)
        .serve(addr)
        .await?;

    Ok(())
}
