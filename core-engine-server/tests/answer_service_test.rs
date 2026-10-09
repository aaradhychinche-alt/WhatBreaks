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
        workspace_id: "".to_string(),
        proposed_change: None,
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
        workspace_id: "".to_string(),
        proposed_change: None,
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
        workspace_id: "".to_string(),
        proposed_change: None,
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
        workspace_id: "".to_string(),
        proposed_change: None,
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
        workspace_id: "".to_string(),
        proposed_change: None,
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
        workspace_id: "".to_string(),
        proposed_change: None,
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

#[tokio::test]
async fn test_rpc_load_state_and_workspace_isolation() {
    let answer_svc = AnswerServiceImpl::default();

    let ev_id_str = "550e8400-e29b-41d4-a716-446655440099";
    let ev = proto::Evidence {
        id: ev_id_str.to_string(),
        source: Some(proto::EvidenceSource {
            provider: "kubernetes".to_string(),
            collector: "k8s-runtime".to_string(),
        }),
        observed_at: "2026-10-06T12:00:00Z".to_string(),
        observation_type: "RUNTIME_CONNECTION".to_string(),
        subject: Some(proto_identity("kubernetes", "service", "orders")),
        data: serde_json::to_vec(&serde_json::json!({"dest": "db"})).unwrap(),
    };

    let rel = proto::Relationship {
        source: Some(proto_identity("kubernetes", "service", "orders")),
        target: Some(proto_identity("kubernetes", "service", "catalog")),
        kind: "DEPENDS_ON".to_string(),
        category: "Dependency".to_string(),
    };

    let assoc = proto::RelationshipEvidence {
        relationship: Some(rel.clone()),
        evidence_ids: vec![ev_id_str.to_string()],
    };

    // Load state into workspace-A
    let load_req = Request::new(proto::LoadStateRequest {
        workspace_id: "workspace-A".to_string(),
        relationships: vec![rel],
        associations: vec![assoc],
        evidence: vec![ev],
    });

    let load_resp = answer_svc.load_state(load_req).await.unwrap().into_inner();
    assert_eq!(load_resp.workspace_id, "workspace-A");
    assert_eq!(load_resp.relationships_loaded, 1);
    assert_eq!(load_resp.evidence_loaded, 1);
    assert_eq!(load_resp.provenance_links_loaded, 1);

    // Query impact for workspace-A -> should find orders impacted
    let req_a = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "catalog")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "workspace-A".to_string(),
        proposed_change: None,
    });
    let resp_a = answer_svc.analyze_impact(req_a).await.unwrap().into_inner();
    assert_eq!(resp_a.summary.unwrap().impacted_count, 1);
    assert_eq!(resp_a.relationships.len(), 1);
    assert_eq!(resp_a.relationships[0].state, "Supported");

    // Query impact for workspace-B (identical target identity) -> MUST BE ISOLATED (0 impacted)
    let req_b = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "catalog")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "workspace-B".to_string(),
        proposed_change: None,
    });
    let resp_b = answer_svc.analyze_impact(req_b).await.unwrap().into_inner();
    assert_eq!(resp_b.summary.unwrap().impacted_count, 0);
    assert!(resp_b.impacted_resources.is_empty());
    assert!(resp_b.relationships.is_empty());
}

#[tokio::test]
async fn test_rpc_change_aware_delete_vs_update() {
    let answer_svc = AnswerServiceImpl::default();

    // Setup graph: frontend -> api -> db
    let rel1 = proto::Relationship {
        source: Some(proto_identity("kubernetes", "service", "api")),
        target: Some(proto_identity("kubernetes", "service", "db")),
        kind: "DEPENDS_ON".to_string(),
        category: "Dependency".to_string(),
    };
    let rel2 = proto::Relationship {
        source: Some(proto_identity("kubernetes", "service", "frontend")),
        target: Some(proto_identity("kubernetes", "service", "api")),
        kind: "DEPENDS_ON".to_string(),
        category: "Dependency".to_string(),
    };

    let load_req = Request::new(proto::LoadStateRequest {
        workspace_id: "test-ws".to_string(),
        relationships: vec![rel1, rel2],
        associations: vec![],
        evidence: vec![],
    });
    answer_svc.load_state(load_req).await.unwrap();

    // 1. DELETE on db -> should reach api (depth 1) and frontend (depth 2)
    let req_delete = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: proto::ChangeType::Delete as i32,
            details: "deleting db instance".to_string(),
        }),
    });
    let resp_delete = answer_svc
        .analyze_impact(req_delete)
        .await
        .unwrap()
        .into_inner();
    assert_eq!(resp_delete.summary.unwrap().impacted_count, 2);
    let assessment = resp_delete.change_assessment.unwrap();
    assert_eq!(assessment.change_type, proto::ChangeType::Delete as i32);
    assert_eq!(assessment.impact_nature, "POTENTIAL_IMPACT");
    assert!(!assessment.assumptions.is_empty());
    assert!(!assessment.limitations.is_empty());

    // Check impacted resources have impact_type and impact_reason
    assert_eq!(resp_delete.impacted_resources[0].impact_type, "DIRECT");
    assert!(!resp_delete.impacted_resources[0].impact_reason.is_empty());
    assert_eq!(resp_delete.impacted_resources[1].impact_type, "INDIRECT");

    // 2. UPDATE on db -> should reach ONLY api (depth 1), frontend suppressed in v1
    let req_update = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: proto::ChangeType::Update as i32,
            details: "updating connection pool".to_string(),
        }),
    });
    let resp_update = answer_svc
        .analyze_impact(req_update)
        .await
        .unwrap()
        .into_inner();
    assert_eq!(resp_update.summary.unwrap().impacted_count, 1);
    assert_eq!(
        resp_update.impacted_resources[0]
            .resource
            .as_ref()
            .unwrap()
            .provider_id,
        "api"
    );
    assert_eq!(resp_update.impacted_resources[0].impact_type, "DIRECT");

    // 3. SCALE on db -> db only has DEPENDS_ON incoming, SCALE requires CALLS => 0 impacted
    let req_scale = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: proto::ChangeType::Scale as i32,
            details: "scaling replicas".to_string(),
        }),
    });
    let resp_scale = answer_svc
        .analyze_impact(req_scale)
        .await
        .unwrap()
        .into_inner();
    assert_eq!(resp_scale.summary.unwrap().impacted_count, 0);
    let scale_assessment = resp_scale.change_assessment.unwrap();
    assert_eq!(
        scale_assessment.change_type,
        proto::ChangeType::Scale as i32
    );
    assert_eq!(scale_assessment.impact_nature, "POTENTIAL_IMPACT");
    let has_dep_explanation = scale_assessment.limitations.iter().any(|l| {
        l.contains("declarative dependencies (e.g. DEPENDS_ON)")
            && l.contains("no runtime CALLS relationships were observed")
    });
    assert!(
        has_dep_explanation,
        "limitations: {:?}",
        scale_assessment.limitations
    );

    // 3b. REPLACE on db -> reaches api (depth 1) only, frontend (depth 2) suppressed
    let req_replace = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: proto::ChangeType::Replace as i32,
            details: "replacing db instance".to_string(),
        }),
    });
    let resp_replace = answer_svc
        .analyze_impact(req_replace)
        .await
        .unwrap()
        .into_inner();
    assert_eq!(resp_replace.summary.unwrap().impacted_count, 1);
    assert_eq!(
        resp_replace.impacted_resources[0]
            .resource
            .as_ref()
            .unwrap()
            .provider_id,
        "api"
    );
    assert_eq!(resp_replace.impacted_resources[0].depth, 1);
    assert_eq!(resp_replace.impacted_resources[0].impact_type, "DIRECT");
    assert!(resp_replace.impacted_resources[0]
        .impact_reason
        .contains("transient rollover"));
    let replace_assessment = resp_replace.change_assessment.unwrap();
    assert_eq!(
        replace_assessment.change_type,
        proto::ChangeType::Replace as i32
    );

    // 3c. Legacy request without proposed_change -> change_assessment is None, impact_type is empty
    let req_legacy = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: None,
    });
    let resp_legacy = answer_svc
        .analyze_impact(req_legacy)
        .await
        .unwrap()
        .into_inner();
    assert_eq!(resp_legacy.summary.unwrap().impacted_count, 2);
    assert!(resp_legacy.change_assessment.is_none());
    assert_eq!(resp_legacy.impacted_resources[0].impact_type, "");
    assert_eq!(resp_legacy.impacted_resources[0].impact_reason, "");

    // 4. Unspecified change_type -> must return InvalidArgument
    let req_unspecified = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: proto::ChangeType::Unspecified as i32,
            details: "".to_string(),
        }),
    });
    let err = answer_svc
        .analyze_impact(req_unspecified)
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("cannot be unspecified"));

    // 5. Unsupported change_type integer -> must return InvalidArgument
    let req_invalid = Request::new(AnalyzeImpactRequest {
        target: Some(proto_identity("kubernetes", "service", "db")),
        direction: "incoming".to_string(),
        max_depth: 3,
        workspace_id: "test-ws".to_string(),
        proposed_change: Some(proto::ProposedChange {
            change_type: 999,
            details: "".to_string(),
        }),
    });
    let err_invalid = answer_svc.analyze_impact(req_invalid).await.unwrap_err();
    assert_eq!(err_invalid.code(), Code::InvalidArgument);
    assert!(err_invalid.message().contains("unsupported change_type"));
}
