//! WhatBreaks Discovery Engine v1
//!
//! The Discovery Engine applies deterministic rules to a set of [`Evidence`]
//! observations and produces [`DiscoveredRelationship`] results — relationships
//! that are derivable from what was actually observed.
//!
//! # Design Principles
//!
//! - **Deterministic**: No probabilities, confidence scores, fuzzy matching,
//!   LLMs, or heuristics. A relationship is derived only when a rule's
//!   conditions are fully and exactly satisfied.
//!
//! - **Evidence-backed**: Every derived relationship carries the [`EvidenceId`]
//!   references that caused it. Nothing is asserted without an observation trail.
//!
//! - **Conservative**: Insufficient or ambiguous evidence does NOT produce a
//!   relationship. The absence of evidence for a relationship is not treated as
//!   proof that the relationship does not exist.
//!
//! - **Non-mutating**: Discovery never modifies the original [`Evidence`] or
//!   [`Relationship`] models.
//!
//! - **Extensible**: New [`DiscoveryRule`] implementations can be added without
//!   changing the engine or the existing domain models.
//!
//! # Architecture
//!
//! ```text
//! Evidence[]
//!     ↓
//! DiscoveryEngine (applies DiscoveryRule implementations)
//!     ↓
//! DiscoveryResult[] (Discovered | Insufficient | Conflict | Invalid)
//! ```
//!
//! # Scope of v1
//!
//! v1 implements one concrete rule:
//!
//! [`RuntimeConnectionRule`]: If a `RUNTIME_CONNECTION` observation (with
//! `destination` + `port`) is matched by a `RESOURCE_REFERENCE` observation
//! (with `address` + `port`) that maps the same endpoint to a known resource,
//! derive:
//! ```text
//! source --DEPENDS_ON--> target
//! ```
//!
//! [`Evidence`]: crate::evidence::Evidence
//! [`EvidenceId`]: crate::evidence::EvidenceId
//! [`Relationship`]: crate::relationship::Relationship

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

use crate::evidence::{Evidence, EvidenceId, ObservationType};
use crate::relationship::{Relationship, RelationshipKind};
use crate::resource::ResourceIdentity;

// ---------------------------------------------------------------------------
// DiscoveredRelationship
// ---------------------------------------------------------------------------

/// A relationship derived deterministically from evidence observations.
///
/// Preserves the derived [`Relationship`] and the [`EvidenceId`]s of all
/// observations that caused it to be derived. This enables WB to answer:
/// *"Why does this relationship exist?"*
///
/// # What it does NOT contain
///
/// - Confidence scores
/// - Graph node IDs
/// - The full [`Evidence`] records (only IDs are stored — no unnecessary copies)
/// - Discovery state or inference metadata
///
/// [`Evidence`]: crate::evidence::Evidence
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DiscoveredRelationship {
    /// The derived directional relationship.
    pub relationship: Relationship,
    /// The IDs of all evidence observations that support this relationship.
    /// At least one ID is always present.
    pub supporting_evidence: Vec<EvidenceId>,
}

impl DiscoveredRelationship {
    /// Construct a `DiscoveredRelationship`.
    pub fn new(relationship: Relationship, supporting_evidence: Vec<EvidenceId>) -> Self {
        Self {
            relationship,
            supporting_evidence,
        }
    }
}

// ---------------------------------------------------------------------------
// DiscoveryResult
// ---------------------------------------------------------------------------

/// The outcome of applying a discovery rule to a set of evidence.
///
/// Distinguishes four cases that must not be conflated:
///
/// - [`DiscoveryResult::Discovered`]: A relationship was derived.
/// - [`DiscoveryResult::Insufficient`]: Not enough evidence to derive anything.
/// - [`DiscoveryResult::Conflict`]: Evidence exists but is contradictory; the
///   rule cannot deterministically resolve it.
/// - [`DiscoveryResult::Invalid`]: Evidence was structurally malformed and
///   could not be processed. A description is provided for diagnostics.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "outcome", rename_all = "snake_case")]
pub enum DiscoveryResult {
    /// A relationship was deterministically derived from the evidence.
    Discovered(DiscoveredRelationship),

    /// Evidence exists but is insufficient to derive a relationship.
    ///
    /// Example: a `RUNTIME_CONNECTION` observation exists for an endpoint, but
    /// no `RESOURCE_REFERENCE` observation maps that endpoint to a resource.
    Insufficient,

    /// Evidence is contradictory and cannot be deterministically resolved.
    ///
    /// Example: two `RESOURCE_REFERENCE` observations map the same endpoint to
    /// two different resources. Discovery cannot pick one without confidence
    /// scoring, which is explicitly excluded from v1.
    Conflict {
        /// A human-readable description of the conflict.
        description: String,
    },

    /// Evidence was structurally malformed (missing required fields, wrong
    /// types, etc.). The rule returned this rather than panicking.
    Invalid {
        /// A human-readable description of what was malformed.
        description: String,
    },
}

impl DiscoveryResult {
    /// Return the [`DiscoveredRelationship`] if this result is `Discovered`.
    pub fn discovered(&self) -> Option<&DiscoveredRelationship> {
        match self {
            Self::Discovered(r) => Some(r),
            _ => None,
        }
    }

    /// Return `true` if this result represents a successful discovery.
    pub fn is_discovered(&self) -> bool {
        matches!(self, Self::Discovered(_))
    }

    /// Return `true` if evidence was insufficient.
    pub fn is_insufficient(&self) -> bool {
        matches!(self, Self::Insufficient)
    }

    /// Return `true` if evidence was contradictory.
    pub fn is_conflict(&self) -> bool {
        matches!(self, Self::Conflict { .. })
    }

    /// Return `true` if evidence was malformed.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Self::Invalid { .. })
    }
}

// ---------------------------------------------------------------------------
// DiscoveryRule (trait)
// ---------------------------------------------------------------------------

/// A deterministic rule that can derive relationships from evidence observations.
///
/// Each implementation encodes one specific discovery strategy. New rules can
/// be added without changing the [`DiscoveryEngine`] or the existing domain
/// models.
///
/// # Contract
///
/// - Rules must NEVER panic on malformed evidence. Return `Invalid` instead.
/// - Rules must NEVER return `Discovered` without complete, valid evidence.
/// - Rules must preserve relationship directionality as specified by the rule.
/// - Rules must store `EvidenceId` references, not copies of evidence payloads.
pub trait DiscoveryRule: Send + Sync {
    /// A short, unique name for this rule (for diagnostics and logging).
    fn name(&self) -> &str;

    /// Apply this rule to the given slice of evidence observations.
    ///
    /// Returns a list of [`DiscoveryResult`]s — one per candidate relationship
    /// discovered (or one per conflict/invalid case). An empty `Vec` means the
    /// evidence is simply not relevant to this rule.
    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult>;
}

// ---------------------------------------------------------------------------
// Endpoint (internal helper)
// ---------------------------------------------------------------------------

/// An address+port pair used as a matching key within rules.
///
/// This is an internal type — it never appears in public outputs.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct Endpoint {
    address: String,
    port: u16,
}

impl Endpoint {
    /// Extract an `Endpoint` from a `RUNTIME_CONNECTION` evidence payload.
    ///
    /// Required fields:
    /// - `data.destination`: string IP/hostname
    /// - `data.port`: u16 integer
    fn from_connection(ev: &Evidence) -> Option<Self> {
        let destination = ev.data.get("destination")?.as_str()?.to_owned();
        let port = u16::try_from(ev.data.get("port")?.as_u64()?).ok()?;
        Some(Self {
            address: destination,
            port,
        })
    }

    /// Extract an `Endpoint` from a `RESOURCE_REFERENCE` evidence payload
    /// that encodes an address mapping.
    ///
    /// Required fields:
    /// - `data.address`: string IP/hostname
    /// - `data.port`: u16 integer
    fn from_mapping(ev: &Evidence) -> Option<Self> {
        let address = ev.data.get("address")?.as_str()?.to_owned();
        let port = u16::try_from(ev.data.get("port")?.as_u64()?).ok()?;
        Some(Self { address, port })
    }
}

// ---------------------------------------------------------------------------
// RuntimeConnectionRule
// ---------------------------------------------------------------------------

/// Derives `source --DEPENDS_ON--> target` from two evidence observations:
///
/// 1. A [`ObservationType::RUNTIME_CONNECTION`] observation on `source` that
///    records a `destination` address and `port` in its `data`.
///
/// 2. A [`ObservationType::RESOURCE_REFERENCE`] observation on `target` that
///    records an `address` and `port` in its `data`, mapping the same endpoint
///    to the target resource's identity.
///
/// # Matching
///
/// Both `destination`/`address` (string equality) AND `port` (exact integer
/// match) must agree. If they agree, the rule derives the relationship. If
/// the same endpoint maps to two different resources, the result is `Conflict`.
///
/// # Validation
///
/// Evidence with missing or malformed `destination`/`address`/`port` fields
/// is silently excluded from matching (it is not an `Invalid` result — the
/// evidence may simply be intended for a different rule). `Invalid` is only
/// returned when evidence passes the `observation_type` filter but has an
/// unrecoverable structural error that makes it impossible to determine whether
/// a relationship exists.
pub struct RuntimeConnectionRule;

impl DiscoveryRule for RuntimeConnectionRule {
    fn name(&self) -> &str {
        "RuntimeConnectionRule"
    }

    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        // Partition evidence by observation type.
        let connections: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::RUNTIME_CONNECTION)
            .collect();

        let mappings: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::RESOURCE_REFERENCE)
            .collect();

        // No connection evidence → nothing for this rule to do.
        if connections.is_empty() {
            return vec![];
        }

        // Build a mapping: Endpoint → Vec<(ResourceIdentity, EvidenceId)>
        // Multiple entries for the same endpoint indicate a conflict.
        let mut endpoint_to_targets: HashMap<Endpoint, Vec<(ResourceIdentity, EvidenceId)>> =
            HashMap::new();

        for mapping_ev in &mappings {
            if let Some(ep) = Endpoint::from_mapping(mapping_ev) {
                endpoint_to_targets
                    .entry(ep)
                    .or_default()
                    .push((mapping_ev.subject.clone(), mapping_ev.id));
            }
        }

        let mut results: Vec<DiscoveryResult> = Vec::new();

        for conn_ev in &connections {
            let Some(endpoint) = Endpoint::from_connection(conn_ev) else {
                // RUNTIME_CONNECTION evidence with missing/malformed required
                // fields cannot participate in this rule.
                results.push(DiscoveryResult::Invalid {
                    description: format!(
                        "RUNTIME_CONNECTION evidence {} is missing required 'destination' or 'port' fields",
                        conn_ev.id
                    ),
                });
                continue;
            };

            match endpoint_to_targets.get(&endpoint) {
                None => {
                    // Valid connection evidence, but no mapping found → insufficient.
                    results.push(DiscoveryResult::Insufficient);
                }

                Some(targets) if targets.len() > 1 => {
                    // Multiple resources claim the same endpoint → conflict.
                    let resource_strs: Vec<String> =
                        targets.iter().map(|(ri, _)| ri.to_string()).collect();
                    results.push(DiscoveryResult::Conflict {
                        description: format!(
                            "Endpoint {}:{} maps to multiple resources: {}",
                            endpoint.address,
                            endpoint.port,
                            resource_strs.join(", ")
                        ),
                    });
                }

                Some(targets) => {
                    // Exactly one target → derive the relationship.
                    let (target_identity, mapping_evidence_id) = &targets[0];

                    let relationship = Relationship::new(
                        conn_ev.subject.clone(),
                        target_identity.clone(),
                        RelationshipKind::DEPENDS_ON,
                    );

                    results.push(DiscoveryResult::Discovered(DiscoveredRelationship::new(
                        relationship,
                        vec![conn_ev.id, *mapping_evidence_id],
                    )));
                }
            }
        }

        results
    }
}

// ---------------------------------------------------------------------------
// DiscoveryEngine
// ---------------------------------------------------------------------------

/// Applies a set of [`DiscoveryRule`] implementations to a collection of
/// [`Evidence`] observations and returns all [`DiscoveryResult`]s.
///
/// # Usage
///
/// ```rust,ignore
/// let engine = DiscoveryEngine::new(vec![Box::new(RuntimeConnectionRule)]);
/// let results = engine.run(&evidence_slice);
/// ```
///
/// # Extensibility
///
/// Additional rules are added by implementing [`DiscoveryRule`] and passing
/// the new rule instance to [`DiscoveryEngine::new`]. The engine itself does
/// not need to be modified.
pub struct DiscoveryEngine {
    rules: Vec<Box<dyn DiscoveryRule>>,
}

impl DiscoveryEngine {
    /// Create a `DiscoveryEngine` with the given set of rules.
    pub fn new(rules: Vec<Box<dyn DiscoveryRule>>) -> Self {
        Self { rules }
    }

    /// Create a `DiscoveryEngine` pre-loaded with the default v1 rule set.
    ///
    /// v1 rules: [`RuntimeConnectionRule`].
    pub fn default_v1() -> Self {
        Self::new(vec![Box::new(RuntimeConnectionRule)])
    }

    /// Apply all rules to the given evidence and return all results.
    ///
    /// Each rule contributes zero or more results. Results from all rules are
    /// concatenated. The engine does not attempt to deduplicate results across
    /// rules — callers may group by relationship identity if needed.
    pub fn run(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        self.rules
            .iter()
            .flat_map(|rule| rule.apply(evidence))
            .collect()
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::evidence::{CollectorId, EvidenceSource};
    use crate::resource::{Provider, ResourceKind};
    use chrono::Utc;
    use serde_json::json;

    // -----------------------------------------------------------------------
    // Helpers
    // -----------------------------------------------------------------------

    fn k8s_source() -> EvidenceSource {
        EvidenceSource::new(
            Provider::new("kubernetes"),
            CollectorId::from_static("k8s-runtime"),
        )
    }

    fn aws_source() -> EvidenceSource {
        EvidenceSource::new(
            Provider::new("aws"),
            CollectorId::from_static("aws-network"),
        )
    }

    fn pod_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            format!("payments/{name}"),
        )
    }

    fn db_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            format!("arn:aws:rds:us-east-1:123:{name}"),
        )
    }

    fn connection_evidence(subject: ResourceIdentity, destination: &str, port: u16) -> Evidence {
        Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RUNTIME_CONNECTION,
            subject,
            json!({ "destination": destination, "port": port, "protocol": "tcp" }),
        )
    }

    fn mapping_evidence(subject: ResourceIdentity, address: &str, port: u16) -> Evidence {
        Evidence::new(
            aws_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            subject,
            json!({ "address": address, "port": port }),
        )
    }

    // -----------------------------------------------------------------------
    // 1. Runtime connection + resource mapping discovers DEPENDS_ON.
    // -----------------------------------------------------------------------
    #[test]
    fn test_runtime_connection_mapping_discovers_depends_on() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);
        let evidence = vec![conn, map];

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&evidence);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());
        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 2. Discovered relationship has correct source.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_source() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.source, pod);
    }

    // -----------------------------------------------------------------------
    // 3. Discovered relationship has correct target.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_target() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.target, db);
    }

    // -----------------------------------------------------------------------
    // 4. Discovered relationship has correct RelationshipKind.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_kind() {
        let pod = pod_identity("checkout");
        let db = db_identity("orders-db");

        let conn = connection_evidence(pod.clone(), "192.168.1.10", 3306);
        let map = mapping_evidence(db.clone(), "192.168.1.10", 3306);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
        assert_eq!(
            dr.relationship.category,
            crate::relationship::RelationshipCategory::Dependency
        );
    }

    // -----------------------------------------------------------------------
    // 5. Supporting EvidenceIds are preserved.
    // -----------------------------------------------------------------------
    #[test]
    fn test_supporting_evidence_ids_preserved() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let conn_id = conn.id;
        let map_id = map.id;

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.supporting_evidence.len(), 2);
        assert!(dr.supporting_evidence.contains(&conn_id));
        assert!(dr.supporting_evidence.contains(&map_id));
    }

    // -----------------------------------------------------------------------
    // 6. Same endpoint produces a valid discovery.
    // -----------------------------------------------------------------------
    #[test]
    fn test_same_endpoint_produces_valid_discovery() {
        let pod = pod_identity("api");
        let db = db_identity("main-db");

        let conn = connection_evidence(pod.clone(), "172.16.0.5", 5439);
        let map = mapping_evidence(db.clone(), "172.16.0.5", 5439);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());
    }

    // -----------------------------------------------------------------------
    // 7. Different endpoints do not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_different_endpoints_no_relationship() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // connection to .15, mapping for .99 — no match
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.99", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 8. Missing connection evidence does not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_missing_connection_evidence_no_relationship() {
        let db = db_identity("payments-db");

        // Only a mapping, no connection
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[map]);

        // No RUNTIME_CONNECTION → rule returns nothing at all
        assert!(results.is_empty());
    }

    // -----------------------------------------------------------------------
    // 9. Missing resource mapping does not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_missing_resource_mapping_no_relationship() {
        let pod = pod_identity("payments-api");

        // Connection exists, but no RESOURCE_REFERENCE to map the endpoint
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 10. Malformed evidence does not panic — returns Invalid.
    // -----------------------------------------------------------------------
    #[test]
    fn test_malformed_connection_evidence_does_not_panic() {
        let pod = pod_identity("payments-api");

        // RUNTIME_CONNECTION with missing destination field
        let malformed = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RUNTIME_CONNECTION,
            pod,
            json!({ "port": 5432 }), // missing "destination"
        );

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[malformed]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_invalid());
    }

    // -----------------------------------------------------------------------
    // 11. Duplicate discovery does not change semantic relationship identity.
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_discovery_same_relationship_identity() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn1 = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let conn2 = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn1, conn2, map]);

        // Two connections to the same endpoint produce two discovery results
        // but both describe the SAME semantic relationship (source, target, kind).
        assert_eq!(results.len(), 2);
        let dr1 = results[0].discovered().unwrap();
        let dr2 = results[1].discovered().unwrap();

        // Same semantic identity
        assert_eq!(dr1.relationship, dr2.relationship);
        // But different supporting evidence (different connection observations)
        assert_ne!(dr1.supporting_evidence, dr2.supporting_evidence);
    }

    // -----------------------------------------------------------------------
    // 12. Multiple supporting EvidenceIds can support the same relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_supporting_evidence_ids() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let conn_id = conn.id;
        let map_id = map.id;

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        // Both connection evidence and mapping evidence are preserved
        assert_eq!(dr.supporting_evidence.len(), 2);
        assert!(dr.supporting_evidence.contains(&conn_id));
        assert!(dr.supporting_evidence.contains(&map_id));
    }

    // -----------------------------------------------------------------------
    // 13. Conflicting resource mappings are not arbitrarily resolved.
    // -----------------------------------------------------------------------
    #[test]
    fn test_conflicting_resource_mappings_produce_conflict() {
        let pod = pod_identity("payments-api");
        let db_b = db_identity("database-b");
        let db_c = db_identity("database-c");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        // Two different resources claim the same endpoint
        let map_b = mapping_evidence(db_b.clone(), "10.0.2.15", 5432);
        let map_c = mapping_evidence(db_c.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map_b, map_c]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_conflict());

        if let DiscoveryResult::Conflict { description } = &results[0] {
            assert!(description.contains("10.0.2.15"));
            assert!(description.contains("5432"));
        }
    }

    // -----------------------------------------------------------------------
    // 14. Discovery does not modify the original Evidence.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_does_not_modify_evidence() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let original_conn_id = conn.id;
        let original_conn_subject = conn.subject.clone();
        let original_conn_data = conn.data.clone();

        let rule = RuntimeConnectionRule;
        let _ = rule.apply(&[conn.clone(), map]);

        // Evidence is unchanged
        assert_eq!(conn.id, original_conn_id);
        assert_eq!(conn.subject, original_conn_subject);
        assert_eq!(conn.data, original_conn_data);
    }

    // -----------------------------------------------------------------------
    // 15. Discovery does not modify the existing Relationship model.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_does_not_modify_relationship_model() {
        // Verify that Relationship is unchanged post-discovery — its fields
        // match what was specified before discovery ran.
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        // Relationship fields are exactly what we expect — no extra fields added
        assert_eq!(dr.relationship.source, pod);
        assert_eq!(dr.relationship.target, db);
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
        // No evidence inside Relationship itself
        // (verified at compile time — Relationship has no evidence field)
    }

    // -----------------------------------------------------------------------
    // 16. Serialization works for DiscoveredRelationship and DiscoveryResult.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialization_round_trip() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap().clone();

        // Round-trip DiscoveredRelationship
        let json = serde_json::to_string(&dr).expect("serialization failed");
        let restored: DiscoveredRelationship =
            serde_json::from_str(&json).expect("deserialization failed");
        assert_eq!(dr, restored);
        assert_eq!(dr.supporting_evidence, restored.supporting_evidence);

        // Round-trip DiscoveryResult
        let result = DiscoveryResult::Discovered(dr.clone());
        let result_json = serde_json::to_string(&result).expect("result serialization failed");
        let restored_result: DiscoveryResult =
            serde_json::from_str(&result_json).expect("result deserialization failed");
        assert_eq!(result, restored_result);
    }

    // -----------------------------------------------------------------------
    // 17. DiscoveryEngine::default_v1 runs RuntimeConnectionRule correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_engine_default_v1() {
        let pod = pod_identity("checkout-api");
        let db = db_identity("orders-db");

        let conn = connection_evidence(pod.clone(), "10.1.0.5", 5432);
        let map = mapping_evidence(db.clone(), "10.1.0.5", 5432);

        let engine = DiscoveryEngine::default_v1();
        let results = engine.run(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.source, pod);
        assert_eq!(dr.relationship.target, db);
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 18. Port mismatch alone prevents discovery.
    // -----------------------------------------------------------------------
    #[test]
    fn test_port_mismatch_prevents_discovery() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // Same IP, different port — should not match
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5433);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 19. Non-RUNTIME_CONNECTION evidence is not treated as connection evidence.
    // -----------------------------------------------------------------------
    #[test]
    fn test_unrelated_observation_types_are_ignored() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // CONFIGURATION observation — should not be treated as a connection
        let config_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::CONFIGURATION,
            pod,
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[config_ev, map]);

        // No RUNTIME_CONNECTION → rule returns nothing
        assert!(results.is_empty());
    }
}
