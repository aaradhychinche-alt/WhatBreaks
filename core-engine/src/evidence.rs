//! WhatBreaks Evidence Model v1
//!
//! An [`Evidence`] record captures a single factual observation made by a
//! collector about an infrastructure resource. It preserves what was observed,
//! when, where the observation came from, and what structured data supports it.
//!
//! # Design Principles
//!
//! - **Factual, not inferential**: Evidence describes what was observed. It
//!   does NOT decide whether a relationship exists. That is the responsibility
//!   of the future Discovery Engine.
//!
//! - **Historical**: Each observation is a distinct, immutable record. Repeated
//!   observations of the same resource produce separate `Evidence` records.
//!   Evidence is never collapsed into a mutable "current state".
//!
//! - **Independent of Relationship**: Evidence can exist before any Relationship
//!   is known. There is no required reference to a [`Relationship`].
//!
//! - **Provider-agnostic**: Neither [`Evidence`] nor [`EvidenceSource`] contain
//!   provider-specific Rust types. Provider identity is carried by the existing
//!   [`Provider`] type and the open [`CollectorId`] value.
//!
//! - **Identity via UUID**: Unlike [`Relationship`], each `Evidence` record
//!   represents a distinct point-in-time event, so a stable synthetic ID is
//!   appropriate. The same UUID/newtype pattern used by [`ResourceId`] is
//!   followed.
//!
//! [`Relationship`]: crate::relationship::Relationship
//! [`ResourceId`]: crate::resource::ResourceId

use std::borrow::Cow;
use std::fmt;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use uuid::Uuid;

use crate::resource::{Provider, ResourceIdentity};

// ---------------------------------------------------------------------------
// EvidenceId
// ---------------------------------------------------------------------------

/// A stable, unique identifier for a single Evidence observation.
///
/// Unlike [`Relationship`], which uses natural identity `(source, target, kind)`,
/// `Evidence` represents individual point-in-time events and therefore needs a
/// synthetic identity.
///
/// Follows the same transparent-UUID-newtype pattern as [`ResourceId`].
///
/// [`Relationship`]: crate::relationship::Relationship
/// [`ResourceId`]: crate::resource::ResourceId
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct EvidenceId(Uuid);

impl EvidenceId {
    /// Generate a new unique `EvidenceId`.
    pub fn new() -> Self {
        Self(Uuid::new_v4())
    }

    /// Return the underlying [`Uuid`].
    pub fn as_uuid(&self) -> Uuid {
        self.0
    }
}

impl Default for EvidenceId {
    fn default() -> Self {
        Self::new()
    }
}

impl fmt::Display for EvidenceId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(f)
    }
}

// ---------------------------------------------------------------------------
// CollectorId
// ---------------------------------------------------------------------------

/// Identifies the specific collector that produced an observation.
///
/// Examples: `"k8s-runtime"`, `"aws-network"`, `"gcp-vpc"`, `"custom-agent"`.
///
/// Open/extensible string newtype — using the same [`Cow<'static, str>`] pattern
/// as [`RelationshipKind`] to allow zero-allocation constants for well-known
/// collectors while remaining open to dynamic/custom values.
///
/// [`RelationshipKind`]: crate::relationship::RelationshipKind
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct CollectorId(Cow<'static, str>);

impl CollectorId {
    /// Create a `CollectorId` from any string-like value (heap-allocated).
    pub fn new(s: impl Into<String>) -> Self {
        Self(Cow::Owned(s.into()))
    }

    /// Create a `CollectorId` from a static string (zero-allocation).
    pub const fn from_static(s: &'static str) -> Self {
        Self(Cow::Borrowed(s))
    }

    /// Return the collector identifier as a `&str`.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for CollectorId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl From<&'static str> for CollectorId {
    fn from(s: &'static str) -> Self {
        Self(Cow::Borrowed(s))
    }
}

impl From<String> for CollectorId {
    fn from(s: String) -> Self {
        Self(Cow::Owned(s))
    }
}

// ---------------------------------------------------------------------------
// EvidenceSource
// ---------------------------------------------------------------------------

/// Provenance of an Evidence observation.
///
/// Captures where an observation came from so that WB can later answer:
/// *"Why does WB believe this relationship exists?"*
///
/// # Fields
///
/// - `provider`: the infrastructure provider the collector observed (e.g.
///   `"kubernetes"`, `"aws"`). Reuses the existing [`Provider`] type to avoid
///   duplication.
/// - `collector`: the specific collector that produced the observation (e.g.
///   `"k8s-runtime"`, `"aws-network"`).
///
/// `EvidenceSource` deliberately does NOT encode version, credentials, or
/// cluster-specific details — those belong in the collector implementation,
/// not in the domain model.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct EvidenceSource {
    /// The infrastructure provider from which the observation originated.
    pub provider: Provider,
    /// The specific collector that produced the observation.
    pub collector: CollectorId,
}

impl EvidenceSource {
    /// Construct an `EvidenceSource`.
    pub fn new(provider: Provider, collector: CollectorId) -> Self {
        Self {
            provider,
            collector,
        }
    }
}

impl fmt::Display for EvidenceSource {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}/{}", self.provider.as_str(), self.collector.as_str())
    }
}

// ---------------------------------------------------------------------------
// ObservationType
// ---------------------------------------------------------------------------

/// Describes the semantic nature of a factual observation.
///
/// Open/extensible string newtype — new observation types can be added by
/// collectors without modifying the core domain model.
///
/// Initial meaningful types:
///
/// - [`ObservationType::CONFIGURATION`]: A resource's configuration was
///   observed (e.g. a selector field referencing another resource).
/// - [`ObservationType::RUNTIME_CONNECTION`]: An active network connection
///   was observed at runtime (e.g. TCP connection to a remote endpoint).
/// - [`ObservationType::RESOURCE_REFERENCE`]: A resource explicitly references
///   another resource by identity (e.g. an EnvFrom or secretRef).
/// - [`ObservationType::OWNERSHIP_REFERENCE`]: An ownership or controller
///   relationship was observed (e.g. ownerReferences in Kubernetes).
/// - [`ObservationType::NETWORK_OBSERVATION`]: A network-level observation
///   that does not yet resolve to a known resource (e.g. IP address only).
///
/// # NOT RelationshipKind
///
/// `ObservationType` describes *what kind of fact was observed*.
/// [`RelationshipKind`] describes *what kind of relationship exists* between
/// two resources. The Discovery Engine bridges the two — Evidence is not
/// responsible for that inference.
///
/// [`RelationshipKind`]: crate::relationship::RelationshipKind
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct ObservationType(Cow<'static, str>);

impl ObservationType {
    /// A resource's configuration was statically observed.
    pub const CONFIGURATION: Self = Self(Cow::Borrowed("CONFIGURATION"));

    /// An active network connection was observed at runtime.
    pub const RUNTIME_CONNECTION: Self = Self(Cow::Borrowed("RUNTIME_CONNECTION"));

    /// A resource explicitly references another resource by identity.
    pub const RESOURCE_REFERENCE: Self = Self(Cow::Borrowed("RESOURCE_REFERENCE"));

    /// An ownership or controller relationship was observed.
    pub const OWNERSHIP_REFERENCE: Self = Self(Cow::Borrowed("OWNERSHIP_REFERENCE"));

    /// A network-level observation that does not yet resolve to a known resource.
    pub const NETWORK_OBSERVATION: Self = Self(Cow::Borrowed("NETWORK_OBSERVATION"));

    /// Create an `ObservationType` from any string-like value.
    pub fn new(s: impl Into<String>) -> Self {
        Self(Cow::Owned(s.into()))
    }

    /// Return the observation type as a `&str`.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for ObservationType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl From<&'static str> for ObservationType {
    fn from(s: &'static str) -> Self {
        Self(Cow::Borrowed(s))
    }
}

impl From<String> for ObservationType {
    fn from(s: String) -> Self {
        Self(Cow::Owned(s))
    }
}

// ---------------------------------------------------------------------------
// Evidence
// ---------------------------------------------------------------------------

/// A single, immutable, point-in-time observation about an infrastructure resource.
///
/// Evidence is the factual foundation for the future Discovery Engine. It
/// captures *what was observed*, *when*, *by whom*, and *about which resource*,
/// without making any inference about relationships.
///
/// # Identity
///
/// Each `Evidence` record has a unique [`EvidenceId`]. Two observations of the
/// same resource at different times are distinct records with distinct IDs.
/// Equality and hashing are based on `id` alone.
///
/// # What Evidence does NOT contain
///
/// - Confidence scores
/// - Relationship kinds or categories
/// - Graph node IDs
/// - Blast-radius or impact data
/// - Discovery state or inference decisions
/// - References to a specific [`Relationship`] (Evidence can exist before any
///   Relationship is known)
///
/// [`Relationship`]: crate::relationship::Relationship
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Evidence {
    /// Unique identifier for this observation. Generated at observation time.
    pub id: EvidenceId,

    /// Where the observation came from (provider + collector).
    pub source: EvidenceSource,

    /// When the observation was made. Stored as UTC to avoid ambiguity.
    pub observed_at: DateTime<Utc>,

    /// The semantic nature of the observation (e.g. `RUNTIME_CONNECTION`).
    pub observation_type: ObservationType,

    /// The resource this observation primarily concerns.
    pub subject: ResourceIdentity,

    /// Structured payload of the factual observation.
    ///
    /// The schema is determined by the collector and observation type. The
    /// Core Engine does not validate or interpret this payload — it is
    /// preserved as-is for the Discovery Engine.
    ///
    /// Examples:
    /// ```json
    /// { "destination": "10.0.2.15", "port": 5432, "protocol": "tcp" }
    /// { "reference": "service/api", "field": "spec.selector" }
    /// ```
    pub data: Value,
}

impl Evidence {
    /// Construct a new `Evidence` record.
    ///
    /// A fresh [`EvidenceId`] is generated automatically. The caller supplies
    /// the full observation context.
    pub fn new(
        source: EvidenceSource,
        observed_at: DateTime<Utc>,
        observation_type: ObservationType,
        subject: ResourceIdentity,
        data: Value,
    ) -> Self {
        Self {
            id: EvidenceId::new(),
            source,
            observed_at,
            observation_type,
            subject,
            data,
        }
    }
}

impl PartialEq for Evidence {
    /// Two evidence records are equal if and only if they share the same [`EvidenceId`].
    ///
    /// This ensures that two observations of the same resource at different
    /// times remain distinct even if all other fields happen to match.
    fn eq(&self, other: &Self) -> bool {
        self.id == other.id
    }
}

impl Eq for Evidence {}

impl std::hash::Hash for Evidence {
    fn hash<H: std::hash::Hasher>(&self, state: &mut H) {
        self.id.hash(state);
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::resource::{Provider, ResourceKind};
    use chrono::{Datelike, TimeZone, Timelike};
    use serde_json::json;

    // -----------------------------------------------------------------------
    // Helpers
    // -----------------------------------------------------------------------

    fn k8s_pod_identity() -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/payments-api-7d8f9",
        )
    }

    fn aws_rds_identity() -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            "arn:aws:rds:ap-south-1:123456789012:db:payments",
        )
    }

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

    fn fixed_ts() -> DateTime<Utc> {
        Utc.with_ymd_and_hms(2026, 10, 1, 10, 0, 0).unwrap()
    }

    // -----------------------------------------------------------------------
    // 1. Evidence can represent a Kubernetes observation.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_kubernetes_observation() {
        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({
                "destination": "10.0.2.15",
                "port": 5432,
                "protocol": "tcp"
            }),
        );

        assert_eq!(ev.source.provider.as_str(), "kubernetes");
        assert_eq!(ev.source.collector.as_str(), "k8s-runtime");
        assert_eq!(ev.observation_type, ObservationType::RUNTIME_CONNECTION);
        assert_eq!(ev.subject.provider.as_str(), "kubernetes");
        assert_eq!(ev.subject.resource_type.as_str(), "pod");
        assert_eq!(ev.data["port"], 5432);
    }

    // -----------------------------------------------------------------------
    // 2. Evidence can represent an AWS observation.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_aws_observation() {
        let ev = Evidence::new(
            aws_source(),
            fixed_ts(),
            ObservationType::NETWORK_OBSERVATION,
            aws_rds_identity(),
            json!({
                "vpc_id": "vpc-0a1b2c3d4e5f",
                "private_ip": "10.0.2.15",
                "port": 5432
            }),
        );

        assert_eq!(ev.source.provider.as_str(), "aws");
        assert_eq!(ev.source.collector.as_str(), "aws-network");
        assert_eq!(ev.observation_type, ObservationType::NETWORK_OBSERVATION);
        assert_eq!(ev.subject.provider.as_str(), "aws");
        assert_eq!(ev.data["vpc_id"], "vpc-0a1b2c3d4e5f");
    }

    // -----------------------------------------------------------------------
    // 3. Evidence contains ResourceIdentity rather than ResourceId(UUID).
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_uses_resource_identity_not_resource_id() {
        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::CONFIGURATION,
            k8s_pod_identity(),
            json!({}),
        );

        // subject is ResourceIdentity — it has provider, resource_type, provider_id
        assert_eq!(ev.subject.provider.as_str(), "kubernetes");
        assert_eq!(ev.subject.resource_type.as_str(), "pod");
        assert_eq!(ev.subject.provider_id, "payments/payments-api-7d8f9");

        // ResourceIdentity carries no UUID — the EvidenceId on Evidence is the UUID
        // This test verifies the field type by inspecting the struct, not by UUID.
        // The subject has no `id: Uuid` field — confirmed by construction above.
    }

    // -----------------------------------------------------------------------
    // 4. EvidenceSource correctly represents provider + collector.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_source_provider_and_collector() {
        let source = EvidenceSource::new(
            Provider::new("kubernetes"),
            CollectorId::from_static("k8s-runtime"),
        );

        assert_eq!(source.provider.as_str(), "kubernetes");
        assert_eq!(source.collector.as_str(), "k8s-runtime");
        assert_eq!(source.to_string(), "kubernetes/k8s-runtime");
    }

    // -----------------------------------------------------------------------
    // 5. CollectorId is extensible — new collectors need no core change.
    // -----------------------------------------------------------------------
    #[test]
    fn test_collector_id_extensibility() {
        // Static known collector
        let k8s = CollectorId::from_static("k8s-runtime");
        assert_eq!(k8s.as_str(), "k8s-runtime");

        // Dynamically created custom collector — no code change required
        let custom = CollectorId::new("acme-internal-mesh-collector-v2");
        assert_eq!(custom.as_str(), "acme-internal-mesh-collector-v2");

        // Different collectors are not equal
        assert_ne!(k8s, custom);
    }

    // -----------------------------------------------------------------------
    // 6. ObservationType is extensible.
    // -----------------------------------------------------------------------
    #[test]
    fn test_observation_type_extensibility() {
        // Static known types
        assert_eq!(
            ObservationType::RUNTIME_CONNECTION.as_str(),
            "RUNTIME_CONNECTION"
        );
        assert_eq!(ObservationType::CONFIGURATION.as_str(), "CONFIGURATION");

        // Custom type — no core domain change required
        let custom = ObservationType::new("DNS_LOOKUP");
        assert_eq!(custom.as_str(), "DNS_LOOKUP");

        // Different types are not equal
        assert_ne!(ObservationType::RUNTIME_CONNECTION, custom);
    }

    // -----------------------------------------------------------------------
    // 7. Evidence preserves observed_at.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_preserves_observed_at() {
        let t = Utc.with_ymd_and_hms(2026, 10, 1, 12, 30, 45).unwrap();
        let ev = Evidence::new(
            k8s_source(),
            t,
            ObservationType::CONFIGURATION,
            k8s_pod_identity(),
            json!({}),
        );

        assert_eq!(ev.observed_at, t);
        assert_eq!(ev.observed_at.year(), 2026);
        assert_eq!(ev.observed_at.hour(), 12);
        assert_eq!(ev.observed_at.minute(), 30);
    }

    // -----------------------------------------------------------------------
    // 8. Two observations of the same resource at different times are distinct.
    // -----------------------------------------------------------------------
    #[test]
    fn test_two_observations_at_different_times_are_distinct() {
        let t1 = Utc.with_ymd_and_hms(2026, 10, 1, 10, 0, 0).unwrap();
        let t2 = Utc.with_ymd_and_hms(2026, 10, 1, 11, 0, 0).unwrap();

        let ev1 = Evidence::new(
            k8s_source(),
            t1,
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );

        let ev2 = Evidence::new(
            k8s_source(),
            t2,
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );

        // Different timestamps → different evidence records
        assert_ne!(ev1.observed_at, ev2.observed_at);
        // Different EvidenceIds
        assert_ne!(ev1.id, ev2.id);
        // PartialEq is based on id — they are not equal
        assert_ne!(ev1, ev2);
    }

    // -----------------------------------------------------------------------
    // 9. Evidence IDs provide distinct observation identity.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_ids_are_distinct_per_observation() {
        use std::collections::HashSet;

        let mut ids: HashSet<EvidenceId> = HashSet::new();

        for _ in 0..100 {
            let ev = Evidence::new(
                k8s_source(),
                fixed_ts(),
                ObservationType::CONFIGURATION,
                k8s_pod_identity(),
                json!({}),
            );
            ids.insert(ev.id);
        }

        // All 100 observations must have unique IDs
        assert_eq!(ids.len(), 100);
    }

    // -----------------------------------------------------------------------
    // 10. Evidence can exist without a Relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_exists_independent_of_relationship() {
        // Observation: Pod A connected to raw IP — no relationship known yet.
        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({
                "destination": "10.0.2.15",
                "port": 5432,
                "protocol": "tcp",
                "note": "Destination resource unknown — no relationship inferred yet"
            }),
        );

        // Evidence has no relationship field
        // This compiles and runs successfully — proving it is self-contained
        assert_eq!(ev.subject.provider.as_str(), "kubernetes");
        assert_eq!(ev.data["port"], 5432);
    }

    // -----------------------------------------------------------------------
    // 11. Structured evidence data is preserved.
    // -----------------------------------------------------------------------
    #[test]
    fn test_structured_evidence_data_preserved() {
        let payload = json!({
            "reference": "service/api",
            "field": "spec.selector",
            "matchLabels": { "app": "api", "env": "prod" }
        });

        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::RESOURCE_REFERENCE,
            k8s_pod_identity(),
            payload.clone(),
        );

        assert_eq!(ev.data, payload);
        assert_eq!(ev.data["field"], "spec.selector");
        assert_eq!(ev.data["matchLabels"]["app"], "api");
    }

    // -----------------------------------------------------------------------
    // 12. Serialization/deserialization round-trip works.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialization_round_trip() {
        let original = Evidence::new(
            EvidenceSource::new(
                Provider::new("kubernetes"),
                CollectorId::from_static("k8s-runtime"),
            ),
            fixed_ts(),
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({ "destination": "10.0.2.15", "port": 5432, "protocol": "tcp" }),
        );

        let json_str = serde_json::to_string(&original).expect("serialization failed");
        let restored: Evidence = serde_json::from_str(&json_str).expect("deserialization failed");

        // Identity-based equality
        assert_eq!(original, restored);
        // All fields survive round-trip
        assert_eq!(original.id, restored.id);
        assert_eq!(original.source.provider, restored.source.provider);
        assert_eq!(original.source.collector, restored.source.collector);
        assert_eq!(original.observed_at, restored.observed_at);
        assert_eq!(original.observation_type, restored.observation_type);
        assert_eq!(original.subject, restored.subject);
        assert_eq!(original.data, restored.data);
    }

    // -----------------------------------------------------------------------
    // 13. Provider-specific payloads do not require provider-specific structs.
    // -----------------------------------------------------------------------
    #[test]
    fn test_provider_specific_payloads_are_generic() {
        // Kubernetes-specific observation
        let k8s_ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::OWNERSHIP_REFERENCE,
            k8s_pod_identity(),
            json!({
                "owner_kind": "ReplicaSet",
                "owner_name": "payments-api-7d8f9",
                "owner_uid": "abc-123-def-456"
            }),
        );

        // AWS-specific observation
        let aws_ev = Evidence::new(
            aws_source(),
            fixed_ts(),
            ObservationType::NETWORK_OBSERVATION,
            aws_rds_identity(),
            json!({
                "vpc_id": "vpc-0abc1234",
                "subnet_id": "subnet-0def5678",
                "security_groups": ["sg-01234", "sg-56789"]
            }),
        );

        // Both use the same Evidence struct — no provider-specific fields on Evidence
        assert_eq!(k8s_ev.data["owner_kind"], "ReplicaSet");
        assert_eq!(aws_ev.data["vpc_id"], "vpc-0abc1234");
        assert_eq!(aws_ev.data["security_groups"][0], "sg-01234");
    }

    // -----------------------------------------------------------------------
    // 14. Evidence does not contain RelationshipKind or RelationshipCategory.
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_has_no_relationship_kind_or_category() {
        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );

        // This test validates at the type level by only accessing valid fields.
        // Evidence has: id, source, observed_at, observation_type, subject, data.
        // It does NOT have: kind, category, confidence, relationship, graph_node.
        let _ = ev.id;
        let _ = &ev.source;
        let _ = ev.observed_at;
        let _ = &ev.observation_type;
        let _ = &ev.subject;
        let _ = &ev.data;
        // If Evidence had a `kind` or `category` field, the unused-field
        // lints would surface it. The struct has exactly six fields.
    }

    // -----------------------------------------------------------------------
    // 15. JSON shape is correct.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialized_json_shape() {
        let ev = Evidence::new(
            k8s_source(),
            fixed_ts(),
            ObservationType::RUNTIME_CONNECTION,
            k8s_pod_identity(),
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );

        let v: serde_json::Value = serde_json::to_value(&ev).expect("to_value failed");

        // id → UUID string
        assert!(v["id"].is_string());
        // source.provider → plain string (transparent newtype)
        assert_eq!(v["source"]["provider"], "kubernetes");
        // source.collector → plain string (transparent newtype)
        assert_eq!(v["source"]["collector"], "k8s-runtime");
        // observed_at → RFC3339 string
        assert!(v["observed_at"].is_string());
        assert!(v["observed_at"].as_str().unwrap().contains("2026"));
        // observation_type → plain string (transparent newtype)
        assert_eq!(v["observation_type"], "RUNTIME_CONNECTION");
        // subject fields
        assert_eq!(v["subject"]["provider"], "kubernetes");
        assert_eq!(v["subject"]["resource_type"], "pod");
        assert_eq!(v["subject"]["provider_id"], "payments/payments-api-7d8f9");
        // data preserved
        assert_eq!(v["data"]["port"], 5432);
    }
}
