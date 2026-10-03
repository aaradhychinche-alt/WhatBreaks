//! WhatBreaks Entity / Resource Model
//!
//! A [`Resource`] represents an identifiable thing that exists in an observed
//! customer infrastructure environment. Examples include Kubernetes Pods, AWS
//! Lambda functions, GitHub repositories, internal services, and custom
//! company-specific systems.
//!
//! # Design Principles
//!
//! - **Provider-agnostic**: The Core Engine does not enumerate every possible
//!   provider or resource type. New providers and new resource types can be
//!   observed without changing this module.
//!
//! - **Identity separation**: [`ResourceId`] is the WhatBreaks-internal
//!   identity. [`Resource::provider_id`] is the provider-native identity.
//!   These two identities serve different purposes and must not be merged.
//!
//! - **Generic attributes**: Provider-specific properties (e.g.
//!   `kubernetes_namespace`, `aws_region`) are stored in a generic
//!   [`Attributes`] map. The Core Engine does not understand or validate
//!   provider-specific fields.
//!
//! - **No business logic**: `Resource` is a domain model only. It carries no
//!   relationship information, no graph awareness, no collector awareness, and
//!   no persistence awareness.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// ResourceId
// ---------------------------------------------------------------------------

/// The canonical WhatBreaks identity of a resource.
///
/// This is an *internal* WB identity — it is NOT the provider-native
/// identifier. Two resources from entirely different providers (Kubernetes,
/// AWS, an internal service) each receive a unique `ResourceId` that is
/// meaningful only within the WB Core Engine.
///
/// Implemented as a transparent newtype over [`Uuid`] so that:
/// - It is strongly typed (cannot be confused with a provider ID or any other
///   string/UUID in the codebase).
/// - It is globally unique without requiring a database or distributed
///   coordination.
/// - It serialises cleanly to/from a UUID string in JSON.
///
/// # Why UUID and not something simpler?
///
/// A sequential integer would require a shared counter (persistence).
/// A hash of provider fields would couple identity to provider data.
/// A UUID v4 is self-contained, collision-resistant, and requires no
/// infrastructure — appropriate for the current stage of the Core Engine.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct ResourceId(Uuid);

impl ResourceId {
    /// Create a new, unique `ResourceId`.
    pub fn new() -> Self {
        Self(Uuid::new_v4())
    }

    /// Return the underlying [`Uuid`].
    pub fn as_uuid(&self) -> Uuid {
        self.0
    }
}

impl Default for ResourceId {
    fn default() -> Self {
        Self::new()
    }
}

impl std::fmt::Display for ResourceId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.fmt(f)
    }
}

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

/// Identifies the system or environment from which a resource originates.
///
/// Examples: `"kubernetes"`, `"aws"`, `"gcp"`, `"azure"`, `"github"`,
/// `"postgresql"`, `"internal"`, `"custom"`.
///
/// # Why a newtype String rather than an enum?
///
/// WhatBreaks must support providers that do not exist at the time this code
/// was written. An enum would require modifying the Core Engine every time a
/// new provider is observed. A strongly-typed newtype preserves type safety
/// (you cannot accidentally pass a `ResourceKind` where a `Provider` is
/// expected) while remaining open to any provider string.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Provider(String);

impl Provider {
    /// Create a `Provider` from any string-like value.
    pub fn new(s: impl Into<String>) -> Self {
        Self(s.into())
    }

    /// Return the provider identifier as a `&str`.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl std::fmt::Display for Provider {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

// ---------------------------------------------------------------------------
// ResourceKind
// ---------------------------------------------------------------------------

/// Identifies what kind of resource this is within its provider.
///
/// Examples:
/// - provider `"kubernetes"` → kind `"pod"`, `"service"`, `"deployment"`
/// - provider `"aws"` → kind `"lambda"`, `"rds"`, `"s3-bucket"`
/// - provider `"github"` → kind `"repository"`
/// - provider `"internal"` → kind `"payment-processing-service-v2"`
///
/// # Why a newtype String rather than an enum?
///
/// The universe of resource types across all providers is effectively
/// unbounded. Enumerating them in the Core Engine would couple this
/// provider-agnostic model to every possible external system. A newtype
/// String is open by design: adding a new resource type requires no change to
/// this module.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct ResourceKind(String);

impl ResourceKind {
    /// Create a `ResourceKind` from any string-like value.
    pub fn new(s: impl Into<String>) -> Self {
        Self(s.into())
    }

    /// Return the kind identifier as a `&str`.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl std::fmt::Display for ResourceKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

// ---------------------------------------------------------------------------
// Attributes
// ---------------------------------------------------------------------------

/// Provider- and resource-specific observed attributes.
///
/// The Core Engine does not attempt to define or validate every possible
/// infrastructure attribute. Instead, attributes are stored as a flat map of
/// string keys to arbitrary JSON values. This allows collectors for any
/// provider to supply whatever fields are relevant without requiring the Core
/// Engine to understand them.
///
/// Examples:
///
/// Kubernetes Pod:
/// ```json
/// { "namespace": "payments", "image": "payments:v42", "port": 8080 }
/// ```
///
/// AWS RDS:
/// ```json
/// { "engine": "postgres", "version": "15.3", "region": "ap-south-1" }
/// ```
///
/// Internal service:
/// ```json
/// { "team": "payments", "environment": "production" }
/// ```
///
/// # Why `serde_json::Value`?
///
/// It is the standard Rust representation for arbitrary structured JSON.
/// It avoids defining a custom attribute schema at this stage, supports
/// booleans, numbers, strings, arrays, and nested objects, and round-trips
/// cleanly through JSON serialisation.
pub type Attributes = HashMap<String, Value>;

// ---------------------------------------------------------------------------
// ResourceMetadata
// ---------------------------------------------------------------------------

/// WhatBreaks-owned metadata about the Resource itself.
///
/// This is distinct from [`Attributes`]: attributes describe the *observed*
/// resource (provider-specific data); metadata describes information that WB
/// *attaches* to the resource for its own operational purposes.
///
/// # Current fields
///
/// - `labels`: Arbitrary string key/value pairs that WB (or operators) can
///   attach to a resource for filtering, grouping, or annotation. No schema
///   is enforced — this is intentionally open.
///
/// # What is deliberately absent
///
/// `first_seen`, `last_seen`, discovery state, impact state, confidence,
/// evidence, graph state, and relationship data are NOT included. They belong
/// to future components (discovery, impact analysis) and must not be
/// prematurely added here.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct ResourceMetadata {
    /// Arbitrary string labels WB can attach to this resource.
    /// Keys and values are unvalidated — the Core Engine imposes no schema.
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub labels: HashMap<String, String>,
}

impl ResourceMetadata {
    /// Create an empty `ResourceMetadata` with no labels.
    pub fn empty() -> Self {
        Self::default()
    }

    /// Create `ResourceMetadata` with a given set of labels.
    pub fn with_labels(labels: HashMap<String, String>) -> Self {
        Self { labels }
    }
}

// ---------------------------------------------------------------------------
// Resource
// ---------------------------------------------------------------------------

/// A resource is an identifiable thing that exists in an observed customer
/// infrastructure environment.
///
/// Resources are provider-agnostic: the same `Resource` type represents a
/// Kubernetes Pod, an AWS Lambda, a GitHub repository, an internal service,
/// or any custom infrastructure entity.
///
/// # Field summary
///
/// | Field           | Meaning                                                   |
/// |-----------------|-----------------------------------------------------------|
/// | `id`            | WhatBreaks-internal stable identity ([`ResourceId`])      |
/// | `provider`      | Origin system (`"kubernetes"`, `"aws"`, `"internal"`, …)  |
/// | `provider_id`   | Provider-native identity (ARN, cluster path, etc.)        |
/// | `resource_type` | Kind within the provider (`"pod"`, `"rds"`, …)            |
/// | `name`          | Human-readable name (not guaranteed unique)               |
/// | `attributes`    | Provider-specific observed properties ([`Attributes`])    |
/// | `metadata`      | WB-owned metadata ([`ResourceMetadata`])                  |
///
/// # What `Resource` does NOT contain
///
/// - Relationships or dependencies
/// - Graph references
/// - Confidence or evidence fields
/// - Impact or blast-radius data
/// - Collector references
/// - Persistence identifiers (database row IDs, etc.)
/// - Any provider-specific Rust types (no Kubernetes SDK imports)
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Resource {
    /// WhatBreaks-internal identity. Stable, unique within WB. Never derived
    /// from the provider-native ID or from the resource name.
    pub id: ResourceId,

    /// The system from which this resource originates.
    /// Examples: `"kubernetes"`, `"aws"`, `"gcp"`, `"internal"`, `"custom"`.
    /// Open-ended: new providers do not require changes to `Resource`.
    pub provider: Provider,

    /// The provider-native identity of this resource.
    ///
    /// Examples:
    /// - Kubernetes: `"cluster/prod/namespace/payments/pod/payments-api-7d8f9"`
    /// - AWS RDS: `"arn:aws:rds:ap-south-1:123456789012:db:payments"`
    /// - GitHub: `"owner/repository"`
    /// - Internal: `"internal://services/payment-processor"`
    ///
    /// Kept separate from [`Resource::id`] because the provider identity may
    /// change (e.g. pod restart) while the WB identity should remain stable,
    /// and because the provider identity format varies per provider.
    pub provider_id: String,

    /// The kind of resource within its provider.
    /// Examples: `"pod"`, `"rds"`, `"repository"`, `"payment-processing-service-v2"`.
    /// Open-ended: new resource types do not require changes to `Resource`.
    pub resource_type: ResourceKind,

    /// Human-readable name for identification and display.
    /// Examples: `"payments-api"`, `"payments-db"`, `"checkout-service"`.
    /// NOT guaranteed to be globally unique. Must NOT be used as identity.
    pub name: String,

    /// Provider-specific observed attributes.
    /// The Core Engine does not validate or interpret these fields.
    /// See [`Attributes`] for examples.
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub attributes: Attributes,

    /// WB-owned metadata attached to this resource.
    /// Separate from attributes: attributes describe what was observed;
    /// metadata describes what WB knows about the resource itself.
    #[serde(default)]
    pub metadata: ResourceMetadata,
}

impl Resource {
    /// Construct a new `Resource`.
    ///
    /// A fresh [`ResourceId`] is generated automatically. The caller supplies
    /// everything the Core Engine needs to identify and describe the resource.
    pub fn new(
        provider: Provider,
        provider_id: impl Into<String>,
        resource_type: ResourceKind,
        name: impl Into<String>,
        attributes: Attributes,
        metadata: ResourceMetadata,
    ) -> Self {
        Self {
            id: ResourceId::new(),
            provider,
            provider_id: provider_id.into(),
            resource_type,
            name: name.into(),
            attributes,
            metadata,
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // -----------------------------------------------------------------------
    // Helper: build a minimal Attributes map from a serde_json object literal.
    // -----------------------------------------------------------------------
    fn attrs(v: serde_json::Value) -> Attributes {
        match v {
            serde_json::Value::Object(map) => map.into_iter().collect(),
            _ => panic!("attrs() expects a JSON object literal"),
        }
    }

    // -----------------------------------------------------------------------
    // 1. A Kubernetes resource can be represented.
    // -----------------------------------------------------------------------
    #[test]
    fn test_kubernetes_pod_resource() {
        let resource = Resource::new(
            Provider::new("kubernetes"),
            "cluster/prod/namespace/payments/pod/payments-api-7d8f9",
            ResourceKind::new("pod"),
            "payments-api",
            attrs(json!({
                "namespace": "payments",
                "image": "payments:v42"
            })),
            ResourceMetadata::empty(),
        );

        assert_eq!(resource.provider.as_str(), "kubernetes");
        assert_eq!(resource.resource_type.as_str(), "pod");
        assert_eq!(resource.name, "payments-api");
        assert_eq!(
            resource.provider_id,
            "cluster/prod/namespace/payments/pod/payments-api-7d8f9"
        );
        assert_eq!(
            resource.attributes.get("namespace").and_then(|v| v.as_str()),
            Some("payments")
        );
        assert_eq!(
            resource.attributes.get("image").and_then(|v| v.as_str()),
            Some("payments:v42")
        );
    }

    // -----------------------------------------------------------------------
    // 2. An AWS resource can be represented.
    // -----------------------------------------------------------------------
    #[test]
    fn test_aws_rds_resource() {
        let resource = Resource::new(
            Provider::new("aws"),
            "arn:aws:rds:ap-south-1:123456789012:db:payments",
            ResourceKind::new("rds"),
            "payments-db",
            attrs(json!({
                "engine": "postgres",
                "version": "15.3",
                "region": "ap-south-1"
            })),
            ResourceMetadata::empty(),
        );

        assert_eq!(resource.provider.as_str(), "aws");
        assert_eq!(resource.resource_type.as_str(), "rds");
        assert_eq!(resource.name, "payments-db");
        assert_eq!(
            resource.provider_id,
            "arn:aws:rds:ap-south-1:123456789012:db:payments"
        );
        assert_eq!(
            resource.attributes.get("engine").and_then(|v| v.as_str()),
            Some("postgres")
        );
        assert_eq!(
            resource.attributes.get("region").and_then(|v| v.as_str()),
            Some("ap-south-1")
        );
    }

    // -----------------------------------------------------------------------
    // 3. A completely new / custom provider can be represented WITHOUT
    //    changing the Resource type.
    // -----------------------------------------------------------------------
    #[test]
    fn test_custom_provider_no_type_change_required() {
        // "acme-observability" is a provider that did not exist when this
        // module was written. No enum variant was added; no code was changed.
        let resource = Resource::new(
            Provider::new("acme-observability"),
            "acme://traces/service/checkout",
            ResourceKind::new("trace-sink"),
            "checkout-traces",
            attrs(json!({ "retention_days": 30 })),
            ResourceMetadata::empty(),
        );

        assert_eq!(resource.provider.as_str(), "acme-observability");
        assert_eq!(resource.resource_type.as_str(), "trace-sink");
    }

    // -----------------------------------------------------------------------
    // 4. A custom resource type can be represented WITHOUT modifying Resource.
    // -----------------------------------------------------------------------
    #[test]
    fn test_custom_resource_type_no_type_change_required() {
        // "payment-processing-service-v2" is not an enum variant anywhere.
        let resource = Resource::new(
            Provider::new("internal"),
            "internal://services/payment-processor",
            ResourceKind::new("payment-processing-service-v2"),
            "payment-processor",
            attrs(json!({
                "team": "payments",
                "environment": "production"
            })),
            ResourceMetadata::empty(),
        );

        assert_eq!(resource.resource_type.as_str(), "payment-processing-service-v2");
        assert_eq!(resource.provider.as_str(), "internal");
    }

    // -----------------------------------------------------------------------
    // 5. `id` and `provider_id` remain separate.
    // -----------------------------------------------------------------------
    #[test]
    fn test_id_and_provider_id_are_separate() {
        let provider_id = "cluster/prod/namespace/payments/pod/payments-api-7d8f9";
        let resource = Resource::new(
            Provider::new("kubernetes"),
            provider_id,
            ResourceKind::new("pod"),
            "payments-api",
            Attributes::new(),
            ResourceMetadata::empty(),
        );

        // WB identity is a UUID — it does NOT equal the provider_id string.
        assert_ne!(resource.id.to_string(), provider_id);
        // The provider_id is preserved exactly as supplied.
        assert_eq!(resource.provider_id, provider_id);
        // Two distinct resources for the same provider_id get different WB ids.
        let resource2 = Resource::new(
            Provider::new("kubernetes"),
            provider_id,
            ResourceKind::new("pod"),
            "payments-api",
            Attributes::new(),
            ResourceMetadata::empty(),
        );
        assert_ne!(resource.id, resource2.id);
    }

    // -----------------------------------------------------------------------
    // 6. Provider-specific attributes are stored without adding provider-
    //    specific fields to Resource.
    // -----------------------------------------------------------------------
    #[test]
    fn test_attributes_are_generic_no_provider_specific_fields() {
        let k8s = Resource::new(
            Provider::new("kubernetes"),
            "cluster/prod/namespace/default/pod/web-abc123",
            ResourceKind::new("pod"),
            "web",
            attrs(json!({ "namespace": "default", "image": "web:v1", "port": 80 })),
            ResourceMetadata::empty(),
        );
        let aws = Resource::new(
            Provider::new("aws"),
            "arn:aws:lambda:us-east-1:123456789012:function:checkout",
            ResourceKind::new("lambda"),
            "checkout",
            attrs(json!({ "runtime": "nodejs20.x", "region": "us-east-1", "memory_mb": 512 })),
            ResourceMetadata::empty(),
        );

        // Both use the same Resource struct — no kubernetes_namespace or
        // aws_region fields exist on Resource itself.
        assert!(k8s.attributes.contains_key("namespace"));
        assert!(k8s.attributes.contains_key("image"));
        assert!(aws.attributes.contains_key("runtime"));
        assert!(aws.attributes.contains_key("region"));

        // Numeric values work too.
        assert_eq!(k8s.attributes.get("port").and_then(|v| v.as_u64()), Some(80));
        assert_eq!(
            aws.attributes.get("memory_mb").and_then(|v| v.as_u64()),
            Some(512)
        );
    }

    // -----------------------------------------------------------------------
    // 7. Metadata remains separate from attributes.
    // -----------------------------------------------------------------------
    #[test]
    fn test_metadata_is_separate_from_attributes() {
        let mut labels = HashMap::new();
        labels.insert("managed-by".to_string(), "wb-core".to_string());
        labels.insert("env".to_string(), "production".to_string());

        let resource = Resource::new(
            Provider::new("kubernetes"),
            "cluster/prod/namespace/payments/pod/api-xyz",
            ResourceKind::new("pod"),
            "api",
            attrs(json!({ "namespace": "payments", "image": "api:v5" })),
            ResourceMetadata::with_labels(labels),
        );

        // Metadata labels are separate from provider attributes.
        assert_eq!(
            resource.metadata.labels.get("managed-by").map(String::as_str),
            Some("wb-core")
        );
        assert_eq!(
            resource.metadata.labels.get("env").map(String::as_str),
            Some("production")
        );

        // Provider attribute "namespace" is NOT in metadata.
        assert!(!resource.metadata.labels.contains_key("namespace"));
        // Metadata label "managed-by" is NOT in attributes.
        assert!(!resource.attributes.contains_key("managed-by"));
    }

    // -----------------------------------------------------------------------
    // 8. Serialisation / deserialisation round-trips correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialization_round_trip() {
        let mut labels = HashMap::new();
        labels.insert("team".to_string(), "platform".to_string());

        let original = Resource::new(
            Provider::new("aws"),
            "arn:aws:rds:eu-west-1:999888777666:db:analytics",
            ResourceKind::new("rds"),
            "analytics-db",
            attrs(json!({ "engine": "mysql", "region": "eu-west-1" })),
            ResourceMetadata::with_labels(labels),
        );

        // Serialise to JSON string.
        let json_str = serde_json::to_string(&original).expect("serialisation failed");

        // Deserialise back.
        let restored: Resource = serde_json::from_str(&json_str).expect("deserialisation failed");

        // All fields must survive the round-trip.
        assert_eq!(original.id, restored.id);
        assert_eq!(original.provider, restored.provider);
        assert_eq!(original.provider_id, restored.provider_id);
        assert_eq!(original.resource_type, restored.resource_type);
        assert_eq!(original.name, restored.name);
        assert_eq!(original.attributes, restored.attributes);
        assert_eq!(original.metadata.labels, restored.metadata.labels);
    }

    // -----------------------------------------------------------------------
    // 9. JSON structure is correct (field names and nesting are as expected).
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialized_json_shape() {
        let resource = Resource::new(
            Provider::new("github"),
            "acme-corp/checkout-service",
            ResourceKind::new("repository"),
            "checkout-service",
            attrs(json!({ "default_branch": "main" })),
            ResourceMetadata::empty(),
        );

        let v: serde_json::Value =
            serde_json::to_value(&resource).expect("serialisation to value failed");

        // `id` serialises as a UUID string.
        assert!(v["id"].is_string());
        // `provider` serialises as a plain string (transparent newtype).
        assert_eq!(v["provider"], "github");
        // `provider_id` is a plain string.
        assert_eq!(v["provider_id"], "acme-corp/checkout-service");
        // `resource_type` serialises as a plain string (transparent newtype).
        assert_eq!(v["resource_type"], "repository");
        // `name` is a plain string.
        assert_eq!(v["name"], "checkout-service");
        // `attributes` contains the expected key.
        assert_eq!(v["attributes"]["default_branch"], "main");
        // `metadata` is omitted when labels are empty (skip_serializing_if).
        // The metadata object itself is present (default), but labels key is absent.
        assert!(v.get("metadata").is_some());
        assert!(v["metadata"].get("labels").is_none());
    }
}
