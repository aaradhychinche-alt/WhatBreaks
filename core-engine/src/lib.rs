//! WhatBreaks Core Engine
//!
//! This is the root of the WB Core Engine crate.
//! The Core Engine is provider-agnostic: it does not import Kubernetes,
//! AWS, GCP, or any other provider SDK.
//!
//! # Modules
//!
//! - [`resource`]: The foundational Entity / Resource model.
//! - [`relationship`]: Directional relationship model between resource identities.
//! - [`evidence`]: Point-in-time observation model (factual foundation for discovery).
//! - [`discovery`]: Deterministic, evidence-backed relationship discovery engine.
//! - [`graph`]: In-memory directional graph layer for resource relationships.
//! - [`traversal`]: Deterministic, in-memory graph traversal layer.
//! - [`impact`]: Deterministic, in-memory candidate blast radius / impact analysis engine.

pub mod discovery;
pub mod evidence;
pub mod graph;
pub mod impact;
pub mod relationship;
pub mod resource;
pub mod traversal;

pub use discovery::{
    DiscoveredRelationship, DiscoveryEngine, DiscoveryResult, DiscoveryRule, RuntimeConnectionRule,
};
pub use evidence::{CollectorId, Evidence, EvidenceId, EvidenceSource, ObservationType};
pub use graph::Graph;
pub use impact::{propagates_impact, ImpactEngine, ImpactRequest, ImpactResult, ImpactedResource};
pub use relationship::{Relationship, RelationshipCategory, RelationshipKind};
pub use resource::{
    Attributes, Provider, Resource, ResourceId, ResourceIdentity, ResourceKind, ResourceMetadata,
};
pub use traversal::{Traversal, TraversalDirection, TraversalNode, TraversalResult};
