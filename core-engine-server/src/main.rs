//! Binary entry point for the WhatBreaks Core Engine gRPC server.

use std::env;
use std::net::SocketAddr;
use wb_core_server::run_server;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let addr_str = env::var("WB_CORE_GRPC_ADDR").unwrap_or_else(|_| "127.0.0.1:50051".to_string());
    let addr: SocketAddr = addr_str.parse().map_err(|e| {
        format!(
            "Invalid socket address in WB_CORE_GRPC_ADDR '{}': {}",
            addr_str, e
        )
    })?;

    println!("Starting WhatBreaks Core gRPC Discovery Server on {}", addr);
    run_server(addr).await?;
    Ok(())
}
