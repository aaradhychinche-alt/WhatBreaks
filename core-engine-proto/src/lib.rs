//! WhatBreaks Core Engine – protobuf/gRPC generated types
//!
//! This crate contains ONLY the generated protobuf message types and gRPC
//! service stubs produced from `proto/wb/core/v1/core.proto`.
//!
//! # Separation of concerns
//!
//! The existing [`wb-core-engine`] crate contains the authoritative Rust domain
//! types (`Resource`, `Relationship`, `Evidence`, `DiscoveryResult`, etc.).
//! This crate contains the **transport** types used at the Go ↔ Rust gRPC
//! boundary. The two type systems are intentionally separate:
//!
//! - Domain types own business logic (derivation, validation, etc.).
//! - Proto types are plain data containers for serialization over the wire.
//!
//! Conversion between domain types and proto types will live in the future
//! Rust gRPC server crate (not here and not in `wb-core-engine`).
//!
//! # What this crate does NOT contain
//!
//! - No domain logic.
//! - No ResourceId (internal WB UUID — must never cross the process boundary).
//! - No confidence scores.
//! - No graph node IDs.
//! - No gRPC server implementation (future work).
//! - No gRPC client implementation (future work).

/// Generated protobuf types and gRPC stubs for the `wb.core.v1` package.
///
/// All types in this module are auto-generated from
/// `proto/wb/core/v1/core.proto` by `tonic-build` during `cargo build`.
/// Do not edit the generated code directly — edit the `.proto` file instead.
pub mod wb {
    pub mod core {
        pub mod v1 {
            // tonic-build output is placed here at build time.
            tonic::include_proto!("wb.core.v1");
        }
    }
}

// Re-export the v1 module at the crate root for convenience.
pub use wb::core::v1 as core_v1;

#[cfg(test)]
mod tests {
    use super::core_v1::*;
    use prost::Message;

    #[test]
    fn test_resource_identity_roundtrip() {
        let identity = ResourceIdentity {
            provider: "kubernetes".to_string(),
            resource_type: "pod".to_string(),
            provider_id: "cluster/prod/namespace/payments/pod/api-abc".to_string(),
        };

        let mut buf = Vec::new();
        identity.encode(&mut buf).unwrap();

        let decoded = ResourceIdentity::decode(&buf[..]).unwrap();
        assert_eq!(decoded.provider, "kubernetes");
        assert_eq!(decoded.resource_type, "pod");
        assert_eq!(decoded.provider_id, "cluster/prod/namespace/payments/pod/api-abc");
    }

    #[test]
    fn test_evidence_roundtrip() {
        let ev = Evidence {
            id: "550e8400-e29b-41d4-a716-446655440000".to_string(),
            source: Some(EvidenceSource {
                provider: "kubernetes".to_string(),
                collector: "k8s-runtime".to_string(),
            }),
            observed_at: "2026-10-04T12:00:00Z".to_string(),
            observation_type: "RUNTIME_CONNECTION".to_string(),
            subject: Some(ResourceIdentity {
                provider: "kubernetes".to_string(),
                resource_type: "pod".to_string(),
                provider_id: "default/frontend-xyz".to_string(),
            }),
            data: br#"{"remote_ip":"10.0.0.1","remote_port":5432}"#.to_vec(),
        };

        let mut buf = Vec::new();
        ev.encode(&mut buf).unwrap();

        let decoded = Evidence::decode(&buf[..]).unwrap();
        assert_eq!(decoded.id, "550e8400-e29b-41d4-a716-446655440000");
        assert_eq!(decoded.observation_type, "RUNTIME_CONNECTION");
        assert_eq!(decoded.data, br#"{"remote_ip":"10.0.0.1","remote_port":5432}"#);
    }

    #[test]
    fn test_discovery_result_variants() {
        let discovered_result = DiscoveryResult {
            outcome: Some(discovery_result::Outcome::Discovered(DiscoveredRelationship {
                relationship: Some(Relationship {
                    source: Some(ResourceIdentity {
                        provider: "kubernetes".to_string(),
                        resource_type: "pod".to_string(),
                        provider_id: "default/app".to_string(),
                    }),
                    target: Some(ResourceIdentity {
                        provider: "aws".to_string(),
                        resource_type: "rds".to_string(),
                        provider_id: "arn:aws:rds:us-east-1:123456789012:db:main".to_string(),
                    }),
                    kind: "DEPENDS_ON".to_string(),
                    category: "Dependency".to_string(),
                }),
                supporting_evidence_ids: vec!["ev-1".to_string(), "ev-2".to_string()],
            })),
        };

        let mut buf = Vec::new();
        discovered_result.encode(&mut buf).unwrap();
        let decoded = DiscoveryResult::decode(&buf[..]).unwrap();
        match decoded.outcome {
            Some(discovery_result::Outcome::Discovered(dr)) => {
                assert_eq!(dr.supporting_evidence_ids.len(), 2);
                let rel = dr.relationship.unwrap();
                assert_eq!(rel.kind, "DEPENDS_ON");
                assert_eq!(rel.category, "Dependency");
            }
            _ => panic!("Expected Discovered variant"),
        }

        let conflict_result = DiscoveryResult {
            outcome: Some(discovery_result::Outcome::Conflict(Conflict {
                description: "Ambiguous mapping".to_string(),
            })),
        };
        buf.clear();
        conflict_result.encode(&mut buf).unwrap();
        let decoded_conflict = DiscoveryResult::decode(&buf[..]).unwrap();
        match decoded_conflict.outcome {
            Some(discovery_result::Outcome::Conflict(c)) => {
                assert_eq!(c.description, "Ambiguous mapping");
            }
            _ => panic!("Expected Conflict variant"),
        }
    }

    #[test]
    fn test_run_discovery_request_response_roundtrip() {
        let req = RunDiscoveryRequest {
            evidence: vec![Evidence {
                id: "ev-1".to_string(),
                source: None,
                observed_at: "2026-10-04T12:00:00Z".to_string(),
                observation_type: "TEST".to_string(),
                subject: None,
                data: vec![],
            }],
        };

        let mut buf = Vec::new();
        req.encode(&mut buf).unwrap();
        let decoded_req = RunDiscoveryRequest::decode(&buf[..]).unwrap();
        assert_eq!(decoded_req.evidence.len(), 1);

        let res = RunDiscoveryResponse {
            results: vec![DiscoveryResult {
                outcome: Some(discovery_result::Outcome::Insufficient(Insufficient {})),
            }],
        };
        buf.clear();
        res.encode(&mut buf).unwrap();
        let decoded_res = RunDiscoveryResponse::decode(&buf[..]).unwrap();
        assert_eq!(decoded_res.results.len(), 1);
    }
}

