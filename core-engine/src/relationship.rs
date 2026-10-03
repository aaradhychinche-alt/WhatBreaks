//! WhatBreaks Relationship Model v1
//!
//! A [`Relationship`] represents a directional link from a source resource to a
//! target resource, classified by a [`RelationshipCategory`] and typed by a
//! [`RelationshipKind`].
//!
//! # Design Principles
//!
//! - **Directional**: Every relationship flows strictly from `source` to `target`.
//! - **Natural Identity**: Identity is `(source, target, kind)`. Category is
//!   derived classification and not part of the identity.
//! - **No Synthetic IDs**: No UUID or synthetic identifier is assigned to relationships.
//! - **Open Extensibility**: [`RelationshipKind`] is an open value type supporting
//!   future kinds without core domain modifications.
//! - **Deterministic Classification**: Every initial [`RelationshipKind`] maps to
//!   exactly one [`RelationshipCategory`].
//! - **No Graph / Storage Awareness**: No evidence, confidence, graph nodes, or
//!   database logic.

use std::borrow::Cow;
use std::fmt;
use std::ops::Deref;

use serde::{Deserialize, Serialize};

use crate::resource::ResourceIdentity;

// ---------------------------------------------------------------------------
// RelationshipCategory
// ---------------------------------------------------------------------------

/// High-level classification of a relationship.
///
/// This is a small, closed classification. Every [`RelationshipKind`] maps to
/// exactly one `RelationshipCategory`.
///
/// Notice: `Network` currently has no initial [`RelationshipKind`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum RelationshipCategory {
    /// Direct structural or operational dependency.
    Dependency,
    /// Ingestion, reading, or writing of data.
    DataFlow,
    /// Synchronous or asynchronous invocation (RPC, HTTP, events).
    Invocation,
    /// Permission, identity, or policy granting access.
    Authorization,
    /// Lifecycle or hierarchical ownership.
    Ownership,
    /// Physical or virtual execution placement.
    Placement,
    /// Network routing, peering, or connectivity.
    Network,
}

impl RelationshipCategory {
    /// Return the category name as a static string slice.
    pub const fn as_str(&self) -> &'static str {
        match self {
            Self::Dependency => "Dependency",
            Self::DataFlow => "DataFlow",
            Self::Invocation => "Invocation",
            Self::Authorization => "Authorization",
            Self::Ownership => "Ownership",
            Self::Placement => "Placement",
            Self::Network => "Network",
        }
    }

    /// Derive the category for a [`RelationshipKind`], if known.
    pub fn from_kind(kind: &RelationshipKind) -> Option<Self> {
        kind.category()
    }
}

impl fmt::Display for RelationshipCategory {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

// ---------------------------------------------------------------------------
// RelationshipKind
// ---------------------------------------------------------------------------

/// The specific kind of relationship between resources.
///
/// An open/extensible value type rather than a closed enum, allowing new kinds
/// to be introduced without modifying the core domain model.
///
/// Initial kinds:
/// - [`RelationshipKind::DEPENDS_ON`] -> [`RelationshipCategory::Dependency`]
/// - [`RelationshipKind::CALLS`] -> [`RelationshipCategory::Invocation`]
/// - [`RelationshipKind::READS_FROM`] -> [`RelationshipCategory::DataFlow`]
/// - [`RelationshipKind::WRITES_TO`] -> [`RelationshipCategory::DataFlow`]
/// - [`RelationshipKind::OWNS`] -> [`RelationshipCategory::Ownership`]
/// - [`RelationshipKind::RUNS_ON`] -> [`RelationshipCategory::Placement`]
/// - [`RelationshipKind::AUTHORIZES`] -> [`RelationshipCategory::Authorization`]
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct RelationshipKind(Cow<'static, str>);

impl RelationshipKind {
    /// Direct structural or lifecycle dependency.
    pub const DEPENDS_ON: Self = Self(Cow::Borrowed("DEPENDS_ON"));

    /// Service or API invocation (HTTP, gRPC, function call).
    pub const CALLS: Self = Self(Cow::Borrowed("CALLS"));

    /// Reading data from a datastore, topic, or stream.
    pub const READS_FROM: Self = Self(Cow::Borrowed("READS_FROM"));

    /// Writing data to a datastore, topic, or sink.
    pub const WRITES_TO: Self = Self(Cow::Borrowed("WRITES_TO"));

    /// Lifecycle or controller ownership (e.g. Deployment owns ReplicaSet).
    pub const OWNS: Self = Self(Cow::Borrowed("OWNS"));

    /// Execution placement (e.g. Pod runs on Node, container runs on VM).
    pub const RUNS_ON: Self = Self(Cow::Borrowed("RUNS_ON"));

    /// Access control or permission authorization (e.g. Role authorizes ServiceAccount).
    pub const AUTHORIZES: Self = Self(Cow::Borrowed("AUTHORIZES"));

    /// Create a `RelationshipKind` from any string-like value.
    pub fn new(s: impl Into<String>) -> Self {
        Self(Cow::Owned(s.into()))
    }

    /// Return the relationship kind as a string slice.
    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Return the deterministic [`RelationshipCategory`] for this kind.
    ///
    /// Every initial relationship kind maps to exactly one category.
    /// Returns `None` for unmapped custom kinds.
    pub fn category(&self) -> Option<RelationshipCategory> {
        match self.as_str() {
            "DEPENDS_ON" => Some(RelationshipCategory::Dependency),
            "CALLS" => Some(RelationshipCategory::Invocation),
            "READS_FROM" => Some(RelationshipCategory::DataFlow),
            "WRITES_TO" => Some(RelationshipCategory::DataFlow),
            "OWNS" => Some(RelationshipCategory::Ownership),
            "RUNS_ON" => Some(RelationshipCategory::Placement),
            "AUTHORIZES" => Some(RelationshipCategory::Authorization),
            _ => None,
        }
    }
}

impl Deref for RelationshipKind {
    type Target = str;

    fn deref(&self) -> &Self::Target {
        self.as_str()
    }
}

impl AsRef<str> for RelationshipKind {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}

impl fmt::Display for RelationshipKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl From<&'static str> for RelationshipKind {
    fn from(s: &'static str) -> Self {
        Self(Cow::Borrowed(s))
    }
}

impl From<String> for RelationshipKind {
    fn from(s: String) -> Self {
        Self(Cow::Owned(s))
    }
}

// ---------------------------------------------------------------------------
// Relationship
// ---------------------------------------------------------------------------

/// A directional relationship between two infrastructure resources.
///
/// Relationships are thin domain values representing:
/// `source --[kind (category)]--> target`
///
/// # Identity
///
/// The natural identity of a `Relationship` is `(source, target, kind)`.
/// Category is a classification derived from kind and is NOT part of the identity.
/// Equality and hashing strictly compare `(source, target, kind)`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Relationship {
    /// Origin resource of the directional relationship.
    pub source: ResourceIdentity,
    /// Destination resource of the directional relationship.
    pub target: ResourceIdentity,
    /// High-level classification, deterministically derived from `kind`.
    pub category: RelationshipCategory,
    /// Specific typed kind of the relationship.
    pub kind: RelationshipKind,
}

impl Relationship {
    /// Construct a new directional `Relationship`.
    ///
    /// The `category` is deterministically derived from `kind`.
    ///
    /// # Panics
    /// Panics if `kind` is not mapped to a known category. For custom kinds,
    /// use [`Relationship::with_category`].
    pub fn new(source: ResourceIdentity, target: ResourceIdentity, kind: RelationshipKind) -> Self {
        let category = kind.category().unwrap_or_else(|| {
            panic!(
                "relationship kind '{}' has no deterministic category; use with_category for custom kinds",
                kind
            )
        });
        Self {
            source,
            target,
            category,
            kind,
        }
    }

    /// Construct a directional `Relationship` with an explicitly provided category.
    ///
    /// If `kind` is an initial canonical kind, `category` MUST match its canonical
    /// category.
    ///
    /// # Panics
    /// Panics if `category` conflicts with the canonical category of a known kind.
    pub fn with_category(
        source: ResourceIdentity,
        target: ResourceIdentity,
        kind: RelationshipKind,
        category: RelationshipCategory,
    ) -> Self {
        if let Some(canonical) = kind.category() {
            assert_eq!(
                canonical, category,
                "relationship kind '{}' must map to '{:?}', cannot assign '{:?}'",
                kind, canonical, category
            );
        }
        Self {
            source,
            target,
            category,
            kind,
        }
    }

    /// Return the natural identity tuple of the relationship: `(source, target, kind)`.
    pub fn identity(&self) -> (&ResourceIdentity, &ResourceIdentity, &RelationshipKind) {
        (&self.source, &self.target, &self.kind)
    }
}

impl PartialEq for Relationship {
    fn eq(&self, other: &Self) -> bool {
        self.source == other.source && self.target == other.target && self.kind == other.kind
    }
}

impl Eq for Relationship {}

impl std::hash::Hash for Relationship {
    fn hash<H: std::hash::Hasher>(&self, state: &mut H) {
        self.source.hash(state);
        self.target.hash(state);
        self.kind.hash(state);
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::resource::{Attributes, Provider, Resource, ResourceKind, ResourceMetadata};
    use std::collections::HashSet;

    // -----------------------------------------------------------------------
    // 1. ResourceIdentity can represent a Kubernetes resource.
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_identity_kubernetes() {
        let identity = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "cluster/prod/namespace/payments/pod/payments-api-7d8f9",
        );

        assert_eq!(identity.provider.as_str(), "kubernetes");
        assert_eq!(identity.resource_type.as_str(), "pod");
        assert_eq!(
            identity.provider_id,
            "cluster/prod/namespace/payments/pod/payments-api-7d8f9"
        );
        assert_eq!(
            identity.to_string(),
            "kubernetes:pod:cluster/prod/namespace/payments/pod/payments-api-7d8f9"
        );
    }

    // -----------------------------------------------------------------------
    // 2. ResourceIdentity can represent an AWS resource.
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_identity_aws() {
        let identity = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            "arn:aws:rds:ap-south-1:123456789012:db:payments",
        );

        assert_eq!(identity.provider.as_str(), "aws");
        assert_eq!(identity.resource_type.as_str(), "rds");
        assert_eq!(
            identity.provider_id,
            "arn:aws:rds:ap-south-1:123456789012:db:payments"
        );
        assert_eq!(
            identity.to_string(),
            "aws:rds:arn:aws:rds:ap-south-1:123456789012:db:payments"
        );
    }

    // -----------------------------------------------------------------------
    // 3. ResourceIdentity is independent of ResourceId(UUID).
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_identity_independent_of_resource_id_uuid() {
        let provider = Provider::new("kubernetes");
        let provider_id = "cluster/prod/namespace/payments/pod/payments-api-7d8f9";
        let resource_type = ResourceKind::new("pod");

        let resource1 = Resource::new(
            provider.clone(),
            provider_id,
            resource_type.clone(),
            "payments-api",
            Attributes::new(),
            ResourceMetadata::empty(),
        );

        let resource2 = Resource::new(
            provider.clone(),
            provider_id,
            resource_type.clone(),
            "payments-api-replica",
            Attributes::new(),
            ResourceMetadata::empty(),
        );

        // ResourceId is an internal UUID — different per Resource instance
        assert_ne!(resource1.id, resource2.id);

        // Derived ResourceIdentity is independent of the UUID and identical for both
        let identity1 = resource1.identity();
        let identity2 = resource2.identity();
        assert_eq!(identity1, identity2);
        assert_eq!(identity1.provider_id, provider_id);
    }

    // -----------------------------------------------------------------------
    // 4. Two ResourceIdentity values with same fields compare equal.
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_identity_equality() {
        let id1 = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );
        let id2 = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        assert_eq!(id1, id2);

        let mut set = HashSet::new();
        set.insert(id1);
        assert!(set.contains(&id2));
    }

    // -----------------------------------------------------------------------
    // 5. Different fields produce different identities.
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_identity_differences() {
        let base = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        let diff_provider = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        let diff_type = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("deployment"),
            "payments/payments-service",
        );

        let diff_id = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/checkout-service",
        );

        assert_ne!(base, diff_provider);
        assert_ne!(base, diff_type);
        assert_ne!(base, diff_id);
    }

    // -----------------------------------------------------------------------
    // 6. A Relationship is directional.
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_is_directional() {
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/payments-api-abc",
        );
        let service = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        let forward = Relationship::new(pod.clone(), service.clone(), RelationshipKind::CALLS);
        let backward = Relationship::new(service.clone(), pod.clone(), RelationshipKind::CALLS);

        assert_ne!(forward, backward);
        assert_eq!(forward.source, pod);
        assert_eq!(forward.target, service);
        assert_eq!(backward.source, service);
        assert_eq!(backward.target, pod);
    }

    // -----------------------------------------------------------------------
    // 7. RelationshipKind is preserved correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_kind_preserved() {
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/pod-1",
        );
        let node = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("node"),
            "node-prod-worker-1",
        );

        let rel = Relationship::new(pod, node, RelationshipKind::RUNS_ON);
        assert_eq!(rel.kind, RelationshipKind::RUNS_ON);
        assert_eq!(rel.kind.as_str(), "RUNS_ON");
        assert_eq!(rel.kind.to_string(), "RUNS_ON");
    }

    // -----------------------------------------------------------------------
    // 8. Each initial RelationshipKind maps to exactly one RelationshipCategory.
    // -----------------------------------------------------------------------
    #[test]
    fn test_initial_kinds_map_to_exactly_one_category() {
        let cases = [
            (
                RelationshipKind::DEPENDS_ON,
                RelationshipCategory::Dependency,
            ),
            (RelationshipKind::CALLS, RelationshipCategory::Invocation),
            (RelationshipKind::READS_FROM, RelationshipCategory::DataFlow),
            (RelationshipKind::WRITES_TO, RelationshipCategory::DataFlow),
            (RelationshipKind::OWNS, RelationshipCategory::Ownership),
            (RelationshipKind::RUNS_ON, RelationshipCategory::Placement),
            (
                RelationshipKind::AUTHORIZES,
                RelationshipCategory::Authorization,
            ),
        ];

        for (kind, expected_category) in cases {
            assert_eq!(
                kind.category(),
                Some(expected_category),
                "kind '{}' should map to '{:?}'",
                kind,
                expected_category
            );
            assert_eq!(
                RelationshipCategory::from_kind(&kind),
                Some(expected_category)
            );
        }

        // Verify Network category exists but has no initial kind
        assert_eq!(RelationshipCategory::Network.as_str(), "Network");
    }

    // -----------------------------------------------------------------------
    // 9. Category is derived consistently from kind.
    // -----------------------------------------------------------------------
    #[test]
    fn test_category_derived_consistently_from_kind() {
        let src = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/payments-api",
        );
        let tgt = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            "arn:aws:rds:us-east-1:123:db/payments",
        );

        let rel_reads = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::READS_FROM);
        assert_eq!(rel_reads.category, RelationshipCategory::DataFlow);

        let rel_writes = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::WRITES_TO);
        assert_eq!(rel_writes.category, RelationshipCategory::DataFlow);

        let rel_calls = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::CALLS);
        assert_eq!(rel_calls.category, RelationshipCategory::Invocation);

        let rel_depends = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::DEPENDS_ON);
        assert_eq!(rel_depends.category, RelationshipCategory::Dependency);

        let rel_owns = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::OWNS);
        assert_eq!(rel_owns.category, RelationshipCategory::Ownership);

        let rel_runs = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::RUNS_ON);
        assert_eq!(rel_runs.category, RelationshipCategory::Placement);

        let rel_auth = Relationship::new(src, tgt, RelationshipKind::AUTHORIZES);
        assert_eq!(rel_auth.category, RelationshipCategory::Authorization);
    }

    // -----------------------------------------------------------------------
    // 10. Natural relationship identity is effectively (source, target, kind).
    // -----------------------------------------------------------------------
    #[test]
    fn test_natural_relationship_identity() {
        let src = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/payments-api",
        );
        let tgt = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        let rel = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::CALLS);
        assert_eq!(rel.identity(), (&src, &tgt, &RelationshipKind::CALLS));
    }

    // -----------------------------------------------------------------------
    // 11. Same (source, target, kind) represents same relationship regardless
    //     of how category was derived.
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_equality_and_hashing_by_identity() {
        let src = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/payments-api",
        );
        let tgt = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "payments/payments-service",
        );

        let rel1 = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::CALLS);
        let rel2 = Relationship::with_category(
            src.clone(),
            tgt.clone(),
            RelationshipKind::CALLS,
            RelationshipCategory::Invocation,
        );

        // They must compare equal
        assert_eq!(rel1, rel2);

        // They must hash identically
        let mut set = HashSet::new();
        set.insert(rel1);
        assert!(set.contains(&rel2));
    }

    // -----------------------------------------------------------------------
    // 12. Serialization / deserialization round-trips correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialization_round_trip() {
        let src = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "default/pod-1",
        );
        let tgt = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            "arn:aws:rds:us-east-1:123456789012:db:main",
        );

        let rel = Relationship::new(src, tgt, RelationshipKind::WRITES_TO);

        let json = serde_json::to_string(&rel).expect("serialization failed");
        let restored: Relationship = serde_json::from_str(&json).expect("deserialization failed");

        assert_eq!(rel, restored);
        assert_eq!(rel.category, restored.category);
        assert_eq!(rel.kind, restored.kind);
        assert_eq!(rel.source, restored.source);
        assert_eq!(rel.target, restored.target);

        // Check JSON shape
        let v: serde_json::Value = serde_json::to_value(&rel).expect("to_value failed");
        assert_eq!(v["category"], "DataFlow");
        assert_eq!(v["kind"], "WRITES_TO");
        assert_eq!(v["source"]["provider"], "kubernetes");
        assert_eq!(v["target"]["provider"], "aws");
    }

    // -----------------------------------------------------------------------
    // 13. Extensibility: custom RelationshipKind with with_category works.
    // -----------------------------------------------------------------------
    #[test]
    fn test_custom_relationship_kind_extensibility() {
        let vpc1 = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("vpc"),
            "arn:aws:ec2:us-east-1:123:vpc/vpc-111",
        );
        let vpc2 = ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("vpc"),
            "arn:aws:ec2:us-east-1:123:vpc/vpc-222",
        );

        // A custom network peering kind that did not exist initially
        let custom_kind = RelationshipKind::new("PEERS_WITH");
        assert_eq!(custom_kind.category(), None);

        let rel = Relationship::with_category(
            vpc1,
            vpc2,
            custom_kind.clone(),
            RelationshipCategory::Network,
        );

        assert_eq!(rel.kind.as_str(), "PEERS_WITH");
        assert_eq!(rel.category, RelationshipCategory::Network);
    }

    // -----------------------------------------------------------------------
    // 14. Ambiguity prevention: assigning wrong category to known kind panics.
    // -----------------------------------------------------------------------
    #[test]
    #[should_panic(expected = "cannot assign 'DataFlow'")]
    fn test_ambiguity_prevention_on_known_kind() {
        let src = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "default/pod-1",
        );
        let tgt = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "default/svc-1",
        );

        // Attempting to classify CALLS as DataFlow must fail
        Relationship::with_category(
            src,
            tgt,
            RelationshipKind::CALLS,
            RelationshipCategory::DataFlow,
        );
    }
}
