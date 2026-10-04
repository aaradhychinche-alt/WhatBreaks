//! Server setup and execution for WhatBreaks Discovery Service.

use std::net::SocketAddr;
use std::sync::Arc;
use tonic::transport::Server;
use wb_core_engine::DiscoveryEngine;
use wb_core_proto::core_v1::discovery_service_server::DiscoveryServiceServer;

use crate::service::DiscoveryServiceImpl;

/// Create a `DiscoveryServiceServer` wrapping a `DiscoveryServiceImpl` with the given engine.
pub fn create_service(
    engine: Arc<DiscoveryEngine>,
) -> DiscoveryServiceServer<DiscoveryServiceImpl> {
    DiscoveryServiceServer::new(DiscoveryServiceImpl::new(engine))
}

/// Run the gRPC server on the specified address with default v1 DiscoveryEngine.
pub async fn run_server(addr: SocketAddr) -> Result<(), Box<dyn std::error::Error>> {
    let engine = Arc::new(DiscoveryEngine::default_v1());
    let svc = create_service(engine);

    Server::builder().add_service(svc).serve(addr).await?;

    Ok(())
}
