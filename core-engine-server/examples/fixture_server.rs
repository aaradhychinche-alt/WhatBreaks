//! Standalone test fixture server for cross-process integration tests.
//!
//! Exposes AnswerService with a pre-populated deterministic graph containing
//! both Supported and Unknown relationships, multi-hop paths, and supporting evidence.

use std::env;
use std::net::SocketAddr;
use wb_core_engine::evidence::{CollectorId, Evidence, EvidenceSource, ObservationType};
use wb_core_engine::relationship::{Relationship, RelationshipKind};
use wb_core_engine::resource::{Provider, ResourceIdentity, ResourceKind};
use wb_core_engine::{Graph, ProvenanceStore};
use wb_core_server::{create_answer_service, AnswerServiceImpl};

fn domain_identity(provider: &str, kind: &str, id: &str) -> ResourceIdentity {
    ResourceIdentity::new(
        Provider::new(provider),
        ResourceKind::new(kind),
        id.to_string(),
    )
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let addr_str = env::var("WB_CORE_GRPC_ADDR").unwrap_or_else(|_| "127.0.0.1:50052".to_string());
    let addr: SocketAddr = addr_str.parse()?;

    let mut graph = Graph::new();
    let mut prov = ProvenanceStore::new();

    let target = domain_identity("kubernetes", "service", "payments-api");
    let checkout = domain_identity("kubernetes", "service", "checkout-service");
    let frontend = domain_identity("kubernetes", "service", "frontend");

    let rel1 = Relationship::new(
        checkout.clone(),
        target.clone(),
        RelationshipKind::DEPENDS_ON,
    );
    let rel2 = Relationship::new(
        frontend.clone(),
        checkout.clone(),
        RelationshipKind::DEPENDS_ON,
    );

    graph.add_relationship(rel1.clone());
    graph.add_relationship(rel2.clone());
    prov.add_relationship(rel1.clone());
    prov.add_relationship(rel2.clone());

    let ev1 = Evidence::new(
        EvidenceSource::new(Provider::new("kubernetes"), CollectorId::new("k8s-runtime")),
        chrono::Utc::now(),
        ObservationType::RUNTIME_CONNECTION,
        checkout.clone(),
        serde_json::json!({ "endpoint": "10.0.0.1:8080" }),
    );
    prov.add_evidence(&rel1, ev1.id);
    // rel2 has no evidence, so its state will derive as Unknown

    let evidence_catalog = vec![ev1];

    let answer_svc = AnswerServiceImpl::with_state(graph, prov, evidence_catalog);

    tonic::transport::Server::builder()
        .add_service(create_answer_service(answer_svc))
        .serve(addr)
        .await?;

    Ok(())
}
