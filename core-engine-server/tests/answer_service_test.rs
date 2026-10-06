use std::net::SocketAddr;
use tonic::{Code, Request};
use wb_core_engine::evidence::{CollectorId, Evidence, EvidenceSource, ObservationType};
use wb_core_engine::relationship::{Relationship, RelationshipKind};
use wb_core_engine::resource::{Provider, ResourceIdentity, ResourceKind};
use wb_core_engine::{Graph, ProvenanceStore};
use wb_core_proto::core_v1::answer_service_client::AnswerServiceClient;
use wb_core_proto::core_v1::answer_service_server::AnswerService;
use wb_core_proto::core_v1::{self as proto, AnalyzeImpactRequest};
use wb_core_server::{create_answer_service, AnswerServiceImpl};

fn proto_identity(provider: &str, kind: &str, id: &str) -> proto::ResourceIdentity {
    proto::ResourceIdentity {
        provider: provider.to_string(),
        resource_type: kind.to_string(),
        provider_id: id.to_string(),
    }
}

fn domain_identity(provider: &str, kind: &str, id: &str) -> ResourceIdentity {
    ResourceIdentity::new(
        Provider::new(provider),
        ResourceKind::new(kind),
        id.to_string(),
    )
}

fn make_evidence(provider: &str, collector: &str, subject: &ResourceIdentity) -> Evidence {
    Evidence::new(
        EvidenceSource::new(Provider::new(provider), CollectorId::new(collector)),
        chrono::Utc::now(),
        ObservationType::RUNTIME_CONNECTION,
        subject.clone(),
        serde_json::json!({ "active": true }),
    )
}

#[tokio::test]
async fn test_rpc_empty_target() {
    let svc = AnswerServiceImpl::default();
    let req = Request::new(AnalyzeImpactRequest {
        target: None,
        direction: "incoming".to_string(),
        max_depth: 3,
    });
    let err = svc.analyze_impact(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("missing required field: target"));
}

#[tokio::test]
async fn test_rpc_empty_resource_identity_field() {
    let svc = AnswerServiceImpl::default();
    let req = Request::new(AnalyzeImpactRequest {
        target: Some(proto::ResourceIdentity {
            provider: "".to_string(),
            resource_type: "pod".to_string(),
            provider_id: "pod-1".to_string(),
        }),
        direction: "incoming".to_string(),
        max_depth: 3,
    });
    let err = svc.analyze_impact(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("provider cannot be empty"));
}

#[tokio::test]
async fn test_rpc_invalid_direction() {
    let svc = AnswerServiceImpl::default();
    let req = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "api")),
        direction: "sideways".to_string(),
        max_depth: 3,
    });
    let err = svc.analyze_impact(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("invalid traversal direction"));
}

#[tokio::test]
async fn test_rpc_zero_max_depth() {
    let svc = AnswerServiceImpl::default();
    let req = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "api")),
        direction: "incoming".to_string(),
        max_depth: 0,
    });
    let err = svc.analyze_impact(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("max_depth must be greater than 0"));
}

#[tokio::test]
async fn test_rpc_empty_state_returns_empty_answer() {
    let svc = AnswerServiceImpl::default();
    let req = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "api")),
        direction: "incoming".to_string(),
        max_depth: 3,
    });
    let resp = svc.analyze_impact(req).await.unwrap().into_inner();
    assert_eq!(resp.target.unwrap().provider_id, "api");
    assert_eq!(resp.summary.unwrap().impacted_count, 0);
    assert!(resp.impacted_resources.is_empty());
    assert!(resp.relationships.is_empty());
    assert!(resp.paths.is_empty());
    assert!(resp.evidence.is_empty());
    assert!(resp.explanation_facts.is_empty());
}

#[tokio::test]
async fn test_in_process_grpc_server_and_client_roundtrip() {
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

    let ev1 = make_evidence("kubernetes", "k8s-runtime", &checkout);
    let ev2 = make_evidence("kubernetes", "k8s-runtime", &frontend);
    prov.add_evidence(&rel1, ev1.id);
    prov.add_evidence(&rel2, ev2.id);

    let evidence_catalog = vec![ev1.clone(), ev2.clone()];

    let answer_svc = AnswerServiceImpl::with_state(graph, prov, evidence_catalog);

    // Bind to OS-assigned port
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr: SocketAddr = listener.local_addr().unwrap();

    let server_handle = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(create_answer_service(answer_svc))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener))
            .await
            .unwrap();
    });

    let channel = tonic::transport::Channel::from_shared(format!("http://{}", addr))
        .unwrap()
        .connect()
        .await
        .unwrap();

    let mut client = AnswerServiceClient::new(channel);

    let req = AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "payments-api")),
        direction: "incoming".to_string(),
        max_depth: 2,
    };

    let resp = client.analyze_impact(req).await.unwrap().into_inner();

    assert_eq!(resp.target.unwrap().provider_id, "payments-api");
    let summary = resp.summary.unwrap();
    assert_eq!(summary.impacted_count, 2);
    assert_eq!(summary.direct_count, 1);
    assert_eq!(summary.indirect_count, 1);
    assert_eq!(summary.max_depth, 2);

    assert_eq!(resp.impacted_resources.len(), 2);
    assert_eq!(resp.relationships.len(), 2);
    assert_eq!(resp.paths.len(), 2);
    assert_eq!(resp.evidence.len(), 2);
    assert_eq!(resp.explanation_facts.len(), 2);

    // Verify Supported state survived boundary
    for rel in &resp.relationships {
        assert_eq!(rel.state, "Supported");
    }

    server_handle.abort();
}
