use std::net::SocketAddr;
use std::sync::Arc;
use tonic::{Code, Request};
use wb_core_engine::DiscoveryEngine;
use wb_core_proto::core_v1::discovery_service_client::DiscoveryServiceClient;
use wb_core_proto::core_v1::discovery_service_server::DiscoveryService;
use wb_core_proto::core_v1::{self as proto, RunDiscoveryRequest};
use wb_core_server::create_service;
use wb_core_server::DiscoveryServiceImpl;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn k8s_source() -> proto::EvidenceSource {
    proto::EvidenceSource {
        provider: "kubernetes".to_string(),
        collector: "k8s-runtime".to_string(),
    }
}

fn aws_source() -> proto::EvidenceSource {
    proto::EvidenceSource {
        provider: "aws".to_string(),
        collector: "aws-network".to_string(),
    }
}

fn pod_subject(name: &str) -> proto::ResourceIdentity {
    proto::ResourceIdentity {
        provider: "kubernetes".to_string(),
        resource_type: "pod".to_string(),
        provider_id: format!("payments/{name}"),
    }
}

fn db_subject(name: &str) -> proto::ResourceIdentity {
    proto::ResourceIdentity {
        provider: "aws".to_string(),
        resource_type: "rds".to_string(),
        provider_id: format!("arn:aws:rds:us-east-1:123:{name}"),
    }
}

fn make_connection_evidence(
    id: &str,
    subject: proto::ResourceIdentity,
    dest: &str,
    port: u16,
) -> proto::Evidence {
    let data = serde_json::json!({
        "destination": dest,
        "port": port,
        "protocol": "tcp"
    });

    proto::Evidence {
        id: id.to_string(),
        source: Some(k8s_source()),
        observed_at: "2026-10-04T12:00:00Z".to_string(),
        observation_type: "RUNTIME_CONNECTION".to_string(),
        subject: Some(subject),
        data: serde_json::to_vec(&data).unwrap(),
    }
}

fn make_mapping_evidence(
    id: &str,
    subject: proto::ResourceIdentity,
    address: &str,
    port: u16,
) -> proto::Evidence {
    let data = serde_json::json!({
        "address": address,
        "port": port
    });

    proto::Evidence {
        id: id.to_string(),
        source: Some(aws_source()),
        observed_at: "2026-10-04T12:00:01Z".to_string(),
        observation_type: "RESOURCE_REFERENCE".to_string(),
        subject: Some(subject),
        data: serde_json::to_vec(&data).unwrap(),
    }
}

// ---------------------------------------------------------------------------
// Test A: Discovered Relationship
// ---------------------------------------------------------------------------
#[tokio::test]
async fn test_rpc_discovered_relationship() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let conn_id = "550e8400-e29b-41d4-a716-446655440001";
    let map_id = "550e8400-e29b-41d4-a716-446655440002";

    let conn = make_connection_evidence(conn_id, pod_subject("payments-api"), "10.0.2.15", 5432);
    let map = make_mapping_evidence(map_id, db_subject("payments-db"), "10.0.2.15", 5432);

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn, map],
    });

    let resp = service.run_discovery(req).await.unwrap().into_inner();

    assert_eq!(resp.results.len(), 1);
    let result = &resp.results[0];

    match &result.outcome {
        Some(proto::discovery_result::Outcome::Discovered(dr)) => {
            let rel = dr
                .relationship
                .as_ref()
                .expect("relationship must be present");

            // Verify source
            let src = rel.source.as_ref().expect("source must be present");
            assert_eq!(src.provider, "kubernetes");
            assert_eq!(src.resource_type, "pod");
            assert_eq!(src.provider_id, "payments/payments-api");

            // Verify target
            let tgt = rel.target.as_ref().expect("target must be present");
            assert_eq!(tgt.provider, "aws");
            assert_eq!(tgt.resource_type, "rds");
            assert_eq!(tgt.provider_id, "arn:aws:rds:us-east-1:123:payments-db");

            // Verify relationship kind & category
            assert_eq!(rel.kind, "DEPENDS_ON");
            assert_eq!(rel.category, "Dependency");

            // Verify supporting evidence IDs
            assert_eq!(dr.supporting_evidence_ids.len(), 2);
            assert!(dr.supporting_evidence_ids.contains(&conn_id.to_string()));
            assert!(dr.supporting_evidence_ids.contains(&map_id.to_string()));
        }
        other => panic!("expected Discovered outcome, got {:?}", other),
    }
}

// ---------------------------------------------------------------------------
// Test B: Insufficient Evidence
// ---------------------------------------------------------------------------
#[tokio::test]
async fn test_rpc_insufficient_evidence() {
    let service = DiscoveryServiceImpl::with_default_engine();

    // Endpoints do not match (10.0.2.15 vs 10.0.2.99)
    let conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440010",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    let map = make_mapping_evidence(
        "550e8400-e29b-41d4-a716-446655440011",
        db_subject("payments-db"),
        "10.0.2.99",
        5432,
    );

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn, map],
    });

    let resp = service.run_discovery(req).await.unwrap().into_inner();

    assert_eq!(resp.results.len(), 1);
    match &resp.results[0].outcome {
        Some(proto::discovery_result::Outcome::Insufficient(_)) => {
            // Success: insufficient outcome returned
        }
        other => panic!("expected Insufficient outcome, got {:?}", other),
    }
}

// ---------------------------------------------------------------------------
// Test C: Conflict
// ---------------------------------------------------------------------------
#[tokio::test]
async fn test_rpc_conflict() {
    let service = DiscoveryServiceImpl::with_default_engine();

    // Two different database resources claim the exact same endpoint
    let conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440020",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    let map_b = make_mapping_evidence(
        "550e8400-e29b-41d4-a716-446655440021",
        db_subject("database-b"),
        "10.0.2.15",
        5432,
    );
    let map_c = make_mapping_evidence(
        "550e8400-e29b-41d4-a716-446655440022",
        db_subject("database-c"),
        "10.0.2.15",
        5432,
    );

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn, map_b, map_c],
    });

    let resp = service.run_discovery(req).await.unwrap().into_inner();

    assert_eq!(resp.results.len(), 1);
    match &resp.results[0].outcome {
        Some(proto::discovery_result::Outcome::Conflict(c)) => {
            assert!(c.description.contains("10.0.2.15"));
            assert!(c.description.contains("5432"));
        }
        other => panic!("expected Conflict outcome, got {:?}", other),
    }
}

// ---------------------------------------------------------------------------
// Test D: Invalid Input Handling (No panics, return InvalidArgument)
// ---------------------------------------------------------------------------
#[tokio::test]
async fn test_rpc_malformed_uuid() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.id = "not-a-valid-uuid".to_string();

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("malformed evidence id"));
}

#[tokio::test]
async fn test_rpc_malformed_timestamp() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.observed_at = "not-an-rfc3339-timestamp".to_string();

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("malformed observed_at timestamp"));
}

#[tokio::test]
async fn test_rpc_malformed_json_data() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.data = b"not-valid-json{{".to_vec();

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("malformed evidence data JSON"));
}

#[tokio::test]
async fn test_rpc_empty_data() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.data = vec![];

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err.message().contains("evidence.data cannot be empty"));
}

#[tokio::test]
async fn test_rpc_missing_source() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.source = None;

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err
        .message()
        .contains("missing required field: evidence.source"));
}

#[tokio::test]
async fn test_rpc_missing_subject() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.subject = None;

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err
        .message()
        .contains("missing required field: evidence.subject"));
}

#[tokio::test]
async fn test_rpc_empty_resource_identity_field() {
    let service = DiscoveryServiceImpl::with_default_engine();

    let mut conn = make_connection_evidence(
        "550e8400-e29b-41d4-a716-446655440001",
        pod_subject("payments-api"),
        "10.0.2.15",
        5432,
    );
    conn.subject = Some(proto::ResourceIdentity {
        provider: "".to_string(),
        resource_type: "pod".to_string(),
        provider_id: "payments/api".to_string(),
    });

    let req = Request::new(RunDiscoveryRequest {
        evidence: vec![conn],
    });

    let err = service.run_discovery(req).await.unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert!(err
        .message()
        .contains("resource_identity.provider cannot be empty"));
}

// ---------------------------------------------------------------------------
// In-Process Real gRPC Transport Test (Network Server + Client)
// ---------------------------------------------------------------------------
#[tokio::test]
async fn test_in_process_grpc_server_and_client_roundtrip() {
    // 1. Bind to ephemeral port
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr: SocketAddr = listener.local_addr().unwrap();

    let engine = Arc::new(DiscoveryEngine::default_v1());
    let svc = create_service(engine);

    // 2. Spawn tonic server on background task
    tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(svc)
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener))
            .await
            .unwrap();
    });

    // 3. Connect real generated gRPC client
    let mut client = DiscoveryServiceClient::connect(format!("http://{}", addr))
        .await
        .expect("gRPC client must connect to in-process server");

    let conn_id = "550e8400-e29b-41d4-a716-446655440001";
    let map_id = "550e8400-e29b-41d4-a716-446655440002";

    let conn = make_connection_evidence(conn_id, pod_subject("payments-api"), "10.0.2.15", 5432);
    let map = make_mapping_evidence(map_id, db_subject("payments-db"), "10.0.2.15", 5432);

    let req = RunDiscoveryRequest {
        evidence: vec![conn, map],
    };

    // 4. Execute RPC over the actual TCP/HTTP2 wire
    let resp = client.run_discovery(req).await.unwrap().into_inner();

    assert_eq!(resp.results.len(), 1);
    match &resp.results[0].outcome {
        Some(proto::discovery_result::Outcome::Discovered(dr)) => {
            let rel = dr.relationship.as_ref().unwrap();
            assert_eq!(rel.kind, "DEPENDS_ON");
            assert_eq!(rel.category, "Dependency");
            assert_eq!(dr.supporting_evidence_ids.len(), 2);
        }
        other => panic!("expected Discovered outcome over wire, got {:?}", other),
    }

    // 5. Verify error transmission over wire
    let malformed_req = RunDiscoveryRequest {
        evidence: vec![proto::Evidence {
            id: "bad-uuid".to_string(),
            source: None,
            observed_at: "".to_string(),
            observation_type: "".to_string(),
            subject: None,
            data: vec![],
        }],
    };

    let status = client.run_discovery(malformed_req).await.unwrap_err();
    assert_eq!(status.code(), Code::InvalidArgument);
}
